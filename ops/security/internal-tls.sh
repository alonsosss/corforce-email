#!/usr/bin/env bash
# TLS interno de la produccion autoalojada: una CA propia y los certificados de servidor de
# postgres-primary y redis, FUERA del repositorio (docker-compose.selfhosted.yml los monta).
#
# En AWS el cifrado entre la plataforma y la base o Redis lo dan RDS y ElastiCache con
# certificados de una CA publica. En un servidor propio no hay nadie que los emita: sin esto, o
# se relaja verify-full de PgBouncer y REDIS_TLS, o no arranca nada fuera de development.
#
# Uso (en el servidor, como root; idempotente):
#   ops/security/internal-tls.sh                     # crea lo que falte y corrige permisos
#   ops/security/internal-tls.sh --renovar --recargar  # renueva lo que caduca pronto y lo aplica
#   ops/security/internal-tls.sh --comprobar         # sale con 1 si algo necesita accion
#   ops/security/internal-tls.sh --solo-detectar     # uid:gid de cada imagen, sin tocar nada
#   ops/security/internal-tls.sh --solo-recargar     # aplica los certificados vigentes
#
# Opciones: --dir <ruta> (INTERNAL_TLS_DIR, por defecto /opt/core-force-mail/tls), --proyecto
# <nombre de compose> (por defecto app), --forzar (reemite los certificados de servidor aunque
# esten vigentes), --duenos postgres=UID:GID,redis=UID:GID (sin docker a mano; por defecto se
# leen de las imagenes de docker-compose.yml).
#
# Estructura:
#   <dir>/ca/ca.key            0600 root   clave de la CA; nunca sale del servidor ni se monta
#   <dir>/publico/ca.crt       0644        CA publica: PgBouncer, servicios Go, redis-cli
#   <dir>/postgres/server.*    duenos del uid de postgres en su imagen, clave 0600
#   <dir>/redis/server.*       duenos del uid de redis en su imagen, clave 0600
# Se montan DIRECTORIOS, no ficheros: un fichero montado ata el inodo y la renovacion, que
# reemplaza por rename, no llegaria al contenedor.
#
# La CA dura CA_DIAS y no se rota sola: cambiarla obliga a recrear a todos sus clientes. Los
# certificados de servidor duran CERT_DIAS y --renovar los reemite al quedar MARGEN_DIAS, con la
# misma CA, asi que ningun cliente necesita reiniciarse; --recargar se los hace leer a Postgres
# (SIGHUP) y a Redis (CONFIG SET, sin cortar conexiones). Nunca imprime una clave.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# INTERNAL_TLS_DIR del entorno o, si no, del .env del despliegue (configuracion, no secreto): el
# mismo valor que usa docker-compose.selfhosted.yml para montarlo.
if [[ -z "${INTERNAL_TLS_DIR:-}" && -f "$ROOT/.env" ]]; then
  INTERNAL_TLS_DIR="$(sed -n -E 's/^[[:space:]]*INTERNAL_TLS_DIR[[:space:]]*=[[:space:]]*"?([^"]*)"?[[:space:]]*$/\1/p' "$ROOT/.env" | tail -n 1)"
fi
DIR="${INTERNAL_TLS_DIR:-/opt/core-force-mail/tls}"
PROYECTO="${COMPOSE_PROJECT_NAME:-app}"
CA_DIAS="${INTERNAL_TLS_CA_DAYS:-3650}"
CERT_DIAS="${INTERNAL_TLS_CERT_DAYS:-365}"
MARGEN_DIAS="${INTERNAL_TLS_RENEW_DAYS:-30}"
# Donde ven los contenedores cada directorio: los mismos puntos de montaje que
# docker-compose.selfhosted.yml (ops/scaffold/check-selfhosted-profile.sh los ata).
MONTAJE_PUBLICO=/run/core-force-mail/tls/publico
MONTAJE_REDIS=/run/core-force-mail/tls/redis

MODO=crear
FORZAR=0
RECARGAR=0
DUENOS=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dir) DIR="${2:?--dir necesita una ruta}"; shift 2 ;;
    --proyecto) PROYECTO="${2:?--proyecto necesita un nombre}"; shift 2 ;;
    --duenos) DUENOS="${2:?--duenos necesita postgres=UID:GID,redis=UID:GID}"; shift 2 ;;
    --renovar) MODO=renovar; shift ;;
    --comprobar) MODO=comprobar; shift ;;
    --solo-detectar) MODO=detectar; shift ;;
    --solo-recargar) MODO=recargar; shift ;;
    --forzar) FORZAR=1; shift ;;
    --recargar) RECARGAR=1; shift ;;
    *) echo "internal-tls: opcion desconocida: $1" >&2; exit 2 ;;
  esac
done

falla() { echo "internal-tls: $*" >&2; exit 1; }
info() { echo "internal-tls: $*"; }

for n in CA_DIAS CERT_DIAS MARGEN_DIAS; do
  [[ "${!n}" =~ ^[1-9][0-9]{0,4}$ ]] || falla "$n debe ser un entero positivo (valor: '${!n}')"
done
((MARGEN_DIAS < CERT_DIAS)) || falla "el margen de renovacion ($MARGEN_DIAS) debe ser menor que la vigencia ($CERT_DIAS)"
[[ "$PROYECTO" =~ ^[a-z0-9][a-z0-9_-]*$ ]] || falla "nombre de proyecto de compose no valido: '$PROYECTO'"
command -v openssl >/dev/null || falla "falta openssl"

# Servicios con certificado y los nombres con que los alcanzan sus clientes en la red de compose:
# el del servicio (DB_UPSTREAM_HOST, REDIS_HOST), el del contenedor y el bucle local de sus
# propios chequeos. "postgres" NO va en el de postgres-primary: es el alias de PgBouncer.
SERVICIOS=(postgres redis)
declare -A NOMBRE_COMPOSE=([postgres]=postgres-primary [redis]=redis)
declare -A USUARIO_IMAGEN=([postgres]=postgres [redis]=redis)
san_de() {
  local svc="${NOMBRE_COMPOSE[$1]}"
  echo "DNS:$svc,DNS:$PROYECTO-$svc-1,DNS:localhost,IP:127.0.0.1"
}

# imagen_de <servicio de compose>: la imagen que declara docker-compose.yml.
imagen_de() {
  awk -v s="  $1:" '
    $0 == s { dentro = 1; next }
    dentro && /^  [^ #]/ { exit }
    dentro && /^    image:/ { sub(/^    image:[[:space:]]*/, ""); print; exit }
  ' "$ROOT/docker-compose.yml"
}

declare -A DUENO
detectar_duenos() {
  local svc img usuario uid gid par
  if [[ -n "$DUENOS" ]]; then
    IFS=, read -r -a pares <<<"$DUENOS"
    for par in "${pares[@]}"; do
      [[ "$par" =~ ^(postgres|redis)=([0-9]+):([0-9]+)$ ]] || falla "--duenos mal formado: '$par'"
      DUENO[${BASH_REMATCH[1]}]="${BASH_REMATCH[2]}:${BASH_REMATCH[3]}"
    done
  fi
  for svc in "${SERVICIOS[@]}"; do
    [[ -n "${DUENO[$svc]:-}" ]] && continue
    command -v docker >/dev/null || falla "sin docker no se puede leer el uid de $svc en su imagen: usa --duenos"
    img="$(imagen_de "${NOMBRE_COMPOSE[$svc]}")"
    [[ -n "$img" ]] || falla "docker-compose.yml no declara la imagen de ${NOMBRE_COMPOSE[$svc]}"
    usuario="${USUARIO_IMAGEN[$svc]}"
    uid="$(docker run --rm --entrypoint id "$img" -u "$usuario" 2>/dev/null)" || falla "no se pudo leer el uid de '$usuario' en $img"
    gid="$(docker run --rm --entrypoint id "$img" -g "$usuario" 2>/dev/null)" || falla "no se pudo leer el gid de '$usuario' en $img"
    [[ "$uid" =~ ^[0-9]+$ && "$gid" =~ ^[0-9]+$ ]] || falla "uid:gid inesperado de '$usuario' en $img"
    DUENO[$svc]="$uid:$gid"
  done
}

# ---------------------------------------------------------------------------------------------
# Recarga en los contenedores del proyecto (por etiqueta de compose, no por nombre).
contenedor() {
  docker ps -q --filter "label=com.docker.compose.project=$PROYECTO" \
    --filter "label=com.docker.compose.service=$1" | head -1
}
recargar() {
  command -v docker >/dev/null || falla "sin docker no se puede recargar"
  local id fallos=0
  id="$(contenedor postgres-primary)"
  if [[ -n "$id" ]]; then
    # SIGHUP al postmaster: relee postgresql.conf y los ficheros TLS (PostgreSQL 10+).
    if docker kill -s HUP "$id" >/dev/null; then info "postgres-primary: certificado recargado"; else fallos=1; fi
  else
    info "postgres-primary no esta en marcha en el proyecto $PROYECTO; tomara el certificado al arrancar"
  fi
  id="$(contenedor redis)"
  if [[ -n "$id" ]]; then
    # CONFIG SET de un parametro tls-* rehace el contexto TLS con los ficheros del disco. La
    # contrasena la toma redis-cli de REDISCLI_AUTH dentro del contenedor: no pasa por argumentos.
    if docker exec "$id" sh -c "REDISCLI_AUTH=\"\$REDIS_PASSWORD\" redis-cli --tls --cacert $MONTAJE_PUBLICO/ca.crt -h 127.0.0.1 -p 6379 CONFIG SET tls-cert-file $MONTAJE_REDIS/server.crt" | grep -q '^OK$'; then
      info "redis: certificado recargado"
    else
      echo "internal-tls: redis no acepto la recarga del certificado" >&2
      fallos=1
    fi
  else
    info "redis no esta en marcha en el proyecto $PROYECTO; tomara el certificado al arrancar"
  fi
  return $fallos
}

if [[ "$MODO" == detectar ]]; then
  detectar_duenos
  for svc in "${SERVICIOS[@]}"; do echo "$svc=${DUENO[$svc]}"; done
  exit 0
fi
if [[ "$MODO" == recargar ]]; then
  recargar
  exit $?
fi

# ---------------------------------------------------------------------------------------------
[[ "$DIR" == /* ]] || falla "--dir debe ser una ruta absoluta: '$DIR'"
case "$(realpath -m "$DIR")/" in
  "$(realpath -m "$ROOT")/"*) falla "el TLS interno no se escribe dentro del repositorio ($DIR)" ;;
esac
if [[ "$MODO" != comprobar ]]; then
  [[ "$(id -u)" -eq 0 ]] || falla "ejecuta como root: los ficheros llevan el dueno de cada imagen"
  detectar_duenos
fi

umask 077
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
RENOVADOS=0

# vence_pronto <cert> <dias>: cierto si caduca dentro de <dias>.
vence_pronto() { ! openssl x509 -checkend $(($2 * 86400)) -noout -in "$1" >/dev/null 2>&1; }
fecha_fin() { openssl x509 -enddate -noout -in "$1" | cut -d= -f2; }
# sans_de <cert>: los SAN normalizados y ordenados (formato de san_de).
sans_de() {
  openssl x509 -noout -ext subjectAltName -in "$1" 2>/dev/null | tail -n +2 |
    tr ',' '\n' | sed -e 's/^[[:space:]]*//' -e 's/^IP Address:/IP:/' | grep -v '^$' | LC_ALL=C sort | paste -sd,
}
ordenar() { tr ',' '\n' <<<"$1" | LC_ALL=C sort | paste -sd,; }
# misma_clave <cert> <key>: la clave publica del certificado es la de la clave privada.
misma_clave() {
  [[ "$(openssl x509 -pubkey -noout -in "$1" 2>/dev/null)" == "$(openssl pkey -pubout -in "$2" 2>/dev/null)" ]]
}

cnf_ca() {
  cat >"$TMP/ca.cnf" <<'EOF'
[req]
distinguished_name = dn
prompt = no
x509_extensions = v3_ca
[dn]
O = Core Force Mail
CN = Core Force Mail CA interna
[v3_ca]
basicConstraints = critical, CA:TRUE, pathlen:0
keyUsage = critical, keyCertSign, cRLSign
subjectKeyIdentifier = hash
EOF
}

asegurar_ca() {
  install -d -m 0755 -o 0 -g 0 "$DIR" "$DIR/publico"
  install -d -m 0700 -o 0 -g 0 "$DIR/ca"
  local key="$DIR/ca/ca.key" crt="$DIR/publico/ca.crt"
  if [[ -f "$crt" && ! -f "$key" ]]; then
    falla "existe $crt sin su clave $key: no se puede firmar nada; restaura la clave o retira la CA a mano"
  fi
  if [[ ! -f "$key" ]]; then
    openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$TMP/ca.key" 2>/dev/null
    cnf_ca
    openssl req -x509 -new -key "$TMP/ca.key" -sha256 -days "$CA_DIAS" -config "$TMP/ca.cnf" -out "$TMP/ca.crt"
    install -m 0600 -o 0 -g 0 "$TMP/ca.key" "$key.nuevo" && mv -f "$key.nuevo" "$key"
    install -m 0644 -o 0 -g 0 "$TMP/ca.crt" "$crt.nuevo" && mv -f "$crt.nuevo" "$crt"
    info "CA interna creada (vence $(fecha_fin "$crt"))"
  fi
  misma_clave "$crt" "$key" || falla "$crt no corresponde a $key"
  chown 0:0 "$key" "$crt"
  chmod 0600 "$key"
  chmod 0644 "$crt"
  if vence_pronto "$crt" "$MARGEN_DIAS"; then
    # No se rota sola: todos sus clientes la cargan al arrancar.
    echo "internal-tls: AVISO la CA interna vence el $(fecha_fin "$crt"); rotarla es un procedimiento manual (docs/Operacion_Despliegue.md, produccion autoalojada)" >&2
  fi
}

# necesita_emitir <svc>: imprime el motivo si el certificado de servidor debe (re)emitirse.
necesita_emitir() {
  local d="$DIR/$1" ca="$DIR/publico/ca.crt"
  if [[ ! -f "$d/server.crt" || ! -f "$d/server.key" ]]; then echo "no existe"; return; fi
  if ! openssl verify -CAfile "$ca" "$d/server.crt" >/dev/null 2>&1; then echo "no lo firma la CA vigente"; return; fi
  if ! misma_clave "$d/server.crt" "$d/server.key"; then echo "no corresponde a su clave"; return; fi
  if [[ "$(sans_de "$d/server.crt")" != "$(ordenar "$(san_de "$1")")" ]]; then echo "sus nombres no son los esperados"; return; fi
  if vence_pronto "$d/server.crt" "$MARGEN_DIAS"; then echo "vence el $(fecha_fin "$d/server.crt")"; return; fi
  if ((FORZAR)); then echo "reemision pedida"; return; fi
}

emitir() {
  local svc="$1" d="$DIR/$1" dueno="${DUENO[$1]}"
  local uid="${dueno%%:*}" gid="${dueno##*:}"
  install -d -m 0700 -o "$uid" -g "$gid" "$d"
  openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$TMP/$svc.key" 2>/dev/null
  openssl req -new -key "$TMP/$svc.key" -subj "/O=Core Force Mail/CN=${NOMBRE_COMPOSE[$svc]}" -out "$TMP/$svc.csr"
  cat >"$TMP/$svc.ext" <<EOF
basicConstraints = critical, CA:FALSE
keyUsage = critical, digitalSignature
extendedKeyUsage = serverAuth
subjectAltName = $(san_de "$svc")
subjectKeyIdentifier = hash
authorityKeyIdentifier = keyid
EOF
  openssl x509 -req -in "$TMP/$svc.csr" -CA "$DIR/publico/ca.crt" -CAkey "$DIR/ca/ca.key" \
    -set_serial "0x$(openssl rand -hex 16)" -days "$CERT_DIAS" -sha256 -extfile "$TMP/$svc.ext" \
    -out "$TMP/$svc.crt" 2>/dev/null
  # Primero el certificado nuevo con su clave en ficheros temporales del mismo directorio y
  # despues los dos renombres: un lector ve el par viejo o el nuevo, salvo en el instante entre
  # ambos, y ninguno lee antes de la recarga.
  install -m 0600 -o "$uid" -g "$gid" "$TMP/$svc.key" "$d/server.key.nuevo"
  install -m 0644 -o "$uid" -g "$gid" "$TMP/$svc.crt" "$d/server.crt.nuevo"
  mv -f "$d/server.key.nuevo" "$d/server.key"
  mv -f "$d/server.crt.nuevo" "$d/server.crt"
  RENOVADOS=1
}

asegurar_permisos() {
  local d="$DIR/$1" dueno="${DUENO[$1]}"
  chown "$dueno" "$d" "$d/server.key" "$d/server.crt"
  chmod 0700 "$d"
  chmod 0600 "$d/server.key"
  chmod 0644 "$d/server.crt"
}

if [[ "$MODO" == comprobar ]]; then
  [[ -f "$DIR/publico/ca.crt" ]] || { echo "internal-tls: falta la CA interna ($DIR/publico/ca.crt)"; exit 1; }
  estado=0
  if vence_pronto "$DIR/publico/ca.crt" "$MARGEN_DIAS"; then
    echo "internal-tls: la CA interna vence el $(fecha_fin "$DIR/publico/ca.crt")"
    estado=1
  fi
  for svc in "${SERVICIOS[@]}"; do
    if [[ -r "$DIR/$svc/server.key" || ! -e "$DIR/$svc/server.key" ]]; then
      motivo="$(necesita_emitir "$svc")"
    else
      # Sin root no se lee la clave: se juzga solo el certificado.
      motivo=""
      openssl verify -CAfile "$DIR/publico/ca.crt" "$DIR/$svc/server.crt" >/dev/null 2>&1 || motivo="no lo firma la CA vigente"
      vence_pronto "$DIR/$svc/server.crt" "$MARGEN_DIAS" && motivo="vence el $(fecha_fin "$DIR/$svc/server.crt")"
    fi
    if [[ -n "$motivo" ]]; then
      echo "internal-tls: $svc necesita certificado nuevo: $motivo"
      estado=1
    else
      echo "internal-tls: $svc vigente hasta $(fecha_fin "$DIR/$svc/server.crt")"
    fi
  done
  exit $estado
fi

asegurar_ca
for svc in "${SERVICIOS[@]}"; do
  motivo="$(necesita_emitir "$svc")"
  if [[ -n "$motivo" ]]; then
    emitir "$svc"
    info "$svc: certificado emitido ($motivo); vence el $(fecha_fin "$DIR/$svc/server.crt")"
  else
    info "$svc: vigente hasta $(fecha_fin "$DIR/$svc/server.crt")"
  fi
  asegurar_permisos "$svc"
done

if ((RECARGAR)); then
  if ((RENOVADOS)); then
    recargar
  else
    info "nada que recargar"
  fi
fi

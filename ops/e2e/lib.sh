#!/usr/bin/env bash
# Piezas comunes de las pruebas de punta a punta (ops/e2e/*.sh). Se carga con `source` y no
# ejecuta nada por si misma: comprobaciones con salida OK/FALLA, infraestructura desechable
# (Postgres, NATS, Redis), bases de celda migradas, arranque de binarios del host, una
# plataforma vacia con ops/db/bootstrap-platform.sh y el alta de una empresa por el gateway.
#
# Quien la carga fija antes ROOT (raiz del repositorio, directorio actual), WORK (carpeta
# temporal de la ejecucion), E2E_PREFIX (prefijo de los contenedores), PG_PORT, NATS_PORT y
# REDIS_PORT (puertos en 127.0.0.1) y, para gateway y logins, GW (URL de /api/v1).
#
# Las credenciales se generan en cada ejecucion; ninguna vive en este fichero.

rand_hex() { head -c "$1" /dev/urandom | od -An -tx1 | tr -d ' \n'; }

fallos=0
aciertos=0
ok()   { printf '  OK    %s\n' "$1"; aciertos=$((aciertos + 1)); }
mal()  { printf '  FALLA %s\n' "$1" >&2; fallos=$((fallos + 1)); }
# expect <descripcion> <obtenido> <esperado>
expect() { if [[ "$2" == "$3" ]]; then ok "$1"; else mal "$1 (obtenido: '$2', esperado: '$3')"; fi; }
# contains <descripcion> <texto> <fragmento>
contains() { if [[ "$2" == *"$3"* ]]; then ok "$1"; else mal "$1 (no contiene '$3': ${2:0:300})"; fi; }
# lacks <descripcion> <texto> <fragmento>
lacks() { if [[ "$2" != *"$3"* ]]; then ok "$1"; else mal "$1 (contiene '$3': ${2:0:300})"; fi; }

# jget <ruta.con.puntos>: extrae un campo del JSON de stdin; vacio si no existe.
jget() {
  python3 -c '
import sys, json
try:
    d = json.load(sys.stdin)
except Exception:
    print(""); sys.exit(0)
for k in sys.argv[1].split("."):
    if not k:
        continue
    if isinstance(d, list):
        d = d[int(k)] if k.isdigit() and int(k) < len(d) else None
    elif isinstance(d, dict):
        d = d.get(k)
    else:
        d = None
print("" if d is None else (json.dumps(d) if isinstance(d, (dict, list)) else d))' "$1" 2>/dev/null
}

# e2e_fuera_del_rango_efimero <puerto>...: dentro del rango efimero el kernel puede dar el
# puerto de un servicio a una conexion saliente justo antes de que el servicio lo abra
# ("address already in use" sin nadie escuchando despues).
e2e_fuera_del_rango_efimero() {
  local min max p
  read -r min max < /proc/sys/net/ipv4/ip_local_port_range 2>/dev/null || { min=32768; max=60999; }
  for p in "$@"; do
    if (( p >= min && p <= max )); then
      echo "E2E: el puerto $p cae en el rango efimero $min-$max; usa otra base de puertos" >&2
      exit 2
    fi
  done
}

# e2e_puertos_libres <puerto>...: nadie escucha ya en ellos (un choque no se ve en el log).
e2e_puertos_libres() {
  local p ocupado
  for p in "$@"; do
    ocupado=$(ss -Hltn "sport = :$p" 2>/dev/null)
    if [[ -n "$ocupado" ]]; then
      echo "E2E: el puerto $p ya esta en uso; usa otra base de puertos" >&2
      exit 2
    fi
  done
}

# e2e_reservar: una sola ejecucion por prefijo a la vez; una segunda recrearia los contenedores
# de la primera en sus mismos puertos y, al terminar, se los borraria. La reserva es el
# contenedor $E2E_PREFIX-lock (creado, nunca arrancado) con el pid de su dueno: docker no admite
# dos contenedores con el mismo nombre, asi que dos ejecuciones que arrancan a la vez no reservan
# las dos, y la reserva existe desde el principio aunque los contenedores de la prueba lleguen
# minutos despues (mail.sh construye antes sus imagenes). Se llama ANTES de instalar la limpieza,
# que si no borraria los ajenos, y con un dueno vivo sale con 3. Una reserva sin dueno vivo es el
# resto de una ejecucion cortada y se reemplaza, por id para no borrar la que otra acabe de crear.
e2e_reservar() {
  local id dueno error=""
  for _ in 1 2 3 4 5; do
    error=$(docker create --name "$E2E_PREFIX-lock" --label "cfm-e2e.pid=$$" redis:7.4.10-alpine 2>&1 >/dev/null) && return 0
    read -r id dueno < <(docker inspect -f '{{.Id}} {{index .Config.Labels "cfm-e2e.pid"}}' "$E2E_PREFIX-lock" 2>/dev/null) || continue
    if [[ "$dueno" =~ ^[0-9]+$ ]] && ps -p "$dueno" >/dev/null 2>&1; then
      echo "E2E: otra ejecucion (pid $dueno) esta usando los contenedores $E2E_PREFIX-*; espera a que termine o detenla" >&2
      echo "     (si ese pid no es una prueba, la reserva es un resto: docker rm -f $E2E_PREFIX-lock)" >&2
      exit 3
    fi
    docker rm -f "$id" >/dev/null 2>&1
  done
  echo "E2E: no se pudo reservar $E2E_PREFIX-lock: ${error:0:300}" >&2
  exit 1
}

# e2e_liberar: retira la reserva si es de esta ejecucion. Va al final de la limpieza, despues de
# borrar los contenedores: si no, la siguiente ejecucion podria crear los suyos antes.
e2e_liberar() {
  [[ "$(docker inspect -f '{{index .Config.Labels "cfm-e2e.pid"}}' "$E2E_PREFIX-lock" 2>/dev/null)" == "$$" ]] &&
    docker rm -f "$E2E_PREFIX-lock" >/dev/null 2>&1
  return 0
}

# cf_pg_pasar_entorno <VARIABLE>...: el mismo contrato que ops/db/pg-credentials.sh. Los guiones
# de ops/db pasan por aqui los secretos que su SQL lee con \getenv (un verificador SCRAM, la
# contrasena del primer superadmin) para que no viajen como argumento. Aqui, ademas de exportarlos,
# hay que apuntar SUS NOMBRES: el psql de estas pruebas corre dentro del contenedor y no hereda el
# entorno del guion, asi que sin reenviarlos \getenv dejaba la variable sin definir y el SQL se
# ejecutaba con `:'verifier'` literal (error de sintaxis en bootstrap-platform.sh y en los roles de
# celda y de motores). La lista viaja en el entorno porque quien la escribe es un proceso hijo.
cf_pg_pasar_entorno() {
  export "${@?}"
  CF_PG_ENV_NOMBRES="${CF_PG_ENV_NOMBRES:-} $*"
  export CF_PG_ENV_NOMBRES
}
export -f cf_pg_pasar_entorno

# psql sin cliente local: el de dentro del contenedor. Exportada para que la use tambien
# ops/db/bootstrap-platform.sh y ops/db/cell-service-role.sh, que son parte de lo que se prueba.
# Los secretos entran por el entorno del contenedor con solo su nombre, nunca como argumento.
psql() {
  local pasar=(-e PGPASSWORD) nombre
  export PGPASSWORD="${PGPASSWORD:-}"
  # shellcheck disable=SC2086  # son nombres de variables separados por espacios
  for nombre in ${CF_PG_ENV_NOMBRES:-}; do
    pasar+=(-e "$nombre")
  done
  docker exec -i "${pasar[@]}" "$PG_CONTAINER" psql -U "${PGUSER:-mail_admin}" "$@"
}
export -f psql
sql() { psql -v ON_ERROR_STOP=1 -q -At -d "$1" -c "$2"; }

# e2e_infra_up [red]: Postgres (pgvector), NATS y Redis desechables, publicados solo en
# 127.0.0.1. Con red, ademas se unen a ella con su nombre de contenedor para los servicios
# que corren en contenedores. La espera es por TCP: el servidor temporal del initdb solo
# escucha en el socket y responderia antes de tiempo.
e2e_infra_up() {
  local red=()
  [[ -n "${1:-}" ]] && red=(--network "$1")
  export PG_CONTAINER="$E2E_PREFIX-pg"
  docker rm -f "$E2E_PREFIX-pg" "$E2E_PREFIX-nats" "$E2E_PREFIX-redis" >/dev/null 2>&1
  export POSTGRES_PASSWORD; POSTGRES_PASSWORD="$(rand_hex 16)"
  # Una veintena de servicios, las instancias de una segunda celda y un pool por base de empresa,
  # de hasta 10 conexiones cada uno: el tope por defecto de Postgres (100) se queda corto.
  docker run -d --name "$E2E_PREFIX-pg" "${red[@]}" -e POSTGRES_USER=mail_admin -e POSTGRES_PASSWORD="$POSTGRES_PASSWORD" \
    -e POSTGRES_DB=mail_registry -p "127.0.0.1:$PG_PORT:5432" pgvector/pgvector:pg16 -c max_connections=300 >/dev/null || return 1
  docker run -d --name "$E2E_PREFIX-nats" "${red[@]}" -p "127.0.0.1:$NATS_PORT:4222" nats:2.10-alpine -js >/dev/null || return 1
  docker run -d --name "$E2E_PREFIX-redis" "${red[@]}" -p "127.0.0.1:$REDIS_PORT:6379" redis:7.4.10-alpine >/dev/null || return 1
  for _ in $(seq 1 60); do
    docker exec "$E2E_PREFIX-pg" pg_isready -h 127.0.0.1 -U mail_admin -d mail_registry >/dev/null 2>&1 && break
    sleep 1
  done
  export PGUSER=mail_admin PGPASSWORD="$POSTGRES_PASSWORD"
}

# e2e_celda <codigo>: crea mail_cell_<codigo> y le aplica las migraciones de la celda en su
# orden (platform antes que los servicios, que conceden sobre sus tablas).
e2e_celda() {
  local db="mail_cell_${1//-/_}" f
  sql mail_registry "CREATE DATABASE $db" >/dev/null || { mal "base de la celda $1"; return 1; }
  for f in migrations/cell/canonical/platform/*.sql migrations/cell/canonical/mail-directory/*.sql migrations/cell/canonical/mail-security/*.sql; do
    psql -v ON_ERROR_STOP=1 -q -d "$db" < "$f" >/dev/null 2>&1 || mal "migracion de celda $f en $db"
  done
}

# e2e_credencial_celda <codigo> <contrasena>: rol propio de la celda (ops/db/cell-service-role.sh).
e2e_credencial_celda() {
  PGHOST=127.0.0.1 CELL_DB_PASSWORD="$2" bash ops/db/cell-service-role.sh --cell "$1" >/dev/null || mal "cell-service-role.sh $1"
}

# e2e_credencial_motores <codigo> <contrasena>: rol de los motores de esa celda
# (ops/db/cell-engine-role.sh), el que usan Postfix y Dovecot en lugar del mail_engine
# compartido por todas las celdas del cluster.
e2e_credencial_motores() {
  PGHOST=127.0.0.1 MAIL_DB_PASSWORD="$2" bash ops/db/cell-engine-role.sh --cell "$1" >/dev/null ||
    mal "cell-engine-role.sh $1"
}

# e2e_credencial_enrutado <contrasena>: rol mail_router (ops/db/tenant-service-role.sh), con
# el que los servicios de empresa resuelven la celda y la base de cada empresa. Necesita las
# migraciones del registro ya aplicadas (las aplica organization al arrancar).
e2e_credencial_enrutado() {
  PGHOST=127.0.0.1 TENANT_ROUTER_DB_PASSWORD="$1" bash ops/db/tenant-service-role.sh --router >/dev/null ||
    mal "tenant-service-role.sh --router"
}

# e2e_entorno_comun: variables que comparten los binarios del host. El par de firma del token
# sale de la herramienta de operacion y la privada solo la recibe identity (arrancar).
e2e_entorno_comun() {
  local claves
  export ENVIRONMENT=development
  claves="$(bash ops/security/jwt-keygen.sh --privada "$WORK/jwt-signing-key" 2>/dev/null)" || { echo "jwt-keygen.sh fallo" >&2; return 1; }
  export JWT_SIGNING_KID JWT_PUBLIC_KEYS JWT_SIGNING_KEY
  JWT_SIGNING_KID="$(sed -n 's/^JWT_SIGNING_KID=//p' <<<"$claves")"
  JWT_PUBLIC_KEYS="$(sed -n 's/^JWT_PUBLIC_KEY_ENTRY=//p' <<<"$claves")"
  JWT_SIGNING_KEY=$(sed -n 's/^JWT_SIGNING_KEY=//p' "$WORK/jwt-signing-key")
  export INTERNAL_GATEWAY_TOKEN; INTERNAL_GATEWAY_TOKEN="$(rand_hex 24)"
  export MAIL_ENCRYPTION_KEY; MAIL_ENCRYPTION_KEY="$(rand_hex 32)"
  export MAIL_LINK_SIGNING_KEY; MAIL_LINK_SIGNING_KEY="$(rand_hex 32)"
  export CONTACTS_FORM_TOKEN_KEY; CONTACTS_FORM_TOKEN_KEY="$(rand_hex 32)"
  export API_KEY_HASH_KEY; API_KEY_HASH_KEY="$(rand_hex 32)"
  export POSTGRES_HOST=127.0.0.1 POSTGRES_PORT="$PG_PORT" POSTGRES_USER=mail_admin POSTGRES_DB=mail_registry
  export NATS_URL="nats://127.0.0.1:$NATS_PORT" REDIS_HOST=127.0.0.1 REDIS_PORT="$REDIS_PORT"
  export REGISTRY_MIGRATION_DIR="$ROOT/migrations/registry" TENANT_MIGRATION_DIR="$ROOT/migrations/tenant/canonical"
  export AUTH_COOKIE_SECURE=false PASSWORD_BREACH_CHECK=off MFA_ISSUER="Core Force Mail"
}

# e2e_compilar <servicio>...: binarios en $WORK/bin.
e2e_compilar() {
  local s
  mkdir -p "$WORK/bin" "$WORK/log"
  for s in "$@"; do go build -o "$WORK/bin/$s" "./services/$s" || { echo "no compila $s" >&2; return 1; }; done
}

# arrancar <servicio>: binario de $WORK/bin en segundo plano con registro en $WORK/log. La clave
# de firma del token solo la recibe identity; los de SERVICIOS_DE_CELDA arrancan con la
# credencial de su celda (CELL_PASS) y sin la de plataforma. Al arrancar organization deja
# exportada ORGANIZATION_URL: los servicios de celda le preguntan si cada empresa es de su celda.
arrancar() {
  local sin_firma=(-u JWT_SIGNING_KEY)
  [[ "$1" == identity ]] && sin_firma=()
  [[ "$1" == organization ]] && export ORGANIZATION_URL="http://127.0.0.1:${PORT[organization]}"
  if [[ "${SERVICIOS_DE_CELDA:-}" == *" $1 "* ]]; then
    env -u POSTGRES_PASSWORD "${sin_firma[@]}" CELL_DB_PASSWORD="$CELL_PASS" "$WORK/bin/$1" >"$WORK/log/$1.log" 2>&1 &
  elif [[ "${SERVICIOS_DE_EMPRESA:-}" == *" $1 "* ]]; then
    # Los servicios del plano de empresa resuelven la empresa en el registro con el rol de
    # enrutado (solo SELECT sobre organization.v_tenant_routing): si a alguno le hiciera
    # falta cualquier otra cosa del registro, sus comprobaciones fallarian aqui.
    env "${sin_firma[@]}" REGISTRY_DB_USER=mail_router REGISTRY_DB_PASSWORD="$ROUTER_PASS" \
      "$WORK/bin/$1" >"$WORK/log/$1.log" 2>&1 &
  else
    env "${sin_firma[@]}" "$WORK/bin/$1" >"$WORK/log/$1.log" 2>&1 &
  fi
}

# esperar_salud <nombre> <puerto>: /healthz en 127.0.0.1:<puerto>. Si falla, el final del
# registro y, si el puerto lo tiene otro proceso, cual.
esperar_salud() {
  for _ in $(seq 1 60); do
    [[ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$2/healthz")" == 200 ]] && return 0
    sleep 0.5
  done
  mal "$1 no responde en /healthz"
  [[ -f "$WORK/log/$1.log" ]] && tail -5 "$WORK/log/$1.log" >&2
  ss -ltnp 2>/dev/null | grep ":$2 " >&2
  return 1
}

# e2e_plataforma <celda>: plataforma vacia con ops/db/bootstrap-platform.sh. Deja el
# superadmin en ADMIN_EMAIL / ADMIN_PASS (aleatoria).
e2e_plataforma() {
  ADMIN_EMAIL=root@platform.test
  ADMIN_PASS="$(rand_hex 12)Aa1!"
  PGHOST=127.0.0.1 PLATFORM_ADMIN_EMAIL="$ADMIN_EMAIL" PLATFORM_ADMIN_PASSWORD="$ADMIN_PASS" \
    bash ops/db/bootstrap-platform.sh --cell "$1" --region sa-east-1 >/dev/null || mal "bootstrap-platform.sh"
}

# e2e_login <email> <contrasena>: respuesta JSON del login por el gateway.
e2e_login() {
  curl -s -X POST "$GW/auth/login" -H 'Content-Type: application/json' -d "{\"email\":\"$1\",\"password\":\"$2\"}"
}

# e2e_alta_empresa <token superadmin> <slug> <celda> <email admin> <contrasena admin>:
# POST /organizations (crea y migra la base de la empresa y siembra su tenant_admin).
e2e_alta_empresa() {
  curl -s -X POST "$GW/organizations" -H "Authorization: Bearer $1" -H 'Content-Type: application/json' \
    -d "{\"slug\":\"$2\",\"name\":\"$2\",\"cell_code\":\"$3\",\"admin_email\":\"$4\",\"admin_password\":\"$5\",\"admin_first_name\":\"Ana\",\"admin_last_name\":\"Perez\"}"
}

# e2e_registros_sin_errores [ERE]: ningun binario del host registro un error, salvo las lineas que
# casan con ERE: los errores que la prueba provoca a proposito, acotados por empresa y motivo.
e2e_registros_sin_errores() {
  local ignorar="${1:-}" f lineas limpio=1
  for f in "$WORK"/log/*.log; do
    [[ -f "$f" ]] || continue
    lineas=$(grep '"level":"error"' "$f")
    [[ -n "$ignorar" && -n "$lineas" ]] && lineas=$(grep -Ev -- "$ignorar" <<<"$lineas")
    if [[ -n "$lineas" ]]; then
      limpio=0
      mal "errores en $(basename "$f")"
      head -3 <<<"$lineas" >&2
    fi
  done
  [[ $limpio -eq 1 ]] && ok "ningun servicio del host registro errores"
  return 0
}

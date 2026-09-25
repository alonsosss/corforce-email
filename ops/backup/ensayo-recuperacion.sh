#!/usr/bin/env bash
# Ensayo de recuperación: ¿se vuelve de un desastre con SOLO lo que hay fuera del servidor?
#
#   ops/backup/ensayo-recuperacion.sh
#
# La diferencia con verify-restore.sh, que corre cada semana EN el servidor: aquel prueba que un
# volcado restaura, desde la copia local y con el Postgres de producción delante. Esto prueba lo
# otro, que es lo que hará falta el día que el servidor no esté:
#
#   * se parte del BUCKET, no del disco del servidor;
#   * en una máquina cualquiera, en contenedores desechables;
#   * sin leer nada de producción: ni su .env, ni su almacén, ni su base;
#   * con lo que vive fuera: la frase de cifrado de los respaldos, la llave de desbloqueo de OpenBao
#     con su identificador, y la credencial de despliegue del almacen. Las tres cosas, no dos: el
#     ensayo descubrio que sin la credencial no hay forma de leer una instantanea restaurada (el
#     token raiz anterior deja de valer y sys/generate-root no esta disponible con el sello estatico
#     de esta version, aunque la clave de recuperacion exista).
#
# Si con eso no se vuelve, el respaldo no vale, y eso no se sabe hasta intentarlo.
#
# Qué comprueba, por fases, midiendo cuánto tarda cada una (el dato que no teníamos: cuánto dura
# un desastre):
#
#   1. Inventario   el bucket tiene una corrida completa y reciente.
#   2. Descifrado   todo lo de esa corrida se descifra con la frase.
#   3. Bases        registro, celda y empresa restauran en un Postgres limpio, con sus esquemas,
#                   su historial de migraciones y datos de verdad.
#   4. Secretos     OpenBao arranca vacío, traga la instantánea, se desbloquea con la llave y
#                   devuelve un secreto. Sin esto, lo cifrado en la base no se descifra y lo demás
#                   no sirve.
#   5. Config      la configuración del servidor está y trae lo que hace falta para arrancar: qué
#                  celda es, qué dominio sirve y a dónde apunta cada servicio.
#   6. Correo       los volúmenes de buzones traen maildirs con mensajes, y cada buzón del volumen
#                   tiene su fila y su contraseña en la celda restaurada. Es lo que Dovecot necesita
#                   para arrancar; arrancarlo de verdad lo cubre `make e2e-mail` sobre el árbol.
#
# Lo que NO cubre, dicho para que nadie lo suponga: levantar la plataforma entera (eso es un
# despliegue, y se prueba al desplegar) y el DNS, que no está en ningún respaldo.
#
# Variables:
#   BACKUP_S3_BUCKET, BACKUP_S3_ENDPOINT, BACKUP_S3_REGION   de dónde se baja
#   BACKUP_S3_ACCESS_KEY_ID, BACKUP_S3_SECRET_ACCESS_KEY     con qué (vacías: rol de instancia)
#   BACKUP_ENCRYPTION_PASSPHRASE                             con qué se descifra
#   OPENBAO_UNSEAL_KEY, OPENBAO_UNSEAL_KEY_ID                la llave de fuera y su identificador
#   OPENBAO_ROLE_ID, OPENBAO_SECRET_ID                       la credencial de despliegue, tambien de fuera
#   ENSAYO_MAX_EDAD_H                                        antigüedad máxima admitida (24)
#
# Las tres primeras y las credenciales salen de ENSAYO_ENV_FILE si existe (por defecto
# ~/.config/core-force-mail/ensayo.env, 0600), para no escribirlas en la línea de órdenes.
set -uo pipefail
umask 077

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"

PG_IMAGE="${ENSAYO_PG_IMAGE:-postgres:17.6-alpine}"
AWS_IMAGE="${BACKUP_S3_CLI_IMAGE:-amazon/aws-cli:2.27.50}"
MAX_EDAD_H="${ENSAYO_MAX_EDAD_H:-24}"

FALLOS=0
FASE=""
T_FASE=0
mal() { echo "  FALLA: $*" >&2; FALLOS=$((FALLOS + 1)); }
ok() { echo "  OK    $*"; }
fase() {
  [[ -n "$FASE" ]] && printf '  (%s: %ds)\n\n' "$FASE" "$(($(date +%s) - T_FASE))"
  FASE="$1"
  T_FASE="$(date +%s)"
  echo "== $FASE"
}
T0="$(date +%s)"

ENV_FILE="${ENSAYO_ENV_FILE:-$HOME/.config/core-force-mail/ensayo.env}"
if [[ -f "$ENV_FILE" ]]; then
  # Solo asignaciones, sin ejecutar el fichero.
  while read -r linea; do
    # Se parte a mano y no con IFS='=': un valor en base64 termina en '=' de relleno, y read lo
    # perdia. Con la llave de desbloqueo a un caracter de la buena, OpenBao arrancaba y fallaba
    # mucho despues diciendo "illegal base64 data".
    # Los nombres llevan digitos (BACKUP_S3_*): sin ellos en la clase se ignoraban en silencio.
    k="${linea%%=*}"
    v="${linea#*=}"
    [[ "$k" =~ ^[A-Z][A-Z0-9_]*$ ]] || continue
    [[ -n "${!k:-}" ]] || printf -v "$k" '%s' "$v"
    export "${k?}"
  done < <(grep -E '^[A-Z][A-Z0-9_]*=' "$ENV_FILE")
fi

: "${BACKUP_S3_BUCKET:?falta BACKUP_S3_BUCKET (bucket de los respaldos)}"
: "${BACKUP_ENCRYPTION_PASSPHRASE:?falta BACKUP_ENCRYPTION_PASSPHRASE (la frase que vive fuera del servidor)}"
command -v docker >/dev/null || { echo "falta docker" >&2; exit 1; }
command -v gpg >/dev/null || { echo "falta gpg" >&2; exit 1; }

TMP="$(mktemp -d)"
PREFIJO="cfm-ensayo-$$"
limpiar() {
  docker rm -f "$PREFIJO-pg" >/dev/null 2>&1 || true
  rm -rf "$TMP"
}
trap limpiar EXIT

# s3 <argumentos de aws s3|s3api>: el CLI en un contenedor efímero, con el directorio de trabajo
# montado en la misma ruta. Las credenciales entran por el entorno y solo por su nombre.
s3() {
  local extra=()
  # Las credenciales viajan por NOMBRE, no por valor: con -e VAR=valor el secreto queda en la lista
  # de procesos y en docker inspect.
  export AWS_ACCESS_KEY_ID="${BACKUP_S3_ACCESS_KEY_ID:-}" AWS_SECRET_ACCESS_KEY="${BACKUP_S3_SECRET_ACCESS_KEY:-}"
  # Los S3-compatibles no aceptan todas las sumas que el CLI 2.23+ anade; tambien por nombre.
  export AWS_REQUEST_CHECKSUM_CALCULATION=when_required AWS_RESPONSE_CHECKSUM_VALIDATION=when_required
  [[ -n "${BACKUP_S3_ENDPOINT:-}" ]] && extra+=(--endpoint-url "$BACKUP_S3_ENDPOINT")
  [[ -n "${BACKUP_S3_REGION:-}" ]] && extra+=(--region "$BACKUP_S3_REGION")
  docker run --rm --network host --user "$(id -u):$(id -g)" -e HOME=/tmp \
    -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY \
    -e AWS_REQUEST_CHECKSUM_CALCULATION -e AWS_RESPONSE_CHECKSUM_VALIDATION \
    -v "$TMP:$TMP" "$AWS_IMAGE" "$@" "${extra[@]}"
}

descifrar() { # descifrar <fichero.gpg> <destino>
  local home
  home="$(mktemp -d)"
  printf '%s' "$BACKUP_ENCRYPTION_PASSPHRASE" |
    gpg --homedir "$home" --batch --yes --quiet --no-tty --pinentry-mode loopback --passphrase-fd 0 \
      --decrypt -o "$2" "$1"
  local rc=$?
  rm -rf "$home"
  return $rc
}

# ── 1. Inventario ────────────────────────────────────────────────────────────────────────────────
fase "1. Inventario del bucket"
LISTA="$TMP/lista.txt"
if ! s3 s3 ls --recursive "s3://$BACKUP_S3_BUCKET/" >"$LISTA" 2>"$TMP/s3.err"; then
  echo "  FALLA: no se pudo listar s3://$BACKUP_S3_BUCKET: $(head -2 "$TMP/s3.err")" >&2
  exit 1
fi
# Solo lo que tiene forma de volcado de una corrida: postgres/<base>/<sello>.dump[.gpg] con el
# sello completo. Cualquier otra cosa en el bucket -una prueba, un resto- se ignora; si no, el
# "ultimo sello" podia ser un fichero suelto y el ensayo se quedaba sin bases sin decir por que.
awk '{print $4}' "$LISTA" | grep -E '^postgres/[^/]+/[0-9]{8}T[0-9]{6}Z\.dump(\.gpg)?$' |
  sed -E 's#^postgres/[^/]+/##; s#\.dump(\.gpg)?$##' | sort -u >"$TMP/sellos.txt"
SELLO="$(tail -1 "$TMP/sellos.txt")"
[[ -n "$SELLO" ]] || { echo "  FALLA: el bucket no tiene ningun volcado en postgres/" >&2; exit 1; }
BASES="$(awk '{print $4}' "$LISTA" | grep -E "^postgres/[^/]+/$SELLO\.dump(\.gpg)?$" | sed -E 's#^postgres/([^/]+)/.*#\1#' | sort -u)"
[[ -n "$BASES" ]] || { echo "  FALLA: la corrida $SELLO no tiene ninguna base" >&2; exit 1; }
ok "corrida $SELLO con $(wc -w <<<"$BASES") base(s): $(tr '\n' ' ' <<<"$BASES")"

edad_h() { # edad en horas de la corrida, por el sello 20260924T191502Z
  local s="$1" y m d H M S
  y=${s:0:4} m=${s:4:2} d=${s:6:2} H=${s:9:2} M=${s:11:2} S=${s:13:2}
  echo $((($(date -u +%s) - $(date -u -d "$y-$m-$d $H:$M:$S" +%s)) / 3600))
}
EDAD="$(edad_h "$SELLO")"
if [[ "$EDAD" -gt "$MAX_EDAD_H" ]]; then
  mal "el respaldo mas reciente tiene $EDAD h (el maximo admitido es $MAX_EDAD_H)"
else
  ok "antiguedad $EDAD h"
fi
grep -qx mail_registry <<<"$BASES" || mal "la corrida no trae mail_registry, sin el no hay nada que levantar"
grep -qE "^openbao/$SELLO\.snap" <<<"$(awk '{print $4}' "$LISTA")" ||
  mal "la corrida no trae la instantanea de OpenBao; las credenciales cifradas no se podrian descifrar"
VOLS="$(awk '{print $4}' "$LISTA" | grep -E '^correo/' | sed -E 's#^correo/([^/]+)/.*#\1#' | sort -u)"
[[ -n "$VOLS" ]] || mal "el bucket no tiene volumenes de correo en correo/"

# ── 2. Descarga y descifrado ─────────────────────────────────────────────────────────────────────
fase "2. Descarga y descifrado"
mkdir -p "$TMP/bajado"
# bajar <clave> <nombre local>: el nombre lo pone quien llama porque todas las claves de una corrida
# comparten sello (postgres/<base>/<sello>.dump), y por su basename se pisarian entre si.
bajar() {
  local clave="$1" destino="$TMP/bajado/$2" crudo="$TMP/bajado/$2.cifrado"
  if [[ "$clave" == *.gpg ]]; then
    s3 s3 cp --only-show-errors "s3://$BACKUP_S3_BUCKET/$clave" "$crudo" >/dev/null 2>&1 || return 1
    descifrar "$crudo" "$destino" || { rm -f "$crudo"; return 1; }
    rm -f "$crudo"
  else
    s3 s3 cp --only-show-errors "s3://$BACKUP_S3_BUCKET/$clave" "$destino" >/dev/null 2>&1 || return 1
  fi
  [[ -s "$destino" ]]
}
CLAVES="$(awk '{print $4}' "$LISTA")"
for db in $BASES; do
  clave="$(grep -E "^postgres/$db/$SELLO\.dump(\.gpg)?$" <<<"$CLAVES" | head -1)"
  if bajar "$clave" "$db.dump"; then ok "$db descifrado"; else mal "$db no se pudo bajar o descifrar"; fi
done
clave="$(grep -E "^openbao/$SELLO\.snap(\.gpg)?$" <<<"$CLAVES" | head -1)"
if [[ -n "$clave" ]]; then
  if bajar "$clave" "openbao.snap"; then ok "instantanea de OpenBao descifrada"; else mal "la instantanea de OpenBao no se descifra"; fi
fi
for vol in $VOLS; do
  clave="$(grep -E "^correo/$vol/" <<<"$CLAVES" | sort | tail -1)"
  [[ -n "$clave" ]] || continue
  if bajar "$clave" "$vol.tar.gz"; then ok "volumen $vol descifrado"; else mal "el volumen $vol no se descifra"; fi
done

# ── 3. Bases en un Postgres limpio ───────────────────────────────────────────────────────────────
fase "3. Bases en un Postgres limpio"
# La contrasena del Postgres desechable, por nombre y no por valor, como cualquier otra.
POSTGRES_PASSWORD="$(openssl rand -hex 16)"
export POSTGRES_PASSWORD PGPASSWORD="$POSTGRES_PASSWORD"
docker run -d --name "$PREFIJO-pg" -e POSTGRES_PASSWORD -e POSTGRES_DB=postgres \
  -v "$TMP/bajado:/respaldo:ro" "$PG_IMAGE" >/dev/null 2>&1 ||
  { echo "  FALLA: no arranca el Postgres de prueba" >&2; exit 1; }
for _ in $(seq 1 60); do
  docker exec "$PREFIJO-pg" pg_isready -U postgres -q && break
  sleep 1
done
docker exec "$PREFIJO-pg" pg_isready -U postgres -q || { echo "  FALLA: el Postgres de prueba no responde" >&2; exit 1; }
# sql(): el cliente DENTRO del Postgres desechable. No pasa por cf_psql a proposito: este ensayo
# no habla con la base de produccion ni con sus credenciales, esa es justo la idea.
sql() { docker exec -e PGPASSWORD "$PREFIJO-pg" psql -U postgres -v ON_ERROR_STOP=1 "$@"; }

for db in $BASES; do
  fichero="/respaldo/$db.dump"
  [[ -s "$TMP/bajado/$db.dump" ]] || { mal "$db no llego a descifrarse; no se restaura"; continue; }
  sql -c "DROP DATABASE IF EXISTS $db" >/dev/null 2>&1
  sql -c "CREATE DATABASE $db" >/dev/null 2>&1 || { mal "no se pudo crear $db"; continue; }
  if docker exec -e PGPASSWORD "$PREFIJO-pg" \
    pg_restore -U postgres -d "$db" --no-owner --no-privileges "$fichero" >"$TMP/restore-$db.log" 2>&1; then
    ok "$db restaurada"
  else
    # pg_restore avisa de objetos que no existen; se juzga por el contenido, no por su salida.
    ok "$db restaurada con avisos ($(grep -c . "$TMP/restore-$db.log") lineas)"
  fi
  esquemas="$(sql -d "$db" -Atc "SELECT count(*) FROM information_schema.schemata WHERE schema_name NOT LIKE 'pg\\_%' AND schema_name <> 'information_schema'")"
  [[ "${esquemas:-0}" -ge 2 ]] || mal "$db restaurada sin esquemas propios ($esquemas)"
  migraciones="$(sql -d "$db" -Atc "SELECT count(*) FROM public.schema_migrations" 2>/dev/null || echo 0)"
  if [[ "$db" == mail_cell_* ]]; then
    ok "$db: $esquemas esquemas (la celda no deja historial de migraciones)"
  elif [[ "${migraciones:-0}" -gt 0 ]]; then
    ok "$db: $esquemas esquemas, $migraciones migraciones"
  else
    mal "$db no trae historial de migraciones: el volcado es de esquema, no de datos"
  fi
done
USUARIOS="$(sql -d mail_registry -Atc "SELECT count(*) FROM identity.users" 2>/dev/null || echo 0)"
[[ "${USUARIOS:-0}" -gt 0 ]] && ok "el registro trae $USUARIOS usuario(s)" || mal "el registro restaurado no tiene usuarios"

# ── 4. Secretos ──────────────────────────────────────────────────────────────────────────────────
fase "4. Secretos (OpenBao)"
if [[ ! -s "$TMP/bajado/openbao.snap" ]]; then
  mal "no hay instantanea de OpenBao descifrada que comprobar"
elif [[ -z "${OPENBAO_UNSEAL_KEY:-}" || -z "${OPENBAO_UNSEAL_KEY_ID:-}" ]]; then
  mal "faltan OPENBAO_UNSEAL_KEY y/o OPENBAO_UNSEAL_KEY_ID: la llave de desbloqueo y su identificador
       viven FUERA del servidor y hacen falta los dos para descifrar la instantanea"
elif [[ -z "${OPENBAO_ROLE_ID:-}" || -z "${OPENBAO_SECRET_ID:-}" ]]; then
  mal "faltan OPENBAO_ROLE_ID y OPENBAO_SECRET_ID: con la instantanea restaurada, el token raiz
       anterior ya no vale y sys/generate-root no esta disponible en esta version, asi que la
       credencial de despliegue es lo UNICO que abre el almacen. Tiene que vivir fuera del servidor"
else
  # La herramienta de verificacion espera la llave en un directorio, como en el servidor; aqui se
  # materializa desde las variables, que es lo unico que habria el dia del desastre. OPENBAO_CRED_DIR
  # apunta al mismo sitio a proposito: sin credenciales de AppRole, la lectura va con el token raiz
  # de la instancia desechable.
  mkdir -m 0700 -p "$TMP/secretos/openbao-llave"
  # La llave son 32 bytes en crudo; fuera del servidor viaja en base64. Si no descodifica, se para:
  # una llave a medias arranca OpenBao y falla mucho despues, que es peor que no arrancar.
  if ! printf '%s' "$OPENBAO_UNSEAL_KEY" | base64 -d >"$TMP/secretos/openbao-llave/desbloqueo.key" 2>/dev/null; then
    mal "OPENBAO_UNSEAL_KEY no es base64 valido; la llave de desbloqueo son 32 bytes en base64"
  fi
  printf '%s' "$OPENBAO_UNSEAL_KEY_ID" >"$TMP/secretos/openbao-llave/id"
  chmod 600 "$TMP/secretos/openbao-llave/desbloqueo.key" "$TMP/secretos/openbao-llave/id"
  mkdir -m 0700 -p "$TMP/secretos/openbao"
  printf '%s' "$OPENBAO_ROLE_ID" >"$TMP/secretos/openbao/despliegue.role_id"
  printf '%s' "$OPENBAO_SECRET_ID" >"$TMP/secretos/openbao/despliegue.secret_id"
  chmod 600 "$TMP/secretos/openbao/despliegue".*
  if SECRETS_DIR="$TMP/secretos" OPENBAO_CRED_DIR="$TMP/secretos/openbao" \
    bash "$ROOT/ops/security/openbao/verificar-instantanea.sh" "$TMP/bajado/openbao.snap" >"$TMP/openbao.log" 2>&1; then
    ok "$(tail -1 "$TMP/openbao.log")"
  else
    mal "la instantanea de OpenBao no devuelve secretos: $(tail -2 "$TMP/openbao.log" | tr '\n' ' ')"
  fi
fi

# ── 5. Correo ────────────────────────────────────────────────────────────────────────────────────
fase "5. Configuracion del servidor"
CFG="$(grep -E "^config/" <<<"$CLAVES" | sort | tail -1)"
if [[ -z "$CFG" ]]; then
  mal "el bucket no trae la configuracion del servidor (config/): los datos volverian, pero no se
       sabria que celda es, que dominio sirve ni a donde apunta cada servicio"
elif ! bajar "$CFG" "config.tar.gz"; then
  mal "la configuracion no se pudo bajar o descifrar"
else
  mkdir -p "$TMP/config"
  if tar -xzf "$TMP/bajado/config.tar.gz" -C "$TMP/config" 2>/dev/null; then
    n_env=0
    for f in "$TMP/config"/*.env; do [[ -s "$f" ]] && n_env=$((n_env + 1)); done
    [[ "$n_env" -gt 0 ]] && ok "$n_env fichero(s) de configuracion" || mal "el paquete de configuracion viene vacio"
    # Lo minimo para saber que levantar: el perfil, la celda y el nombre del servidor de correo.
    for clave_cfg in DEPLOY_PROFILE CELL_DB_NAME MAIL_HOSTNAME; do
      if grep -qhE "^$clave_cfg=." "$TMP/config"/*.env 2>/dev/null; then
        ok "la configuracion trae $clave_cfg"
      else
        mal "la configuracion no trae $clave_cfg: sin eso no se sabe que levantar"
      fi
    done
  else
    mal "el paquete de configuracion no se puede desempaquetar"
  fi
fi

fase "6. Correo (buzones y su directorio)"
CELDA="$(tr ' ' '\n' <<<"$BASES" | grep -E '^mail_cell_' | head -1)"
VMAIL=""
for f in "$TMP/bajado"/vmail*.tar.gz; do [[ -s "$f" ]] && { VMAIL="$(basename "$f")"; break; }; done
if [[ -z "$VMAIL" ]]; then
  mal "el respaldo no trae el volumen de buzones (vmail): sin el no hay correo que recuperar"
else
  mkdir -p "$TMP/vmail"
  if tar -xzf "$TMP/bajado/$VMAIL" -C "$TMP/vmail" 2>"$TMP/tar.err"; then
    maildirs="$(find "$TMP/vmail" -type d -name cur | wc -l)"
    mensajes="$(find "$TMP/vmail" -type f -path '*/cur/*' | wc -l)"
    [[ "$maildirs" -gt 0 ]] && ok "$maildirs buzon(es) en el volumen, con $mensajes mensaje(s)" ||
      mal "el volumen de buzones no trae ningun maildir"
    # Cada buzon del volumen tiene que existir en la celda restaurada y traer su contrasena: es lo
    # que Dovecot necesita para dejar entrar a su dueno.
    if [[ -n "$CELDA" ]]; then
      faltan=0
      declare -A vistos=()
      # El volumen guarda /<dominio>/<buzon>/..., asi que el buzon son las DOS primeras carpetas
      # bajo la raiz extraida; lo de mas adentro son sus carpetas de correo (.Sent, .Junk).
      while read -r dir; do
        rel="${dir#"$TMP/vmail/"}"
        dominio="${rel%%/*}"
        resto="${rel#*/}"
        usuario="${resto%%/*}"
        buzon="$usuario@$dominio"
        [[ "$dominio" == *.* && -n "$usuario" && "$usuario" != "$dominio" ]] || continue
        # Un buzon tiene varias carpetas (.Sent, .Junk): se comprueba una vez por buzon.
        [[ -n "${vistos[$buzon]:-}" ]] && continue
        vistos[$buzon]=1
        n="$(sql -d "$CELDA" -Atc "SELECT count(*) FROM mail.mailboxes WHERE username = '${buzon//\'/}' AND length(password_hash) > 10" 2>/dev/null || echo 0)"
        [[ "${n:-0}" -ge 1 ]] || { faltan=$((faltan + 1)); echo "    sin fila util en la celda: $buzon" >&2; }
      done < <(find "$TMP/vmail" -type d -name cur -printf '%h\n' | sort -u | head -50 | sort -u)
      if [[ "$faltan" -eq 0 ]]; then
        ok "los ${#vistos[@]} buzones del volumen tienen fila y contrasena en $CELDA"
      else
        mal "$faltan de ${#vistos[@]} buzon(es) del volumen no tienen fila util en $CELDA (ojo si el volumen y la base se respaldaron en momentos distintos)"
      fi
    else
      mal "la corrida no trae ninguna base de celda: los buzones no tendrian directorio"
    fi
  else
    mal "el volumen de buzones no se puede desempaquetar: $(head -1 "$TMP/tar.err")"
  fi
fi

fase "fin"
TOTAL=$(($(date +%s) - T0))
echo
if [[ "$FALLOS" -eq 0 ]]; then
  echo "ENSAYO DE RECUPERACION: OK en ${TOTAL}s, partiendo solo del bucket y de lo que vive fuera."
  exit 0
fi
echo "ENSAYO DE RECUPERACION: $FALLOS FALLO(S) en ${TOTAL}s. El respaldo NO basta para volver." >&2
exit 1

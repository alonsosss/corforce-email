#!/usr/bin/env bash
# Prueba con docker de los respaldos en el perfil de produccion autoalojada: levanta postgres-primary
# con docker-compose.yml + docker-compose.selfhosted.yml, en ENVIRONMENT=production, con el TLS de
# ops/security/internal-tls.sh y sin puerto publicado en el host, como en el servidor, y ejecuta los
# guiones de ops/backup desde una copia del arbol desplegado (APP_DIR con su .env):
#   - las tres clases de base (registro, celda y empresa, con sus migraciones reales) se respaldan
#     con la herramienta de la imagen del servidor, por TLS verify-full, y sin CLI de AWS ni
#     cliente de Postgres en el host;
#   - con otra CA el respaldo no conecta (se verifica el certificado, no solo se cifra);
#   - los volcados quedan 0600 en un directorio 0700, con su suma, y la metrica registra el exito;
#   - verify-restore.sh sin argumentos restaura y comprueba una base de cada clase;
#   - restore-tenant.sh restaura una empresa a una base nueva con sus filas;
#   - un volcado alterado se detecta por su suma, y uno truncado sin suma, al restaurarlo;
#   - con destino S3-compatible (MinIO desechable): sube cifrado, no sube sin frase ni con el
#     fichero de secretos legible por otros (y aun asi respalda en local), y restore-tenant.sh
#     restaura desde el bucket;
#   - los volumenes de correo (buzones y claves de mail_crypt): se archivan en caliente con los
#     uid de Dovecot, sin _garbage, con su suma, la clave comprobada contra su publica y cifrados
#     al salir; una clave que no corresponde se detecta; restore-mail-volume.sh devuelve el
#     volumen (tambien desde el bucket) con su contenido intacto; el de MinIO del perfil
#     (minio-data) entra con el uid de minio, sin .minio.sys/tmp y cifrado al salir;
#   - los guiones de ops/db alcanzan la base con el mismo camino, y ninguno se queda con la
#     entrada estandar del guion que los llama (tenant-service-role.sh --all crea el rol de
#     enrutado y el de cada servicio de empresa, no solo el primero del bucle):
#     apply-migration.sh aplica una
#     canonica, cell-service-role.sh y cell-engine-role.sh crean los roles de la celda (que
#     inician sesion de verdad con su contrasena) y bootstrap-platform.sh deja el primer
#     superadmin, sin que la contrasena ni el verificador SCRAM aparezcan en la linea de ordenes
#     de ningun contenedor;
#   - install-timers.sh reescribe usuario y ruta de las unidades y es idempotente.
#
#   bash ops/scaffold/test-selfhosted-backup.sh     # RESPALDO_KEEP=1 deja todo en pie
#
# Proyecto de compose propio (RESPALDO_TEST_PROJECT) y MinIO solo en 127.0.0.1
# (RESPALDO_TEST_PORT, por defecto 47590). Una sola ejecucion a la vez: otra sale con 3.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
PROYECTO="${RESPALDO_TEST_PROJECT:-cfm-respaldo-prueba}"
PUERTO_S3="${RESPALDO_TEST_PORT:-47590}"
IMG_PG="$(awk '/^  postgres-primary:/{d=1;next} d&&/^    image:/{print $2;exit}' "$ROOT/docker-compose.yml")"
# La misma imagen que el servicio minio del perfil (minio/minio ya no se publica en Docker Hub).
IMG_MINIO="${RESPALDO_TEST_MINIO_IMAGE:-$(awk '/^  minio:/{d=1;next} d&&/^    image:/{print $2;exit}' "$ROOT/docker-compose.selfhosted.yml")}"

CERROJO="${TMPDIR:-/tmp}/$PROYECTO.lock"
exec 9>"$CERROJO"
if ! flock -n 9; then
  echo "test-selfhosted-backup: otra ejecucion (pid $(cat "$CERROJO.pid" 2>/dev/null || echo '?')) usa el proyecto $PROYECTO" >&2
  exit 3
fi
echo $$ >"$CERROJO.pid"

W="$(mktemp -d "${TMPDIR:-/tmp}/$PROYECTO.XXXXXX")"
APP="$W/app"
RESPALDOS="$W/respaldos"
METRICAS="$W/metricas"
MINIO="$PROYECTO-minio"
FALLOS=0
ok() { echo "  OK: $*"; }
mal() { echo "  FALLA: $*" >&2; FALLOS=1; }

compose() { docker compose -p "$PROYECTO" --project-directory "$APP" --env-file "$APP/.env" \
  -f "$APP/docker-compose.yml" -f "$APP/docker-compose.selfhosted.yml" -f "$APP/prueba.yml" "$@"; }

limpiar() {
  local rc=$?
  if [[ "${RESPALDO_KEEP:-0}" == 1 ]]; then
    echo "RESPALDO_KEEP=1: se deja en pie el proyecto $PROYECTO ($W)"
  else
    compose down -v --remove-orphans >/dev/null 2>&1 || true
    docker rm -f "$MINIO" >/dev/null 2>&1 || true
    docker volume ls -q --filter "name=^${PROYECTO}_" | xargs -r docker volume rm -f >/dev/null 2>&1 || true
    docker run --rm -v "$W:/w" --entrypoint sh "$IMG_PG" -c 'rm -rf /w/tls /w/instalar /w/otra-ca' >/dev/null 2>&1 || true
    rm -rf "$W"
  fi
  rm -f "$CERROJO.pid"
  exit $rc
}
trap limpiar EXIT

aleatorio() { openssl rand -hex "$1"; }

echo "== Preparacion (proyecto $PROYECTO) =="
mkdir -p "$APP/ops" "$METRICAS" "$W/bin"
cp "$ROOT/docker-compose.yml" "$ROOT/docker-compose.selfhosted.yml" "$APP/"
cp -r "$ROOT/selfhosted" "$ROOT/pgbouncer" "$ROOT/migrations" "$APP/"
cp -r "$ROOT/ops/backup" "$ROOT/ops/db" "$ROOT/ops/maintenance" "$ROOT/ops/security" "$APP/ops/"
rm -f "$APP/pgbouncer/userlist.txt"

# En el servidor no hay CLI de AWS: el almacen no responde y load.sh recurre al .env. Un `aws` que
# falla lo reproduce y evita que la prueba lea el almacen real con las credenciales de esta maquina.
printf '#!/bin/sh\nexit 1\n' >"$W/bin/aws"
chmod +x "$W/bin/aws"
for herramienta in psql pg_dump pg_restore; do
  if command -v "$herramienta" >/dev/null; then
    echo "  nota: esta maquina tiene $herramienta; los guiones del perfil no lo usan (se comprueba abajo)"
  fi
done

POSTGRES_PASSWORD="$(aleatorio 24)"
umask 077
{
  echo "ENVIRONMENT=production"
  echo "TZ=UTC"
  echo "DEPLOY_PROFILE=selfhosted"
  echo "COMPOSE_PROJECT_NAME=$PROYECTO"
  echo "POSTGRES_HOST=postgres"
  echo "POSTGRES_PORT=5432"
  echo "POSTGRES_USER=mail_admin"
  echo "POSTGRES_DB=mail_registry"
  echo "INTERNAL_TLS_DIR=$W/tls"
  echo "BACKUP_DIR=$RESPALDOS"
  echo "BACKUP_KEEP_DAYS=3"
  echo "BACKUP_SECRETS_FILE=$W/backup.env"
  # Sin almacen, el .env del servidor lleva TODAS las credenciales obligatorias del inventario.
  for clave in $(sed -e '/^#/d' -e '/^$/d' -e '/?$/d' "$ROOT/ops/security/secrets/secret-keys.txt" "$ROOT/ops/security/secrets/secret-keys-db.txt"); do
    if [[ "$clave" == POSTGRES_PASSWORD ]]; then
      echo "POSTGRES_PASSWORD=$POSTGRES_PASSWORD"
    else
      echo "$clave=$(aleatorio 24)"
    fi
  done
} >"$APP/.env"
umask 022
cat >"$APP/prueba.yml" <<'EOF'
services:
  postgres-primary:
    ports: !reset []
EOF

# Envoltorio de docker que registra la linea de ordenes de cada invocacion y sigue adelante: con el
# se comprueba que ningun secreto viaja como argumento de un contenedor efimero.
mkdir -p "$W/shim"
cat >"$W/shim/docker" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$DOCKER_ARGV_LOG"
exec /usr/bin/docker "$@"
EOF
chmod +x "$W/shim/docker"
ARGV_LOG="$W/docker-argv.log"
: >"$ARGV_LOG"

# Guiones como en el servidor: desde el arbol desplegado, con su .env, sin variables de la prueba.
guion() {
  ops_guion "ops/backup/$1" "${@:2}"
}

# ops_guion <ruta bajo APP_DIR> [args...]: como los ejecuta el servidor. Las variables de entorno
# extra se pasan en el array CF_ENV_PRUEBA (NOMBRE=valor), nunca por argumento.
ops_guion() {
  env -i PATH="$W/shim:$W/bin:/usr/local/bin:/usr/bin:/bin" HOME="$HOME" APP_DIR="$APP" \
    CF_METRICS_DIR="$METRICAS" DOCKER_ARGV_LOG="$ARGV_LOG" \
    ${DOCKER_HOST:+DOCKER_HOST="$DOCKER_HOST"} "${CF_ENV_PRUEBA[@]}" bash "$APP/$1" "${@:2}"
}
CF_ENV_PRUEBA=()

duenos="$(bash "$ROOT/ops/security/internal-tls.sh" --solo-detectar | paste -sd,)"
docker run --rm -v "$ROOT:/repo:ro" -v "$W:/w" "$IMG_PG" bash /repo/ops/security/internal-tls.sh \
  --dir /w/tls --proyecto "$PROYECTO" --duenos "$duenos" >/dev/null
ok "TLS interno emitido con ops/security/internal-tls.sh"

echo "== postgres-primary del perfil, sin puerto en el host =="
compose up -d postgres-primary >/dev/null 2>"$W/up.log" || { cat "$W/up.log" >&2; exit 1; }
bash "$ROOT/ops/maintenance/esperar-sanos.sh" --proyecto "$PROYECTO" --plazo 120 postgres-primary >/dev/null ||
  { mal "postgres-primary no arranco"; exit 1; }
PG="$(compose ps -q postgres-primary)"
RED_INTERNA="$(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{println $k}}{{end}}' "$PG" | sed '/^$/d' | head -1)"
[[ -z "$(docker port "$PG")" ]] && ok "postgres-primary sin puertos publicados en el host" || mal "postgres-primary publica puertos: $(docker port "$PG")"

sql() { docker exec -i "$PG" psql -v ON_ERROR_STOP=1 -q -At -U mail_admin -d "$1" "${@:2}"; }

# Migraciones como las aplica organization (registro y empresa: orden global por numero y nombre,
# una transaccion y una fila en public.schema_migrations cada una) y como las aplica la celda.
migrar_con_historial() {
  local db="$1" dir="$2" nombre
  sql "$db" -c "CREATE TABLE IF NOT EXISTS public.schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())"
  while IFS= read -r nombre; do
    { cat "$dir/$nombre"; printf '\nINSERT INTO public.schema_migrations (name) VALUES (%s);\n' "'$nombre'"; } |
      sql "$db" -1 >/dev/null 2>"$W/migracion.err" || { mal "migracion $nombre en $db: $(head -3 "$W/migracion.err")"; return 1; }
  done < <(cd "$dir" && find . -name '*.sql' -printf '%P\n' | python3 -c '
import re, sys
def orden(n):
    m = re.search(r"(^|/)(\d+)_", n)
    return (int(m.group(2)) if m else 999999, n)
print("\n".join(sorted((l.strip() for l in sys.stdin if l.strip()), key=orden)))')
}

sql mail_registry -c "CREATE DATABASE mail_cell_prueba" -c "CREATE DATABASE mail_tenant_prueba"
migrar_con_historial mail_registry "$ROOT/migrations/registry"
for f in "$ROOT"/migrations/cell/canonical/platform/*.sql "$ROOT"/migrations/cell/canonical/mail-directory/*.sql "$ROOT"/migrations/cell/canonical/mail-security/*.sql; do
  sql mail_cell_prueba -1 <"$f" >/dev/null 2>"$W/migracion.err" || mal "migracion de celda $f: $(head -3 "$W/migracion.err")"
done
migrar_con_historial mail_tenant_prueba "$ROOT/migrations/tenant/canonical"
MARCA="$(aleatorio 8)"
sql mail_registry -c "INSERT INTO identity.users (tenant_id, email, password_hash, first_name, last_name)
  VALUES (gen_random_uuid(), 'superadmin@prueba.test', 'x', 'Super', 'Admin')"
sql mail_tenant_prueba -c "INSERT INTO platform.event_outbox (id, subject, payload) VALUES (gen_random_uuid(), 'prueba.respaldo.marca', '{\"marca\":\"$MARCA\"}')"
[[ $FALLOS -eq 0 ]] && ok "registro, celda y empresa creados con sus migraciones reales y una fila marcada en la empresa"

echo "== Respaldo local =="
if guion backup-tenants.sh >"$W/respaldo1.log" 2>&1; then
  ok "backup-tenants.sh termina bien ($(tail -1 "$W/respaldo1.log"))"
else
  cat "$W/respaldo1.log" >&2
  mal "backup-tenants.sh fallo"
  exit 1
fi
CORRIDA="$(find "$RESPALDOS" -mindepth 1 -maxdepth 1 -type d -name '*T*Z' | sort | tail -1)"
for db in mail_registry mail_cell_prueba mail_tenant_prueba; do
  [[ -s "$CORRIDA/$db.dump" && -s "$CORRIDA/$db.dump.sha256" ]] && ok "volcado y suma de $db" || mal "falta el volcado o la suma de $db"
done
permisos="$(stat -c '%a' "$CORRIDA" "$CORRIDA"/*.dump | sort -u | paste -sd' ')"
[[ "$permisos" == "600 700" ]] && ok "volcados 0600 en un directorio 0700" || mal "permisos de los volcados: $permisos"
version_volcado="$(head -c 12 "$CORRIDA/mail_tenant_prueba.dump" | od -An -tu1 | awk '{print $6"."$7}')"
version_servidor="$(docker exec "$PG" pg_restore --version | grep -oE '[0-9]+' | head -1)"
if docker run --rm -v "$CORRIDA:/r:ro" --entrypoint pg_restore "$IMG_PG" --list /r/mail_tenant_prueba.dump 2>&1 | grep -q "Dumped by pg_dump version: $version_servidor\."; then
  ok "el volcado lo hizo el pg_dump $version_servidor del servidor (formato $version_volcado) y lo lee el pg_restore de su imagen"
else
  mal "el volcado no es del pg_dump del servidor ($version_servidor)"
fi
grep -q '^core_force_job_last_success_timestamp_seconds{trabajo="respaldo"}' "$METRICAS/core_force_respaldo.prom" &&
  grep -q '^core_force_job_units{trabajo="respaldo"} 3$' "$METRICAS/core_force_respaldo.prom" &&
  ok "metrica del respaldo: exito con 3 bases" || mal "metrica del respaldo: $(cat "$METRICAS/core_force_respaldo.prom" 2>&1)"
if grep -qF "$POSTGRES_PASSWORD" "$W/respaldo1.log"; then mal "la contrasena aparece en la salida"; else ok "la contrasena no aparece en la salida"; fi

tls_de_la_herramienta="$(env -i PATH="$W/bin:/usr/bin:/bin" HOME="$HOME" APP_DIR="$APP" bash -c \
  '. "$APP_DIR/ops/db/pg-credentials.sh" >/dev/null 2>&1 && echo "$PGSSLMODE" && cf_psql -d postgres -Atc "select ssl, version from pg_stat_ssl where pid = pg_backend_pid()"' 2>&1 || true)"
[[ "$(head -1 <<<"$tls_de_la_herramienta")" == verify-full ]] && grep -qxE 't\|TLSv1\.[23]' <<<"$tls_de_la_herramienta" && ok "las herramientas conectan con verify-full y TLS ($(tr '\n' ' ' <<<"$tls_de_la_herramienta"))" ||
  mal "TLS de las herramientas: $tls_de_la_herramienta"

# Con otra CA el certificado del servidor no se verifica y no hay respaldo.
docker run --rm -v "$W:/w" --entrypoint bash "$IMG_PG" -c 'set -e; mkdir -p /w/otra-ca/publico; cd /w/otra-ca/publico
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 1 -subj "/CN=otra" -keyout /dev/null -out ca.crt 2>/dev/null
  chmod -R a+rX /w/otra-ca'
sed -i "s|^INTERNAL_TLS_DIR=.*|INTERNAL_TLS_DIR=$W/otra-ca|" "$APP/.env"
if guion backup-tenants.sh mail_registry >"$W/otra-ca.log" 2>&1; then
  mal "con otra CA el respaldo conecto"
else
  grep -qi 'certificate verify failed\|certificado' "$W/otra-ca.log" && ok "con otra CA el respaldo no conecta (certificate verify failed)" ||
    mal "con otra CA fallo por otro motivo: $(grep -m2 FALLA "$W/otra-ca.log")"
fi
sed -i "s|^INTERNAL_TLS_DIR=.*|INTERNAL_TLS_DIR=$W/tls|" "$APP/.env"
find "$RESPALDOS" -mindepth 1 -maxdepth 1 -type d -newer "$CORRIDA" -exec rm -rf {} +

echo "== Verificacion de restauracion =="
if guion verify-restore.sh >"$W/verificacion.log" 2>&1; then
  grep -q 'VERIFICACIÓN COMPLETA OK: mail_registry mail_cell_prueba mail_tenant_prueba' "$W/verificacion.log" &&
    ok "verify-restore.sh restaura y comprueba registro, celda y empresa ($(tail -1 "$W/verificacion.log"))" ||
    mal "verify-restore.sh no verifico las tres clases: $(tail -3 "$W/verificacion.log")"
else
  cat "$W/verificacion.log" >&2
  mal "verify-restore.sh fallo con respaldos buenos"
fi
grep -q '^core_force_job_last_success_timestamp_seconds{trabajo="verificacion_respaldo"}' "$METRICAS/core_force_verificacion_respaldo.prom" &&
  ok "metrica de la verificacion: exito" || mal "metrica de la verificacion sin exito"
[[ "$(sql mail_registry -c "SELECT count(*) FROM pg_database WHERE datname LIKE 'verify_restore_%'")" == 0 ]] &&
  ok "no quedan bases desechables" || mal "quedan bases verify_restore_*"

echo "== Restauracion de una empresa a una base nueva =="
if guion restore-tenant.sh mail_tenant_prueba --into mail_tenant_prueba_rescate >"$W/restauracion.log" 2>&1; then
  [[ "$(sql mail_tenant_prueba_rescate -c "SELECT payload->>'marca' FROM platform.event_outbox WHERE subject = 'prueba.respaldo.marca'")" == "$MARCA" ]] &&
    ok "restore-tenant.sh restaura la empresa con su fila marcada en mail_tenant_prueba_rescate" || mal "la base restaurada no trae la fila marcada"
else
  cat "$W/restauracion.log" >&2
  mal "restore-tenant.sh fallo"
fi

echo "== Volcados dañados =="
DUMP_EMPRESA="$CORRIDA/mail_tenant_prueba.dump"
cp -p "$DUMP_EMPRESA" "$W/empresa.dump.bueno"
tam="$(stat -c %s "$DUMP_EMPRESA")"
printf 'XXXXXXXXXXXXXXXX' | dd of="$DUMP_EMPRESA" bs=1 seek=$((tam / 2)) conv=notrunc status=none
if guion verify-restore.sh mail_tenant_prueba >"$W/alterado.log" 2>&1; then
  mal "verify-restore.sh dio por bueno un volcado alterado"
else
  grep -q 'no coincide con su suma' "$W/alterado.log" && ok "un volcado alterado se detecta por su suma y no se restaura" ||
    mal "volcado alterado detectado por otro motivo: $(grep -m2 FALLA "$W/alterado.log")"
fi
grep -q '^core_force_job_last_exit_code{trabajo="verificacion_respaldo"} 1$' "$METRICAS/core_force_verificacion_respaldo.prom" &&
  grep -q '^core_force_job_last_success_timestamp_seconds' "$METRICAS/core_force_verificacion_respaldo.prom" &&
  ok "la metrica registra el fallo y conserva la marca del ultimo exito" || mal "metrica tras el fallo: $(cat "$METRICAS/core_force_verificacion_respaldo.prom")"
cp -p "$W/empresa.dump.bueno" "$DUMP_EMPRESA"
rm -f "$DUMP_EMPRESA.sha256"
truncate -s $((tam / 2)) "$DUMP_EMPRESA"
if guion verify-restore.sh mail_tenant_prueba >"$W/truncado.log" 2>&1; then
  mal "verify-restore.sh dio por bueno un volcado truncado sin suma"
else
  ok "un volcado truncado sin suma se detecta al restaurarlo ($(grep -m1 FALLA "$W/truncado.log" | cut -c1-110))"
fi
cp -p "$W/empresa.dump.bueno" "$DUMP_EMPRESA"
(cd "$CORRIDA" && sha256sum mail_tenant_prueba.dump >mail_tenant_prueba.dump.sha256)

echo "== Copia externa S3-compatible (MinIO) =="
CLAVE_MINIO="$(aleatorio 20)"
USUARIO_MINIO="respaldo$(aleatorio 4)"
MINIO_ROOT_USER="${USUARIO_MINIO}" MINIO_ROOT_PASSWORD="${CLAVE_MINIO}" docker run -d --name "$MINIO" -e MINIO_ROOT_USER -e MINIO_ROOT_PASSWORD \
  -p "127.0.0.1:$PUERTO_S3:9000" "$IMG_MINIO" server /data >/dev/null
for _ in $(seq 1 60); do
  curl -fsS "http://127.0.0.1:$PUERTO_S3/minio/health/ready" >/dev/null 2>&1 && break
  sleep 1
done
IMG_CLI="$(sed -n 's/^CF_BACKUP_S3_CLI_IMAGE="\${BACKUP_S3_CLI_IMAGE:-\(.*\)}"$/\1/p' "$ROOT/ops/backup/destino-externo.sh")"
s3() { AWS_ACCESS_KEY_ID="$USUARIO_MINIO" AWS_SECRET_ACCESS_KEY="$CLAVE_MINIO" docker run --rm --network host -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY \
  "$IMG_CLI" --endpoint-url "http://127.0.0.1:$PUERTO_S3" --region us-east-1 "$@"; }
s3 s3 mb s3://respaldos >/dev/null
{
  echo "BACKUP_S3_BUCKET=respaldos"
  echo "BACKUP_S3_ENDPOINT=http://127.0.0.1:$PUERTO_S3"
  echo "BACKUP_S3_REGION=us-east-1"
} >>"$APP/.env"
FRASE="$(openssl rand -base64 48 | tr -d '\n')"

# Sin frase: no sube, pero el respaldo local se hace.
umask 077
printf 'BACKUP_S3_ACCESS_KEY_ID=%s\nBACKUP_S3_SECRET_ACCESS_KEY=%s\n' "$USUARIO_MINIO" "$CLAVE_MINIO" >"$W/backup.env"
umask 022
antes="$(find "$RESPALDOS" -name mail_tenant_prueba.dump | wc -l)"
if guion backup-tenants.sh mail_tenant_prueba >"$W/sin-frase.log" 2>&1; then
  mal "con endpoint y sin frase el respaldo termino bien"
else
  grep -q 'sin BACKUP_ENCRYPTION_PASSPHRASE' "$W/sin-frase.log" && [[ "$(find "$RESPALDOS" -name mail_tenant_prueba.dump | wc -l)" -gt "$antes" ]] &&
    [[ -z "$(s3 s3 ls --recursive s3://respaldos/ 2>/dev/null)" ]] &&
    ok "con endpoint y sin frase: falla, no sube nada y el volcado local se hace igual" || mal "sin frase: $(grep -m2 'FALLA\|AVISO' "$W/sin-frase.log")"
fi

printf 'BACKUP_ENCRYPTION_PASSPHRASE=%s\n' "$FRASE" >>"$W/backup.env"
chmod 0644 "$W/backup.env"
if guion backup-tenants.sh mail_tenant_prueba >"$W/permisos.log" 2>&1; then
  mal "con el fichero de secretos legible por otros el respaldo termino bien"
else
  grep -q 'debe ser del usuario' "$W/permisos.log" && ok "un fichero de secretos legible por otros no se usa" || mal "fichero 0644: $(grep -m2 FALLA "$W/permisos.log")"
fi
chmod 0600 "$W/backup.env"

if guion backup-tenants.sh >"$W/externo.log" 2>&1; then
  ok "respaldo con copia externa cifrada ($(tail -1 "$W/externo.log"))"
else
  cat "$W/externo.log" >&2
  mal "respaldo con copia externa fallo"
fi
objetos="$(s3 s3 ls --recursive s3://respaldos/ | awk '{print $4}')"
[[ "$(grep -c '^postgres/mail_\(registry\|cell_prueba\|tenant_prueba\)/[0-9T]*Z\.dump\.gpg$' <<<"$objetos")" == 3 ]] &&
  ok "tres objetos cifrados en el bucket: $(tr '\n' ' ' <<<"$objetos")" || mal "objetos del bucket: $objetos"
clave_empresa="$(grep '^postgres/mail_tenant_prueba/' <<<"$objetos" | tail -1)"
mkdir -p "$W/bajado"
AWS_ACCESS_KEY_ID="$USUARIO_MINIO" AWS_SECRET_ACCESS_KEY="$CLAVE_MINIO" docker run --rm --network host -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY \
  -v "$W/bajado:/b" --user "$(id -u):$(id -g)" -e HOME=/tmp "$IMG_CLI" --endpoint-url "http://127.0.0.1:$PUERTO_S3" --region us-east-1 \
  s3 cp "s3://respaldos/$clave_empresa" /b/objeto >/dev/null
if [[ -s "$W/bajado/objeto" ]] && ! head -c 5 "$W/bajado/objeto" | grep -q PGDMP && ! grep -qaF "$MARCA" "$W/bajado/objeto"; then
  ok "el objeto del bucket no es un volcado legible ni contiene la fila marcada"
else
  mal "el objeto del bucket no esta cifrado"
fi
[[ -z "$(find "$RESPALDOS" -name '*.gpg')" ]] && ok "no quedan copias cifradas en el disco local" || mal "quedan .gpg en local"
if grep -qF "$FRASE" "$W/externo.log" || grep -qF "$CLAVE_MINIO" "$W/externo.log"; then mal "un secreto aparece en la salida"; else ok "ni la frase ni la clave del bucket aparecen en la salida"; fi

if guion restore-tenant.sh mail_tenant_prueba "s3://respaldos/$clave_empresa" --into mail_tenant_prueba_desde_s3 >"$W/desde-s3.log" 2>&1; then
  [[ "$(sql mail_tenant_prueba_desde_s3 -c "SELECT payload->>'marca' FROM platform.event_outbox WHERE subject = 'prueba.respaldo.marca'")" == "$MARCA" ]] &&
    ok "restore-tenant.sh baja, descifra y restaura desde el bucket con la fila marcada" || mal "la base restaurada desde S3 no trae la fila marcada"
else
  cat "$W/desde-s3.log" >&2
  mal "restore-tenant.sh desde S3 fallo"
fi
[[ -z "$(find "$RESPALDOS" -maxdepth 1 -name 'descarga-*')" ]] && ok "la descarga temporal se borra" || mal "queda la descarga temporal"

echo "== Guiones de ops/db con el perfil autoalojado =="
: >"$ARGV_LOG"
# El array no se puede pasar como prefijo de una orden: se fija antes de cada llamada.
CF_ENV_PRUEBA=(VERIFY_SQL="to_regclass('platform.event_outbox') IS NOT NULL")
if ops_guion ops/db/apply-migration.sh migrations/tenant/canonical/platform/00_outbox.sql mail_tenant_prueba >"$W/migracion-ops.log" 2>&1; then
  ok "apply-migration.sh aplica una canonica y comprueba su efecto ($(tail -1 "$W/migracion-ops.log"))"
else
  cat "$W/migracion-ops.log" >&2
  mal "apply-migration.sh fallo con el perfil autoalojado"
fi

# CELL_DB_PASSWORD y MAIL_DB_PASSWORD estan en el inventario de secretos: el guion las exige en el
# entorno (en el servidor las pone with-secrets.sh) y el resolvedor las vuelve a exportar desde el
# almacen o, sin almacen, desde el .env. Se toman del .env para que el valor sea el mismo por los
# dos caminos, que es lo que pasa en el servidor.
CLAVE_SVC="$(sed -n 's/^CELL_DB_PASSWORD=//p' "$APP/.env" | tail -1)"
CLAVE_MOTORES="$(sed -n 's/^MAIL_DB_PASSWORD=//p' "$APP/.env" | tail -1)"
CLAVE_ADMIN="$(aleatorio 12)"
CF_ENV_PRUEBA=(CELL_DB_PASSWORD="$CLAVE_SVC")
if ops_guion ops/db/cell-service-role.sh --cell prueba >"$W/rol-celda.log" 2>&1; then
  ok "cell-service-role.sh crea el rol de la celda ($(tail -2 "$W/rol-celda.log" | head -1))"
else
  cat "$W/rol-celda.log" >&2
  mal "cell-service-role.sh fallo con el perfil autoalojado"
fi
CF_ENV_PRUEBA=(MAIL_DB_PASSWORD="$CLAVE_MOTORES")
if ops_guion ops/db/cell-engine-role.sh --cell prueba >"$W/rol-motores.log" 2>&1; then
  ok "cell-engine-role.sh crea el rol de los motores de la celda"
else
  cat "$W/rol-motores.log" >&2
  mal "cell-engine-role.sh fallo con el perfil autoalojado"
fi
# El verificador SCRAM lo calcula python3 en el host: la prueba de que quedo bien es que el rol
# inicia sesion de verdad, por TLS verificado y desde la red interna.
entra_como() {
  PGPASSWORD="$2" docker run --rm --network "$RED_INTERNA" -e PGPASSWORD -v "$W/tls/publico:/ca:ro" "$IMG_PG" \
    psql "host=postgres-primary port=5432 user=$1 dbname=mail_cell_prueba sslmode=verify-full sslrootcert=/ca/ca.crt" \
    -Atc 'SELECT current_user' 2>&1 || true
}
[[ "$(entra_como mail_cell_prueba_svc "$CLAVE_SVC")" == mail_cell_prueba_svc ]] &&
  ok "el rol de la celda inicia sesion con su contrasena (verificador SCRAM calculado en el host)" ||
  mal "el rol de la celda no inicia sesion: $(entra_como mail_cell_prueba_svc "$CLAVE_SVC")"
[[ "$(entra_como mail_cell_prueba_engine "$CLAVE_MOTORES")" == mail_cell_prueba_engine ]] &&
  ok "el rol de los motores inicia sesion con su contrasena" ||
  mal "el rol de los motores no inicia sesion: $(entra_como mail_cell_prueba_engine "$CLAVE_MOTORES")"

# tenant-service-role.sh --all recorre los servicios de empresa con `while read ... done < <(...)`:
# si una herramienta se queda con la entrada estandar, el bucle acaba en la primera vuelta y solo
# nace el primer rol, con codigo de salida 0. Paso en produccion. Las contrasenas van al .env,
# que es de donde las toma el resolvedor cuando no hay almacen.
SERVICIOS_EMPRESA="$(python3 - "$APP/ops/db/service-credentials.json" <<'PYSVC'
import json, sys
for nombre, s in sorted(json.load(open(sys.argv[1], encoding="utf-8"))["servicios"].items()):
    if s.get("plane") == "tenant":
        print(nombre)
PYSVC
)"
CF_ENV_PRUEBA=(TENANT_ROUTER_DB_PASSWORD="$(aleatorio 20)")
while read -r svc; do
  [[ -n "$svc" ]] || continue
  CF_ENV_PRUEBA+=("$(tr 'a-z-' 'A-Z_' <<<"$svc")_DB_PASSWORD=$(aleatorio 20)")
done <<<"$SERVICIOS_EMPRESA"
printf '%s\n' "${CF_ENV_PRUEBA[@]}" >>"$APP/.env"
if ops_guion ops/db/tenant-service-role.sh --all >"$W/roles-empresa.log" 2>&1; then
  ok "tenant-service-role.sh --all termina bien ($(grep -c "^servicio '" "$W/roles-empresa.log") servicios)"
else
  cat "$W/roles-empresa.log" >&2
  mal "tenant-service-role.sh --all fallo con el perfil autoalojado"
fi
esperados="${#CF_ENV_PRUEBA[@]}"
creados="$(sql mail_registry -c "SELECT count(*) FROM pg_roles WHERE rolname = 'mail_router' OR rolname LIKE 'mail\_svc\_%'")"
[[ "$creados" == "$esperados" ]] &&
  ok "los $esperados roles de empresa existen (el enrutado y uno por servicio), no solo el primero del bucle" ||
  mal "el bucle de --all creo $creados roles de $esperados: alguna herramienta se come la entrada estandar del bucle"
clave_transactional="$(sed -n 's/^TRANSACTIONAL_DB_PASSWORD=//p' "$APP/.env" | tail -1)"
entra_en_empresa() {
  PGPASSWORD="$2" docker run --rm --network "$RED_INTERNA" -e PGPASSWORD -v "$W/tls/publico:/ca:ro" "$IMG_PG" \
    psql "host=postgres-primary port=5432 user=$1 dbname=mail_tenant_prueba sslmode=verify-full sslrootcert=/ca/ca.crt" \
    -Atc 'SELECT current_user' 2>&1 || true
}
[[ "$(entra_en_empresa mail_svc_transactional "$clave_transactional")" == mail_svc_transactional ]] &&
  ok "mail_svc_transactional -el rol que falto en produccion- inicia sesion con su contrasena" ||
  mal "mail_svc_transactional no inicia sesion: $(entra_en_empresa mail_svc_transactional "$clave_transactional")"

CF_ENV_PRUEBA=(PLATFORM_ADMIN_EMAIL=root@prueba.test PLATFORM_ADMIN_PASSWORD="$CLAVE_ADMIN")
if ops_guion ops/db/bootstrap-platform.sh --cell prueba --region sa-east-1 >"$W/bootstrap.log" 2>&1; then
  hash_admin="$(CLAVE_ADMIN="$CLAVE_ADMIN" docker exec -i -e CLAVE_ADMIN "$PG" psql -q -At -U mail_admin -d mail_registry <<'SQL'
\getenv clave CLAVE_ADMIN
SELECT password_hash LIKE '$2a$10$%' AND password_hash = crypt(:'clave', password_hash)
  FROM identity.users WHERE email = 'root@prueba.test';
SQL
)"
  [[ "$hash_admin" == t ]] && ok "bootstrap-platform.sh deja el superadmin con su hash bcrypt de coste 10 verificable" ||
    mal "el superadmin no quedo con un hash que verifique la contrasena: $hash_admin"
else
  cat "$W/bootstrap.log" >&2
  mal "bootstrap-platform.sh fallo con el perfil autoalojado"
fi

# Ni la contrasena del superadmin ni las de los roles ni el verificador pueden aparecer en la
# linea de ordenes de un contenedor: se pasan por el entorno y el SQL los lee con \getenv.
fuga=""
for secreto in "$CLAVE_ADMIN" "$CLAVE_SVC" "$CLAVE_MOTORES"; do
  grep -qF "$secreto" "$ARGV_LOG" && fuga="si"
done
grep -qE 'SCRAM-SHA-256\$' "$ARGV_LOG" && fuga="${fuga:+$fuga y }verificador"
if [[ -z "$fuga" ]]; then
  ok "ningun secreto aparece en los $(wc -l <"$ARGV_LOG") docker run de estos guiones"
else
  mal "un secreto viaja en la linea de ordenes de docker ($fuga)"
fi
CF_ENV_PRUEBA=()
grep -q -- '-e PLATFORM_ADMIN_PASSWORD' "$ARGV_LOG" && grep -q -- '-e CF_SCRAM_VERIFIER' "$ARGV_LOG" &&
  ok "los secretos llegan al contenedor como nombres de variable (-e PLATFORM_ADMIN_PASSWORD, -e CF_SCRAM_VERIFIER)" ||
  mal "los secretos no se pasan por el entorno al contenedor: $(grep -c -- '-e ' "$ARGV_LOG") invocaciones con -e"

echo "== Volumenes de correo (buzones y claves de mail_crypt) =="
echo "MAIL_COMPOSE_PROJECT=$PROYECTO" >>"$APP/.env"
docker volume create "${PROYECTO}_crypt-vol" >/dev/null
docker volume create "${PROYECTO}_vmail-vol" >/dev/null
# El volumen de MinIO del perfil (proyecto de la plataforma, COMPOSE_PROJECT_NAME): un objeto del uid
# de minio y un temporal de .minio.sys/tmp, que no se respalda.
docker volume create "${PROYECTO}_minio-data" >/dev/null
docker run --rm -v "${PROYECTO}_minio-data:/m" --entrypoint bash "$IMG_PG" -c '
  set -e
  mkdir -p /m/medios/public/t/templates/abc.png /m/.minio.sys/tmp/subida
  printf "objeto\n" > /m/medios/public/t/templates/abc.png/xl.meta
  printf "temporal\n" > /m/.minio.sys/tmp/subida/parte
  chown -R 10001:10001 /m' >/dev/null
CUERPO="mensaje de prueba $(aleatorio 6)"
CUERPO="$CUERPO" docker run --rm -e CUERPO -v "${PROYECTO}_crypt-vol:/crypt" -v "${PROYECTO}_vmail-vol:/vmail" --entrypoint bash "$IMG_PG" -c '
  set -e
  umask 077
  # Igual que dovecot/docker-entrypoint.sh: clave global de mail_crypt del uid 401.
  openssl ecparam -name prime256v1 -genkey | openssl pkey -out /crypt/ecprivkey.pem
  openssl pkey -in /crypt/ecprivkey.pem -pubout -out /crypt/ecpubkey.pem
  chown 401 /crypt/ecprivkey.pem /crypt/ecpubkey.pem
  # Un buzon Maildir del uid vmail (5000) y una carpeta _garbage, que no se respalda.
  mkdir -p /vmail/prueba.test/buzon/{cur,new,tmp} /vmail/_garbage/borrado
  printf "%s\n" "$CUERPO" > /vmail/prueba.test/buzon/cur/1234567890.M1P1.host,S=42,W=43:2,S
  printf "basura\n" > /vmail/_garbage/borrado/mensaje
  chown -R 5000:5000 /vmail; chmod -R go-rwx /vmail' >/dev/null

if guion backup-mail-volumes.sh >"$W/correo.log" 2>&1; then
  ok "backup-mail-volumes.sh termina bien ($(tail -1 "$W/correo.log"))"
else
  cat "$W/correo.log" >&2
  mal "backup-mail-volumes.sh fallo"
fi
CORRIDA_CORREO="$(find "$RESPALDOS/correo" -mindepth 1 -maxdepth 1 -type d -name '*T*Z' | sort | tail -1)"
for v in crypt-vol vmail-vol minio-data; do
  [[ -s "$CORRIDA_CORREO/$v.tar.gz" && -s "$CORRIDA_CORREO/$v.tar.gz.sha256" ]] && ok "archivo y suma de $v" || mal "falta el archivo o la suma de $v"
done
listado_minio="$(tar -tvzf "$CORRIDA_CORREO/minio-data.tar.gz")"
grep -q 'xl.meta' <<<"$listado_minio" && grep -q '10001/10001' <<<"$listado_minio" &&
  ok "los objetos de MinIO entran con el uid de minio (10001)" || mal "archivo de MinIO: $(head -3 <<<"$listado_minio")"
grep -q 'minio.sys/tmp/' <<<"$listado_minio" && mal "el archivo de MinIO incluye .minio.sys/tmp" || ok ".minio.sys/tmp queda fuera del archivo de MinIO"
permisos="$(stat -c '%a' "$CORRIDA_CORREO" "$CORRIDA_CORREO"/*.tar.gz | sort -u | paste -sd' ')"
[[ "$permisos" == "600 700" ]] && ok "archivos de correo 0600 en un directorio 0700" || mal "permisos de los archivos de correo: $permisos"
listado="$(tar -tvzf "$CORRIDA_CORREO/vmail-vol.tar.gz")"
grep -q '_garbage' <<<"$listado" && mal "el archivo de buzones incluye _garbage" || ok "_garbage queda fuera del archivo de buzones"
grep -q '5000/5000' <<<"$listado" && ok "los buzones conservan el uid de vmail (5000)" || mal "los buzones pierden el uid de vmail: $(head -3 <<<"$listado")"
objetos_correo="$(s3 s3 ls --recursive s3://respaldos/correo/ | awk '{print $4}')"
[[ "$(grep -c '^correo/\(crypt-vol\|vmail-vol\|minio-data\)/[0-9T]*Z\.tar\.gz\.gpg$' <<<"$objetos_correo")" == 3 ]] &&
  ok "buzones y claves de mail_crypt cifrados en el bucket: $(tr '\n' ' ' <<<"$objetos_correo")" || mal "objetos de correo en el bucket: $objetos_correo"
grep -q '^core_force_job_last_success_timestamp_seconds{trabajo="respaldo_correo"}' "$METRICAS/core_force_respaldo_correo.prom" &&
  ok "metrica del respaldo de correo: exito" || mal "metrica del respaldo de correo: $(cat "$METRICAS/core_force_respaldo_correo.prom" 2>&1)"

if guion restore-mail-volume.sh vmail-vol --into "${PROYECTO}_vmail-restaurado" >"$W/correo-restaurado.log" 2>&1; then
  restaurado="$(docker run --rm -v "${PROYECTO}_vmail-restaurado:/v:ro" --entrypoint sh "$IMG_PG" -c \
    'cat /v/prueba.test/buzon/cur/*; stat -c %u:%g /v/prueba.test/buzon/cur/*')"
  [[ "$restaurado" == *"$CUERPO"* && "$restaurado" == *"5000:5000"* ]] &&
    ok "restore-mail-volume.sh devuelve el buzon con su mensaje y su dueno" || mal "el volumen restaurado no trae el mensaje: $restaurado"
else
  cat "$W/correo-restaurado.log" >&2
  mal "restore-mail-volume.sh fallo"
fi
if guion restore-mail-volume.sh vmail-vol --into "${PROYECTO}_vmail-restaurado" >"$W/correo-repetido.log" 2>&1; then
  mal "restore-mail-volume.sh sobrescribio un volumen existente sin --force"
else
  ok "restore-mail-volume.sh no sobrescribe un volumen que ya existe sin --force"
fi
clave_crypt="$(grep '^correo/crypt-vol/' <<<"$objetos_correo" | tail -1)"
if guion restore-mail-volume.sh crypt-vol "s3://respaldos/$clave_crypt" --into "${PROYECTO}_crypt-desde-s3" >"$W/crypt-s3.log" 2>&1; then
  par="$(docker run --rm -v "${PROYECTO}_crypt-desde-s3:/v:ro" --entrypoint sh "$IMG_PG" -c \
    '[ "$(openssl pkey -in /v/ecprivkey.pem -pubout)" = "$(cat /v/ecpubkey.pem)" ] && echo par-correcto')"
  [[ "$par" == par-correcto ]] && ok "la clave de mail_crypt baja del bucket, se descifra y sigue siendo el par bueno" ||
    mal "la clave restaurada desde el bucket no es el par bueno"
else
  cat "$W/crypt-s3.log" >&2
  mal "restore-mail-volume.sh de crypt-vol desde S3 fallo"
fi

# Una publica que no corresponde a la privada: el respaldo de la clave no sirve y hay que verlo.
docker run --rm -v "${PROYECTO}_crypt-vol:/crypt" --entrypoint bash "$IMG_PG" -c '
  set -e; umask 077
  openssl ecparam -name prime256v1 -genkey | openssl pkey -out /tmp/otra.pem
  openssl pkey -in /tmp/otra.pem -pubout -out /crypt/ecpubkey.pem
  chown 401 /crypt/ecpubkey.pem' >/dev/null
if guion backup-mail-volumes.sh >"$W/crypt-mal.log" 2>&1; then
  mal "una clave de mail_crypt que no corresponde a su publica paso por buena"
else
  grep -q 'no corresponde a la publica' "$W/crypt-mal.log" && [[ -s "$CORRIDA_CORREO/../$(basename "$(find "$RESPALDOS/correo" -maxdepth 1 -type d -name '*T*Z' | sort | tail -1)")/crypt-vol.tar.gz.ilegible" ]] &&
    ok "la clave que no corresponde se detecta y el archivo queda marcado como ilegible" || mal "clave mal emparejada: $(grep -m2 FALLA "$W/crypt-mal.log")"
fi

echo "== Retencion local =="
viejo="$RESPALDOS/20200101T000000Z"
mkdir -m 0700 "$viejo" && touch -d '2020-01-01' "$viejo"
guion backup-tenants.sh mail_registry >"$W/retencion.log" 2>&1 || mal "respaldo para la retencion: $(tail -2 "$W/retencion.log")"
[[ ! -d "$viejo" ]] && ok "tras una corrida buena se borran las corridas de mas de BACKUP_KEEP_DAYS" || mal "la corrida antigua sigue"

echo "== install-timers.sh =="
mkdir -p "$W/instalar"
docker run --rm -v "$ROOT/ops/backup:/srv/plantilla/ops/backup:ro" -v "$W/instalar:/salida" --entrypoint bash "$IMG_PG" -c '
  set -e
  mkdir -p /usr/local/sbin /etc/systemd/system /srv/app
  printf "#!/bin/sh\necho \"\$@\" >> /salida/systemctl.log\ncase \"\$1\" in is-enabled) echo enabled;; is-active) echo active;; esac\n" > /usr/local/sbin/systemctl
  printf "#!/bin/sh\nexit 1\n" > /usr/local/sbin/crontab
  chmod +x /usr/local/sbin/systemctl /usr/local/sbin/crontab
  groupadd -f docker; useradd -m -G docker operador
  printf "DEPLOY_PROFILE=selfhosted\n" > /srv/app/.env
  export DEPLOY_USER=operador DEPLOY_PATH=/srv/app CORE_ROOT=/srv/cfm
  bash /srv/plantilla/ops/backup/install-timers.sh > /salida/primera.log 2>&1
  bash /srv/plantilla/ops/backup/install-timers.sh > /salida/segunda.log 2>&1
  cp /etc/systemd/system/core-force-mail-backup*.service /salida/
  stat -c "%n %a %U" /srv/cfm/backups /srv/cfm/env >> /salida/dirs.log
  useradd -m sindocker
  DEPLOY_USER=sindocker bash /srv/plantilla/ops/backup/install-timers.sh > /salida/sindocker.log 2>&1 && echo 0 > /salida/sindocker.rc || echo $? > /salida/sindocker.rc
  chmod -R a+r /salida' || mal "install-timers.sh en contenedor: $(cat "$W/instalar/primera.log" 2>/dev/null)"
svc="$W/instalar/core-force-mail-backup.service"
grep -qx 'User=operador' "$svc" && grep -qx 'ExecStart=/srv/app/ops/backup/backup-tenants.sh' "$svc" && grep -qx 'Environment=APP_DIR=/srv/app' "$svc" &&
  ! grep -q '/opt/core-force-mail/app\|User=deploy' "$W"/instalar/*.service &&
  ok "unidades con el usuario y la ruta del servidor (User=operador, /srv/app)" || mal "unidades renderizadas: $(cat "$svc")"
[[ "$(grep -c 'instalada' "$W/instalar/primera.log")" == 6 && "$(grep -c 'instalada' "$W/instalar/segunda.log")" == 0 ]] &&
  grep -q 'enable --now core-force-mail-backup.timer core-force-mail-backup-mail.timer core-force-mail-backup-verify.timer' "$W/instalar/systemctl.log" &&
  ok "install-timers.sh instala las seis unidades, activa los temporizadores y es idempotente" || mal "install-timers.sh: $(cat "$W/instalar/primera.log" "$W/instalar/segunda.log")"
grep -q '/srv/cfm/backups 700 operador' "$W/instalar/dirs.log" && grep -q '/srv/cfm/env 700 operador' "$W/instalar/dirs.log" &&
  ok "directorios de respaldos y de secretos 0700 del usuario del respaldo" || mal "directorios: $(cat "$W/instalar/dirs.log")"
[[ "$(cat "$W/instalar/sindocker.rc")" != 0 ]] && grep -q 'grupo docker' "$W/instalar/sindocker.log" &&
  ok "en el perfil autoalojado se niega con un usuario fuera del grupo docker" || mal "usuario sin docker: $(cat "$W/instalar/sindocker.log")"

if [[ $FALLOS -ne 0 ]]; then
  echo "test-selfhosted-backup: FALLA" >&2
  exit 1
fi
echo "test-selfhosted-backup: OK"

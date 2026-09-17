#!/usr/bin/env bash
# Credencial de base propia de una celda: el rol de login con el que mail-directory,
# mail-security y mail-auth abren la base de su celda en lugar de la credencial de
# plataforma. Un servicio de celda comprometido deja de poder abrir el registro, otra
# celda o una base de empresa.
#
# Uso, en el servidor y con la contrasena ya publicada en el almacen como CELL_DB_PASSWORD:
#   ops/security/secrets/with-secrets.sh ops/db/cell-service-role.sh --cell pe-01
#   ops/db/cell-service-role.sh --cell pe-01 --cell-db mail_cell_pe_01   # otra base
#
# Cuando: al abrir una celda, despues de crear su base y aplicarle las migraciones de
# celda (necesita los roles mail_app y mail_service que crean) y antes de arrancar sus
# servicios; y en cada rotacion de su contrasena.
#
# Deja, de forma idempotente:
#   - el rol <base>_svc (el nombre que config.CellServiceRole espera), LOGIN y sin
#     atributos de administracion, miembro de mail_app (peticiones con usuario, bajo RLS)
#     y de mail_service (motores, tareas de fondo y rele de la outbox);
#   - CONNECT sobre la base de la celda para ese rol y para mail_engine;
#   - ninguna base del cluster abierta a PUBLIC. Postgres da CONNECT a todo rol de login en
#     toda base nueva: sin este cierre, el rol nuevo entraria al registro, a las demas
#     celdas y a las empresas. mail_engine conserva CONNECT en las bases de celda mientras
#     siga siendo el rol de login compartido de los motores; cuando cada celda tiene el suyo
#     (ops/db/cell-engine-role.sh) y el compartido se retira, este script ya no se lo
#     devuelve; la base de mantenimiento `postgres` no tiene datos y queda como esta;
#   - la contrasena del rol, fijada en cada ejecucion: rotar es publicar el valor nuevo,
#     volver a correr esto y recrear los servicios de la celda.
#
# La contrasena llega SOLO por la variable CELL_DB_PASSWORD, nunca por argumento. A Postgres
# viaja convertida en un verificador SCRAM-SHA-256: el texto de ALTER ROLE puede acabar en
# el registro de sentencias y en pg_stat_activity, y ahi solo queda el verificador.
#
# Se ejecuta con la credencial de plataforma (ops/db/pg-credentials.sh: el almacen en el
# servidor, POSTGRES_* en desarrollo), que es la que puede crear roles; ningun servicio la
# usa para esto. Termina comprobando que el rol solo alcanza su base, y falla si no.
set -euo pipefail
LC_ALL=C
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

CELL=""; CELL_DB=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --cell) CELL="${2:-}"; shift 2 ;;
    --cell-db) CELL_DB="${2:-}"; shift 2 ;;
    *) echo "argumento desconocido: $1" >&2; exit 2 ;;
  esac
done

[[ -n "$CELL" ]] || { echo "indica la celda con --cell" >&2; exit 2; }
[[ "$CELL" =~ ^[a-z0-9]+(-[a-z0-9]+)*$ ]] || { echo "codigo de celda invalido: $CELL" >&2; exit 2; }
CELL_DB="${CELL_DB:-mail_cell_${CELL//-/_}}"
# 59 + "_svc" = 63, el largo maximo de un identificador de Postgres.
[[ "$CELL_DB" =~ ^[a-z_][a-z0-9_]*$ && ${#CELL_DB} -le 59 ]] || { echo "nombre de base invalido: $CELL_DB" >&2; exit 2; }
ROLE="${CELL_DB}_svc"

: "${CELL_DB_PASSWORD:?CELL_DB_PASSWORD es obligatoria (por entorno, nunca por argumento)}"
[[ ${#CELL_DB_PASSWORD} -ge 32 ]] || { echo "la contrasena de la celda debe tener al menos 32 caracteres" >&2; exit 2; }
# ASCII imprimible: el verificador se calcula aqui sin la normalizacion SASLprep, que para
# ese juego de caracteres es la identidad.
[[ "$CELL_DB_PASSWORD" =~ ^[!-~]+$ ]] || { echo "la contrasena de la celda solo admite ASCII imprimible sin espacios" >&2; exit 2; }

if [[ -f "$SCRIPT_DIR/pg-credentials.sh" && -z "${PGHOST:-}" ]]; then
  # shellcheck source=/dev/null
  . "$SCRIPT_DIR/pg-credentials.sh"
fi
export PGHOST="${PGHOST:-${POSTGRES_HOST:-127.0.0.1}}" PGPORT="${PGPORT:-${POSTGRES_PORT:-5432}}"
export PGUSER="${PGUSER:-${POSTGRES_USER:-mail_admin}}" PGPASSWORD="${PGPASSWORD:-${POSTGRES_PASSWORD:-}}"

# Sin pg-credentials.sh (con PGHOST ya en el entorno: pruebas y make e2e) las herramientas son
# las del host, como hasta ahora.
declare -F cf_psql >/dev/null || cf_psql() { psql "$@" </dev/null; }
declare -F cf_psql_entrada >/dev/null || cf_psql_entrada() { psql "$@"; }
declare -F cf_pg_pasar_entorno >/dev/null || cf_pg_pasar_entorno() {
  export "${@?}"
  # Tambien los nombres: un psql que corre dentro de un contenedor (pruebas de integracion,
  # e2e) no hereda el entorno y su \getenv se quedaba sin valor.
  CF_PG_ENV_NOMBRES="${CF_PG_ENV_NOMBRES:-} $*"
  export CF_PG_ENV_NOMBRES
}

# shellcheck disable=SC2034  # lo lee psql del entorno con \getenv
CF_SCRAM_VERIFIER="$(CELL_DB_PASSWORD="$CELL_DB_PASSWORD" python3 - <<'PY'
import base64, hashlib, hmac, os

password = os.environ["CELL_DB_PASSWORD"].encode("ascii")
salt = os.urandom(16)
iterations = 4096
salted = hashlib.pbkdf2_hmac("sha256", password, salt, iterations)
client_key = hmac.new(salted, b"Client Key", hashlib.sha256).digest()
server_key = hmac.new(salted, b"Server Key", hashlib.sha256).digest()
b64 = lambda raw: base64.b64encode(raw).decode("ascii")
print(f"SCRAM-SHA-256${iterations}:{b64(salt)}${b64(hashlib.sha256(client_key).digest())}:{b64(server_key)}")
PY
)"
# El verificador viaja por el ENTORNO y el SQL lo lee con \getenv: con -v quedaria en la
# linea de ordenes de psql (y en la de docker run del perfil autoalojado), a la vista de
# cualquier `ps` o `docker inspect`.
cf_pg_pasar_entorno CF_SCRAM_VERIFIER

# Los valores entran como variables de psql (-v, y \getenv para el verificador) y se citan con
# :'x' / :"x": nunca se interpolan en el texto SQL desde bash.
cf_psql_entrada -v ON_ERROR_STOP=1 -q -d "$CELL_DB" \
  -v role="$ROLE" -v cell_db="$CELL_DB" <<'SQL'
\getenv verifier CF_SCRAM_VERIFIER
DO $$
BEGIN
    IF to_regclass('mail.mailboxes') IS NULL OR to_regclass('mail_security.quarantine') IS NULL
       OR to_regclass('platform.event_outbox') IS NULL
       OR NOT EXISTS (SELECT 1 FROM pg_policies
                       WHERE schemaname IN ('mail', 'mail_security') AND policyname = 'service_all') THEN
        RAISE EXCEPTION 'faltan las migraciones de la celda (mail, mail_security, platform y el rol mail_service): aplicalas antes';
    END IF;
END $$;

SELECT format('CREATE ROLE %I LOGIN', :'role')
 WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'role') \gexec
ALTER ROLE :"role" WITH LOGIN PASSWORD :'verifier';
GRANT mail_app, mail_service TO :"role";

SELECT format('REVOKE CONNECT, TEMPORARY ON DATABASE %I FROM PUBLIC', datname)
  FROM pg_database
 WHERE datallowconn AND NOT datistemplate AND datname NOT IN ('postgres', 'rdsadmin')
 ORDER BY datname \gexec

-- mail_engine conserva CONNECT en las bases de celda MIENTRAS siga siendo el rol de login
-- compartido de los motores. En cuanto una celda pasa a su rol propio
-- (ops/db/cell-engine-role.sh) y se retira el compartido con --retire-shared, mail_engine
-- se queda NOLOGIN: volver a darle CONNECT aqui desharia esa retirada la proxima vez que se
-- abriera o se rotara una celda cualquiera del cluster.
SELECT format('GRANT CONNECT ON DATABASE %I TO mail_engine', datname)
  FROM pg_database
 WHERE datallowconn AND NOT datistemplate AND datname LIKE 'mail\_cell\_%'
   AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'mail_engine' AND rolcanlogin)
 ORDER BY datname \gexec

GRANT CONNECT ON DATABASE :"cell_db" TO :"role";
SQL

problemas="$(cf_psql_entrada -v ON_ERROR_STOP=1 -q -At -d "$CELL_DB" -v role="$ROLE" -v cell_db="$CELL_DB" <<'SQL'
SELECT 'tiene atributos de administracion o no puede iniciar sesion'
  FROM pg_roles WHERE rolname = :'role'
   AND (rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls OR NOT rolcanlogin);
SELECT 'no es miembro de ' || r FROM unnest(ARRAY['mail_app', 'mail_service']) AS r
 WHERE NOT pg_has_role(:'role', r, 'MEMBER');
SELECT 'no conecta a su base' WHERE NOT has_database_privilege(:'role', :'cell_db', 'CONNECT');
SELECT 'puede crear tablas temporales en su base' WHERE has_database_privilege(:'role', :'cell_db', 'TEMPORARY');
SELECT 'conecta tambien a ' || datname
  FROM pg_database
 WHERE datallowconn AND NOT datistemplate AND datname NOT IN (:'cell_db', 'postgres', 'rdsadmin')
   AND has_database_privilege(:'role', datname, 'CONNECT')
 ORDER BY datname;
SELECT 'puede crear objetos en el esquema ' || nspname
  FROM pg_namespace WHERE has_schema_privilege(:'role', oid, 'CREATE')
 ORDER BY nspname;
SQL
)"
if [[ -n "$problemas" ]]; then
  echo "cell-service-role: el rol $ROLE no quedo aislado:" >&2
  while IFS= read -r linea; do echo "  - $linea" >&2; done <<<"$problemas"
  echo "  (una base cuyo dueno no es $PGUSER no se puede cerrar desde aqui: cierrala con su dueno)" >&2
  exit 1
fi

echo "celda '$CELL': rol $ROLE con CONNECT solo a $CELL_DB (y a la base de mantenimiento)"
echo "  los servicios de la celda lo usan con CELL_DB_PASSWORD; si pasan por PgBouncer, el rol necesita su entrada en userlist.txt"

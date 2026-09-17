#!/usr/bin/env bash
# Credencial de los MOTORES de una celda: el rol de login con el que Postfix y Dovecot
# consultan el directorio de SU celda.
#
# Uso, en el servidor y con la contrasena ya publicada en el almacen como MAIL_DB_PASSWORD:
#   ops/security/secrets/with-secrets.sh ops/db/cell-engine-role.sh --cell pe-01
#   ops/db/cell-engine-role.sh --cell pe-01 --cell-db mail_cell_pe_01      # otra base
#   ops/db/cell-engine-role.sh --cell pe-01 --retire-shared                # segunda fase
#
# Cuando: al abrir una celda, junto a ops/db/cell-service-role.sh (despues de las
# migraciones de la celda, antes de arrancar sus motores); y en cada rotacion de la
# contrasena de sus motores.
#
# El problema que resuelve: mail_engine era UN rol de login compartido por todas las celdas
# de un cluster, con CONNECT en todas sus bases de celda. La contrasena de los motores de
# una celda abria el directorio de las demas: todos los buzones, dominios y credenciales de
# relayhost de todas las empresas del cluster. Aqui pasa a haber un rol por celda.
#
# Como, sin poder perder un permiso: mail_engine deja de ser el rol que inicia sesion y se
# queda como GRUPO donde viven los permisos que enumera la migracion 01_mail.sql (SELECT
# sobre lo que consultan Postfix y Dovecot, escritura solo en quota_usage, nada sobre
# app_passwords ni sasl_logins) y la politica engine_read de la 02. El rol de login
# <base>_engine es MIEMBRO de el, asi que hereda exactamente esos permisos y ni uno mas: no
# hay una segunda lista de GRANT que pueda quedarse corta y dejar a la plataforma sin
# recibir correo. Lo unico propio del rol de login es CONNECT, y solo a su base.
#
# Se despliega en dos fases, porque entre la primera y la segunda los motores siguen
# conectados como mail_engine:
#   1. (por defecto) crea <base>_engine, le fija la contrasena y le da CONNECT a su base.
#      mail_engine sigue como estaba: los motores en marcha no se enteran.
#      Despues: MAIL_DB_USER=<base>_engine y MAIL_DB_PASSWORD en deploy/mail y recrear los
#      motores de la celda.
#   2. --retire-shared, una vez que NINGUNA celda del cluster usa ya mail_engine: le quita
#      LOGIN y el CONNECT a todas las bases de celda. A partir de ahi es solo un grupo.
#      Correrlo antes de recrear los motores les corta el acceso a la base: no se recibe
#      correo hasta que arranquen con el rol nuevo.
#
# La contrasena llega SOLO por la variable MAIL_DB_PASSWORD, nunca por argumento, y viaja a
# Postgres convertida en verificador SCRAM-SHA-256: el texto de ALTER ROLE puede acabar en
# el registro de sentencias y en pg_stat_activity, y ahi solo queda el verificador.
#
# Se ejecuta con la credencial de plataforma (ops/db/pg-credentials.sh), que es la que puede
# crear roles. Termina comprobando que el rol lee lo que los motores necesitan, que no ve
# credenciales y que no alcanza otra base, y falla si no.
set -euo pipefail
LC_ALL=C
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

CELL=""; CELL_DB=""; RETIRE=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --cell) CELL="${2:-}"; shift 2 ;;
    --cell-db) CELL_DB="${2:-}"; shift 2 ;;
    --retire-shared) RETIRE=1; shift ;;
    *) echo "argumento desconocido: $1" >&2; exit 2 ;;
  esac
done

[[ -n "$CELL" ]] || { echo "indica la celda con --cell" >&2; exit 2; }
[[ "$CELL" =~ ^[a-z0-9]+(-[a-z0-9]+)*$ ]] || { echo "codigo de celda invalido: $CELL" >&2; exit 2; }
CELL_DB="${CELL_DB:-mail_cell_${CELL//-/_}}"
# 56 + "_engine" = 63, el largo maximo de un identificador de Postgres.
[[ "$CELL_DB" =~ ^[a-z_][a-z0-9_]*$ && ${#CELL_DB} -le 56 ]] || { echo "nombre de base invalido: $CELL_DB" >&2; exit 2; }
ROLE="${CELL_DB}_engine"

: "${MAIL_DB_PASSWORD:?MAIL_DB_PASSWORD es obligatoria (por entorno, nunca por argumento)}"
[[ ${#MAIL_DB_PASSWORD} -ge 32 ]] || { echo "la contrasena de los motores debe tener al menos 32 caracteres" >&2; exit 2; }
# ASCII imprimible: el verificador se calcula aqui sin la normalizacion SASLprep, que para
# ese juego de caracteres es la identidad. Ademas viaja a Postfix y a Dovecot dentro de una
# cadena de conexion, donde un espacio o una comilla la partirian.
[[ "$MAIL_DB_PASSWORD" =~ ^[!-~]+$ ]] || { echo "la contrasena de los motores solo admite ASCII imprimible sin espacios" >&2; exit 2; }

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
CF_SCRAM_VERIFIER="$(MAIL_DB_PASSWORD="$MAIL_DB_PASSWORD" python3 - <<'PY'
import base64, hashlib, hmac, os

password = os.environ["MAIL_DB_PASSWORD"].encode("ascii")
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
    IF to_regclass('mail.mailboxes') IS NULL
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'mail_engine') THEN
        RAISE EXCEPTION 'faltan las migraciones de la celda (esquema mail y rol mail_engine): aplicalas antes';
    END IF;
END $$;

SELECT format('CREATE ROLE %I LOGIN', :'role')
 WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'role') \gexec
ALTER ROLE :"role" WITH LOGIN PASSWORD :'verifier';

-- Los permisos los tiene el grupo (migracion 01_mail.sql). El rol de login solo hereda.
GRANT mail_engine TO :"role";
GRANT CONNECT ON DATABASE :"cell_db" TO :"role";

-- Nada de esto lo necesita un motor: son las puertas que Postgres abre por defecto.
--
-- El REVOKE a PUBLIC es el que de verdad cierra: Postgres concede CONNECT y TEMPORARY a
-- PUBLIC en toda base nueva, y un rol de login los hereda por ahi aunque no se los de nadie.
-- ops/db/cell-service-role.sh ya lo hace para todas las bases del cluster, pero este script
-- no puede darlo por hecho: si se corre antes, o sobre una celda creada despues, el rol de
-- los motores entraria en bases que no son la suya y podria crear temporales en la propia.
REVOKE CONNECT, TEMPORARY ON DATABASE :"cell_db" FROM PUBLIC;
REVOKE TEMPORARY ON DATABASE :"cell_db" FROM :"role";
SQL

if [[ $RETIRE -eq 1 ]]; then
  cf_psql_entrada -v ON_ERROR_STOP=1 -q -d "$CELL_DB" <<'SQL'
-- Segunda fase: mail_engine deja de poder iniciar sesion y de alcanzar ninguna base. Los
-- permisos sobre el esquema mail se quedan donde estaban, que es lo que heredan los roles
-- de login por celda.
ALTER ROLE mail_engine WITH NOLOGIN;

SELECT format('REVOKE CONNECT ON DATABASE %I FROM mail_engine', datname)
  FROM pg_database
 WHERE datallowconn AND NOT datistemplate
   AND has_database_privilege('mail_engine', datname, 'CONNECT')
 ORDER BY datname \gexec
SQL
fi

problemas="$(cf_psql_entrada -v ON_ERROR_STOP=1 -q -At -d "$CELL_DB" -v role="$ROLE" -v cell_db="$CELL_DB" <<'SQL'
SELECT 'tiene atributos de administracion o no puede iniciar sesion'
  FROM pg_roles WHERE rolname = :'role'
   AND (rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls OR NOT rolcanlogin);
SELECT 'no es miembro de mail_engine' WHERE NOT pg_has_role(:'role', 'mail_engine', 'MEMBER');
SELECT 'no conecta a su base' WHERE NOT has_database_privilege(:'role', :'cell_db', 'CONNECT');
SELECT 'puede crear tablas temporales en su base' WHERE has_database_privilege(:'role', :'cell_db', 'TEMPORARY');
-- Mientras mail_engine siga siendo el rol de login COMPARTIDO conserva CONNECT en las bases
-- de todas las celdas, y el rol nuevo lo hereda por ser miembro suyo: la membresia no
-- distingue permisos de tabla de permisos de base. El aislamiento entre celdas lo cierra la
-- segunda fase (--retire-shared), que le quita ese CONNECT; hasta entonces esto es el estado
-- de la transicion y no un fallo, y por eso solo se exige cuando ya esta retirado.
-- Acotado a las bases de ESTA plataforma (mail_registry, mail_cell_*, mail_tenant_*), que
-- son sobre las que este rol no debe tener nada que hacer. Un cluster compartido puede
-- tener ademas bases ajenas abiertas a PUBLIC, y senalarlas aqui seria ruido: cerrarlas no
-- es cosa de este script ni cambia el aislamiento entre celdas.
SELECT 'conecta tambien a ' || datname
  FROM pg_database
 WHERE datallowconn AND NOT datistemplate AND datname NOT IN (:'cell_db', 'postgres', 'rdsadmin')
   AND datname LIKE 'mail\_%'
   AND has_database_privilege(:'role', datname, 'CONNECT')
   AND NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'mail_engine' AND rolcanlogin)
 ORDER BY datname;
SELECT 'puede crear objetos en el esquema ' || nspname
  FROM pg_namespace WHERE has_schema_privilege(:'role', oid, 'CREATE')
 ORDER BY nspname;
-- Lo que los motores TIENEN que poder leer. Si falta uno, Postfix o Dovecot dejan de
-- resolver destinatarios o de autenticar, y eso es correo que no se recibe.
SELECT 'no puede leer mail.' || t
  FROM unnest(ARRAY['domains', 'alias_domains', 'mailboxes', 'aliases', 'spam_aliases',
                    'sender_acl', 'relayhosts', 'transports', 'tls_policy_overrides',
                    'recipient_maps', 'bcc_maps', 'sieve_filters', 'v_sieve_before',
                    'v_sieve_after']) AS t
 WHERE NOT has_table_privilege(:'role', 'mail.' || t, 'SELECT');
SELECT 'no puede escribir mail.quota_usage'
 WHERE NOT has_table_privilege(:'role', 'mail.quota_usage', 'INSERT')
    OR NOT has_table_privilege(:'role', 'mail.quota_usage', 'UPDATE');
-- Y lo que NO puede ver nunca: los motores no verifican contrasenas, eso es mail-auth.
SELECT 'alcanza mail.' || t
  FROM unnest(ARRAY['app_passwords', 'sasl_logins']) AS t
 WHERE has_table_privilege(:'role', 'mail.' || t, 'SELECT');
SQL
)"
if [[ -n "$problemas" ]]; then
  echo "cell-engine-role: el rol $ROLE no quedo como debe:" >&2
  while IFS= read -r linea; do echo "  - $linea" >&2; done <<<"$problemas"
  exit 1
fi

echo "celda '$CELL': rol de motores $ROLE con CONNECT solo a $CELL_DB"
if [[ $RETIRE -eq 1 ]]; then
  echo "  mail_engine retirado: sin LOGIN y sin CONNECT en ninguna base (queda como grupo de permisos)"
else
  echo "  siguiente paso: MAIL_DB_USER=$ROLE y MAIL_DB_PASSWORD en los motores de la celda (deploy/mail), recrearlos,"
  echo "  y cuando ninguna celda use ya mail_engine, volver con --retire-shared"
  echo "  AVISO: hasta esa segunda fase, $ROLE hereda de mail_engine el CONNECT a las bases de las demas"
  echo "  celdas del cluster; el aislamiento entre celdas se cierra al retirarlo"
fi
echo "  si los motores pasan por PgBouncer, el rol necesita su entrada: ops/db/pgbouncer-userlist.sh"

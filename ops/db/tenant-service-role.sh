#!/usr/bin/env bash
# Credencial propia de un servicio del plano de EMPRESA: el rol de login con el que abre la
# base de cada empresa, y el rol de enrutado con el que resuelve en cual vive cada una.
#
# Uso, en el servidor y con las contrasenas ya publicadas en el almacen:
#   ops/security/secrets/with-secrets.sh ops/db/tenant-service-role.sh --router
#   ops/security/secrets/with-secrets.sh ops/db/tenant-service-role.sh --service contacts
#   ops/security/secrets/with-secrets.sh ops/db/tenant-service-role.sh --all
#
# Cuando: antes de recrear el servicio con su credencial, y en cada rotacion. Es idempotente
# y se puede repetir: fija la contrasena en cada ejecucion, que es tambien como se rota.
#
# Que deja, de forma idempotente:
#   --router:  el rol de login mail_router, miembro del grupo tenant_router (registro,
#              migracion 031). Con el, un servicio de empresa lee organization.v_tenant_routing
#              y NADA MAS del registro: ni tenants, ni identity, ni access_control.
#   --service: el rol de login mail_svc_<esquema>, miembro del grupo <esquema>_service, que
#              su migracion canonica de empresa crea y al que concede DML solo sobre su
#              esquema y sobre platform.event_outbox, en CADA base de empresa.
#
# Los permisos NO se enumeran aqui: viven en las migraciones, junto a las tablas que los
# necesitan, y el rol de login solo es miembro del grupo. Asi un servicio no puede quedarse
# con mas privilegio del que su migracion declara, ni con menos porque un script se quedara
# atras.
#
# CONNECT tampoco se concede aqui: lo da la migracion del servicio sobre la base en la que
# corre (GRANT CONNECT ... TO <esquema>_service), asi que una empresa nueva queda lista sola.
# Este script comprueba que efectivamente lo tiene en las bases de empresa registradas, y
# avisa de las que no (les falta la canonica nueva).
#
# Las contrasenas llegan SOLO por entorno (<SERVICIO>_DB_PASSWORD, TENANT_ROUTER_DB_PASSWORD),
# nunca por argumento, y viajan a Postgres como verificador SCRAM-SHA-256.
#
# Se ejecuta con la credencial de plataforma (ops/db/pg-credentials.sh), que es la que puede
# crear roles. Termina comprobando que cada rol solo alcanza lo suyo, y falla si no.
set -euo pipefail
LC_ALL=C
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MATRIZ="$SCRIPT_DIR/service-credentials.json"

ROUTER=0; SERVICIO=""; TODOS=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --router) ROUTER=1; shift ;;
    --service) SERVICIO="${2:-}"; shift 2 ;;
    --all) TODOS=1; ROUTER=1; shift ;;
    *) echo "argumento desconocido: $1" >&2; exit 2 ;;
  esac
done
[[ $ROUTER -eq 1 || -n "$SERVICIO" ]] || {
  echo "uso: $0 --router | --service <servicio> | --all" >&2; exit 2; }
[[ -f "$MATRIZ" ]] || { echo "no existe $MATRIZ" >&2; exit 2; }

# Igual que ops/db/cell-service-role.sh: con PGHOST ya en el entorno (pruebas y make e2e,
# que corren contra un Postgres desechable) no se toca el almacen de secretos.
if [[ -f "$SCRIPT_DIR/pg-credentials.sh" && -z "${PGHOST:-}" ]]; then
  # shellcheck source=/dev/null
  . "$SCRIPT_DIR/pg-credentials.sh" || exit 1
fi
export PGHOST="${PGHOST:-${POSTGRES_HOST:-127.0.0.1}}" PGPORT="${PGPORT:-${POSTGRES_PORT:-5432}}"
export PGUSER="${PGUSER:-${POSTGRES_USER:-mail_admin}}" PGPASSWORD="${PGPASSWORD:-${POSTGRES_PASSWORD:-}}"

# Sin pg-credentials.sh (con PGHOST ya en el entorno: pruebas y make e2e) las herramientas son
# las del host, como hasta ahora.
declare -F cf_psql >/dev/null || cf_psql() { psql "$@" </dev/null; }
declare -F cf_psql_entrada >/dev/null || cf_psql_entrada() { psql "$@"; }
declare -F cf_pg_pasar_entorno >/dev/null || cf_pg_pasar_entorno() { export "${@?}"; }

# cf_tenant_databases la define pg-credentials.sh; sin el (pruebas) se lee igual del
# registro, que es la fuente: nunca un patron sobre los nombres de base.
if ! declare -F cf_tenant_databases >/dev/null; then
  cf_tenant_databases() {
    cf_psql -d "${POSTGRES_DB:-mail_registry}" -At -c \
      "SELECT db_name FROM organization.tenants
        WHERE status = 'active' AND db_name <> ''
        ORDER BY db_name"
  }
fi

# El registro, cerrado a PUBLIC. Postgres concede CONNECT y TEMPORARY a PUBLIC en toda base
# nueva, asi que un rol de login recien creado entraria al registro -donde viven cuentas,
# permisos y facturacion- sin que nadie se lo conceda. ops/db/cell-service-role.sh cierra
# todas las bases del cluster, pero este script no puede darlo por hecho: un despliegue sin
# celdas propias, o un registro restaurado despues, se quedaria abierto. El dueno (la
# credencial de plataforma) conserva su acceso, y el rol de enrutado tiene el CONNECT que le
# concede su migracion. Idempotente.
cf_psql_entrada -v ON_ERROR_STOP=1 -q -d "${POSTGRES_DB:-mail_registry}" <<'SQL'
SELECT format('REVOKE CONNECT, TEMPORARY ON DATABASE %I FROM PUBLIC', current_database()) \gexec
SQL

# esquema_de <servicio>: el esquema que el servicio posee en la base de empresa, segun la
# matriz, y vacio si el servicio no es del plano de empresa.
esquema_de() {
  python3 - "$MATRIZ" "$1" <<'PY'
import json, sys
m = json.load(open(sys.argv[1], encoding="utf-8"))["servicios"].get(sys.argv[2])
print(m["schema"] if m and m.get("plane") == "tenant" else "")
PY
}

servicios_de_empresa() {
  python3 - "$MATRIZ" <<'PY'
import json, sys
for nombre, s in sorted(json.load(open(sys.argv[1], encoding="utf-8"))["servicios"].items()):
    if s.get("plane") == "tenant":
        print(nombre)
PY
}

# verificador <contrasena por entorno>: SCRAM-SHA-256 calculado FUERA de Postgres. El texto
# de ALTER ROLE puede acabar en el registro de sentencias y en pg_stat_activity, y ahi solo
# debe quedar el verificador.
verificador() {
  CFM_PASSWORD="$1" python3 - <<'PY'
import base64, hashlib, hmac, os

password = os.environ["CFM_PASSWORD"].encode("ascii")
salt = os.urandom(16)
iterations = 4096
salted = hashlib.pbkdf2_hmac("sha256", password, salt, iterations)
client_key = hmac.new(salted, b"Client Key", hashlib.sha256).digest()
server_key = hmac.new(salted, b"Server Key", hashlib.sha256).digest()
b64 = lambda raw: base64.b64encode(raw).decode("ascii")
print(f"SCRAM-SHA-256${iterations}:{b64(salt)}${b64(hashlib.sha256(client_key).digest())}:{b64(server_key)}")
PY
}

# crear_rol <rol de login> <grupo> <contrasena>
crear_rol() {
  local rol="$1" grupo="$2" clave="$3"
  [[ ${#clave} -ge 32 ]] || { echo "la contrasena de $rol debe tener al menos 32 caracteres" >&2; exit 2; }
  [[ "$clave" =~ ^[!-~]+$ ]] || { echo "la contrasena de $rol solo admite ASCII imprimible sin espacios" >&2; exit 2; }
  # El grupo lo crea la MIGRACION, que es donde estan sus permisos. Sin el, el rol de login
  # existiria sin poder hacer nada y el servicio fallaria al primer SELECT.
  # Por la entrada estandar y no con -c: psql solo sustituye :'variable' en lo que lee como
  # entrada, no en el texto de -c, y ahi la consulta llegaria literal al servidor.
  if [[ "$(cf_psql_entrada -At -d "${POSTGRES_DB:-mail_registry}" -v grp="$grupo" <<'SQL'
SELECT 1 FROM pg_roles WHERE rolname = :'grp';
SQL
)" != "1" ]]; then
    echo "falta el rol de grupo $grupo: aplica antes la migracion que lo crea" >&2
    exit 1
  fi
  # shellcheck disable=SC2034  # lo lee psql del entorno con \getenv
  CF_SCRAM_VERIFIER="$(verificador "$clave")"
  # Por el ENTORNO y con \getenv, no con -v: el verificador no puede quedar en la linea de
  # ordenes de psql ni en la de docker run del perfil autoalojado.
  cf_pg_pasar_entorno CF_SCRAM_VERIFIER
  cf_psql_entrada -v ON_ERROR_STOP=1 -q -d "${POSTGRES_DB:-mail_registry}" \
    -v role="$rol" -v grp="$grupo" <<'SQL'
\getenv verifier CF_SCRAM_VERIFIER
SELECT format('CREATE ROLE %I LOGIN', :'role')
 WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'role') \gexec
ALTER ROLE :"role" WITH LOGIN PASSWORD :'verifier';
SELECT format('GRANT %I TO %I', :'grp', :'role') \gexec
SQL
}

# comprobar_rol <rol> <grupo> <esquema o vacio para el enrutado>
comprobar_rol() {
  local rol="$1" grupo="$2" esquema="${3:-}" problemas
  problemas="$(cf_psql_entrada -v ON_ERROR_STOP=1 -q -At -d "${POSTGRES_DB:-mail_registry}" \
    -v role="$rol" -v grp="$grupo" <<'SQL'
SELECT 'tiene atributos de administracion o no puede iniciar sesion'
  FROM pg_roles WHERE rolname = :'role'
   AND (rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls OR NOT rolcanlogin);
SELECT 'no es miembro de ' || :'grp' WHERE NOT pg_has_role(:'role', :'grp', 'MEMBER');
SELECT 'puede crear objetos en el esquema ' || nspname
  FROM pg_namespace WHERE has_schema_privilege(:'role', oid, 'CREATE')
 ORDER BY nspname;
SQL
)"
  # En el registro, el rol de enrutado solo lee la vista publicada; los de servicio no
  # entran siquiera. Es la comprobacion que separa "creado" de "acotado".
  local registro="${POSTGRES_DB:-mail_registry}"
  if [[ -z "$esquema" ]]; then
    problemas+="$(cf_psql_entrada -v ON_ERROR_STOP=1 -q -At -d "$registro" -v role="$rol" <<'SQL'
SELECT 'no puede leer organization.v_tenant_routing'
 WHERE NOT has_table_privilege(:'role', 'organization.v_tenant_routing', 'SELECT');
SELECT 'alcanza la tabla ' || c
  FROM unnest(ARRAY['organization.tenants', 'organization.cells', 'identity.users',
                    'access_control.permissions', 'platform.event_outbox']) AS c
 WHERE to_regclass(c) IS NOT NULL AND has_table_privilege(:'role', c, 'SELECT');
SQL
)"
  else
    problemas+="$(cf_psql_entrada -v ON_ERROR_STOP=1 -q -At -d "$registro" -v role="$rol" -v db="$registro" <<'SQL'
SELECT 'conecta al registro' WHERE has_database_privilege(:'role', :'db', 'CONNECT');
SQL
)"
  fi
  if [[ -n "$problemas" ]]; then
    echo "tenant-service-role: el rol $rol no quedo aislado:" >&2
    while IFS= read -r linea; do [[ -n "$linea" ]] && echo "  - $linea" >&2; done <<<"$problemas"
    exit 1
  fi
}

# bases_sin_connect <grupo>: bases de empresa registradas en las que el grupo aun no tiene
# CONNECT, es decir, a las que les falta la canonica que lo concede.
bases_sin_connect() {
  local grupo="$1" db
  while IFS= read -r db; do
    [[ -z "$db" ]] && continue
    if [[ "$(cf_psql_entrada -At -d "${POSTGRES_DB:-mail_registry}" -v grp="$grupo" -v db="$db" 2>/dev/null <<'SQL'
SELECT has_database_privilege(:'grp', :'db', 'CONNECT');
SQL
)" != "t" ]]; then
      echo "$db"
    fi
  done < <(cf_tenant_databases)
}

if [[ $ROUTER -eq 1 ]]; then
  : "${TENANT_ROUTER_DB_PASSWORD:?TENANT_ROUTER_DB_PASSWORD es obligatoria (por entorno, nunca por argumento)}"
  crear_rol mail_router tenant_router "$TENANT_ROUTER_DB_PASSWORD"
  comprobar_rol mail_router tenant_router ""
  echo "enrutado: rol mail_router con SELECT solo sobre organization.v_tenant_routing"
fi

procesar_servicio() {
  local svc="$1" esquema rol grupo var clave faltan
  esquema="$(esquema_de "$svc")"
  [[ -n "$esquema" ]] || { echo "'$svc' no es un servicio del plano de empresa en $(basename "$MATRIZ")" >&2; exit 2; }
  rol="mail_svc_$esquema"
  grupo="${esquema}_service"
  var="$(echo "$svc" | tr 'a-z-' 'A-Z_')_DB_PASSWORD"
  clave="${!var:-}"
  [[ -n "$clave" ]] || { echo "falta $var en el entorno (almacen de secretos)" >&2; exit 2; }
  crear_rol "$rol" "$grupo" "$clave"
  comprobar_rol "$rol" "$grupo" "$esquema"
  faltan="$(bases_sin_connect "$grupo")"
  if [[ -n "$faltan" ]]; then
    echo "  AVISO: estas bases de empresa aun no dejan entrar a $rol (les falta la canonica nueva):" >&2
    while IFS= read -r db; do echo "    - $db" >&2; done <<<"$faltan"
    echo "  Aplica las migraciones de empresa (organization las barre solo) antes de recrear $svc." >&2
  fi
  echo "servicio '$svc': rol $rol, miembro de $grupo (DML solo sobre el esquema $esquema)"
}

if [[ $TODOS -eq 1 ]]; then
  while IFS= read -r svc; do procesar_servicio "$svc"; done < <(servicios_de_empresa)
elif [[ -n "$SERVICIO" ]]; then
  procesar_servicio "$SERVICIO"
fi

echo "recuerda: si los servicios pasan por PgBouncer, sus roles necesitan entrada en userlist.txt"
echo "  ops/security/secrets/with-secrets.sh ops/db/pgbouncer-userlist.sh --write"

#!/usr/bin/env bash
# Prueba de punta a punta de la plataforma con binarios reales contra Postgres, NATS y
# Redis desechables. Recorre lo que ya esta integrado: arranque de una plataforma vacia,
# alta de celda y empresa, acceso por el gateway con sus tres capas, dominio y buzon en
# la celda, propagacion a Redis por eventos, mapas de los motores, plantillas y supresion.
#
# Cada paso COMPRUEBA su resultado y la ejecucion termina con error si alguno falla: no
# basta con que los servicios arranquen, tienen que hablarse. Las credenciales se generan
# en cada ejecucion; ninguna vive en este fichero.
#
# Uso:
#   make e2e
#   E2E_KEEP=1 make e2e          # deja contenedores y binarios para inspeccionar
#   E2E_PORT_BASE=48000 make e2e # otro rango de puertos si 58000-58099 esta ocupado
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

BASE="${E2E_PORT_BASE:-58000}"
PG_PORT="${E2E_PG_PORT:-$((BASE - 2568))}"
NATS_PORT="${E2E_NATS_PORT:-$((BASE - 3778))}"
REDIS_PORT="${E2E_REDIS_PORT:-$((BASE - 1621))}"
WORK="$(mktemp -d)"
PREFIX="cfm-e2e"
export PG_CONTAINER="$PREFIX-pg"

rand_hex() { head -c "$1" /dev/urandom | od -An -tx1 | tr -d ' \n'; }

fallos=0
ok()   { printf '  OK    %s\n' "$1"; }
mal()  { printf '  FALLA %s\n' "$1" >&2; fallos=$((fallos + 1)); }
# expect <descripcion> <obtenido> <esperado>
expect() { if [[ "$2" == "$3" ]]; then ok "$1"; else mal "$1 (obtenido: '$2', esperado: '$3')"; fi; }
# contains <descripcion> <texto> <fragmento>
contains() { if [[ "$2" == *"$3"* ]]; then ok "$1"; else mal "$1 (no contiene '$3': ${2:0:300})"; fi; }

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

limpiar() {
  pkill -f "$WORK/bin/" 2>/dev/null
  if [[ "${E2E_KEEP:-0}" != "1" ]]; then
    docker rm -f "$PREFIX-pg" "$PREFIX-nats" "$PREFIX-redis" >/dev/null 2>&1
    rm -rf "$WORK"
  else
    echo "E2E_KEEP=1: contenedores $PREFIX-* y registros en $WORK/log"
  fi
}
trap limpiar EXIT

# psql sin cliente local: el de dentro del contenedor. Exportada para que la use tambien
# ops/db/bootstrap-platform.sh, que es parte de lo que se prueba.
psql() { docker exec -i -e PGPASSWORD="${PGPASSWORD:-}" "$PG_CONTAINER" psql -U "${PGUSER:-mail_admin}" "$@"; }
export -f psql
sql() { psql -v ON_ERROR_STOP=1 -q -At -d "$1" -c "$2"; }

echo "== Infraestructura desechable"
docker rm -f "$PREFIX-pg" "$PREFIX-nats" "$PREFIX-redis" >/dev/null 2>&1
export POSTGRES_PASSWORD; POSTGRES_PASSWORD="$(rand_hex 16)"
docker run -d --name "$PREFIX-pg" -e POSTGRES_USER=mail_admin -e POSTGRES_PASSWORD="$POSTGRES_PASSWORD" \
  -e POSTGRES_DB=mail_registry -p "127.0.0.1:$PG_PORT:5432" pgvector/pgvector:pg16 >/dev/null || exit 1
docker run -d --name "$PREFIX-nats" -p "127.0.0.1:$NATS_PORT:4222" nats:2.10-alpine -js >/dev/null || exit 1
docker run -d --name "$PREFIX-redis" -p "127.0.0.1:$REDIS_PORT:6379" redis:7.4.10-alpine >/dev/null || exit 1
for _ in $(seq 1 40); do docker exec "$PREFIX-pg" pg_isready -U mail_admin -d mail_registry >/dev/null 2>&1 && break; sleep 1; done

SERVICES=(organization identity access-control gateway mail-directory domain-service mail-security templates suppression)
echo "== Compilacion (${SERVICES[*]})"
mkdir -p "$WORK/bin" "$WORK/log"
for s in "${SERVICES[@]}"; do go build -o "$WORK/bin/$s" "./services/$s" || { echo "no compila $s" >&2; exit 1; }; done

echo "== Base de la celda pe-01"
export PGUSER=mail_admin PGPASSWORD="$POSTGRES_PASSWORD"
sql mail_registry "CREATE DATABASE mail_cell_pe_01" >/dev/null
for f in migrations/cell/canonical/platform/*.sql migrations/cell/canonical/mail-directory/*.sql migrations/cell/canonical/mail-security/*.sql; do
  psql -v ON_ERROR_STOP=1 -q -d mail_cell_pe_01 < "$f" >/dev/null 2>&1 || { mal "migracion de celda $f"; }
done

# ── Entorno comun ────────────────────────────────────────────────────────────
export ENVIRONMENT=development
export JWT_SECRET; JWT_SECRET="$(rand_hex 48)"
export INTERNAL_GATEWAY_TOKEN; INTERNAL_GATEWAY_TOKEN="$(rand_hex 24)"
export MAIL_ENCRYPTION_KEY; MAIL_ENCRYPTION_KEY="$(rand_hex 32)"
export MAIL_LINK_SIGNING_KEY; MAIL_LINK_SIGNING_KEY="$(rand_hex 32)"
export POSTGRES_HOST=127.0.0.1 POSTGRES_PORT="$PG_PORT" POSTGRES_USER=mail_admin POSTGRES_DB=mail_registry
export NATS_URL="nats://127.0.0.1:$NATS_PORT" REDIS_HOST=127.0.0.1 REDIS_PORT="$REDIS_PORT"
export MAIL_REDIS_HOST=127.0.0.1 MAIL_REDIS_PORT="$REDIS_PORT"
export DEFAULT_CELL_CODE=pe-01 CELL_CODE=pe-01 CELL_DB_NAME=mail_cell_pe_01
export REGISTRY_MIGRATION_DIR="$ROOT/migrations/registry" TENANT_MIGRATION_DIR="$ROOT/migrations/tenant/canonical"
export AUTH_COOKIE_SECURE=false PASSWORD_BREACH_CHECK=off MFA_ISSUER="Core Force Mail"
export MAIL_HOSTNAME=mail.cfm.test MAIL_MX_HOSTNAME=mail.cfm.test MAIL_SPF_INCLUDE=include:spf.cfm.test MAIL_DMARC_RUA=dmarc@cfm.test
export CORS_ALLOWED_ORIGINS=http://localhost:3000

declare -A PORT=(
  [identity]=$((BASE + 1)) [access-control]=$((BASE + 2)) [organization]=$((BASE + 3))
  [mail-directory]=$((BASE + 40)) [mail-security]=$((BASE + 42)) [domain-service]=$((BASE + 43))
  [suppression]=$((BASE + 46)) [templates]=$((BASE + 47)) [gateway]=$((BASE + 80))
)
MAPS_PORT=$((BASE + 81))
EXPORT_PORT=$((BASE + 91))
GW="http://127.0.0.1:${PORT[gateway]}/api/v1"
export IDENTITY_PORT=${PORT[identity]} ACCESS_CONTROL_PORT=${PORT[access-control]} ORGANIZATION_PORT=${PORT[organization]}
export MAIL_DIRECTORY_PORT=${PORT[mail-directory]} MAIL_SECURITY_PORT=${PORT[mail-security]} DOMAIN_SERVICE_PORT=${PORT[domain-service]}
export SUPPRESSION_PORT=${PORT[suppression]} TEMPLATES_PORT=${PORT[templates]} GATEWAY_PORT=${PORT[gateway]}
export MAIL_POLICY_MAPS_PORT=$MAPS_PORT MAIL_POLICY_EXPORT_PORT=$EXPORT_PORT
export API_ORIGIN="http://localhost:${PORT[gateway]}" PUBLIC_BASE_URL="http://localhost:${PORT[gateway]}"
# Direcciones internas: las que el gateway lee de routes.json por <SERVICIO>_HOST(_PORT) y
# las que los servicios usan entre si.
for s in identity access-control organization mail-directory mail-security domain-service suppression templates; do
  var="$(echo "$s" | tr 'a-z-' 'A-Z_')_HOST"
  export "$var=127.0.0.1" "${var}_PORT=${PORT[$s]}"
done
export WEB_HOST=127.0.0.1 WEB_HOST_PORT=1
export ACCESS_CONTROL_URL="http://127.0.0.1:${PORT[access-control]}"
export MAIL_DIRECTORY_URL="http://127.0.0.1:${PORT[mail-directory]}" MAIL_SECURITY_URL="http://127.0.0.1:${PORT[mail-security]}"
export SUPPRESSION_URL="http://127.0.0.1:${PORT[suppression]}" TEMPLATES_URL="http://127.0.0.1:${PORT[templates]}"

arrancar() { "$WORK/bin/$1" >"$WORK/log/$1.log" 2>&1 & }
esperar_salud() {
  for _ in $(seq 1 40); do
    [[ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$2/healthz")" == 200 ]] && return 0
    sleep 0.5
  done
  mal "$1 no responde en /healthz"; tail -5 "$WORK/log/$1.log" >&2; return 1
}

echo "== Plano de control"
arrancar organization
esperar_salud organization "${PORT[organization]}" || exit 1

echo "== Arranque de una plataforma vacia (ops/db/bootstrap-platform.sh)"
ADMIN_PASS="$(rand_hex 12)Aa1!"
PGHOST=127.0.0.1 PLATFORM_ADMIN_EMAIL=root@platform.test PLATFORM_ADMIN_PASSWORD="$ADMIN_PASS" \
  bash ops/db/bootstrap-platform.sh --cell pe-01 --region sa-east-1 >/dev/null || mal "bootstrap-platform.sh"

for s in identity access-control mail-directory domain-service mail-security templates suppression gateway; do arrancar "$s"; done
for s in identity access-control mail-directory domain-service mail-security templates suppression gateway; do esperar_salud "$s" "${PORT[$s]}"; done

echo "== Identidad y acceso"
L1=$(curl -s -X POST "$GW/auth/login" -H 'Content-Type: application/json' -d "{\"email\":\"root@platform.test\",\"password\":\"$ADMIN_PASS\"}")
T1=$(echo "$L1" | jget data.access_token)
[[ -n "$T1" ]] && ok "login del superadmin" || { mal "login del superadmin: ${L1:0:200}"; exit 1; }
A1="Authorization: Bearer $T1"
contains "el superadmin lista celdas" "$(curl -s "$GW/cells" -H "$A1")" '"code":"pe-01"'

TENANT_PASS="$(rand_hex 12)Aa1!"
ORG=$(curl -s -X POST "$GW/organizations" -H "$A1" -H 'Content-Type: application/json' \
  -d "{\"slug\":\"acme\",\"name\":\"Acme\",\"cell_code\":\"pe-01\",\"admin_email\":\"admin@acme.test\",\"admin_password\":\"$TENANT_PASS\",\"admin_first_name\":\"Ana\",\"admin_last_name\":\"Perez\"}")
expect "alta de empresa en su celda" "$(echo "$ORG" | jget data.slug)" "acme"
expect "base de la empresa creada y migrada" "$(sql mail_registry "SELECT count(*) FROM pg_database WHERE datname = 'mail_tenant_acme'")" "1"

L2=$(curl -s -X POST "$GW/auth/login" -H 'Content-Type: application/json' -d "{\"email\":\"admin@acme.test\",\"password\":\"$TENANT_PASS\"}")
T2=$(echo "$L2" | jget data.access_token)
TID=$(echo "$L2" | jget data.tenant_id)
[[ -n "$T2" ]] && ok "login del tenant_admin" || { mal "login del tenant_admin: ${L2:0:200}"; exit 1; }
A2="Authorization: Bearer $T2"
contains "el tenant_admin recibe los modulos de correo" "$(curl -s "$GW/access/my-modules" -H "$A2")" '"mailboxes"'
expect "el tenant_admin no ve las celdas" "$(curl -s -o /dev/null -w '%{http_code}' "$GW/cells" -H "$A2")" "403"
expect "el tenant_admin lista sus usuarios" "$(curl -s -o /dev/null -w '%{http_code}' "$GW/users" -H "$A2")" "200"
expect "sin token no hay API" "$(curl -s -o /dev/null -w '%{http_code}' "$GW/users")" "401"

echo "== Correo corporativo en la celda"
DOM=$(curl -s -X POST "$GW/domains" -H "$A2" -H 'Content-Type: application/json' -d '{"domain":"acme.test","purpose":"both"}')
expect "alta de dominio con registros DNS" "$(echo "$DOM" | jget data.status)" "pending"
ACT=$(curl -s -X PUT "http://127.0.0.1:${PORT[mail-directory]}/internal/mail-directory/domains/acme.test/activation" \
  -H "X-Gateway-Token: $INTERNAL_GATEWAY_TOKEN" -H "X-Tenant-ID: $TID" -H 'Content-Type: application/json' -d '{"active":true}')
expect "activacion del dominio en el directorio" "$(echo "$ACT" | jget data.active)" "True"
MBX_PASS="$(rand_hex 10)Aa1!"
MB=$(curl -s -X POST "$GW/mailboxes" -H "$A2" -H 'Content-Type: application/json' \
  -d "{\"local_part\":\"ana\",\"domain\":\"acme.test\",\"password\":\"$MBX_PASS\",\"display_name\":\"Ana Perez\"}")
expect "alta de buzon" "$(echo "$MB" | jget data.username)" "ana@acme.test"
expect "Postfix y Dovecot ven el buzon (rol mail_engine)" \
  "$(sql mail_cell_pe_01 "SET ROLE mail_engine; SELECT mailbox_format || path_prefix || domain || '/' || local_part || '/' FROM mail.mailboxes WHERE username = 'ana@acme.test'" | tail -1)" \
  "maildir:/var/vmail/acme.test/ana/"
expect "otra empresa no ve el buzon (RLS)" "$(curl -s "$GW/mailboxes" -H "$A1" | jget data)" "[]"

dm=""
for _ in $(seq 1 20); do dm=$(docker exec "$PREFIX-redis" redis-cli HGET DOMAIN_MAP acme.test); [[ -n "$dm" ]] && break; sleep 0.5; done
expect "el dominio llega a DOMAIN_MAP (directorio -> NATS -> mail-security -> Redis)" "$dm" "1"
expect "aliasexp resuelve el buzon final para Rspamd" \
  "$(curl -s -X POST "http://127.0.0.1:$MAPS_PORT/aliasexp" -H 'Rcpt: ana@acme.test')" "ana@acme.test"
contains "settings lleva la regla del vigilante" "$(curl -s "http://127.0.0.1:$MAPS_PORT/settings")" "watchdog {"

echo "== Plantillas"
TP=$(curl -s -X POST "$GW/templates" -H "$A2" -H 'Content-Type: application/json' \
  -d '{"name":"bienvenida","kind":"transactional","subject":"Hola {{.name}}","html":"<p>Hola {{.name}}</p>","variables":[{"name":"name","type":"string","required":true}]}')
TPID=$(echo "$TP" | jget data.id)
[[ -n "$TPID" ]] && ok "alta de plantilla" || mal "alta de plantilla: ${TP:0:200}"
expect "publicacion de la version 1" \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$GW/templates/$TPID/versions/1/publish" -H "$A2")" "200"
expect "render por el gateway (gateado como lectura)" \
  "$(curl -s -X POST "$GW/templates/$TPID/render" -H "$A2" -H 'Content-Type: application/json' -d '{"variables":{"name":"Ana"}}' | jget data.subject)" \
  "Hola Ana"

echo "== Supresion"
expect "exclusion manual" "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$GW/suppression/entries" -H "$A2" \
  -H 'Content-Type: application/json' -d '{"email":"baja@otro.test","reason":"manual"}')" "201"
expect "comprobacion masiva normaliza y filtra" \
  "$(curl -s -X POST "$GW/suppression/check" -H "$A2" -H 'Content-Type: application/json' -d '{"emails":["BAJA@otro.test","alta@otro.test"]}' | jget data.suppressed.0.email)" \
  "baja@otro.test"

echo "== Registros"
errores=$(grep -l '"level":"error"' "$WORK"/log/*.log 2>/dev/null)
if [[ -z "$errores" ]]; then ok "ningun servicio registro errores"; else
  for f in $errores; do mal "errores en $(basename "$f")"; grep '"level":"error"' "$f" | head -3 >&2; done
fi

echo
if [[ $fallos -gt 0 ]]; then echo "E2E: $fallos fallos" >&2; exit 1; fi
echo "E2E: OK"

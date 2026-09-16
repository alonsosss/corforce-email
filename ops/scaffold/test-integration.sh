#!/usr/bin/env bash
# Pruebas de integracion (//go:build integration) contra un Postgres y un Redis reales.
#
# Por que: son las unicas pruebas que ejercitan de verdad el SQL, las politicas RLS, los
# roles de la celda, las migraciones aplicadas dos veces y los scripts de Redis, y solo
# corrian cuando alguien las lanzaba a mano con su variable. Sin la variable se saltaban en
# silencio: un "ok" que no habia probado nada.
#
# Que hace:
#   1. Comprueba que toda variable de prueba que lee una prueba de integracion esta definida
#      aqui: una prueba nueva con una variable que nadie le da no puede pasar desapercibida.
#   2. Levanta un Postgres (pgvector/pgvector:pg16) y un Redis (redis:7.4.10-alpine)
#      desechables, o adopta los que le dan (CI: los services de GitHub Actions), y les
#      cambia la credencial por una aleatoria de esta ejecucion. La prueba del TLS hacia
#      Redis crea ella misma un Redis con tls-port (cfm-it-tls-redis, puerto base+2).
#   3. Crea una base por variable *_TEST_DSN en ese Postgres y exporta todas las variables.
#      Cada prueba migra su propia base; las que aun dan su base por migrada reciben aqui
#      sus migraciones (MIGRADAS_POR_EL_SCRIPT), dos veces.
#   4. Corre go test -tags integration -race -count=1 -p 1 con INTEGRATION_REQUIRED=1: los
#      paquetes van en serie (dos paquetes migrando la misma instancia a la vez ya se
#      bloquearon una vez) y falla si una prueba falla O SE SALTA. Los saltos se cuentan en
#      la salida -json de go test, asi que tambien se detectan en los paquetes cuyo helper
#      todavia no respeta INTEGRATION_REQUIRED.
#
# Uso:
#   make test-integration
#   IT_PACKAGES='./services/billing/...' make test-integration   # acota los paquetes
#   IT_KEEP=1 make test-integration       # deja contenedores y la salida json
#   IT_PORT_BASE=26000 make test-integration   # otro rango si 27000-27002 esta ocupado
#   IT_REDIS_TLS_PORT=27890 make test-integration   # solo el puerto del Redis TLS
#
# En CI, con los contenedores ya levantados por services:
#   IT_PG_CONTAINER=<id> IT_PG_PORT=27000 IT_PG_USER=cfm_it \
#   IT_REDIS_CONTAINER=<id> IT_REDIS_PORT=27001 bash ops/scaffold/test-integration.sh
#
# Los puertos quedan por debajo del rango efimero del sistema: dentro de el, el kernel puede
# dar el puerto a una conexion saliente justo antes de que el contenedor lo abra.
set -uo pipefail
LC_ALL=C
export LC_ALL

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

for herramienta in docker python3 go; do
  command -v "$herramienta" >/dev/null 2>&1 || { echo "test-integration: falta $herramienta" >&2; exit 2; }
done

# ── Variables que reciben las pruebas ────────────────────────────────────────
#
# Una base por variable: cada paquete migra la suya sin pisar ni esperar a otro. Las
# variables compartidas por varios paquetes del mismo servicio (BILLING, CONTACTS,
# SUPPRESSION, MAIL_DIRECTORY, MAIL_SECURITY, TRANSACTIONAL) comparten base, y con -p 1
# nunca corren a la vez.
declare -A BASES=(
  [TEST_DATABASE_URL]=it_db
  [OUTBOX_TEST_DSN]=it_outbox
  [ACCESS_CONTROL_TEST_DSN]=it_access_control
  [ANALYTICS_TEST_DSN]=it_analytics
  [AUTOMATIONS_TEST_DSN]=it_automations
  [BILLING_TEST_DSN]=it_billing
  [CAMPAIGNS_TEST_DSN]=it_campaigns
  [CONTACTS_TEST_DSN]=it_contacts
  [DOMAIN_SERVICE_TEST_DSN]=it_domain_service
  [IDENTITY_TEST_DSN]=it_identity
  [MAIL_AUTH_TEST_DSN]=it_mail_auth
  [MAIL_DIRECTORY_TEST_DSN]=it_mail_directory
  [MAIL_SECURITY_TEST_DSN]=it_mail_security
  [ORGANIZATION_TEST_DSN]=it_organization
  [REPUTATION_TEST_DSN]=it_reputation
  [SCHEDULER_TEST_DSN]=it_scheduler
  [SUPPRESSION_TEST_DSN]=it_suppression
  [TEMPLATES_TEST_DSN]=it_templates
  [TRANSACTIONAL_TEST_DSN]=it_transactional
)

# Pruebas que dan su base por migrada y todavia no aplican sus migraciones: el script se
# las aplica, en orden y dos veces. Cuando la prueba las aplique ella misma, su entrada se
# retira de aqui.
declare -A MIGRADAS_POR_EL_SCRIPT=(
  [CONTACTS_TEST_DSN]="migrations/tenant/canonical/platform migrations/tenant/canonical/contacts"
)

REDIS_ADDR_VARS=(REDIS_TEST_ADDR MAIL_SECURITY_TEST_REDIS REPUTATION_TEST_REDIS_ADDR WEBMAIL_TEST_REDIS_ADDR)
REDIS_PASSWORD_VARS=(REDIS_TEST_PASSWORD MAIL_SECURITY_TEST_REDIS_PASSWORD REPUTATION_TEST_REDIS_PASSWORD WEBMAIL_TEST_REDIS_PASSWORD)
# La prueba del rol de celda crea sus propias bases desde la de mantenimiento y usa el psql
# del contenedor (no hay cliente en el anfitrion). La de los roles por servicio (pkg/db)
# hace lo mismo: crea registro, dos bases de empresa y dos celdas, y corre los scripts de
# ops/db contra ellas.
OTRAS_VARS=(CELL_ROLE_TEST_DSN CELL_ROLE_TEST_CONTAINER DB_ROLES_TEST_DSN DB_ROLES_TEST_CONTAINER)
# La prueba del TLS hacia Redis (pkg/config) genera su CA y su certificado y crea con ellos
# su propio Redis con tls-port: recibe solo el nombre del contenedor y el puerto.
REDIS_TLS_VARS=(REDIS_TLS_TEST_CONTAINER REDIS_TLS_TEST_PORT)

echo "== Variables de las pruebas de integracion"
python3 - "$ROOT" "${!BASES[*]} ${REDIS_ADDR_VARS[*]} ${REDIS_PASSWORD_VARS[*]} ${OTRAS_VARS[*]} ${REDIS_TLS_VARS[*]}" <<'PY' || exit 1
import os, re, subprocess, sys

root, declaradas = sys.argv[1], set(sys.argv[2].split())
# Los ficheros versionados y los nuevos sin ignorar: .gitignore deja fuera las copias
# locales de referencia y las dependencias sin nombrarlas aqui.
listado = subprocess.run(
    ["git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "*_test.go"],
    check=True, capture_output=True).stdout.decode().split("\0")
lee = re.compile(r'(?:os\.Getenv|integrationEnv)\(\s*(?:t\s*,\s*)?"([A-Z][A-Z0-9_]*)"')
usadas = {}
for rel in filter(None, listado):
    ruta = os.path.join(root, rel)
    if not os.path.isfile(ruta):
        continue
    txt = open(ruta, encoding="utf-8", errors="ignore").read()
    if not re.search(r"^//go:build\s+integration\b", txt, re.M):
        continue
    for m in lee.finditer(txt):
        if "TEST" in m.group(1).split("_"):
            usadas.setdefault(m.group(1), set()).add(rel)

faltan = sorted(set(usadas) - declaradas)
if faltan:
    print("Pruebas de integracion que leen una variable que test-integration.sh no define:", file=sys.stderr)
    for v in faltan:
        print(f"  {v}: {', '.join(sorted(usadas[v]))}", file=sys.stderr)
    print("Anadela en ops/scaffold/test-integration.sh (con base propia si es un DSN).", file=sys.stderr)
    sys.exit(1)
for v in sorted(declaradas - set(usadas)):
    print(f"  aviso: test-integration.sh define {v} y ninguna prueba la lee", file=sys.stderr)
print(f"  {len(usadas)} variables leidas por las pruebas, todas definidas")
PY

# ── Infraestructura ──────────────────────────────────────────────────────────
BASE="${IT_PORT_BASE:-27000}"
PG_PORT="${IT_PG_PORT:-$BASE}"
REDIS_PORT="${IT_REDIS_PORT:-$((BASE + 1))}"
REDIS_TLS_PORT="${IT_REDIS_TLS_PORT:-$((BASE + 2))}"
REDIS_TLS_CONTAINER=cfm-it-tls-redis
PG_USER="${IT_PG_USER:-cfm_it}"
read -r EFIMERO_MIN EFIMERO_MAX < /proc/sys/net/ipv4/ip_local_port_range 2>/dev/null || { EFIMERO_MIN=32768; EFIMERO_MAX=60999; }
for p in "$PG_PORT" "$REDIS_PORT" "$REDIS_TLS_PORT"; do
  if (( p >= EFIMERO_MIN && p <= EFIMERO_MAX )); then
    echo "test-integration: el puerto $p cae en el rango efimero $EFIMERO_MIN-$EFIMERO_MAX; usa otro IT_PORT_BASE" >&2
    exit 2
  fi
done

if [[ -n "${IT_PG_CONTAINER:-}${IT_REDIS_CONTAINER:-}" ]]; then
  if [[ -z "${IT_PG_CONTAINER:-}" || -z "${IT_REDIS_CONTAINER:-}" ]]; then
    echo "test-integration: IT_PG_CONTAINER e IT_REDIS_CONTAINER se dan juntos" >&2
    exit 2
  fi
  PROPIOS=0 PG_CONTAINER="$IT_PG_CONTAINER" REDIS_CONTAINER="$IT_REDIS_CONTAINER"
else
  PROPIOS=1 PG_CONTAINER=cfm-it-pg REDIS_CONTAINER=cfm-it-redis
fi

# Una sola ejecucion local a la vez: la segunda recrearia los contenedores con otra
# credencial y dejaria a la primera fallando contra ellos (y al terminar, borrandoselos).
# Cada contenedor lleva el pid de la ejecucion que lo creo; si ese proceso sigue vivo se
# rechaza el arranque ANTES de instalar la limpieza, que si no borraria los ajenos. Un
# contenedor sin dueno vivo es un resto de una ejecucion cortada y se reemplaza.
if (( PROPIOS )); then
  for c in "$PG_CONTAINER" "$REDIS_CONTAINER"; do
    dueno="$(docker inspect -f '{{index .Config.Labels "cfm-it.pid"}}' "$c" 2>/dev/null)" || continue
    if [[ "$dueno" =~ ^[0-9]+$ ]] && ps -p "$dueno" >/dev/null 2>&1; then
      echo "test-integration: otra ejecucion (pid $dueno) esta usando $c; espera a que termine o detenla" >&2
      exit 3
    fi
  done
fi

WORK="$(mktemp -d)"
limpiar() {
  if [[ "${IT_KEEP:-0}" == "1" ]]; then
    echo "IT_KEEP=1: contenedores $PG_CONTAINER ($PG_PORT) y $REDIS_CONTAINER ($REDIS_PORT); salida en $WORK"
    return
  fi
  if (( PROPIOS )); then
    docker rm -f "$PG_CONTAINER" "$REDIS_CONTAINER" >/dev/null 2>&1
  fi
  # El Redis TLS lo crea y borra la prueba; esto solo recoge el de una ejecucion cortada.
  docker rm -f "$REDIS_TLS_CONTAINER" >/dev/null 2>&1
  rm -rf "$WORK"
}
trap limpiar EXIT

rand_hex() { head -c "$1" /dev/urandom | od -An -tx1 | tr -d ' \n'; }

esperar() {
  local que="$1"; shift
  for _ in $(seq 1 90); do
    "$@" >/dev/null 2>&1 && return 0
    sleep 1
  done
  echo "test-integration: $que no respondio" >&2
  return 1
}

echo "== Postgres y Redis desechables"
if (( PROPIOS )); then
  docker rm -f "$PG_CONTAINER" "$REDIS_CONTAINER" >/dev/null 2>&1
  # La contrasena de arranque no sale del proceso (-e sin valor la toma del entorno) y se
  # sustituye abajo, igual que la de los services de CI.
  POSTGRES_PASSWORD="$(rand_hex 16)" docker run -d --name "$PG_CONTAINER" --label "cfm-it.pid=$$" \
    -e POSTGRES_USER="$PG_USER" -e POSTGRES_PASSWORD -e POSTGRES_DB=postgres \
    -p "127.0.0.1:$PG_PORT:5432" pgvector/pgvector:pg16 >/dev/null || exit 1
  docker run -d --name "$REDIS_CONTAINER" --label "cfm-it.pid=$$" \
    -p "127.0.0.1:$REDIS_PORT:6379" redis:7.4.10-alpine >/dev/null || exit 1
fi
# Con -h el servidor temporal del arranque (solo socket) no cuenta como listo.
esperar "Postgres" docker exec "$PG_CONTAINER" pg_isready -h 127.0.0.1 -U "$PG_USER" -d postgres \
  || { docker logs --tail 40 "$PG_CONTAINER" >&2; exit 1; }
esperar "Redis" docker exec "$REDIS_CONTAINER" redis-cli ping || exit 1

# Por el socket del contenedor (confianza local de la imagen oficial): no hay psql en el
# anfitrion ni en el runner de CI, y asi no hace falta la contrasena de arranque.
psql_admin() {
  docker exec -i -e PGOPTIONS='-c client_min_messages=warning' "$PG_CONTAINER" \
    psql -U "$PG_USER" -v ON_ERROR_STOP=1 -q "$@"
}

PG_PASSWORD="$(rand_hex 24)"
REDIS_PASSWORD="$(rand_hex 24)"
IT_NEW_PASSWORD="$PG_PASSWORD" docker exec -i -e IT_NEW_PASSWORD "$PG_CONTAINER" \
  psql -U "$PG_USER" -d postgres -v ON_ERROR_STOP=1 -q <<'SQL' || { echo "test-integration: no se pudo fijar la credencial de Postgres" >&2; exit 1; }
\getenv pw IT_NEW_PASSWORD
ALTER ROLE CURRENT_USER PASSWORD :'pw';
SQL
[[ "$(PGPASSWORD="$PG_PASSWORD" docker exec -e PGPASSWORD "$PG_CONTAINER" \
      psql -h 127.0.0.1 -U "$PG_USER" -d postgres -Atqc 'SELECT 1' 2>&1)" == "1" ]] \
  || { echo "test-integration: Postgres no acepta la credencial nueva por TCP" >&2; exit 1; }
[[ "$(printf 'CONFIG SET requirepass %s\n' "$REDIS_PASSWORD" | docker exec -i "$REDIS_CONTAINER" redis-cli 2>&1)" == "OK" ]] \
  || { echo "test-integration: no se pudo fijar la contrasena de Redis" >&2; exit 1; }
[[ "$(REDISCLI_AUTH="$REDIS_PASSWORD" docker exec -e REDISCLI_AUTH "$REDIS_CONTAINER" redis-cli ping 2>&1)" == "PONG" ]] \
  || { echo "test-integration: Redis no acepta la contrasena nueva" >&2; exit 1; }
echo "  credenciales aleatorias de esta ejecucion en Postgres ($PG_PORT) y Redis ($REDIS_PORT)"

echo "== Una base por variable"
dsn() { printf 'postgres://%s:%s@127.0.0.1:%s/%s?sslmode=disable' "$PG_USER" "$PG_PASSWORD" "$PG_PORT" "$1"; }
while IFS= read -r var; do
  db="${BASES[$var]}"
  psql_admin -d postgres -v db="$db" <<'SQL' || { echo "test-integration: no se pudo crear $db" >&2; exit 1; }
DROP DATABASE IF EXISTS :"db" WITH (FORCE);
CREATE DATABASE :"db";
SQL
  export "$var=$(dsn "$db")"
  echo "  $var -> $db"
done < <(printf '%s\n' "${!BASES[@]}" | sort)

for var in $(printf '%s\n' "${!MIGRADAS_POR_EL_SCRIPT[@]}" | sort); do
  db="${BASES[$var]}"
  for pasada in 1 2; do
    for dir in ${MIGRADAS_POR_EL_SCRIPT[$var]}; do
      for f in "$dir"/*.sql; do
        psql_admin -d "$db" < "$f" >/dev/null \
          || { echo "test-integration: $f (pasada $pasada) en $db" >&2; exit 1; }
      done
    done
  done
  echo "  $var: migraciones aplicadas por el script, dos veces (${MIGRADAS_POR_EL_SCRIPT[$var]})"
done

export CELL_ROLE_TEST_DSN; CELL_ROLE_TEST_DSN="$(dsn postgres)"
export CELL_ROLE_TEST_CONTAINER="$PG_CONTAINER"
export DB_ROLES_TEST_DSN; DB_ROLES_TEST_DSN="$(dsn postgres)"
export DB_ROLES_TEST_CONTAINER="$PG_CONTAINER"
for v in "${REDIS_ADDR_VARS[@]}"; do export "$v=127.0.0.1:$REDIS_PORT"; done
for v in "${REDIS_PASSWORD_VARS[@]}"; do export "$v=$REDIS_PASSWORD"; done
export REDIS_TLS_TEST_CONTAINER="$REDIS_TLS_CONTAINER" REDIS_TLS_TEST_PORT="$REDIS_TLS_PORT"
export INTEGRATION_REQUIRED=1

# ── Pruebas ──────────────────────────────────────────────────────────────────
cat > "$WORK/resumen.py" <<'PY'
import json, sys
from collections import defaultdict

salida = defaultdict(list)      # (paquete, prueba) -> lineas
compilacion = defaultdict(list) # ImportPath -> salida del compilador
paquetes = {}                   # paquete -> pass | fail | skip
cuenta = defaultdict(int)
saltadas, fallidas = [], []

for linea in sys.stdin:
    try:
        ev = json.loads(linea)
    except ValueError:
        print(linea, end="", flush=True)
        continue
    accion = ev.get("Action")
    if accion == "build-output":
        compilacion[ev.get("ImportPath", "")].append(ev.get("Output", ""))
        continue
    if accion == "build-fail":
        continue
    pkg, prueba = ev.get("Package", ""), ev.get("Test")
    if accion == "output":
        salida[(pkg, prueba)].append(ev.get("Output", ""))
        continue
    if accion not in ("pass", "fail", "skip"):
        continue
    if prueba:
        cuenta[accion] += 1
        (saltadas if accion == "skip" else fallidas if accion == "fail" else []).append((pkg, prueba))
        continue
    paquetes[pkg] = accion
    if accion == "pass":
        print(f"ok     {pkg}  {ev.get('Elapsed', 0):.1f}s", flush=True)
    elif accion == "fail":
        print(f"FALLA  {pkg}", flush=True)
        if ev.get("FailedBuild"):
            for l in compilacion.get(ev["FailedBuild"], []):
                print("       " + l.rstrip("\n"))

def motivo(pkg, prueba):
    lineas = [l.strip() for l in salida[(pkg, prueba)]
              if l.strip() and not l.strip().startswith(("=== ", "--- "))]
    return lineas[-1] if lineas else "(sin motivo)"

ok = [p for p, a in paquetes.items() if a == "pass"]
mal = sorted(p for p, a in paquetes.items() if a == "fail")
sin = [p for p, a in paquetes.items() if a == "skip"]
print()
print("== Resumen")
print(f"paquetes: {len(ok)} ok, {len(mal)} fallidos, {len(sin)} sin pruebas")
print(f"pruebas:  {cuenta['pass']} pasan, {cuenta['fail']} fallan, {cuenta['skip']} saltadas")
if saltadas:
    print("\nSaltadas (aqui un salto es un fallo: la prueba no corrio):")
    for pkg, prueba in saltadas:
        print(f"  {pkg} {prueba}: {motivo(pkg, prueba)}")
if fallidas:
    print("\nFallidas:")
    for pkg, prueba in fallidas:
        print(f"  --- {pkg} {prueba}")
        for l in salida[(pkg, prueba)][-80:]:
            print("      " + l.rstrip("\n"))
for pkg in mal:
    if not any(p == pkg for p, _ in fallidas):
        print(f"\n  --- {pkg} (fallo del paquete, sin una prueba concreta)")
        for l in salida[(pkg, None)][-60:]:
            print("      " + l.rstrip("\n"))
sys.exit(1 if (mal or saltadas or fallidas) else 0)
PY

read -r -a PAQUETES <<<"${IT_PACKAGES:-./...}"
echo "== go test -tags integration -race -count=1 -p 1 ${PAQUETES[*]} (INTEGRATION_REQUIRED=1)"
go test -tags integration -race -count=1 -p 1 -json "${PAQUETES[@]}" 2>&1 \
  | tee "$WORK/go-test.json" | python3 "$WORK/resumen.py"
estados=("${PIPESTATUS[@]}")
rc_go="${estados[0]}"
rc_resumen="${estados[2]}"

if [[ "$rc_go" != "0" || "$rc_resumen" != "0" ]]; then
  echo "test-integration: FALLA (go test=$rc_go, resumen=$rc_resumen; IT_KEEP=1 conserva la salida json)" >&2
  exit 1
fi
echo "test-integration: OK"

#!/usr/bin/env bash
# Prueba de punta a punta de los MOTORES de correo contra una celda real (`make e2e-mail`).
#
# Levanta la pila de deploy/mail (Postfix, Dovecot, Rspamd, ClamAV, Unbound, Olefy,
# postfix-tlspol y redis-mail) con el perfil de prueba deploy/mail/docker-compose.e2e.yml,
# junto a los servicios de la celda que los motores llaman por nombre (mail-auth y
# mail-security como mail-policy), mail-directory y el webmail, todos en contenedores con sus
# Dockerfile. El plano de control (organization, identity, access-control, gateway) y
# domain-service corren como binarios del host, igual que en ops/e2e/run.sh, contra
# Postgres, NATS y Redis desechables.
#
# La celda y la empresa se crean por el camino real: ops/db/bootstrap-platform.sh, alta de
# la empresa por el gateway, dominio verificado por domain-service contra un DNS de la prueba
# (la verificacion activa el dominio en mail-directory y entrega la clave DKIM a
# mail-security), y buzones, aliases y enrutado por el API de mail-directory. Despues
# comprueba cada mapa de Postfix con postmap, Dovecot con doveadm e IMAP (tambien el usuario
# maestro del webmail y su restriccion de red), el envio autenticado con la regla de
# remitentes, Rspamd con DKIM y el mapa settings de mail-policy, el antivirus y la cuarentena
# (con su enlace firmado por el gateway), las claves de Redis que escribe mail-security, el
# webmail por el gateway y la migracion de buzones: mail-migration (binario del host) y su
# ejecutor con imapsync REAL (contenedor en su propia red, deploy/mail/migration-runner) copian
# de un buzon a otro del mismo Dovecot, con ClamAV real en el camino.
#
# Cada paso escribe OK o FALLA y la ejecucion termina con error si alguno falla. Las
# credenciales se generan en cada ejecucion; ninguna vive en este fichero.
#
# Uso:
#   make e2e-mail
#   E2E_KEEP=1 make e2e-mail                  # deja contenedores, red y registros
#   E2E_MAIL_PORT_BASE=29100 make e2e-mail    # otro rango si 29000-29099 esta ocupado
#   E2E_MAIL_IPV4_NETWORK=172.31.29 make e2e-mail  # otra subred /24 para la red de los motores
#
# Una sola ejecucion a la vez (puede correr junto a make e2e, que usa otro prefijo y otros
# puertos): con otra en marcha sale con 3 sin tocar nada (e2e_reservar en ops/e2e/lib.sh).
#
# Las firmas de ClamAV quedan en el volumen E2E_MAIL_CLAMAV_VOLUME (por defecto
# cfm-e2e-mail-clamav-signatures) entre ejecuciones; E2E_MAIL_PURGE_SIGNATURES=1 lo borra al
# terminar. Motivo y diferencias con produccion: deploy/mail/README.md.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
# shellcheck source=ops/e2e/lib.sh
source ops/e2e/lib.sh

INICIO=$(date +%s)
BASE="${E2E_MAIL_PORT_BASE:-29000}"
PG_PORT=$((BASE + 90)) NATS_PORT=$((BASE + 91)) REDIS_PORT=$((BASE + 92)) DNS_PORT=$((BASE + 53))
declare -A PORT=(
  [identity]=$((BASE + 1)) [access-control]=$((BASE + 2)) [organization]=$((BASE + 3))
  [mail-directory]=$((BASE + 40)) [mail-auth]=$((BASE + 41)) [mail-security]=$((BASE + 42))
  [domain-service]=$((BASE + 43)) [webmail]=$((BASE + 44)) [gateway]=$((BASE + 80)) [mail-migration]=$((BASE + 56))
)
# API del ejecutor de mail-migration: el ejecutor, en su contenedor, la alcanza por host.docker.internal.
MIGRATION_RUNNER_PORT=$((BASE + 57))
e2e_fuera_del_rango_efimero "$PG_PORT" "$NATS_PORT" "$REDIS_PORT" "$DNS_PORT" "${PORT[@]}" "$MIGRATION_RUNNER_PORT"

E2E_PREFIX=cfm-e2e-mail
PROYECTO=cfm-e2e-mail
export E2E_MAIL_NETWORK="${E2E_MAIL_NETWORK:-cfm-e2e-mail-engines}"
export E2E_MAIL_BRIDGE="${E2E_MAIL_BRIDGE:-br-cfme2email}"
export IPV4_NETWORK="${E2E_MAIL_IPV4_NETWORK:-172.30.29}"
export E2E_MAIL_CLAMAV_VOLUME="${E2E_MAIL_CLAMAV_VOLUME:-cfm-e2e-mail-clamav-signatures}"
# Red propia del ejecutor de migracion (la de produccion se llama mail-migration).
export E2E_MIGRATION_NETWORK="${E2E_MIGRATION_NETWORK:-cfm-e2e-mail-migration}"
export E2E_MIGRATION_BRIDGE="${E2E_MIGRATION_BRIDGE:-br-cfme2emig}"
export MAIL_MIGRATION_IPV4_NETWORK="${E2E_MAIL_MIGRATION_IPV4_NETWORK:-172.30.30}"
MIGRATION_DOVECOT_IP="$MAIL_MIGRATION_IPV4_NETWORK.250"
e2e_reservar
WORK="$(mktemp -d)"
MAILDIR="$WORK/repo/deploy/mail"
TLS="$WORK/tls"

c() { printf '%s-%s-1' "$PROYECTO" "$1"; }
en() { local s="$1"; shift; docker exec -i "$(c "$s")" "$@"; }

# Los secretos de los motores y de la celda solo llegan a compose, no al entorno de los
# binarios del host.
compose() {
  CELL_DB_PASSWORD="${CELL_PASS}" MAIL_DB_PASSWORD="$MAIL_DB_PASS" MAIL_REDIS_PASSWORD="$MAIL_REDIS_PASS" \
    DOVECOT_MASTER_USER="${MASTER_USER}" DOVECOT_MASTER_PASS="$MASTER_PASS" DOVEADM_API_KEY="${DOVEADM_KEY}" QUEUE_AGENT_API_KEY="${QUEUE_KEY}" \
    MAIL_MIGRATION_RUNNER_KEY="${MIGRATION_KEY}" DOVECOT_MIGRATION_MASTER_USER="${MIGRATION_MASTER_USER}" DOVECOT_MIGRATION_MASTER_PASS="${MIGRATION_MASTER_PASS}" \
    E2E_PORT_MAIL_MIGRATION_RUNNER="$MIGRATION_RUNNER_PORT" \
    docker compose -p "$PROYECTO" -f "$MAILDIR/docker-compose.mail.yml" -f "$MAILDIR/docker-compose.e2e.yml" "$@"
}

restos() {
  local ids
  ids=$(docker ps -aq --filter "label=com.docker.compose.project=$PROYECTO")
  # shellcheck disable=SC2086
  docker rm -f $ids "$E2E_PREFIX-pg" "$E2E_PREFIX-nats" "$E2E_PREFIX-redis" "$E2E_PREFIX-dns" >/dev/null 2>&1
  docker network rm "$E2E_MAIL_NETWORK" "$E2E_MIGRATION_NETWORK" >/dev/null 2>&1
  ids=$(docker volume ls -q --filter "label=com.docker.compose.project=$PROYECTO")
  # shellcheck disable=SC2086
  [[ -n "$ids" ]] && docker volume rm $ids >/dev/null 2>&1
  return 0
}

diagnostico() {
  local nombre
  echo "== Diagnostico (ultimas lineas de cada contenedor)" >&2
  for nombre in $(docker ps -a --filter "label=com.docker.compose.project=$PROYECTO" --format '{{.Names}}'); do
    echo "-- $nombre ($(docker inspect -f '{{.State.Status}} reinicios={{.RestartCount}}' "$nombre"))" >&2
    docker logs --tail 12 "$nombre" 2>&1 | sed 's/^/     /' >&2
  done
}

limpiar() {
  pkill -f "$WORK/bin/" 2>/dev/null
  if [[ "${E2E_KEEP:-0}" == "1" ]]; then
    echo "E2E_KEEP=1: contenedores $E2E_PREFIX-*, red $E2E_MAIL_NETWORK y registros en $WORK"
    e2e_liberar
    return
  fi
  restos
  [[ "${E2E_MAIL_PURGE_SIGNATURES:-0}" == "1" ]] && docker volume rm "$E2E_MAIL_CLAMAV_VOLUME" >/dev/null 2>&1
  # Los entrypoints de los motores escriben como root en su copia de la configuracion.
  docker run --rm -v "$WORK:/w" --entrypoint /bin/sh redis:7.4.10-alpine -c 'rm -rf /w/repo' >/dev/null 2>&1
  rm -rf "$WORK"
  e2e_liberar
}
trap limpiar EXIT

echo "== Preparacion"
restos
e2e_puertos_libres "$PG_PORT" "$NATS_PORT" "$REDIS_PORT" "$DNS_PORT" "${PORT[@]}" "$MIGRATION_RUNNER_PORT"
docker network inspect $(docker network ls -q) --format '{{.Name}} {{range .IPAM.Config}}{{.Subnet}} {{end}}' 2>/dev/null |
  python3 -c '
import ipaddress, sys
mias = [ipaddress.ip_network(a) for a in sys.argv[1:]]
if mias[0].overlaps(mias[1]):
    sys.exit(f"E2E: las subredes {mias[0]} y {mias[1]} se pisan; usa E2E_MAIL_IPV4_NETWORK y E2E_MAIL_MIGRATION_IPV4_NETWORK")
for linea in sys.stdin:
    nombre, *redes = linea.split()
    for r in redes:
        try:
            for mia in mias:
                if ipaddress.ip_network(r, strict=False).overlaps(mia):
                    sys.exit(f"E2E: la subred {mia} choca con la red {nombre} ({r}); usa E2E_MAIL_IPV4_NETWORK o E2E_MAIL_MIGRATION_IPV4_NETWORK")
        except ValueError:
            pass' "$IPV4_NETWORK.0/24" "$MAIL_MIGRATION_IPV4_NETWORK.0/24" || exit 2

# Copia de deploy/mail con lo versionado y lo nuevo no ignorado: los entrypoints reescriben
# ficheros de sus bind mounts (main.cf, mapas con la credencial) y el repositorio no se toca.
mkdir -p "$WORK/repo" "$WORK/log"
git ls-files -z --cached --others --exclude-standard -- deploy/mail | tar --null -T - -cf - | tar -xf - -C "$WORK/repo" \
  || { echo "no se pudo copiar deploy/mail" >&2; exit 1; }

# CA de la prueba: certificado del servidor de correo (Postfix y Dovecot, nombre MAIL_HOSTNAME)
# y de mail-auth (nombre mail-auth), los dos verificados por el webmail y por el cliente.
export MAIL_HOSTNAME=mail.cfm.test
mkdir -p "$TLS/mail"
openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj "/CN=Core Force Mail e2e CA" \
  -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" \
  -keyout "$TLS/ca-key.pem" -out "$TLS/ca.pem" >/dev/null 2>&1 || { echo "openssl: CA" >&2; exit 1; }
certificado() { # certificado <nombre> <clave> <certificado> [SAN adicional, p. ej. IP:172.30.30.250]
  openssl req -newkey rsa:2048 -nodes -subj "/CN=$1" -keyout "$2" -out "$WORK/$1.csr" >/dev/null 2>&1 &&
    openssl x509 -req -in "$WORK/$1.csr" -CA "$TLS/ca.pem" -CAkey "$TLS/ca-key.pem" -CAcreateserial -days 2 -out "$3" \
      -extfile <(printf 'subjectAltName=DNS:%s%s\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\n' "$1" "${4:+,$4}") >/dev/null 2>&1
}
# El certificado de correo lleva tambien la IP de Dovecot en la red de la migracion: el buzon de origen
# de la prueba se pide por IP (mail-migration lo resuelve desde el host, donde el nombre de la prueba no
# existe) y el ejecutor verifica su certificado como el de cualquier origen.
certificado "$MAIL_HOSTNAME" "$TLS/mail/key.pem" "$TLS/mail/cert.pem" "IP:$MIGRATION_DOVECOT_IP" || { echo "openssl: certificado de correo" >&2; exit 1; }
certificado mail-auth "$TLS/mail-auth-key.pem" "$TLS/mail-auth.pem" || { echo "openssl: certificado de mail-auth" >&2; exit 1; }
cp deploy/mail/ssl-example/dhparams.pem "$TLS/mail/dhparams.pem"
chmod 644 "$TLS"/*.pem "$TLS/mail"/*.pem
python3 ops/e2e/mail_client.py eicar > "$WORK/eicar.com"

# ── Entorno ──────────────────────────────────────────────────────────────────
e2e_entorno_comun || exit 1
CELL_PASS="$(rand_hex 24)"
MAIL_DB_PASS="$(rand_hex 24)"
MAIL_REDIS_PASS="$(rand_hex 24)"
MASTER_USER="e2e-webmail"
MASTER_PASS="$(rand_hex 24)"
# Clave del API HTTP de doveadm: la reciben Dovecot y mail-security (revocacion en Dovecot).
DOVEADM_KEY="$(rand_hex 32)"
# Clave del agente de la cola de Postfix: la reciben Postfix y mail-security (gestor de cola).
QUEUE_KEY="$(rand_hex 32)"
# Clave del ejecutor de migracion (la reciben mail-migration y el ejecutor) y usuario maestro de Dovecot
# propio de la migracion (Dovecot y el ejecutor), distinto del del webmail.
MIGRATION_KEY="$(rand_hex 32)"
MIGRATION_MASTER_USER="e2e-migracion"
MIGRATION_MASTER_PASS="$(rand_hex 24)"
export CELL_CODE=pe-01 CELL_DB_NAME=mail_cell_pe_01 DEFAULT_CELL_CODE=pe-01
export MAIL_MX_HOSTNAME="$MAIL_HOSTNAME" MAIL_SPF_INCLUDE=include:spf.cfm.test MAIL_DMARC_RUA=dmarc@cfm.test
export MAIL_DNS_RESOLVER="127.0.0.1:$DNS_PORT"
export API_ORIGIN="http://localhost:${PORT[gateway]}" PUBLIC_BASE_URL="http://localhost:${PORT[gateway]}" CORS_ALLOWED_ORIGINS=http://localhost:3000
export IDENTITY_PORT=${PORT[identity]} ACCESS_CONTROL_PORT=${PORT[access-control]} ORGANIZATION_PORT=${PORT[organization]}
export DOMAIN_SERVICE_PORT=${PORT[domain-service]} GATEWAY_PORT=${PORT[gateway]} MAIL_MIGRATION_PORT=${PORT[mail-migration]}
for s in identity access-control organization domain-service mail-directory mail-auth mail-security webmail mail-migration; do
  var="$(echo "$s" | tr 'a-z-' 'A-Z_')_HOST"
  export "$var=127.0.0.1" "${var}_PORT=${PORT[$s]}"
done
export WEB_HOST=127.0.0.1 WEB_HOST_PORT=1
export ACCESS_CONTROL_URL="http://127.0.0.1:${PORT[access-control]}"
export MAIL_DIRECTORY_URL="http://127.0.0.1:${PORT[mail-directory]}" MAIL_SECURITY_URL="http://127.0.0.1:${PORT[mail-security]}"
GW="http://127.0.0.1:${PORT[gateway]}/api/v1"
# Compose: servicios de la celda en contenedores y motores.
export E2E_REPO_ROOT="$ROOT" E2E_TLS_DIR="$TLS"
export E2E_PG_HOST="$E2E_PREFIX-pg" E2E_NATS_HOST="$E2E_PREFIX-nats" E2E_REDIS_HOST="$E2E_PREFIX-redis"
export E2E_PORT_MAIL_DIRECTORY=${PORT[mail-directory]} E2E_PORT_MAIL_AUTH=${PORT[mail-auth]}
export E2E_PORT_MAIL_SECURITY=${PORT[mail-security]} E2E_PORT_WEBMAIL=${PORT[webmail]}
# organization corre en el host; mail-directory y mail-security le preguntan desde la red de
# los motores si cada empresa es de su celda.
export E2E_PORT_ORGANIZATION=${PORT[organization]}
export MAIL_DB_HOST="$E2E_PREFIX-pg" MAIL_DB_PORT=5432 MAIL_DB_NAME=mail_cell_pe_01 MAIL_DB_USER=mail_engine
export ENABLE_IPV6=false TZ=UTC SKIP_LETS_ENCRYPT=y SKIP_CLAMD=n SKIP_OLEFY=n
# El chequeo de salud de Unbound hace ping a resolvers publicos; los runners de CI no dejan
# salir ICMP. La resolucion con DNSSEC se comprueba abajo de todos modos.
export SKIP_UNBOUND_HEALTHCHECK="${E2E_MAIL_UNBOUND_HEALTHCHECK_SKIP:-y}"

if compose config -q 2>"$WORK/log/compose-config.log"; then ok "el perfil de prueba es un compose valido sobre docker-compose.mail.yml"; else
  mal "compose config: $(head -c 400 "$WORK/log/compose-config.log")"; exit 1
fi

echo "== Imagenes (motores con sus Dockerfile de deploy/mail, servicios de celda con los suyos)"
t0=$SECONDS
if compose build >"$WORK/log/build.log" 2>&1; then ok "imagenes construidas ($((SECONDS - t0))s)"; else
  mal "compose build"; tail -30 "$WORK/log/build.log" >&2; exit 1
fi
e2e_compilar organization identity access-control gateway domain-service mail-migration || exit 1

echo "== Red de los motores e infraestructura desechable"
docker volume create "$E2E_MAIL_CLAMAV_VOLUME" >/dev/null || exit 1
compose up --no-start >"$WORK/log/compose-create.log" 2>&1 || { mal "compose up --no-start"; tail -20 "$WORK/log/compose-create.log" >&2; exit 1; }
e2e_infra_up "$E2E_MAIL_NETWORK" || { mal "infraestructura desechable"; exit 1; }
ok "Postgres, NATS y Redis de la plataforma en la red $E2E_MAIL_NETWORK ($IPV4_NETWORK.0/24)"

echo "== Celda pe-01"
e2e_celda pe-01
expect "migracion 07: mail.sender_login_owners existe" \
  "$(sql mail_cell_pe_01 "SELECT to_regprocedure('mail.sender_login_owners(text)') IS NOT NULL AND to_regprocedure('mail.sender_identities(text)') IS NOT NULL")" "t"
expect "mail_engine puede llamarla" "$(sql mail_cell_pe_01 "SET ROLE mail_engine; SELECT count(*) FROM mail.sender_login_owners('nadie@acme.test')" | tail -1)" "0"
contains "y no lee las contrasenas de aplicacion" \
  "$(psql -v ON_ERROR_STOP=1 -q -At -d mail_cell_pe_01 -c "SET ROLE mail_engine; SELECT 1 FROM mail.app_passwords LIMIT 1" 2>&1)" "permission denied"
e2e_credencial_celda pe-01 "$CELL_PASS"
# La contrasena de mail_engine la fija operacion (deploy/mail/README.md); aqui, aleatoria.
if psql -v ON_ERROR_STOP=1 -q -d mail_registry -v pw="$MAIL_DB_PASS" <<<"ALTER ROLE mail_engine WITH LOGIN PASSWORD :'pw';" >/dev/null 2>&1; then
  ok "credencial de mail_engine para los motores"
else
  mal "ALTER ROLE mail_engine"
fi

echo "== Plano de control en el host"
arrancar organization
esperar_salud organization "${PORT[organization]}" || exit 1
e2e_plataforma pe-01
for s in identity access-control domain-service gateway; do arrancar "$s"; done
# mail-migration: la clave del ejecutor solo la recibe el, y como la prueba migra entre buzones del mismo
# Dovecot (una red privada) admite origenes privados, lo que solo se permite con ENVIRONMENT=development.
MAIL_MIGRATION_RUNNER_KEY="${MIGRATION_KEY}" MAIL_MIGRATION_RUNNER_PORT="$MIGRATION_RUNNER_PORT" \
  MAIL_MIGRATION_ALLOW_PRIVATE_SOURCES=true arrancar mail-migration
for s in identity access-control domain-service gateway mail-migration; do esperar_salud "$s" "${PORT[$s]}"; done

echo "== Servicios de la celda en contenedores (credencial de la celda)"
# redis-mail primero: mail-security es su unico escritor y reconcilia al arrancar.
compose up -d redis-mail mail-directory mail-auth mail-security >"$WORK/log/compose-celda.log" 2>&1 || { mal "compose up de la celda"; tail -20 "$WORK/log/compose-celda.log" >&2; }
for s in mail-directory mail-auth mail-security; do esperar_salud "$s" "${PORT[$s]}" && ok "$s responde"; done
expect "entran con el rol de la celda y no con el de plataforma" \
  "$(sql mail_registry "SELECT string_agg(DISTINCT usename, ',') FROM pg_stat_activity WHERE datname = 'mail_cell_pe_01' AND usename <> 'mail_admin' AND backend_type = 'client backend'")" \
  "mail_cell_pe_01_svc"

echo "== Motores (arrancan mientras se aprovisiona la empresa)"
t_motores=$SECONDS
compose up -d >"$WORK/log/compose-motores.log" 2>&1 || { mal "compose up de los motores"; tail -20 "$WORK/log/compose-motores.log" >&2; }

echo "== Empresa por el gateway"
L1=$(e2e_login "$ADMIN_EMAIL" "$ADMIN_PASS")
T1=$(echo "$L1" | jget data.access_token)
[[ -n "$T1" ]] && ok "login del superadmin" || { mal "login del superadmin: ${L1:0:200}"; diagnostico; exit 1; }
TENANT_PASS="$(rand_hex 12)Aa1!"
expect "alta de la empresa acme en pe-01" "$(e2e_alta_empresa "$T1" acme pe-01 admin@acme.test "$TENANT_PASS" | jget data.slug)" "acme"
L2=$(e2e_login admin@acme.test "$TENANT_PASS")
T2=$(echo "$L2" | jget data.access_token)
TID=$(echo "$L2" | jget data.tenant_id)
[[ -n "$T2" ]] && ok "login del tenant_admin" || { mal "login del tenant_admin: ${L2:0:200}"; diagnostico; exit 1; }
A2="Authorization: Bearer $T2"

# api <metodo> <ruta> [json]: deja API_CODE y API_BODY.
api() {
  local datos=()
  [[ -n "${3:-}" ]] && datos=(-d "$3")
  API_CODE=$(curl -s -o "$WORK/api.json" -w '%{http_code}' -X "$1" "$GW$2" -H "$A2" -H 'Content-Type: application/json' "${datos[@]}")
  API_BODY=$(cat "$WORK/api.json")
}
# creado <descripcion> <metodo> <ruta> <json>: 201 o FALLA con el cuerpo.
creado() {
  api "$2" "$3" "$4"
  if [[ "$API_CODE" == 201 ]]; then ok "$1"; else mal "$1 ($API_CODE: ${API_BODY:0:300})"; fi
}

echo "== Redis de los motores"
redis_mail() { docker exec -e REDISCLI_AUTH="$MAIL_REDIS_PASS" "$(c redis-mail)" redis-cli --no-auth-warning "$@"; }
redis_sano() { [[ "$(redis_mail PING 2>/dev/null)" == PONG ]]; }
esperar() { # esperar <descripcion> <segundos> <comando...>
  local desc="$1" limite="$2" t=$SECONDS
  shift 2
  until "$@" >/dev/null 2>&1; do
    if (( SECONDS - t >= limite )); then mal "$desc (sin exito en ${limite}s)"; return 1; fi
    sleep 2
  done
  ok "$desc ($((SECONDS - t))s)"
}
esperar "redis-mail responde con requirepass" 120 redis_sano
contains "sin contrasena no responde" "$(docker exec "$(c redis-mail)" redis-cli PING 2>&1)" "NOAUTH"

echo "== Dominio verificado por domain-service contra el DNS de la prueba"
api POST /domains '{"domain":"acme.test","purpose":"both"}'
DOMID=$(echo "$API_BODY" | jget data.id)
expect "alta del dominio en domain-service (pending, con sus registros)" "$(echo "$API_BODY" | jget data.status)" "pending"
echo "$API_BODY" > "$WORK/dominio.json"
SELECTOR=$(python3 -c '
import json, sys
d = json.load(open(sys.argv[1]))["data"]
print(next(r["host"].split("._domainkey.")[0] for r in d["dns_records"] if r["record"] == "dkim"))' "$WORK/dominio.json" 2>/dev/null)
[[ -n "$SELECTOR" ]] && ok "selector DKIM de domain-service: $SELECTOR" || mal "sin registro DKIM en la respuesta: ${API_BODY:0:300}"
# El DNS autoritativo de acme.test publica exactamente lo que domain-service pide.
python3 - "$WORK/dominio.json" > "$WORK/dns.conf" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))["data"]
print("server:")
for linea in ("interface: 0.0.0.0", "port: 53", "do-daemonize: no", 'username: ""', 'chroot: ""',
              'directory: "/tmp"', 'pidfile: ""', "use-syslog: no", 'logfile: ""', "verbosity: 1",
              "access-control: 0.0.0.0/0 allow", 'module-config: "iterator"',
              'local-zone: "%s." static' % d["domain"]):
    print("  " + linea)
for r in d["dns_records"]:
    host = r["host"].rstrip(".") + "."
    if r["type"] == "MX":
        destino, _, prioridad = r["value"].split()
        print("  local-data: \"%s 300 IN MX %s %s.\"" % (host, prioridad, destino.rstrip(".")))
    else:
        v = r["value"]
        trozos = " ".join('"%s"' % v[i:i + 255] for i in range(0, len(v), 255))
        print("  local-data: '%s 300 IN TXT %s'" % (host, trozos))
print("remote-control:")
print("  control-enable: no")
PY
docker run -d --name "$E2E_PREFIX-dns" -p "127.0.0.1:$DNS_PORT:53/udp" -p "127.0.0.1:$DNS_PORT:53/tcp" \
  -v "$WORK/dns.conf:/etc/unbound/e2e.conf:ro" --entrypoint unbound "$PROYECTO-unbound-mail" -d -c /etc/unbound/e2e.conf >/dev/null \
  || mal "DNS de la prueba"
docker network connect --alias e2e-dns "$E2E_MAIL_NETWORK" "$E2E_PREFIX-dns" >/dev/null || mal "conectar el DNS de la prueba a la red de los motores"
dns_listo() { docker logs "$E2E_PREFIX-dns" 2>&1 | grep -q 'start of service'; }
esperar "DNS autoritativo de acme.test (unbound con los registros de domain-service)" 30 dns_listo
api POST "/domains/$DOMID/verify"
expect "domain-service verifica propiedad, MX, SPF, DKIM y DMARC" "$(echo "$API_BODY" | jget data.status)" "verified"
api GET "/mail-domains?search=acme.test"
expect "la verificacion activa el dominio en mail-directory" "$(echo "$API_BODY" | jget data.0.active)" "True"
MDOMID=$(echo "$API_BODY" | jget data.0.id)
dkim_en_redis() { [[ "$(redis_mail HGET DKIM_SELECTORS acme.test)" == "$SELECTOR" ]]; }
esperar "y entrega la clave DKIM a mail-security, que la escribe en redis-mail (DKIM_SELECTORS)" 30 dkim_en_redis
expect "la clave privada del selector esta en DKIM_PRIV_KEYS" "$(redis_mail HEXISTS DKIM_PRIV_KEYS "$SELECTOR.acme.test")" "1"
dominio_en_redis() { [[ "$(redis_mail HGET DOMAIN_MAP acme.test)" == 1 ]]; }
esperar "DOMAIN_MAP recibe el dominio (mail-directory -> outbox -> NATS -> mail-security)" 60 dominio_en_redis

echo "== Directorio por el API de mail-directory (gateway)"
ANA_PASS="$(rand_hex 10)Aa1!"
BEA_PASS="$(rand_hex 10)Aa1!"
creado "buzon ana@acme.test" POST /mailboxes "{\"local_part\":\"ana\",\"domain\":\"acme.test\",\"password\":\"$ANA_PASS\",\"display_name\":\"Ana Perez\"}"
creado "buzon bea@acme.test (TLS obligatorio al recibir)" POST /mailboxes \
  "{\"local_part\":\"bea\",\"domain\":\"acme.test\",\"password\":\"$BEA_PASS\",\"display_name\":\"Bea Diaz\",\"tls_enforce_in\":true}"
creado "recurso sala@acme.test" POST /mailboxes "{\"local_part\":\"sala\",\"domain\":\"acme.test\",\"password\":\"$(rand_hex 10)Aa1!\",\"kind\":\"location\"}"
creado "alias ventas@acme.test -> ana, con permiso de envio" POST /mail-routing/aliases '{"address":"ventas@acme.test","goto":"ana@acme.test","sender_allowed":true}'
creado "alias info@acme.test -> ana, sin permiso de envio" POST /mail-routing/aliases '{"address":"info@acme.test","goto":"ana@acme.test","sender_allowed":false}'
creado "dominio alias acme-alias.test -> acme.test" POST /mail-domains/alias-domains '{"alias_domain":"acme-alias.test","target_domain":"acme.test"}'
creado "bea puede enviar como soporte@acme.test (sender_acl)" POST /mail-routing/sender-acl '{"logged_in_as":"bea@acme.test","send_as":"soporte@acme.test"}'
creado "alias temporal de spam promo@acme.test -> ana" POST /mail-routing/spam-aliases '{"address":"promo@acme.test","goto":"ana@acme.test","permanent":true}'
creado "reescritura antiguo@acme.test -> ana" POST /mail-routing/recipient-maps '{"old_dest":"antiguo@acme.test","new_dest":"ana@acme.test"}'
creado "politica TLS para tls.partner.test" POST /mail-routing/tls-policies '{"dest":"tls.partner.test","policy":"encrypt","parameters":""}'
TRANSPORT_PASS="$(rand_hex 12)"
creado "transporte partner.test con credencial" POST /mail-routing/transports \
  "{\"destination\":\"partner.test\",\"nexthop\":\"[smtp.partner.test]:587\",\"username\":\"tuser\",\"password\":\"$TRANSPORT_PASS\"}"
creado "transporte por MX" POST /mail-routing/transports '{"destination":"mx\\.partner-mx\\.test","nexthop":"[smtp.partner-mx.test]:25","is_mx_based":true}'
RELAY_PASS="$(rand_hex 12)"
creado "relayhost de salida del dominio" POST /mail-routing/relayhosts "{\"hostname\":\"relay.partner.test:587\",\"username\":\"relayuser\",\"password\":\"$RELAY_PASS\"}"
RELAYID=$(echo "$API_BODY" | jget data.id)
api PATCH "/mail-domains/$MDOMID" "{\"relayhost_id\":\"$RELAYID\"}"
expect "acme.test sale por ese relayhost" "$API_CODE" "200"
RELAY2_PASS="$(rand_hex 12)"
creado "relayhost propio de un buzon" POST /mail-routing/relayhosts "{\"hostname\":\"relay2.partner.test:2525\",\"username\":\"relay2user\",\"password\":\"$RELAY2_PASS\"}"
RELAY2ID=$(echo "$API_BODY" | jget data.id)
CARLA_PASS="$(rand_hex 10)Aa1!"
creado "buzon carla@acme.test con TLS obligatorio al enviar y su relayhost" POST /mailboxes \
  "{\"local_part\":\"carla\",\"domain\":\"acme.test\",\"password\":\"$CARLA_PASS\",\"tls_enforce_out\":true,\"relayhost_id\":\"$RELAY2ID\"}"
creado "dominio de respaldo (backup MX) respaldo.test" POST /mail-domains '{"domain":"respaldo.test","backupmx":true,"relay_all_recipients":true,"relay_unknown_only":true}'
RESPID=$(echo "$API_BODY" | jget data.id)
api PATCH "/mail-domains/$RESPID" '{"active":true}'
expect "activar un dominio por el API se rechaza (activar es consecuencia de verificar)" "$API_CODE" "422"
# La activacion es la llamada interna que hace domain-service tras verificar (como en
# ops/e2e/run.sh): verificar por DNS un backup MX no anade nada a lo que se prueba aqui.
expect "lo activa la llamada interna de domain-service" "$(curl -s -o /dev/null -w '%{http_code}' -X PUT \
  "http://127.0.0.1:${PORT[mail-directory]}/internal/mail-directory/domains/respaldo.test/activation" \
  -H "X-Gateway-Token: $INTERNAL_GATEWAY_TOKEN" -H "X-Tenant-ID: $TID" -H 'Content-Type: application/json' -d '{"active":true}')" "200"
creado "alias conocido del backup MX" POST /mail-routing/aliases '{"address":"conocido@respaldo.test","goto":"conocido@respaldo.test"}'

echo "== Motores sanos"
unbound_sano() { [[ "$(docker inspect -f '{{.State.Health.Status}}' "$(c unbound-mail)")" == healthy ]]; }
clamd_sano() { [[ "$(en clamd-mail sh -c 'echo PING | nc -w 3 127.0.0.1 3310' 2>/dev/null)" == PONG ]]; }
postfix_sano() { en postfix-mail postfix status; }
dovecot_sano() { en dovecot-mail doveadm service status; }
rspamd_sano() { [[ "$(en rspamd-mail wget -qO- http://127.0.0.1:11334/ping 2>/dev/null)" == pong* ]]; }
corriendo() { [[ "$(docker inspect -f '{{.State.Running}}' "$(c "$1")" 2>/dev/null)" == true ]]; }
esperar "Unbound sano" 180 unbound_sano
esperar "Postfix arranca contra la celda (mapas pgsql como mail_engine)" 300 postfix_sano
esperar "Dovecot arranca (userdb SQL, passdb por mail-auth)" 300 dovecot_sano
esperar "Rspamd arranca (espera a mail-policy 8081 y 9081)" 300 rspamd_sano
esperar "ClamAV responde a PING con sus firmas (freshclam)" "${E2E_MAIL_CLAMD_TIMEOUT:-900}" clamd_sano
esperar "postfix-tlspol en marcha" 60 corriendo postfix-tlspol-mail
esperar "olefy en marcha" 60 corriendo olefy-mail
esperar_salud webmail "${PORT[webmail]}" && ok "webmail responde"
ok "pila de motores lista en $((SECONDS - t_motores))s"
DNSSEC=$(en unbound-mail dig +dnssec +time=5 +tries=2 @127.0.0.1 isc.org SOA 2>&1)
contains "Unbound resuelve con validacion DNSSEC (flag ad)" "$(grep -m1 'flags:' <<<"$DNSSEC")" " ad"
contains "y rechaza una firma rota (dnssec-failed.org)" "$(en unbound-mail dig +time=5 +tries=2 @127.0.0.1 dnssec-failed.org A 2>&1)" "SERVFAIL"

echo "== Mapas de Postfix (postmap -q contra cada mapa pgsql)"
# mapa <mapa> <clave> <esperado; vacio = sin resultado>
PROBADOS=" "
mapa() {
  local salida rc
  PROBADOS+="$1.cf "
  salida=$(docker exec "$(c postfix-mail)" postmap -c /opt/postfix/conf -q "$2" "pgsql:/opt/postfix/conf/sql/$1.cf" 2>&1)
  rc=$?
  # Una consulta CASE ... ELSE NULL devuelve una fila NULL: Postfix la ignora con un aviso y
  # equivale a no tener resultado.
  salida=$(grep -v 'empty lookup result for' <<<"$salida")
  salida="${salida%"${salida##*[![:space:]]}"}"
  if [[ -z "$3" ]]; then
    if [[ $rc -eq 1 && -z "$salida" ]]; then ok "$1: $2 sin resultado"; else mal "$1: $2 (rc $rc: '${salida:0:200}'; se esperaba sin resultado)"; fi
  elif [[ $rc -eq 0 && "$salida" == "$3" ]]; then ok "$1: $2 -> $3"
  else mal "$1: $2 (rc $rc: '${salida:0:200}', esperado '$3')"
  fi
}
mapa pgsql_virtual_domains_maps acme.test acme.test
mapa pgsql_virtual_domains_maps acme-alias.test acme-alias.test
mapa pgsql_virtual_domains_maps respaldo.test ""
mapa pgsql_virtual_domains_maps otro.test ""
mapa pgsql_virtual_mailbox_maps ana@acme.test "maildir:/var/vmail/acme.test/ana/"
mapa pgsql_virtual_mailbox_maps nadie@acme.test ""
mapa pgsql_virtual_alias_maps ventas@acme.test ana@acme.test
mapa pgsql_virtual_alias_maps nadie@acme.test ""
mapa pgsql_virtual_alias_domain_maps bea@acme-alias.test bea@acme.test
mapa pgsql_virtual_alias_domain_maps nadie@acme-alias.test ""
mapa pgsql_virtual_resource_maps sala@acme.test null@localhost
mapa pgsql_virtual_resource_maps ana@acme.test ""
mapa pgsql_virtual_spamalias_maps promo@acme.test ana@acme.test
mapa pgsql_recipient_canonical_maps antiguo@acme.test ana@acme.test
mapa pgsql_virtual_sender_acl ana@acme.test ana@acme.test
mapa pgsql_virtual_sender_acl ventas@acme.test ana@acme.test
mapa pgsql_virtual_sender_acl ana@acme-alias.test ana@acme.test
mapa pgsql_virtual_sender_acl soporte@acme.test bea@acme.test
mapa pgsql_virtual_sender_acl bea@acme.test bea@acme.test
mapa pgsql_virtual_sender_acl info@acme.test ""
mapa pgsql_tls_enforce_in_policy bea@acme.test reject_plaintext_session
mapa pgsql_tls_enforce_in_policy bea@acme-alias.test reject_plaintext_session
mapa pgsql_tls_enforce_in_policy ana@acme.test ""
mapa pgsql_tls_policy_override_maps tls.partner.test encrypt
mapa pgsql_transport_maps partner.test "smtp_via_transport_maps:[smtp.partner.test]:587"
mapa pgsql_sasl_passwd_maps_transport_maps "[smtp.partner.test]:587" "tuser:$TRANSPORT_PASS"
mapa pgsql_mbr_access_maps mx.partner-mx.test "FILTER smtp_via_transport_maps:[smtp.partner-mx.test]:25"
mapa pgsql_sender_dependent_default_transport_maps ana@acme.test "smtp:relay.partner.test:587"
mapa pgsql_sender_dependent_default_transport_maps carla@acme.test "smtp_enforced_tls:relay2.partner.test:2525"
mapa pgsql_sasl_passwd_maps_sender_dependent ana@acme.test "relayuser:$RELAY_PASS"
mapa pgsql_sasl_passwd_maps_sender_dependent carla@acme.test "relay2user:$RELAY2_PASS"
mapa pgsql_virtual_relay_domain_maps respaldo.test respaldo.test
mapa pgsql_relay_recipient_maps cualquiera@respaldo.test cualquiera@respaldo.test
mapa pgsql_relay_ne conocido@respaldo.test "lmtp:inet:dovecot:24"
mapa pgsql_relay_ne ventas@acme.test ""
generados=$(en postfix-mail ls /opt/postfix/conf/sql 2>/dev/null)
sin_prueba=""
for f in $generados; do [[ "$PROBADOS" == *" $f "* ]] || sin_prueba+=" $f"; done
expect "postfix.sh genero los 18 mapas pgsql y todos tienen comprobacion" "$(wc -w <<<"$generados")${sin_prueba:+ sin prueba:$sin_prueba}" "18"

echo "== Dovecot"
DU=$(en dovecot-mail doveadm user ana@acme.test 2>&1)
contains "doveadm user resuelve el buzon por el userdb SQL" "$DU" "maildir:/var/vmail/acme.test/ana/"
contains "con el uid de vmail" "$DU" "5000"
en dovecot-mail doveadm user nadie@acme.test >/dev/null 2>&1
[[ $? -ne 0 ]] && ok "doveadm user no encuentra un buzon inexistente" || mal "doveadm user encontro nadie@acme.test"
cliente() {
  docker run --rm -i --network "$E2E_MAIL_NETWORK" -v "$ROOT/ops/e2e/mail_client.py:/cliente.py:ro" -v "$TLS/ca.pem:/ca.pem:ro" \
    --entrypoint python3 "$PROYECTO-dovecot-mail" /cliente.py --ca /ca.pem --nombre "$MAIL_HOSTNAME" "$@" 2>&1
}
MAESTRO="$MASTER_USER@platform.local"
expect "IMAP (993, TLS verificado) con la contrasena del buzon: passwd-verify.lua -> mail-auth" "$(cliente login ana@acme.test "$ANA_PASS")" "OK"
contains "una contrasena incorrecta se rechaza" "$(cliente login ana@acme.test "no-$ANA_PASS")" "NO"
expect "el inicio queda en mail.sasl_logins (service imap)" \
  "$(sql mail_cell_pe_01 "SELECT count(*) > 0 FROM mail.sasl_logins WHERE username = 'ana@acme.test' AND service = 'imap'")" "t"
expect "usuario maestro del webmail (buzon*maestro) desde la red de los motores" "$(cliente login bea@acme.test "$MASTER_PASS" --maestro "$MAESTRO")" "OK"
contains "con otra contrasena maestra no entra" "$(cliente login bea@acme.test "no-$MASTER_PASS" --maestro "$MAESTRO")" "NO"
contains "ni sobre un buzon que no existe" "$(cliente login nadie@acme.test "$MASTER_PASS" --maestro "$MAESTRO")" "NO"
docker cp "$ROOT/ops/e2e/mail_client.py" "$(c dovecot-mail):/tmp/cliente.py" >/dev/null && docker cp "$TLS/ca.pem" "$(c dovecot-mail):/tmp/ca.pem" >/dev/null
desde_loopback() { en dovecot-mail python3 /tmp/cliente.py --ca /tmp/ca.pem --nombre "$MAIL_HOSTNAME" --imap-host 127.0.0.1 "$@" 2>&1; }
expect "desde fuera de DOVECOT_MASTER_ALLOWED_NETS el buzon entra con su contrasena" "$(desde_loopback login bea@acme.test "$BEA_PASS")" "OK"
contains "pero el maestro no (allow_nets de la passdb maestra)" "$(desde_loopback login bea@acme.test "$MASTER_PASS" --maestro "$MAESTRO")" "NO"

echo "== Flujo de correo (submission 587 -> Rspamd -> LMTP -> IMAP)"
TOKEN="e2e$(rand_hex 4)"
R=$(cliente enviar ana@acme.test "$ANA_PASS" ana@acme.test bea@acme.test "$TOKEN-directo")
contains "ana envia a bea autenticada (STARTTLS verificado)" "$R" "OK 250"
H=$(cliente buscar bea@acme.test "$BEA_PASS" "$TOKEN-directo")
contains "entregado en el INBOX de bea y leido por IMAP" "$H" "OK 1"
contains "Postfix lo recibio como envio autenticado (ESMTPSA)" "$H" "with ESMTPSA"
contains "Dovecot lo entrego por LMTP" "$H" "with LMTP"
contains "Rspamd lo firmo con DKIM del dominio (d=acme.test)" "$H" "d=acme.test;"
contains "con el selector que publico domain-service" "$H" "s=$SELECTOR;"
contains "enviar como bea siendo ana: reject_authenticated_sender_login_mismatch" \
  "$(cliente enviar ana@acme.test "$ANA_PASS" bea@acme.test bea@acme.test "$TOKEN-ajeno")" "RECHAZO RCPT 553"
contains "enviar como info@ (alias sin permiso de envio) se rechaza" \
  "$(cliente enviar ana@acme.test "$ANA_PASS" info@acme.test bea@acme.test "$TOKEN-info")" "RECHAZO RCPT 553"
contains "enviar como ventas@ (alias con permiso) se acepta" \
  "$(cliente enviar ana@acme.test "$ANA_PASS" ventas@acme.test bea@acme.test "$TOKEN-ventas")" "OK 250"
contains "y llega" "$(cliente buscar bea@acme.test "$BEA_PASS" "$TOKEN-ventas")" "OK 1"
contains "enviar como ana@acme-alias.test (dominio alias) se acepta" \
  "$(cliente enviar ana@acme.test "$ANA_PASS" ana@acme-alias.test bea@acme.test "$TOKEN-aliasdom")" "OK 250"
contains "bea envia como soporte@ (sender_acl)" \
  "$(cliente enviar bea@acme.test "$BEA_PASS" soporte@acme.test ana@acme.test "$TOKEN-soporte")" "OK 250"
contains "con la credencial maestra Postfix ve el buzon como nombre SASL: ana@ se acepta" \
  "$(cliente enviar "ana@acme.test*$MAESTRO" "$MASTER_PASS" ana@acme.test bea@acme.test "$TOKEN-maestro")" "OK 250"
R=$(cliente enviar "ana@acme.test*$MAESTRO" "$MASTER_PASS" bea@acme.test bea@acme.test "$TOKEN-maestro-ajeno")
contains "y bea@ se rechaza con 553" "$R" "RECHAZO RCPT 553"
contains "porque el dueno que compara es el buzon" "$R" "ana@acme.test"
R=$(cliente enviar ana@acme.test "$ANA_PASS" ana@acme.test bea@acme.test "$TOKEN-eicar" --eicar)
contains "un adjunto EICAR se rechaza al final de DATA (Rspamd: CLAM_VIRUS -> VIRUS_FOUND -> reject)" "$R" "RECHAZO DATA 554"
# VIRUS_FOUND es un compuesto de CLAM_VIRUS y Rspamd quita del resultado los simbolos que
# consume un compuesto: la fila lleva VIRUS_FOUND.
en_cuarentena() {
  [[ "$(sql mail_cell_pe_01 "SELECT count(*) FROM mail_security.quarantine WHERE rcpt = 'bea@acme.test' AND subject = '$TOKEN-eicar' AND action = 'reject' AND symbols ? 'VIRUS_FOUND'")" == 1 ]]
}
esperar "y queda en la cuarentena de bea (metadata_exporter -> mail-policy /pipe)" 30 en_cuarentena
lacks "sin llegar a su INBOX" "$(cliente buscar bea@acme.test "$BEA_PASS" "$TOKEN-eicar" --espera 3)" "OK"
# Un mensaje por encima de los topes con los que el antivirus se saltaba el analisis en silencio
# (20 MiB del max_size de Rspamd, 25 MiB del StreamMaxLength de clamd): 20 MiB de relleno mas
# EICAR al final salen a unos 27 MiB. Antes se entregaba sin pasar por ClamAV; ahora se analiza
# entero y se rechaza igual. No se comprueba la cuarentena: con el max_size_bytes por defecto
# (10 MiB) la empresa no guarda un mensaje asi y /pipe responde 505, que es lo correcto.
R=$(cliente enviar ana@acme.test "$ANA_PASS" ana@acme.test bea@acme.test "$TOKEN-grande" --eicar --relleno-mib 20)
contains "un mensaje de unos 27 MiB con EICAR al final tambien se analiza entero y se rechaza" "$R" "RECHAZO DATA 554"
lacks "sin llegar al INBOX de bea" "$(cliente buscar bea@acme.test "$BEA_PASS" "$TOKEN-grande" --espera 3)" "OK"

echo "== Respuesta automatica (mail-directory -> v_sieve_vacation -> sieve_after3 de Dovecot)"
BEA_ID=$(sql mail_cell_pe_01 "SELECT id FROM mail.mailboxes WHERE username = 'bea@acme.test'")
api GET "/mailboxes/$BEA_ID/vacation"
expect "bea sin configurar: la ve desactivada" "$API_CODE/$(echo "$API_BODY" | jget data.enabled)" "200/False"
VAC="$TOKEN-vac"
# El texto lleva comillas, una barra y un salto de linea: tiene que llegar tal cual, no como codigo Sieve.
respuesta() { # respuesta <fin AAAA-MM-DD o vacio>: el cuerpo del PUT
  python3 -c '
import json, sys
c = {"enabled": True, "subject": "Fuera de la oficina " + sys.argv[1], "message": "Estoy fuera hasta el lunes.\nEscriba a \"soporte\" o a C:\\ruta", "interval_days": 1}
if sys.argv[2]:
    c["ends_on"] = sys.argv[2]
print(json.dumps(c))' "$VAC" "$1"
}
api PUT "/mailboxes/$BEA_ID/vacation" "$(respuesta "$(date -u -d yesterday +%F)")"
expect "guardarla con la ventana ya cerrada: 200 y activada" "$API_CODE/$(echo "$API_BODY" | jget data.enabled)" "200/True"
expect "el script generado no sale por la API" "$(echo "$API_BODY" | grep -c 'require')" "0"
expect "la vista de los motores la sirve a Dovecot" \
  "$(sql mail_cell_pe_01 "SELECT count(*) FROM mail.v_sieve_vacation WHERE username = 'bea@acme.test' AND script_name = 'active'")" "1"
contains "ana escribe a bea con la ventana cerrada" "$(cliente enviar ana@acme.test "$ANA_PASS" ana@acme.test bea@acme.test "$TOKEN-ventana")" "OK 250"
contains "y llega a bea" "$(cliente buscar bea@acme.test "$BEA_PASS" "$TOKEN-ventana")" "OK 1"
lacks "fuera de la ventana de fechas no se contesta" "$(cliente buscar ana@acme.test "$ANA_PASS" "$VAC" --espera 8)" "OK"
api PUT "/mailboxes/$BEA_ID/vacation" "$(respuesta "")"
expect "sin ventana: 200" "$API_CODE" "200"
contains "ana vuelve a escribir a bea" "$(cliente enviar ana@acme.test "$ANA_PASS" ana@acme.test bea@acme.test "$TOKEN-contesta")" "OK 250"
H=$(cliente buscar ana@acme.test "$ANA_PASS" "$VAC")
contains "ana recibe la respuesta automatica de bea (sieve_after3, submission_host)" "$H" "OK 1"
contains "sale con la direccion de bea como remitente del sobre (sieve_vacation_send_from_recipient)" "$H" "Return-Path: <bea@acme.test>"
expect "y en la cabecera From" "$(grep -i '^From:' <<< "$H" | grep -c 'bea@acme.test')" "1"
contains "marcada como respuesta automatica" "$H" "Auto-Submitted: auto-replied"
CUERPO=$(en dovecot-mail doveadm fetch -u ana@acme.test text mailbox INBOX subject "Fuera de la oficina $VAC")
contains "con el texto tal cual: primera linea" "$CUERPO" "Estoy fuera hasta el lunes."
contains "las comillas y la barra del texto llegan literales" "$CUERPO" 'Escriba a "soporte" o a C:\ruta'
expect "el filtro escrito a mano de bea y sus filtros de mail.sieve_filters no se tocan" \
  "$(sql mail_cell_pe_01 "SELECT count(*) FROM mail.sieve_filters WHERE username = 'bea@acme.test'")" "0"
api PUT "/mailboxes/$BEA_ID/vacation" '{"enabled":true,"message":"x","interval_days":99}'
expect "un intervalo fuera de 1 a 30 se rechaza (422)" "$API_CODE" "422"
api PUT "/mailboxes/$BEA_ID/vacation" '{"enabled":true,"message":"x","script_data":"discard;"}'
expect "mandar el script a mano se rechaza (400): el usuario no escribe Sieve por este camino" "$API_CODE" "400"

echo "== MTA-STS (mail-directory: politica por dominio y su version; domain-service: el TXT que la anuncia)"
api GET "/mail-domains/mta-sts/acme.test"
expect "acme.test sin politica: none, con la version vacia" "$API_CODE/$(echo "$API_BODY" | jget data.mode)/$(echo "$API_BODY" | jget data.policy_id)" "200/none/"
api PUT "/mail-domains/mta-sts/acme.test" '{"mode":"enforce"}'
expect "de none no se pasa a enforce (409): se entra por testing" "$API_CODE" "409"
api PUT "/mail-domains/mta-sts/noexiste.test" '{"mode":"testing"}'
expect "un dominio que el directorio no tiene: 404" "$API_CODE" "404"
api PUT "/mail-domains/mta-sts/acme.test" '{"mode":"estricto"}'
expect "un modo desconocido se rechaza (422)" "$API_CODE" "422"
MTASTS_URL="$GW/public/mail-directory/mta-sts/pe-01/acme.test"
expect "sin politica el gateway responde 404 al remitente, sin sesion" "$(curl -s -o /dev/null -w '%{http_code}' "$MTASTS_URL")" "404"
api PUT "/mail-domains/mta-sts/acme.test" '{"mode":"testing"}'
expect "activarla la deja en testing" "$API_CODE/$(echo "$API_BODY" | jget data.mode)" "200/testing"
STS_ID=$(sql mail_cell_pe_01 "SELECT policy_id FROM mail.mta_sts_policies WHERE domain = 'acme.test'")
expect "con una version de 32 caracteres alfanumericos" "$(grep -cE '^[A-Za-z0-9]{32}$' <<< "$STS_ID")" "1"
curl -s -D "$WORK/sts.head" -o "$WORK/sts.txt" "$MTASTS_URL"
contains "el remitente descarga la politica como texto plano" "$(cat "$WORK/sts.head")" "Content-Type: text/plain"
expect "con el documento del RFC 8461 y el MX de la plataforma" "$(tr -d '\r' < "$WORK/sts.txt" | tr '\n' '|')" \
  "version: STSv1|mode: testing|mx: ${MAIL_MX_HOSTNAME,,}|max_age: 86400|"
api GET "/domains/$DOMID"
expect "domain-service anuncia esa version en el TXT _mta-sts, recomendado y no requerido" "$(echo "$API_BODY" | python3 -c '
import json, sys
r = [r for r in json.load(sys.stdin)["data"]["dns_records"] if r["record"] == "mta_sts"]
print(r[0]["host"], r[0]["value"], r[0]["required"]) if r else print("sin registro")')" "_mta-sts.acme.test v=STSv1; id=$STS_ID False"
api POST "/domains/$DOMID/verify"
expect "el TXT sin publicar en el DNS no quita la verificacion del dominio" "$(echo "$API_BODY" | jget data.status)" "verified"
api PUT "/mail-domains/mta-sts/acme.test" '{"mode":"enforce"}'
expect "enforce con el dominio activo y sus MX en la plataforma: 200" "$API_CODE/$(echo "$API_BODY" | jget data.mode)" "200/enforce"
expect "cada cambio renueva la version" "$([[ "$(sql mail_cell_pe_01 "SELECT policy_id FROM mail.mta_sts_policies WHERE domain = 'acme.test'")" != "$STS_ID" ]] && echo distinta)" "distinta"
contains "y se sirve en enforce" "$(curl -s "$MTASTS_URL")" "mode: enforce"
api PUT "/mail-domains/mta-sts/acme.test" '{"mode":"none"}'
expect "de enforce no se pasa a none (409): se sale por testing" "$API_CODE" "409"
api PUT "/mail-domains/mta-sts/acme.test" '{"mode":"testing"}'
api PUT "/mail-domains/mta-sts/acme.test" '{"mode":"none"}'
expect "testing a none: 200, y el remitente deja de recibir la politica (404)" "$API_CODE/$(curl -s -o /dev/null -w '%{http_code}' "$MTASTS_URL")" "200/404"
contains "ningun motor lee la tabla de politicas" \
  "$(psql -v ON_ERROR_STOP=1 -q -At -d mail_cell_pe_01 -c "SET ROLE mail_engine; SELECT 1 FROM mail.mta_sts_policies LIMIT 1" 2>&1)" "permission denied"

echo "== Enlace del aviso de cuarentena por el gateway (celda en la ruta y en la firma)"
# Sin transactional no sale el aviso: el enlace se firma aqui con la clave de la ejecucion y la
# forma quarantine-link/v2 de domain.QuarantineLinkSigner (contrato en deploy/mail/README.md).
# enlace <celda> <accion> <id> <qhash> <empresa> <caducidad>: ruta bajo /api/v1.
enlace() {
  python3 - "$@" <<'PY'
import hashlib, hmac, os, sys, urllib.parse
celda, accion, mensaje, qhash, empresa, caducidad = sys.argv[1:]
canonico = "\n".join(("quarantine-link/v2", celda, empresa, mensaje, accion, caducidad))
firma = hmac.new(os.environ["MAIL_LINK_SIGNING_KEY"].encode(), canonico.encode(), hashlib.sha256).hexdigest()
consulta = urllib.parse.urlencode(sorted({"e": caducidad, "q": qhash, "sig": firma, "t": empresa}.items()))
print(f"/public/mail-security/quarantine/{celda}/{accion}?{consulta}")
PY
}
# pagina <metodo> <ruta> <fichero>: el cuerpo en <fichero>; escribe el codigo HTTP.
pagina() {
  local datos=()
  [[ "$1" == POST ]] && datos=(-d '')
  curl -s -o "$3" -w '%{http_code}' -X "$1" "$GW$2" "${datos[@]}"
}
en_la_fila() { sql mail_cell_pe_01 "SELECT count(*) FROM mail_security.quarantine WHERE id = '$QID'"; }
read -r QID QHASH < <(sql mail_cell_pe_01 "SELECT id, qhash FROM mail_security.quarantine WHERE rcpt = 'bea@acme.test' AND subject = '$TOKEN-eicar'" | tr '|' ' ')
LIBERAR=$(enlace pe-01 release "$QID" "$QHASH" "$TID" "$(( $(date +%s) + 3600 ))")
CELDA_PE01="/pe-01/" CELDA_ZZ99="/zz-99/"
OTRA_CELDA="${LIBERAR/"$CELDA_PE01"/"$CELDA_ZZ99"}"
FIRMA="${LIBERAR##*sig=}"; FIRMA="${FIRMA%%&*}"
[[ "${FIRMA:0:1}" == a ]] && OTRO=b || OTRO=a
ALTERADA="${LIBERAR/"sig=$FIRMA"/"sig=$OTRO${FIRMA:1}"}"
expect "la celda cambiada a una desconocida (zz-99) y una firma alterada dan 403" \
  "$(pagina POST "$OTRA_CELDA" "$WORK/q-celda.html")/$(pagina POST "$ALTERADA" "$WORK/q-firma.html")" "403/403"
cmp -s "$WORK/q-celda.html" "$WORK/q-firma.html" && ok "con la misma pagina byte a byte: no delata que celdas existen" \
  || mal "las paginas 403 de la celda desconocida y de la firma alterada difieren"
contains "la de enlace no valido, servida por mail-security" "$(cat "$WORK/q-firma.html")" "Enlace no valido"
expect "y el mensaje sigue en cuarentena sin uso registrado" \
  "$(en_la_fila)/$(sql mail_cell_pe_01 "SELECT count(*) FROM mail_security.quarantine_link_uses WHERE quarantine_id = '$QID'")" "1/0"
expect "GET del enlace valido por el gateway: 200 y no ejecuta nada" "$(pagina GET "$LIBERAR" "$WORK/q-pagina.html")/$(en_la_fila)" "200/1"
contains "muestra la confirmacion de liberar" "$(cat "$WORK/q-pagina.html")" "Liberar mensaje"
expect "POST del enlace: 200" "$(pagina POST "$LIBERAR" "$WORK/q-hecho.html")" "200"
contains "mensaje liberado" "$(cat "$WORK/q-hecho.html")" "Mensaje liberado"
expect "la fila sale de la cuarentena y el uso queda registrado" \
  "$(en_la_fila)/$(sql mail_cell_pe_01 "SELECT action FROM mail_security.quarantine_link_uses WHERE quarantine_id = '$QID'")" "0/release"
contains "la reinyeccion por el puerto 590 lo entrega en el INBOX de bea" "$(cliente buscar bea@acme.test "$BEA_PASS" "$TOKEN-eicar")" "OK 1"
expect "el enlace usado ya no vale (403)" "$(pagina POST "$LIBERAR" "$WORK/q-usado.html")" "403"
cmp -s "$WORK/q-usado.html" "$WORK/q-firma.html" && ok "con la misma pagina que la firma alterada" \
  || mal "la pagina del enlace usado difiere de la de la firma alterada"

echo "== Rspamd con lo que escribe mail-security"
correo_rspamc() { printf 'From: <%s>\r\nTo: <%s>\r\nSubject: rspamc\r\nMessage-ID: <%s@e2e.test>\r\nDate: %s\r\n\r\ncuerpo\r\n' "$1" "$2" "$(rand_hex 8)" "$(date -R)"; }
RS=$(correo_rspamc watchdog@localhost null@localhost | en rspamd-mail rspamc --from watchdog@localhost --rcpt null@localhost 2>&1)
contains "Rspamd aplica la regla watchdog del mapa settings de mail-policy (umbral 9999)" "$RS" "/ 9999"
RS=$(correo_rspamc ana@acme.test bea@acme.test | en rspamd-mail rspamc --from ana@acme.test --rcpt bea@acme.test 2>&1)
contains "Rspamd ve DOMAIN_MAP: destinatario de un dominio de la celda" "$RS" "RCPT_MAILCOW_DOMAIN"
RS=$(correo_rspamc ana@acme.test x@otro.test | en rspamd-mail rspamc --from ana@acme.test --rcpt x@otro.test 2>&1)
lacks "y no marca un dominio ajeno" "$RS" "RCPT_MAILCOW_DOMAIN"
expect "el dominio alias tambien esta en DOMAIN_MAP" "$(redis_mail HGET DOMAIN_MAP acme-alias.test)" "1"

echo "== Webmail por el gateway (cookie propia, usuario maestro, submission)"
WM="$GW/webmail"
# wm <tarro> <metodo> <ruta> [curl...]: deja WM_CODE y WM_BODY.
wm() {
  local tarro="$1" metodo="$2" ruta="$3"
  shift 3
  WM_CODE=$(curl -s -o "$WORK/wm.json" -w '%{http_code}' -b "$tarro" -c "$tarro" -X "$metodo" "$WM$ruta" -H "Origin: $API_ORIGIN" "$@")
  WM_BODY=$(cat "$WORK/wm.json")
}
TARRO_ANA="$WORK/ana.cookies"; TARRO_BEA="$WORK/bea.cookies"
wm "$WORK/nadie.cookies" POST /session -H 'Content-Type: application/json' -d "{\"username\":\"ana@acme.test\",\"password\":\"no-$ANA_PASS\"}"
expect "una contrasena mala no abre sesion" "$WM_CODE" "401"
wm "$TARRO_ANA" POST /session -H 'Content-Type: application/json' -d "{\"username\":\"ana@acme.test\",\"password\":\"$ANA_PASS\"}"
expect "ana abre sesion en el webmail (mail-auth, service webmail)" "$WM_CODE/$(echo "$WM_BODY" | jget data.username)" "200/ana@acme.test"
contains "con la cookie cf_wm" "$(cat "$TARRO_ANA" 2>/dev/null)" "cf_wm"
wm "$TARRO_ANA" GET /folders
expect "lista sus carpetas en Dovecot como usuario maestro" "$WM_CODE" "200"
contains "INBOX" "$WM_BODY" '"name":"INBOX"'
ENVIADOS=$(echo "$WM_BODY" | python3 -c 'import json, sys; print(next((f["name"] for f in json.load(sys.stdin)["data"] if f["role"] == "sent"), ""))' 2>/dev/null)
[[ -n "$ENVIADOS" ]] && ok "y la carpeta de enviados ($ENVIADOS)" || mal "sin carpeta con papel sent: ${WM_BODY:0:300}"
wm "$TARRO_ANA" GET /identities
IDENT=$(echo "$WM_BODY" | python3 -c 'import json, sys; print(" ".join(sorted(i["email"] for i in json.load(sys.stdin)["data"])))' 2>/dev/null)
expect "identidades de ana: el buzon, el dominio alias y el alias con permiso" "$IDENT" "ana@acme-alias.test ana@acme.test ventas@acme.test"
for ident in $IDENT; do
  duenos=$(docker exec "$(c postfix-mail)" postmap -c /opt/postfix/conf -q "$ident" pgsql:/opt/postfix/conf/sql/pgsql_virtual_sender_acl.cf 2>&1)
  contains "Postfix acepta a ana como duena de $ident" "$duenos" "ana@acme.test"
done
lacks "info@ no se ofrece (Postfix la rechaza)" "$IDENT" "info@acme.test"
CLAVE="$(rand_hex 16)"
enviar_wm() { # enviar_wm <de> <asunto> [curl...]
  local de="$1" asunto="$2"
  shift 2
  wm "$TARRO_ANA" POST /send -F "from=$de" -F to=bea@acme.test -F "subject=$asunto" -F "text=Enviado desde el webmail." "$@"
}
enviar_wm ana@acme.test "$TOKEN-webmail" -H "Idempotency-Key: $CLAVE"
expect "envio por el webmail con Idempotency-Key (submission con la credencial maestra)" "$WM_CODE/$(echo "$WM_BODY" | jget data.replayed)" "202/False"
expect "y guarda la copia en enviados" "$(echo "$WM_BODY" | jget data.saved_to_sent)" "True"
enviar_wm ana@acme.test "$TOKEN-webmail" -H "Idempotency-Key: $CLAVE"
expect "un reintento con la misma clave no vuelve a entregar" "$WM_CODE/$(echo "$WM_BODY" | jget data.replayed)" "202/True"
contains "bea lo recibe una sola vez" "$(cliente buscar bea@acme.test "$BEA_PASS" "$TOKEN-webmail" --estable 5)" "OK 1"
wm "$TARRO_BEA" POST /session -H 'Content-Type: application/json' -d "{\"username\":\"bea@acme.test\",\"password\":\"$BEA_PASS\"}"
expect "bea abre sesion en el webmail" "$WM_CODE" "200"
wm "$TARRO_BEA" GET /folders/INBOX/messages
contains "y lo ve en su INBOX por el webmail" "$WM_BODY" "$TOKEN-webmail"
wm "$TARRO_ANA" GET "/folders/$ENVIADOS/messages"
contains "ana lo ve en enviados" "$WM_BODY" "$TOKEN-webmail"
enviar_wm ventas@acme.test "$TOKEN-webmail-ventas" -H "Idempotency-Key: $(rand_hex 16)"
expect "el webmail envia con una identidad que Postfix acepta (ventas@)" "$WM_CODE" "202"
contains "y llega" "$(cliente buscar bea@acme.test "$BEA_PASS" "$TOKEN-webmail-ventas")" "OK 1"
enviar_wm bea@acme.test "$TOKEN-webmail-ajeno" -H "Idempotency-Key: $(rand_hex 16)"
expect "un remitente que no es suyo se rechaza antes de Postfix" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "403/SENDER_NOT_ALLOWED"
enviar_wm ana@acme.test "$TOKEN-webmail-eicar" -H "Idempotency-Key: $(rand_hex 16)" -F "attachments=@$WORK/eicar.com;type=application/octet-stream"
expect "un adjunto EICAR lo para el analisis del webmail con ClamAV" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "422/ATTACHMENT_INFECTED"
wm "$TARRO_ANA" GET "/address-book?q=bea"
expect "la libreta de la empresa encuentra a bea por el texto" "$WM_CODE/$(echo "$WM_BODY" | jget data.0.address)/$(echo "$WM_BODY" | python3 -c 'import json, sys; print(len(json.load(sys.stdin)["data"]))')" "200/bea@acme.test/1"
wm "$TARRO_ANA" GET /address-book
LIBRETA=$(echo "$WM_BODY" | python3 -c 'import json, sys; print(" ".join(sorted(e["address"] for e in json.load(sys.stdin)["data"])))' 2>/dev/null)
contains "y sin texto lista a ana" "$LIBRETA" "ana@acme.test"
contains "y a sus companeros" "$LIBRETA" "bea@acme.test"
lacks "sin exponer nada mas que direccion y nombre" "$WM_BODY" "quota"
wm "$TARRO_ANA" GET /vacation
expect "ana ve su respuesta automatica desactivada, con los topes del servicio" "$WM_CODE/$(echo "$WM_BODY" | jget data.enabled)/$(echo "$WM_BODY" | jget data.limits.message_max_length)" "200/False/8192"
wm "$TARRO_ANA" PUT /vacation -H 'Content-Type: application/json' -d '{"enabled":true,"subject":"Ausente","message":"Vuelvo el lunes.","interval_days":2}'
expect "el webmail guarda la respuesta automatica del buzon de la sesion" "$WM_CODE/$(echo "$WM_BODY" | jget data.enabled)/$(echo "$WM_BODY" | jget data.interval_days)" "200/True/2"
expect "y queda en la vista que lee Dovecot, solo para ana" "$(sql mail_cell_pe_01 "SELECT string_agg(username, ',') FROM mail.v_sieve_vacation WHERE username IN ('ana@acme.test','bea@acme.test') AND script_data LIKE '%Vuelvo el lunes.%'")" "ana@acme.test"
wm "$TARRO_ANA" PUT /vacation -H 'Content-Type: application/json' -d '{"enabled":true,"message":"","interval_days":1}'
expect "una respuesta activa sin mensaje la rechaza el servicio (422)" "$WM_CODE" "422"
wm "$TARRO_ANA" PUT /vacation -H 'Content-Type: application/json' -d '{"enabled":false,"message":"","interval_days":1}'
expect "y desactivarla la retira de la vista de Dovecot" "$WM_CODE/$(sql mail_cell_pe_01 "SELECT count(*) FROM mail.v_sieve_vacation WHERE username = 'ana@acme.test'")" "200/0"
wm "$TARRO_ANA" DELETE /session
expect "cerrar sesion" "$WM_CODE" "204"
wm "$TARRO_ANA" GET /folders
expect "y la cookie ya no abre el buzon" "$WM_CODE" "401"

echo "== Dovecot deja de aceptar una credencial al momento (mail-security: cache y sesiones)"
# Dovecot guarda cada inicio correcto en su cache de autenticacion (auth_cache_ttl, 300 s) y no
# vuelve a preguntar a mail-auth mientras dura, y una sesion IMAP abierta sigue hasta que el cliente
# se va. mail-security consume mail.mailbox.* y, por el API HTTP de doveadm de la celda (TLS y
# DOVEADM_API_KEY), vacia la entrada del buzon y cierra sus sesiones cuando deja de poder entrar o
# cambia su credencial (deploy/mail/README.md, "Revocacion en Dovecot").
# sesion <nombre> <buzon> <contrasena> <limite>: sesion IMAP en segundo plano (mail_client.py) que
# escribe LISTA al entrar y CERRADA si Dovecot la corta, o ABIERTA si sigue viva al cumplir el limite.
declare -A SESION_PID=()
sesion() {
  cliente sesion "$2" "$3" --limite "$4" </dev/null >"$WORK/sesion-$1" 2>&1 &
  SESION_PID[$1]=$!
}
sesion_en() { grep -q "$2" "$WORK/sesion-$1" 2>/dev/null; }
login_rechazado() { [[ "$(cliente login "$1" "$2")" == NO* ]]; }
login_aceptado() { [[ "$(cliente login "$1" "$2")" == OK ]]; }
buzon_id() { api GET "/mailboxes?search=$1" && echo "$API_BODY" | jget data.0.id; }
# revocaciones <flush|kick|fallos>: contadores de mail-security (fallos suma todos los motivos).
revocaciones() {
  curl -s "http://127.0.0.1:${PORT[mail-security]}/metrics" | awk -v a="$1" '
    $1 == "mail_security_dovecot_revocations_total{action=\"" a "\"}" { print $2 }
    a == "fallos" && $1 ~ /^mail_security_dovecot_revocation_failures_total/ { s += $2 }
    END { if (a == "fallos") print s + 0 }'
}
contains "el API de doveadm exige su clave (401 sin ella)" \
  "$(cliente doveadm '[["kick",{"mask":["nadie@acme.test"]},"t"]]' --sin-clave </dev/null)" "HTTP 401"
contains "y con ella solo admite las ordenes de la revocacion (doveadm user: no autorizada)" \
  "$(cliente doveadm '[["user",{"userMask":["ana@acme.test"]},"t"]]' <<<"$DOVEADM_KEY")" "unAuthorized"
ANAID=$(buzon_id ana@acme.test) BEAID=$(buzon_id bea@acme.test) CARLAID=$(buzon_id carla@acme.test)
[[ -n "$ANAID" && -n "$BEAID" && -n "$CARLAID" ]] && ok "ids de ana, bea y carla" || mal "ids de los buzones: '$ANAID' '$BEAID' '$CARLAID'"

# Un cambio que no retira nada (el nombre visible) vacia la cache y no echa a nadie.
F0=$(revocaciones flush) K0=$(revocaciones kick)
sesion ana ana@acme.test "$ANA_PASS" 25
esperar "ana con una sesion IMAP abierta" 30 sesion_en ana LISTA
api PATCH "/mailboxes/$ANAID" '{"display_name":"Ana P. Perez"}'
expect "cambiar el nombre visible de ana" "$API_CODE" "200"
flush_atendido() { (( $(revocaciones flush) > F0 )); }
esperar "mail-security atiende el evento y solo vacia su cache (mail.mailbox.updated, buzon activo)" 30 flush_atendido
expect "sin echar a nadie" "$(revocaciones kick)" "$K0"
esperar "la sesion abierta de ana sigue viva hasta su limite: un cambio de nombre no echa a nadie" 40 sesion_en ana ABIERTA
lacks "sin que Dovecot la corte" "$(cat "$WORK/sesion-ana")" "CERRADA"
expect "y ana sigue entrando (mail-auth la vuelve a verificar)" "$(cliente login ana@acme.test "$ANA_PASS")" "OK"

# Apagar un buzon que acaba de entrar. El control, en la base y sin evento, prueba que la cache le
# seguiria abriendo la puerta: lo que la cierra es el vaciado de mail-security.
expect "carla entra por IMAP (Dovecot guarda el inicio en su cache)" "$(cliente login carla@acme.test "$CARLA_PASS")" "OK"
expect "control: carla apagada en la base sin pasar por mail-directory (sin evento)" \
  "$(sql mail_cell_pe_01 "UPDATE mail.mailboxes SET active = 0 WHERE username = 'carla@acme.test' RETURNING active")" "0"
expect "y la cache de Dovecot la sigue dejando entrar" "$(cliente login carla@acme.test "$CARLA_PASS")" "OK"
expect "control deshecho" "$(sql mail_cell_pe_01 "UPDATE mail.mailboxes SET active = 1 WHERE username = 'carla@acme.test' RETURNING active")" "1"
sesion carla carla@acme.test "$CARLA_PASS" 60
esperar "carla con una sesion IMAP abierta" 30 sesion_en carla LISTA
api PATCH "/mailboxes/$CARLAID" '{"active":0}'
expect "mail-directory apaga a carla" "$API_CODE" "200"
esperar "Dovecot la rechaza al momento aunque la tenia en su cache (mail-security vacia su entrada)" 20 login_rechazado carla@acme.test "$CARLA_PASS"
esperar "y su sesion IMAP abierta se cierra (doveadm kick)" 20 sesion_en carla CERRADA
contains "mail-auth la rechaza por el buzon apagado, no por la contrasena" \
  "$(docker logs "$(c mail-auth)" 2>&1 | grep '"username":"carla@acme.test"')" "buzon sin inicio de sesion"
api PATCH "/mailboxes/$CARLAID" '{"active":1}'
esperar "reactivada, carla vuelve a entrar" 20 login_aceptado carla@acme.test "$CARLA_PASS"

# Cambiar la contrasena de un buzon que acaba de entrar: la anterior deja de valer al momento.
expect "bea entra por IMAP con su contrasena (queda en la cache)" "$(cliente login bea@acme.test "$BEA_PASS")" "OK"
sesion bea bea@acme.test "$BEA_PASS" 60
esperar "bea con una sesion IMAP abierta" 30 sesion_en bea LISTA
BEA_NUEVA="$(rand_hex 10)Aa1!"
api POST "/mailboxes/$BEAID/password" "{\"password\":\"$BEA_NUEVA\"}"
expect "mail-directory cambia la contrasena de bea" "$API_CODE" "204"
esperar "Dovecot rechaza al momento la anterior, que tenia en su cache" 20 login_rechazado bea@acme.test "$BEA_PASS"
expect "y acepta la nueva" "$(cliente login bea@acme.test "$BEA_NUEVA")" "OK"
esperar "la sesion IMAP abierta con la anterior se cierra" 20 sesion_en bea CERRADA
BEA_PASS="$BEA_NUEVA"

# Una contrasena de aplicacion de bea que se desactiva o se borra deja de valer al momento aunque
# Dovecot la tuviera en su cache, y la sesion IMAP abierta con ella se cierra
# (mail.mailbox.credentials_changed con credential app_password). La contrasena principal sigue
# entrando y la sesion del webmail, que solo admite la principal, sigue abierta. Cada paso usa una
# contrasena nueva, que entra en la cache como inicio correcto, y espera a que mail-security eche a
# bea antes de sondear: un intento anterior lo contesta Dovecot desde su cache.
bea_kicks() { docker logs "$(c mail-security)" 2>&1 | grep -c '"username":"bea@acme.test","change":"credentials_changed","action":"kick"'; }
# webmail_ignora: avisos de una contrasena de aplicacion de bea que el webmail atendio sin cerrar sesiones.
webmail_ignora() { docker logs "$(c webmail)" 2>&1 | grep 'contrasena de aplicacion' | grep -c '"username":"bea@acme.test"'; }
bea_expulsada() { (( $(bea_kicks) > BEA_KICKS0 )); }
webmail_atendio() { (( $(webmail_ignora) > BEA_WM0 )); }
wm "$TARRO_BEA" POST /session -H 'Content-Type: application/json' -d "{\"username\":\"bea@acme.test\",\"password\":\"$BEA_PASS\"}"
expect "bea abre sesion en el webmail con su contrasena principal" "$WM_CODE" "200"
for paso in desactivar borrar; do
  api POST "/mailboxes/$BEAID/app-passwords" "{\"name\":\"cliente-$paso\"}"
  BEA_APP=$(echo "$API_BODY" | jget data.password)
  BEA_APPID=$(echo "$API_BODY" | jget data.app_password.id)
  expect "alta de una contrasena de aplicacion de bea para $paso (la genera el servidor)" "$API_CODE/${BEA_APPID:+id}/${BEA_APP:+clave}" "201/id/clave"
  wm "$WORK/bea-app.cookies" POST /session -H 'Content-Type: application/json' -d "{\"username\":\"bea@acme.test\",\"password\":\"$BEA_APP\"}"
  expect "el webmail no la admite" "$WM_CODE" "401"
  expect "bea entra por IMAP con ella (queda en la cache de Dovecot)" "$(cliente login bea@acme.test "$BEA_APP")" "OK"
  sesion "bea-app-$paso" bea@acme.test "$BEA_APP" 60
  esperar "y abre con ella una sesion IMAP" 30 sesion_en "bea-app-$paso" LISTA
  BEA_KICKS0=$(bea_kicks) BEA_WM0=$(webmail_ignora)
  if [[ $paso == desactivar ]]; then
    api PATCH "/mailboxes/$BEAID/app-passwords/$BEA_APPID" '{"active":false}'
    expect "mail-directory la desactiva" "$API_CODE/$(echo "$API_BODY" | jget data.active)" "200/False"
  else
    api DELETE "/mailboxes/$BEAID/app-passwords/$BEA_APPID"
    expect "mail-directory la borra" "$API_CODE" "204"
  fi
  esperar "mail-security vacia la cache de bea y cierra sus sesiones (credentials_changed al $paso)" 20 bea_expulsada
  esperar "Dovecot la rechaza al momento aunque la tenia en su cache" 20 login_rechazado bea@acme.test "$BEA_APP"
  esperar "y la sesion IMAP abierta con ella se cierra" 20 sesion_en "bea-app-$paso" CERRADA
  expect "la contrasena principal de bea sigue entrando" "$(cliente login bea@acme.test "$BEA_PASS")" "OK"
  esperar "el webmail recibe el aviso de la contrasena de aplicacion y no cierra sesiones" 20 webmail_atendio
  wm "$TARRO_BEA" GET /folders
  expect "y la sesion de bea en el webmail sigue abierta" "$WM_CODE" "200"
  if [[ $paso == desactivar ]]; then
    api PATCH "/mailboxes/$BEAID/app-passwords/$BEA_APPID" '{"active":true}'
    expect "bea la reactiva (sin aviso: no retira ninguna credencial)" "$API_CODE/$(echo "$API_BODY" | jget data.active)" "200/True"
    esperar "y vuelve a entrar al momento: la cache negativa no bloquea una contrasena buena" 20 login_aceptado bea@acme.test "$BEA_APP"
  fi
done

# La otra cara: un cambio INOCUO del buzon (la cuota, el nombre visible) no cierra la sesion del
# webmail. mail-directory dice en mail.mailbox.updated que atributos cambio (changed) y el webmail
# solo revoca con los que invalidan su sesion; antes revocaba con cualquier mail.mailbox.updated, que
# no decia que cambio, y echaba al usuario sin motivo. Se espera al registro del webmail antes de
# mirar la sesion: sin esa espera seguiria abierta por no haber llegado el evento, no por el arreglo.
webmail_conservo() { docker logs "$(c webmail)" 2>&1 | grep 'no invalida la sesion' | grep -c '"username":"bea@acme.test"'; }
webmail_conservo_mas() { (( $(webmail_conservo) > BEA_WM_OK0 )); }
wm "$TARRO_BEA" GET /folders
expect "bea sigue con su sesion del webmail abierta antes de los cambios inocuos" "$WM_CODE" "200"
K_INOCUO=$(revocaciones kick)
for inocuo in '{"quota_bytes":1073741824}' '{"display_name":"Bea D. Diaz"}'; do
  BEA_WM_OK0=$(webmail_conservo)
  api PATCH "/mailboxes/$BEAID" "$inocuo"
  expect "mail-directory aplica un cambio inocuo a bea: $inocuo" "$API_CODE" "200"
  esperar "el webmail lo atiende y no revoca ($inocuo)" 20 webmail_conservo_mas
  wm "$TARRO_BEA" GET /folders
  expect "y la sesion de bea en el webmail sigue abierta" "$WM_CODE" "200"
done
expect "mail-security tampoco echo a nadie de Dovecot por ellos (solo vacio la cache)" "$(revocaciones kick)" "$K_INOCUO"
expect "y bea sigue entrando por IMAP" "$(cliente login bea@acme.test "$BEA_PASS")" "OK"

# Quitarle un protocolo al buzon cierra al momento la sesion abierta con el: mail-directory publica en
# la misma transaccion mail.mailbox.credentials_changed con credential password
# (domain.MailboxLoginsRevoked) y mail-security echa al buzon; mail.mailbox.updated con el buzon
# activo solo vaciaria la cache. bea sigue entrando por lo que conserva, y su sesion del webmail, que
# necesita imap y smtp, se cierra.
webmail_cerrada() { wm "$TARRO_BEA" GET /folders; [[ $WM_CODE == 401 ]]; }
expect "bea entra por IMAP (queda en la cache de Dovecot)" "$(cliente login bea@acme.test "$BEA_PASS")" "OK"
sesion bea-imap bea@acme.test "$BEA_PASS" 60
esperar "bea con una sesion IMAP abierta" 30 sesion_en bea-imap LISTA
wm "$TARRO_BEA" GET /folders
expect "y con su sesion del webmail abierta" "$WM_CODE" "200"
BEA_KICKS0=$(bea_kicks)
api PATCH "/mailboxes/$BEAID" '{"imap_access":false}'
expect "mail-directory le quita imap a bea y le deja smtp" \
  "$API_CODE/$(echo "$API_BODY" | jget data.imap_access)/$(echo "$API_BODY" | jget data.smtp_access)" "200/False/True"
esperar "mail-security vacia la cache de bea y cierra sus sesiones (credentials_changed al quitar imap)" 20 bea_expulsada
esperar "la sesion IMAP abierta de bea se cierra" 20 sesion_en bea-imap CERRADA
esperar "Dovecot rechaza al momento el IMAP de bea, que tenia en su cache" 20 login_rechazado bea@acme.test "$BEA_PASS"
contains "mail-auth la rechaza por el protocolo, no por la contrasena" \
  "$(docker logs "$(c mail-auth)" 2>&1 | grep '"username":"bea@acme.test"')" "protocolo deshabilitado para el buzon"
contains "bea sigue enviando por submission con la misma contrasena (smtp_access, que conserva)" \
  "$(cliente enviar bea@acme.test "$BEA_PASS" bea@acme.test bea@acme.test "sin-imap-$(rand_hex 4)")" "OK 250"
esperar "y su sesion del webmail, que necesita imap, se cierra" 20 webmail_cerrada
api PATCH "/mailboxes/$BEAID" '{"imap_access":true}'
expect "bea recupera imap" "$API_CODE/$(echo "$API_BODY" | jget data.imap_access)" "200/True"
esperar "y vuelve a entrar por IMAP" 20 login_aceptado bea@acme.test "$BEA_PASS"

# Apagar el buzon y cambiar su contrasena SI cierran la sesion del webmail al momento: son los dos
# cambios que la invalidan sin tocar un protocolo, y con changed siguen cerrandola como antes.
#
# Aqui NO se sondea /folders esperando el 401, como si se hace arriba con imap_access: con el buzon
# apagado el directorio deja de servirlo, y la peticion del webmail entra como usuario MAESTRO (la
# passdb que autentica es la del maestro, asi que la busqueda del buzon llega al userdb y no la corta
# antes mail-auth, como si pasa con un login normal). Cada intento en esa ventana deja "user not
# found from any userdbs" en el registro de Dovecot y un error en el del webmail, y el escaneo final
# mira el log ENTERO: basta provocarlo una vez para romperlo. Se espera a la senal real (el webmail
# registra la revocacion al aplicarla) y solo despues se mira la sesion: ya revocada, responde 401
# sin abrir IMAP. La sesion previa se comprueba sirviendo carpetas porque una abierta dentro del
# margen de revocacion del cambio anterior nace invalidada y el 401 de despues no probaria nada.
webmail_sesion_viva() {
  wm "$TARRO_BEA" POST /session -H 'Content-Type: application/json' -d "{\"username\":\"bea@acme.test\",\"password\":\"$BEA_PASS\"}"
  [[ $WM_CODE == 200 ]] || return 1
  wm "$TARRO_BEA" GET /folders
  [[ $WM_CODE == 200 ]]
}
webmail_revoco() { docker logs "$(c webmail)" 2>&1 | grep 'sesiones del buzon revocadas' | grep -c '"username":"bea@acme.test"'; }
webmail_revoco_mas() { (( $(webmail_revoco) > BEA_WM_REV0 )); }
esperar "bea abre una sesion del webmail que sirve sus carpetas" 30 webmail_sesion_viva
BEA_WM_REV0=$(webmail_revoco)
api PATCH "/mailboxes/$BEAID" '{"active":0}'
expect "mail-directory apaga a bea" "$API_CODE" "200"
esperar "el webmail revoca sus sesiones al apagarse el buzon" 20 webmail_revoco_mas
wm "$TARRO_BEA" GET /folders
expect "y su sesion del webmail ya no sirve el buzon" "$WM_CODE" "401"
api PATCH "/mailboxes/$BEAID" '{"active":1}'
expect "bea se reactiva" "$API_CODE" "200"
esperar "y vuelve a entrar por IMAP" 20 login_aceptado bea@acme.test "$BEA_PASS"
esperar "y a abrir una sesion del webmail que sirve sus carpetas" 30 webmail_sesion_viva
BEA_WM_REV0=$(webmail_revoco)
BEA_OTRA="$(rand_hex 10)Aa1!"
api POST "/mailboxes/$BEAID/password" "{\"password\":\"$BEA_OTRA\"}"
expect "mail-directory vuelve a cambiar la contrasena de bea" "$API_CODE" "204"
esperar "el webmail revoca sus sesiones al cambiar la contrasena" 20 webmail_revoco_mas
wm "$TARRO_BEA" GET /folders
expect "y su sesion del webmail ya no sirve el buzon" "$WM_CODE" "401"
BEA_PASS="$BEA_OTRA"
esperar "y bea entra por IMAP con la nueva" 20 login_aceptado bea@acme.test "$BEA_PASS"
expect "mail-security sin fallos de revocacion" "$(revocaciones fallos)" "0"

echo "== Gestor de la cola de Postfix (agente en el contenedor -> mail-security -> gateway, solo superadmin)"
# Sin salida real: con defer_transports=smtp, Postfix no intenta entregar y deja el mensaje diferido en la
# cola, que es lo que hay que gestionar. Se restaura al terminar.
POSTCONF=(docker exec "$(c postfix-mail)" postconf -c /opt/postfix/conf)
"${POSTCONF[@]}" -e defer_transports=smtp && docker exec "$(c postfix-mail)" postfix reload >/dev/null 2>&1
MSG_COLA="$TOKEN-cola"
encolar() { # encolar <n>: un mensaje a un destino externo, que queda diferido
  printf 'Subject: %s-%s\n\ncola\n' "$MSG_COLA" "$1" | docker exec -i -e MAIL_CONFIG=/opt/postfix/conf "$(c postfix-mail)" sendmail -f "cola@cfm.test" "destino-$1@ejemplo.org"
}
encolar 1; encolar 2
cola_json() { docker exec "$(c postfix-mail)" postqueue -j; }
# Diferidos y estables: un mensaje que Postfix aun procesa (active) puede cambiar de identificador.
en_cola() { [[ "$(cola_json | grep '"queue_name": "deferred"' | grep -c "destino-[12]@ejemplo.org")" == 2 ]]; }
esperar "los dos mensajes quedan diferidos en la cola de Postfix" 60 en_cola
sleep 3
QID1=$(cola_json | python3 -c 'import json, sys
for l in sys.stdin:
    m = json.loads(l)
    if any(r["address"] == "destino-1@ejemplo.org" for r in m["recipients"]): print(m["queue_id"])')
QID2=$(cola_json | python3 -c 'import json, sys
for l in sys.stdin:
    m = json.loads(l)
    if any(r["address"] == "destino-2@ejemplo.org" for r in m["recipients"]): print(m["queue_id"])')
[[ -n "$QID1" && -n "$QID2" ]] && ok "identificadores de cola: $QID1 y $QID2" || mal "sin identificadores de cola ($QID1, $QID2)"

A1="Authorization: Bearer $(e2e_login "$ADMIN_EMAIL" "$ADMIN_PASS" | jget data.access_token)"
A2="Authorization: Bearer $(e2e_login admin@acme.test "$TENANT_PASS" | jget data.access_token)"
cola() { # cola <cabecera> <metodo> <ruta>: deja COLA_CODE y COLA_BODY
  COLA_CODE=$(curl -s -o "$WORK/cola.json" -w '%{http_code}' -X "$2" "$GW/mail-security/queue$3" -H "$1")
  COLA_BODY=$(cat "$WORK/cola.json")
}
cola "$A1" GET ""
expect "el superadmin lista la cola" "$COLA_CODE" "200"
contains "con el identificador del mensaje" "$COLA_BODY" "$QID1"
contains "su remitente y su destinatario" "$COLA_BODY" "destino-1@ejemplo.org"
contains "y por que sigue en cola" "$COLA_BODY" "delay_reason"
lacks "sin el asunto ni el contenido del mensaje" "$COLA_BODY" "$MSG_COLA"
cola "$A1" GET "?limit=1"
expect "el limite se respeta y avisa de que hay mas" "$COLA_CODE/$(echo "$COLA_BODY" | jget data.truncated)/$(echo "$COLA_BODY" | python3 -c 'import json, sys; print(len(json.load(sys.stdin)["data"]["items"]))')" "200/True/1"

cola "$A2" GET ""
expect "un administrador de empresa no ve la cola" "$COLA_CODE" "403"
cola "$A2" DELETE "/$QID1"
expect "ni borra un mensaje" "$COLA_CODE" "403"
cola "$A1" DELETE "/no-valido"
expect "un identificador invalido se rechaza (422)" "$COLA_CODE" "422"
en_cola_de() { cola_json | python3 -c 'import json, sys
for l in sys.stdin:
    m = json.loads(l)
    if m["queue_id"] == sys.argv[1]: print(m["queue_name"])' "$1"; }
expect "el mensaje sigue en la cola tras los rechazos" "$(en_cola_de "$QID1")" "deferred"

cola "$A1" POST "/$QID1/hold"
expect "retener un mensaje" "$COLA_CODE" "204"
expect "Postfix lo tiene en hold" "$(en_cola_de "$QID1")" "hold"
cola "$A1" POST "/$QID1/unhold"
expect "liberarlo" "$COLA_CODE" "204"
expect "y vuelve a la cola diferida" "$(en_cola_de "$QID1")" "deferred"
cola "$A1" POST "/$QID1/retry"
expect "reintentar la entrega" "$COLA_CODE" "204"
cola "$A1" POST "/flush"
expect "vaciar la cola diferida (202)" "$COLA_CODE" "202"
cola "$A1" DELETE "/$QID1"
expect "borrar un mensaje" "$COLA_CODE" "204"
expect "Postfix ya no lo tiene" "$(en_cola_de "$QID1")" ""
cola "$A1" DELETE "/$QID1"
expect "borrarlo otra vez es un 404" "$COLA_CODE" "404"
# Tras reintentar y vaciar, Postfix lo vuelve a intentar y puede estar en active un instante: lo que cuenta es que sigue en la cola y no retenido.
continua_en_cola() { [[ "$(en_cola_de "$1")" =~ ^(active|deferred)$ ]]; }
continua_en_cola "$QID2" && ok "el otro mensaje no se toco" || mal "el otro mensaje no se toco (estado: '$(en_cola_de "$QID2")')"

# El monitor de mail-security (consulta cada 10 s en la prueba) publica la cola como metricas.
metrica_cola() { curl -s "http://127.0.0.1:${PORT[mail-security]}/metrics" | awk -v q="$1" '$1 == "mail_security_postfix_queue_messages{queue=\"" q "\"}" { print $2 }'; }
cola_vista_por_prometheus() { [[ "$(metrica_cola deferred)" -ge 1 ]] 2>/dev/null; }
esperar "mail-security publica la cola diferida como metrica" 40 cola_vista_por_prometheus
contains "y el instante de la ultima consulta correcta" "$(curl -s "http://127.0.0.1:${PORT[mail-security]}/metrics")" "mail_security_postfix_queue_last_poll_success_timestamp_seconds 1"
antiguo=$(curl -s "http://127.0.0.1:${PORT[mail-security]}/metrics" | python3 -c 'import sys
for l in sys.stdin:
    p = l.split()
    if p and p[0] == "mail_security_postfix_queue_oldest_arrival_timestamp_seconds": print(int(float(p[1])))')
[[ "${antiguo:-0}" -gt 1600000000 ]] && ok "con el mensaje mas antiguo de la cola" || mal "sin instante del mensaje mas antiguo ('$antiguo')"

# El agente por su cuenta: exige la clave y no admite lo que no es suyo.
agente_http() { docker exec "$(c postfix-mail)" curl -sk -o /dev/null -w '%{http_code}' "$@"; }
expect "el agente rechaza una peticion sin clave" "$(agente_http https://127.0.0.1:8590/v1/queue)" "401"
expect "y una clave equivocada" "$(agente_http -H "Authorization: Bearer $(rand_hex 32)" https://127.0.0.1:8590/v1/queue)" "401"
expect "con la clave lista la cola" "$(agente_http -H "Authorization: Bearer $QUEUE_KEY" https://127.0.0.1:8590/v1/queue)" "200"
expect "no borra por un identificador que no es de cola" "$(agente_http -X POST -H "Authorization: Bearer $QUEUE_KEY" "https://127.0.0.1:8590/v1/queue/ALL/delete")" "400"
expect "ni admite una accion que no es suya" "$(agente_http -X POST -H "Authorization: Bearer $QUEUE_KEY" "https://127.0.0.1:8590/v1/queue/$QID2/super_delete")" "400"
continua_en_cola "$QID2" && ok "y el otro mensaje sigue ahi" || mal "el otro mensaje ya no esta (estado: '$(en_cola_de "$QID2")')"

"${POSTCONF[@]}" -X defer_transports && docker exec "$(c postfix-mail)" postfix reload >/dev/null 2>&1
docker exec "$(c postfix-mail)" postsuper -d ALL >/dev/null 2>&1

echo "== Migracion de buzones (mail-migration -> ejecutor con imapsync real -> Dovecot, con ClamAV real)"
# La prueba migra entre buzones del MISMO Dovecot: el origen (un buzon con su contrasena) se pide por la
# IP fija de Dovecot en la red de la migracion, con el certificado verificado; el destino es el buzon
# nuevo, al que el ejecutor entra por el usuario maestro PROPIO de la migracion. Solo aqui el ejecutor
# admite origenes privados (deploy/mail/docker-compose.e2e.yml): en produccion la guarda de origen los
# rechaza.
RUNNER=$(c mail-migration-runner)
esperar "el ejecutor de migracion arranca (healthcheck del propio binario)" 90 docker exec "$RUNNER" /usr/local/bin/migration-runner healthcheck
expect "el ejecutor solo esta en la red de la migracion (no en la de los motores ni en la de la plataforma)" \
  "$(docker inspect -f '{{range $n, $v := .NetworkSettings.Networks}}{{$n}} {{end}}' "$RUNNER" | sed 's/ $//')" "$E2E_MIGRATION_NETWORK"
expect "corre sin privilegios, con el sistema de ficheros de solo lectura y sin capacidades" \
  "$(docker inspect -f '{{.Config.User}} {{.HostConfig.ReadonlyRootfs}} {{.HostConfig.CapDrop}}' "$RUNNER")" "10001:10001 true [ALL]"
expect "sin puertos publicados" "$(docker port "$RUNNER" 2>&1)" ""
contains "resuelve Dovecot y ClamAV, que comparten su red" \
  "$(docker exec "$RUNNER" sh -c 'getent hosts dovecot; getent hosts clamd' 2>&1)" "$MIGRATION_DOVECOT_IP"
expect "no resuelve los motores ni los servicios de la celda (postfix, redis, mail-auth, mail-directory)" \
  "$(docker exec "$RUNNER" sh -c 'for h in postfix redis mail-auth mail-directory; do getent hosts "$h"; done' 2>&1)" ""
if docker exec "$RUNNER" nc -z -w 3 "$IPV4_NETWORK.253" 25 >/dev/null 2>&1; then
  mal "el ejecutor alcanza el SMTP de Postfix por su IP en la red de los motores"
else
  ok "ni alcanza a Postfix por su IP en la red de los motores"
fi
codigo_ejecutor() { curl -s -o /dev/null -w '%{http_code}' -X POST "$@" "http://127.0.0.1:$MIGRATION_RUNNER_PORT/v1/claim"; }
[[ "$(codigo_ejecutor)" =~ ^40[13]$ ]] && ok "la API del ejecutor rechaza una peticion sin clave" || mal "la API del ejecutor acepto una peticion sin clave"
[[ "$(codigo_ejecutor -H 'Authorization: Bearer clave-que-no-es')" =~ ^40[13]$ ]] && ok "y con una clave que no es" || mal "la API del ejecutor acepto una clave que no es"

cliente_migracion() { # como cliente, pero desde la red de la migracion
  docker run --rm -i --network "$E2E_MIGRATION_NETWORK" -v "$ROOT/ops/e2e/mail_client.py:/cliente.py:ro" -v "$TLS/ca.pem:/ca.pem:ro" \
    --entrypoint python3 "$PROYECTO-dovecot-mail" /cliente.py --ca /ca.pem --nombre "$MAIL_HOSTNAME" "$@" 2>&1
}
MAESTRO_MIGRACION="$MIGRATION_MASTER_USER@platform.local"
expect "el maestro de la migracion entra a un buzon desde la red de la migracion" \
  "$(cliente_migracion login bea@acme.test "$MIGRATION_MASTER_PASS" --maestro "$MAESTRO_MIGRACION")" "OK"
contains "y no desde la red de los motores (su allow_nets es solo la de la migracion)" \
  "$(cliente login bea@acme.test "$MIGRATION_MASTER_PASS" --maestro "$MAESTRO_MIGRACION")" "NO"
contains "el maestro del webmail no entra por la red de la migracion" \
  "$(cliente_migracion login bea@acme.test "$MASTER_PASS" --maestro "$MAESTRO")" "NO"

api GET /mail-migration/meta
expect "la interfaz ve la migracion configurada (mail-migration tiene la clave del ejecutor)" "$(echo "$API_BODY" | jget data.configured)" "True"

# Dos pares de buzones: uno limpio y otro con un mensaje con virus en el origen.
MIG="mig$(rand_hex 4)"
ORIG1_PASS="$(rand_hex 10)Aa1!"; DEST1_PASS="$(rand_hex 10)Aa1!"
ORIG2_PASS="$(rand_hex 10)Aa1!"; DEST2_PASS="$(rand_hex 10)Aa1!"
creado "buzon origen1@acme.test (origen de la migracion)" POST /mailboxes "{\"local_part\":\"origen1\",\"domain\":\"acme.test\",\"password\":\"$ORIG1_PASS\"}"
creado "buzon destino1@acme.test" POST /mailboxes "{\"local_part\":\"destino1\",\"domain\":\"acme.test\",\"password\":\"$DEST1_PASS\"}"
DEST1_ID=$(echo "$API_BODY" | jget data.id)
creado "buzon origen2@acme.test (origen con un mensaje con virus)" POST /mailboxes "{\"local_part\":\"origen2\",\"domain\":\"acme.test\",\"password\":\"$ORIG2_PASS\"}"
creado "buzon destino2@acme.test" POST /mailboxes "{\"local_part\":\"destino2\",\"domain\":\"acme.test\",\"password\":\"$DEST2_PASS\"}"
DEST2_ID=$(echo "$API_BODY" | jget data.id)
for n in uno dos; do
  contains "correo real para origen1 ($n)" "$(cliente enviar ana@acme.test "$ANA_PASS" ana@acme.test origen1@acme.test "$MIG-$n")" "OK 250"
done
contains "y para origen2 (mensaje limpio)" "$(cliente enviar ana@acme.test "$ANA_PASS" ana@acme.test origen2@acme.test "$MIG-limpio")" "OK 250"
# El mensaje con virus entra por doveadm y no por SMTP: Rspamd lo rechazaria al final de DATA.
{
  printf 'From: ana@acme.test\r\nTo: origen2@acme.test\r\nSubject: %s-virus\r\nMessage-ID: <%s-virus@acme.test>\r\nDate: Mon, 21 Sep 2026 10:00:00 +0000\r\nContent-Type: text/plain\r\n\r\n' "$MIG" "$MIG"
  cat "$WORK/eicar.com"
  printf '\r\n'
} | en dovecot-mail doveadm save -u origen2@acme.test -m INBOX >/dev/null 2>&1 && ok "mensaje con EICAR guardado en origen2 (el origen no pasa por Rspamd)" || mal "doveadm save del mensaje con EICAR en origen2"
for par in "origen1@acme.test:$ORIG1_PASS:$MIG-uno" "origen1@acme.test:$ORIG1_PASS:$MIG-dos" "origen2@acme.test:$ORIG2_PASS:$MIG-limpio" "origen2@acme.test:$ORIG2_PASS:$MIG-virus"; do
  IFS=: read -r bz clave asunto <<<"$par"
  contains "el origen tiene $asunto" "$(cliente buscar "$bz" "$clave" "$asunto" --espera 30)" "OK 1"
done

migrar() { # migrar <id del buzon destino> <usuario de origen> <contrasena de origen>: crea el trabajo y deja su id en MIG_JOB
  api POST /mail-migration/jobs "{\"mailbox_id\":\"$1\",\"source_host\":\"$MIGRATION_DOVECOT_IP\",\"source_port\":993,\"source_tls\":\"ssl\",\"source_username\":\"$2\",\"source_password\":\"$3\"}"
  MIG_JOB=$(echo "$API_BODY" | jget data.id)
}
terminado() { api GET "/mail-migration/jobs/$1"; [[ "$(echo "$API_BODY" | jget data.status)" =~ ^(succeeded|failed|cancelled)$ ]]; }

migrar "$DEST1_ID" origen1@acme.test "$ORIG1_PASS"
expect "el trabajo se crea (201) pendiente" "$API_CODE/$(echo "$API_BODY" | jget data.status)" "201/pending"
lacks "y su respuesta no lleva la contrasena de origen" "$API_BODY" "$ORIG1_PASS"
esperar "el trabajo 1 termina (reclamo, dos pasadas de imapsync, cierre)" 240 terminado "$MIG_JOB"
expect "trabajo 1: correcto" "$(echo "$API_BODY" | jget data.status)" "succeeded"
expect "sin error" "$(echo "$API_BODY" | jget data.last_error.code)" ""
expect "copio los dos mensajes" "$(echo "$API_BODY" | jget data.progress.messages_copied)" "2"
expect "ninguno fallido" "$(echo "$API_BODY" | jget data.progress.messages_failed)" "0"
expect "con sus bytes" "$([[ "$(echo "$API_BODY" | jget data.progress.bytes_copied)" -gt 0 ]] && echo si)" "si"
lacks "el trabajo terminado tampoco expone la contrasena" "$API_BODY" "$ORIG1_PASS"
for n in uno dos; do
  contains "el buzon destino recibio $n" "$(cliente buscar destino1@acme.test "$DEST1_PASS" "$MIG-$n" --espera 10)" "OK 1"
done

migrar "$DEST1_ID" origen1@acme.test "$ORIG1_PASS"
esperar "repetir el trabajo termina" 240 terminado "$MIG_JOB"
expect "repetirlo es idempotente: correcto" "$(echo "$API_BODY" | jget data.status)" "succeeded"
expect "sin copiar nada de nuevo" "$(echo "$API_BODY" | jget data.progress.messages_copied)" "0"
expect "y sin duplicar correo en el destino" "$(cliente buscar destino1@acme.test "$DEST1_PASS" "$MIG-uno" | head -1)" "OK 1"

migrar "$DEST1_ID" origen1@acme.test "no-$ORIG1_PASS"
esperar "un trabajo con la contrasena de origen equivocada termina" 240 terminado "$MIG_JOB"
expect "falla por autenticacion de origen" "$(echo "$API_BODY" | jget data.status)/$(echo "$API_BODY" | jget data.last_error.code)" "failed/source_auth_failed"
lacks "sin contrasenas en el error" "$API_BODY" "$ORIG1_PASS"

migrar "$DEST2_ID" origen2@acme.test "$ORIG2_PASS"
esperar "el trabajo del origen con virus termina" 240 terminado "$MIG_JOB"
expect "falla por virus (virus_found)" "$(echo "$API_BODY" | jget data.status)/$(echo "$API_BODY" | jget data.last_error.code)" "failed/virus_found"
expect "copio el mensaje limpio" "$(echo "$API_BODY" | jget data.progress.messages_copied)" "1"
expect "y cuenta el infectado como fallido" "$(echo "$API_BODY" | jget data.progress.messages_failed)" "1"
lacks "el error no lleva contenido del correo" "$(echo "$API_BODY" | jget data.last_error.message)" "$MIG"
contains "el destino tiene el mensaje limpio" "$(cliente buscar destino2@acme.test "$DEST2_PASS" "$MIG-limpio" --espera 10)" "OK 1"
lacks "y no el que tenia virus (ClamAV antes de guardar)" "$(cliente buscar destino2@acme.test "$DEST2_PASS" "$MIG-virus" --espera 3)" "OK"
contains "el origen conserva el mensaje con virus: la migracion no borra nada del origen" \
  "$(cliente buscar origen2@acme.test "$ORIG2_PASS" "$MIG-virus" --espera 3)" "OK 1"
contains "y el limpio" "$(cliente buscar origen2@acme.test "$ORIG2_PASS" "$MIG-limpio" --espera 3)" "OK 1"

api GET "/mail-migration/jobs?mailbox_id=$DEST1_ID"
expect "el historial del buzon lista sus tres trabajos" \
  "$(echo "$API_BODY" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["data"]))' 2>/dev/null)" "3"
expect "ningun trabajo activo queda en la empresa" \
  "$(sql mail_tenant_acme "SELECT count(*) FROM mail_migration.jobs WHERE status IN ('pending', 'running')")" "0"
expect "la contrasena de origen ya no esta en la base de la empresa (source_password_enc nulo en todo trabajo terminado)" \
  "$(sql mail_tenant_acme "SELECT count(*) FROM mail_migration.jobs WHERE status IN ('succeeded', 'failed', 'cancelled') AND source_password_enc IS NOT NULL")" "0"
expect "ni en ninguna otra columna" \
  "$(sql mail_tenant_acme "SELECT count(*) FROM mail_migration.jobs j WHERE j::text LIKE '%$ORIG1_PASS%' OR j::text LIKE '%$ORIG2_PASS%'")" "0"
expect "el ejecutor no deja ficheros de trabajo ni contrasenas en su directorio efimero" \
  "$(docker exec "$RUNNER" ls -A /run/migration 2>&1)" ".alive"
lacks "ni la contrasena de origen en el registro del ejecutor" "$(docker logs "$RUNNER" 2>&1)" "$ORIG1_PASS"
lacks "ni la del usuario maestro de la migracion" "$(docker logs "$RUNNER" 2>&1)" "$MIGRATION_MASTER_PASS"

echo "== Rotacion y revocacion de la clave DKIM (domain-service -> mail-security -> redis-mail)"
# Una rotacion programada deposita la clave nueva y sigue firmando con la anterior. Revocar por
# compromiso entrega solo una clave nueva y mail-security retira las demas en esa misma llamada: al
# volver la respuesta, redis-mail ya no tiene ninguna clave revocada y Rspamd firma con la nueva.
A2="Authorization: Bearer $(e2e_login admin@acme.test "$TENANT_PASS" | jget data.access_token)"
api POST "/domains/$DOMID/rotate-dkim"
K2=$(echo "$API_BODY" | jget data.dkim_selector)
expect "rotacion programada: la clave nueva queda en redis-mail" "$API_CODE/$(redis_mail HEXISTS DKIM_PRIV_KEYS "$K2.acme.test")" "200/1"
expect "y se sigue firmando con la anterior mientras su TXT no se publica" "$(redis_mail HGET DKIM_SELECTORS acme.test)" "$SELECTOR"
api POST "/domains/$DOMID/rotate-dkim"
expect "con una clave en gracia no se rota otra vez" "$API_CODE/$(echo "$API_BODY" | jget error.code)" "409/DKIM_ROTATION_IN_PROGRESS"
MOTIVO_DKIM="clave expuesta en la prueba $(rand_hex 4)"
api POST "/domains/$DOMID/revoke-dkim" "{\"current_selector\":\"$K2\",\"reason\":\"$MOTIVO_DKIM\"}"
K3=$(echo "$API_BODY" | jget data.dkim_selector)
expect "revocacion por clave comprometida confirmada por la celda" "$API_CODE/$(echo "$API_BODY" | jget data.engines_retired)" "200/True"
expect "al volver la respuesta redis-mail ya no tiene la clave actual revocada ni la que seguia en gracia" \
  "$(redis_mail HEXISTS DKIM_PRIV_KEYS "$K2.acme.test")$(redis_mail HEXISTS DKIM_PRIV_KEYS "$SELECTOR.acme.test")" "00"
expect "y firma con la nueva" "$(redis_mail HGET DKIM_SELECTORS acme.test)/$(redis_mail HEXISTS DKIM_PRIV_KEYS "$K3.acme.test")" "$K3/1"
expect "la respuesta pide retirar del DNS los dos TXT revocados" \
  "$(echo "$API_BODY" | jget data.remove_dns_records.0.host) $(echo "$API_BODY" | jget data.remove_dns_records.1.host)" \
  "$K2._domainkey.acme.test $SELECTOR._domainkey.acme.test"
api POST "/domains/$DOMID/revoke-dkim" "{\"current_selector\":\"$K2\",\"reason\":\"$MOTIVO_DKIM\"}"
expect "el reintento de la misma revocacion no genera otra clave" "$API_CODE/$(echo "$API_BODY" | jget data.dkim_selector)" "200/$K3"
TOKEN_DKIM="e2e$(rand_hex 4)"
contains "ana envia tras la revocacion" "$(cliente enviar ana@acme.test "$ANA_PASS" ana@acme.test bea@acme.test "$TOKEN_DKIM-revocada")" "OK 250"
contains "Rspamd lo firma ya con la clave nueva" "$(cliente buscar bea@acme.test "$BEA_PASS" "$TOKEN_DKIM-revocada")" "s=$K3;"
SELECTOR="$K3"

echo "== Baja de la empresa: su correo deja de entrar y de autenticar en la celda"
# La saga de baja de organization da de baja a acme en el mail-directory de su celda antes de
# retirarla del registro: los mapas de Postfix dejan de servir su dominio, su dominio alias, sus
# buzones y sus aliases, Dovecot deja de autenticar a sus buzones (mail-auth) y mail-security saca
# sus dominios de DOMAIN_MAP y retira sus claves DKIM con los eventos del directorio.
expect "ana entra por IMAP antes de la baja (queda en la cache de Dovecot)" "$(cliente login ana@acme.test "$ANA_PASS")" "OK"
sesion ana-baja ana@acme.test "$ANA_PASS" 120
esperar "y con una sesion IMAP abierta" 30 sesion_en ana-baja LISTA
# ana_kicks: veces que mail-security vacio la cache de ana y cerro sus sesiones por un cambio que le
# quita el acceso (su registro, "credencial del buzon retirada de Dovecot").
ana_kicks() { docker logs "$(c mail-security)" 2>&1 | grep -c '"username":"ana@acme.test","change":"updated","action":"kick"'; }
ANA_KICKS0=$(ana_kicks)
AS="Authorization: Bearer $(e2e_login "$ADMIN_EMAIL" "$ADMIN_PASS" | jget data.access_token)"
expect "el superadmin deja a acme inactiva" \
  "$(curl -s -X PATCH "$GW/organizations/$TID" -H "$AS" -H 'Content-Type: application/json' -d '{"status":"inactive"}' | jget data.status)" "inactive"
expect "y la borra" "$(curl -s -o /dev/null -w '%{http_code}' -X DELETE "$GW/organizations/$TID" -H "$AS")" "204"
mapa pgsql_virtual_domains_maps acme.test ""
mapa pgsql_virtual_domains_maps acme-alias.test ""
mapa pgsql_virtual_mailbox_maps ana@acme.test ""
mapa pgsql_virtual_alias_maps ventas@acme.test ""
# La baja apaga el buzon de ana (mail.mailbox.updated): mail-security vacia su entrada en la cache
# de Dovecot y cierra su sesion, sin esperar a auth_cache_ttl. El sondeo espera a ese vaciado: un
# intento anterior lo responde Dovecot desde su cache, y como el directorio ya no sirve a ana, falla
# en la busqueda del usuario (un error en su registro) sin llegar a mail-auth.
ana_expulsada() { (( $(ana_kicks) > ANA_KICKS0 )); }
esperar "mail-security vacia la cache de ana y cierra sus sesiones tras la baja" 20 ana_expulsada
esperar "Dovecot deja de aceptar a ana al momento aunque la tenia en su cache" 20 login_rechazado ana@acme.test "$ANA_PASS"
contains "mail-auth la rechaza por el buzon apagado, no por la contrasena" \
  "$(docker logs "$(c mail-auth)" 2>&1 | grep '"username":"ana@acme.test"')" "buzon sin inicio de sesion"
esperar "y su sesion IMAP abierta se cierra" 20 sesion_en ana-baja CERRADA
# Ningun cliente de sesion sigue vivo cuando se derriba la red de los motores.
wait "${SESION_PID[@]}" 2>/dev/null
fuera_de_domain_map() { [[ -z "$(redis_mail HGET DOMAIN_MAP acme.test)" && -z "$(redis_mail HGET DOMAIN_MAP acme-alias.test)" ]]; }
esperar "mail-security saca acme.test y su dominio alias de DOMAIN_MAP" 60 fuera_de_domain_map
sin_dkim() { [[ -z "$(redis_mail HGET DKIM_SELECTORS acme.test)" && "$(redis_mail HEXISTS DKIM_PRIV_KEYS "$SELECTOR.acme.test")" == 0 ]]; }
esperar "y retira sus claves DKIM" 60 sin_dkim
expect "acme.test sale del indice global de organization" \
  "$(sql mail_registry "SELECT count(*) FROM organization.mail_domain_cells WHERE domain = 'acme.test'")" "0"

echo "== Registros"
e2e_registros_sin_errores
for s in mail-directory mail-auth mail-security webmail; do
  # Sin TRANSACTIONAL_URL, mail-security registra como error que el aviso de cuarentena queda
  # desactivado (deploy/mail/README.md): es la configuracion de esta prueba, no un fallo.
  errores=$(docker logs "$(c "$s")" 2>&1 | grep '"level":"error"' | grep -v 'aviso de cuarentena desactivado')
  [[ -z "$errores" ]] && ok "$s sin errores en su registro" || { mal "errores en $s"; head -3 <<<"$errores" >&2; }
done
errores=$(docker logs "$(c mail-migration-runner)" 2>&1 | grep '"level":"ERROR"')
[[ -z "$errores" ]] && ok "el ejecutor de migracion sin errores en su registro" || { mal "errores en el ejecutor de migracion"; head -3 <<<"$errores" >&2; }
errores=$(docker logs "$(c postfix-mail)" 2>&1 | grep -E 'fatal:|panic:|pgsql.*(error|failed)')
[[ -z "$errores" ]] && ok "Postfix sin fatal ni errores de pgsql" || { mal "Postfix"; head -3 <<<"$errores" >&2; }
# El maestro sobre nadie@ es una comprobacion de arriba y Dovecot la registra como error.
errores=$(docker logs "$(c dovecot-mail)" 2>&1 | grep -E 'Fatal:|Panic:|auth.*Error' | grep -v 'nadie@acme.test')
[[ -z "$errores" ]] && ok "Dovecot sin Fatal ni errores de autenticacion" || { mal "Dovecot"; head -3 <<<"$errores" >&2; }
for s in unbound-mail redis-mail clamd-mail rspamd-mail dovecot-mail postfix-mail postfix-tlspol-mail olefy-mail mail-directory mail-auth mail-security webmail mail-migration-runner; do
  reinicios=$(docker inspect -f '{{.RestartCount}}' "$(c "$s")" 2>/dev/null)
  [[ "$reinicios" == 0 ]] || mal "$s se reinicio ($reinicios veces)"
done
ok "ningun contenedor se reinicio"

echo
DURACION=$(( $(date +%s) - INICIO ))
if [[ $fallos -gt 0 ]]; then
  diagnostico
  echo "E2E-MAIL: $fallos fallos, $aciertos comprobaciones en verde (${DURACION}s)" >&2
  exit 1
fi
echo "E2E-MAIL: OK ($aciertos comprobaciones, ${DURACION}s)"

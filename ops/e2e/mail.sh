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
# (con su enlace firmado por el gateway), las claves de Redis que escribe mail-security y el
# webmail por el gateway.
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
  [domain-service]=$((BASE + 43)) [webmail]=$((BASE + 44)) [gateway]=$((BASE + 80))
)
e2e_fuera_del_rango_efimero "$PG_PORT" "$NATS_PORT" "$REDIS_PORT" "$DNS_PORT" "${PORT[@]}"

E2E_PREFIX=cfm-e2e-mail
PROYECTO=cfm-e2e-mail
export E2E_MAIL_NETWORK="${E2E_MAIL_NETWORK:-cfm-e2e-mail-engines}"
export E2E_MAIL_BRIDGE="${E2E_MAIL_BRIDGE:-br-cfme2email}"
export IPV4_NETWORK="${E2E_MAIL_IPV4_NETWORK:-172.30.29}"
export E2E_MAIL_CLAMAV_VOLUME="${E2E_MAIL_CLAMAV_VOLUME:-cfm-e2e-mail-clamav-signatures}"
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
    DOVECOT_MASTER_USER="${MASTER_USER}" DOVECOT_MASTER_PASS="$MASTER_PASS" DOVEADM_API_KEY="${DOVEADM_KEY}" \
    docker compose -p "$PROYECTO" -f "$MAILDIR/docker-compose.mail.yml" -f "$MAILDIR/docker-compose.e2e.yml" "$@"
}

restos() {
  local ids
  ids=$(docker ps -aq --filter "label=com.docker.compose.project=$PROYECTO")
  # shellcheck disable=SC2086
  docker rm -f $ids "$E2E_PREFIX-pg" "$E2E_PREFIX-nats" "$E2E_PREFIX-redis" "$E2E_PREFIX-dns" >/dev/null 2>&1
  docker network rm "$E2E_MAIL_NETWORK" >/dev/null 2>&1
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
e2e_puertos_libres "$PG_PORT" "$NATS_PORT" "$REDIS_PORT" "$DNS_PORT" "${PORT[@]}"
docker network inspect $(docker network ls -q) --format '{{.Name}} {{range .IPAM.Config}}{{.Subnet}} {{end}}' 2>/dev/null |
  python3 -c '
import ipaddress, sys
mia = ipaddress.ip_network(sys.argv[1])
for linea in sys.stdin:
    nombre, *redes = linea.split()
    for r in redes:
        try:
            if ipaddress.ip_network(r, strict=False).overlaps(mia):
                sys.exit(f"E2E: la subred {mia} choca con la red {nombre} ({r}); usa E2E_MAIL_IPV4_NETWORK")
        except ValueError:
            pass' "$IPV4_NETWORK.0/24" || exit 2

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
certificado() { # certificado <nombre> <clave> <certificado>
  openssl req -newkey rsa:2048 -nodes -subj "/CN=$1" -keyout "$2" -out "$WORK/$1.csr" >/dev/null 2>&1 &&
    openssl x509 -req -in "$WORK/$1.csr" -CA "$TLS/ca.pem" -CAkey "$TLS/ca-key.pem" -CAcreateserial -days 2 -out "$3" \
      -extfile <(printf 'subjectAltName=DNS:%s\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\n' "$1") >/dev/null 2>&1
}
certificado "$MAIL_HOSTNAME" "$TLS/mail/key.pem" "$TLS/mail/cert.pem" || { echo "openssl: certificado de correo" >&2; exit 1; }
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
export CELL_CODE=pe-01 CELL_DB_NAME=mail_cell_pe_01 DEFAULT_CELL_CODE=pe-01
export MAIL_MX_HOSTNAME="$MAIL_HOSTNAME" MAIL_SPF_INCLUDE=include:spf.cfm.test MAIL_DMARC_RUA=dmarc@cfm.test
export MAIL_DNS_RESOLVER="127.0.0.1:$DNS_PORT"
export API_ORIGIN="http://localhost:${PORT[gateway]}" PUBLIC_BASE_URL="http://localhost:${PORT[gateway]}" CORS_ALLOWED_ORIGINS=http://localhost:3000
export IDENTITY_PORT=${PORT[identity]} ACCESS_CONTROL_PORT=${PORT[access-control]} ORGANIZATION_PORT=${PORT[organization]}
export DOMAIN_SERVICE_PORT=${PORT[domain-service]} GATEWAY_PORT=${PORT[gateway]}
for s in identity access-control organization domain-service mail-directory mail-auth mail-security webmail; do
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
e2e_compilar organization identity access-control gateway domain-service || exit 1

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
for s in identity access-control domain-service gateway; do esperar_salud "$s" "${PORT[$s]}"; done

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
expect "mail-security sin fallos de revocacion" "$(revocaciones fallos)" "0"

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
errores=$(docker logs "$(c postfix-mail)" 2>&1 | grep -E 'fatal:|panic:|pgsql.*(error|failed)')
[[ -z "$errores" ]] && ok "Postfix sin fatal ni errores de pgsql" || { mal "Postfix"; head -3 <<<"$errores" >&2; }
# El maestro sobre nadie@ es una comprobacion de arriba y Dovecot la registra como error.
errores=$(docker logs "$(c dovecot-mail)" 2>&1 | grep -E 'Fatal:|Panic:|auth.*Error' | grep -v 'nadie@acme.test')
[[ -z "$errores" ]] && ok "Dovecot sin Fatal ni errores de autenticacion" || { mal "Dovecot"; head -3 <<<"$errores" >&2; }
for s in unbound-mail redis-mail clamd-mail rspamd-mail dovecot-mail postfix-mail postfix-tlspol-mail olefy-mail mail-directory mail-auth mail-security webmail; do
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

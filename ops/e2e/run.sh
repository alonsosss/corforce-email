#!/usr/bin/env bash
# Prueba de punta a punta de la plataforma con binarios reales contra Postgres, NATS y
# Redis desechables. Recorre lo que ya esta integrado: arranque de una plataforma vacia,
# alta de celda y empresa, acceso por el gateway con sus tres capas, dominio y buzon en
# la celda, propagacion a Redis por eventos, mapas de los motores, plantillas, supresion,
# planes y derechos de billing, autorizacion de envio en reputation, alcance de permisos,
# contactos con su consentimiento y audiencia, campanas por la via de marketing de
# transactional (hasta su rechazo por remitente sin verificar, sin SES), el doble opt-in por
# automations, el panel de analitica y el rechazo del token de una cuenta borrada o desactivada. Los servicios de la celda corren con la credencial
# propia de la celda (ops/db/cell-service-role.sh), sin la de plataforma.
#
# Cada paso COMPRUEBA su resultado y la ejecucion termina con error si alguno falla: no
# basta con que los servicios arranquen, tienen que hablarse. Las credenciales se generan
# en cada ejecucion; ninguna vive en este fichero.
#
# Uso:
#   make e2e
#   E2E_KEEP=1 make e2e          # deja contenedores y binarios para inspeccionar
#   E2E_PORT_BASE=26000 make e2e # otro rango si 28000-28099 esta ocupado
#
# Una sola ejecucion a la vez (puede correr junto a make e2e-mail, que usa otro prefijo y
# otros puertos): con otra en marcha sale con 3 sin tocar nada (e2e_reservar en ops/e2e/lib.sh).
#
# Los puertos quedan por debajo del rango efimero del sistema: dentro de el, el kernel
# puede dar el puerto de un servicio a una conexion saliente justo antes de que el
# servicio lo abra ("address already in use" sin nadie escuchando despues).
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
E2E_PREFIX=cfm-e2e
# shellcheck source=ops/e2e/lib.sh
source ops/e2e/lib.sh

BASE="${E2E_PORT_BASE:-28000}"
PG_PORT="${E2E_PG_PORT:-$((BASE - 2568))}"
NATS_PORT="${E2E_NATS_PORT:-$((BASE - 3778))}"
REDIS_PORT="${E2E_REDIS_PORT:-$((BASE - 1621))}"
e2e_fuera_del_rango_efimero "$PG_PORT" "$NATS_PORT" "$REDIS_PORT" "$BASE" "$((BASE + 99))"
e2e_reservar
WORK="$(mktemp -d)"

limpiar() {
  pkill -f "$WORK/bin/" 2>/dev/null
  if [[ "${E2E_KEEP:-0}" != "1" ]]; then
    docker rm -f "$E2E_PREFIX-pg" "$E2E_PREFIX-nats" "$E2E_PREFIX-redis" >/dev/null 2>&1
    rm -rf "$WORK"
  else
    echo "E2E_KEEP=1: contenedores $E2E_PREFIX-* y registros en $WORK/log"
  fi
  e2e_liberar
}
trap limpiar EXIT

echo "== Infraestructura desechable"
e2e_infra_up || exit 1

SERVICES=(organization identity access-control gateway mail-directory mail-auth domain-service mail-security templates suppression billing reputation contacts analytics transactional campaigns automations)
echo "== Compilacion (${SERVICES[*]})"
e2e_compilar "${SERVICES[@]}" || exit 1

echo "== Bases de las celdas pe-01 y pe-02"
e2e_celda pe-01
e2e_celda pe-02

echo "== Credencial propia de cada celda (ops/db/cell-service-role.sh)"
CELL_ROLE=mail_cell_pe_01_svc
CELL_PASS="$(rand_hex 24)"
e2e_credencial_celda pe-01 "$CELL_PASS"
e2e_credencial_celda pe-02 "$(rand_hex 24)"
# Por el socket del contenedor: lo que se prueba es el privilegio CONNECT, no la contrasena
# (la prueban los servicios de la celda, que entran por TCP).
como_celda() { psql -U "$CELL_ROLE" -d "$1" -At -c 'SELECT current_user' 2>&1; }
expect "el rol de la celda entra en su base" "$(como_celda mail_cell_pe_01)" "$CELL_ROLE"
contains "pero no en el registro" "$(como_celda mail_registry)" "permission denied for database"
contains "ni en la base de otra celda" "$(como_celda mail_cell_pe_02)" "permission denied for database"

# ── Entorno comun ────────────────────────────────────────────────────────────
e2e_entorno_comun || exit 1
export MAIL_REDIS_HOST=127.0.0.1 MAIL_REDIS_PORT="$REDIS_PORT"
export DEFAULT_CELL_CODE=pe-01 CELL_CODE=pe-01 CELL_DB_NAME=mail_cell_pe_01
export MAIL_HOSTNAME=mail.cfm.test MAIL_MX_HOSTNAME=mail.cfm.test MAIL_SPF_INCLUDE=include:spf.cfm.test MAIL_DMARC_RUA=dmarc@cfm.test
export CORS_ALLOWED_ORIGINS=http://localhost:3000

declare -A PORT=(
  [identity]=$((BASE + 1)) [access-control]=$((BASE + 2)) [organization]=$((BASE + 3))
  [mail-directory]=$((BASE + 40)) [mail-auth]=$((BASE + 41)) [mail-security]=$((BASE + 42)) [domain-service]=$((BASE + 43))
  [suppression]=$((BASE + 46)) [templates]=$((BASE + 47)) [transactional]=$((BASE + 45)) [contacts]=$((BASE + 50)) [campaigns]=$((BASE + 52)) [automations]=$((BASE + 51)) [analytics]=$((BASE + 53)) [reputation]=$((BASE + 54)) [billing]=$((BASE + 55))
  [gateway]=$((BASE + 80))
)
MAPS_PORT=$((BASE + 81))
AUTH_TLS_PORT=$((BASE + 82))
EXPORT_PORT=$((BASE + 91))
GW="http://127.0.0.1:${PORT[gateway]}/api/v1"
export IDENTITY_PORT=${PORT[identity]} ACCESS_CONTROL_PORT=${PORT[access-control]} ORGANIZATION_PORT=${PORT[organization]}
export MAIL_DIRECTORY_PORT=${PORT[mail-directory]} MAIL_SECURITY_PORT=${PORT[mail-security]} DOMAIN_SERVICE_PORT=${PORT[domain-service]}
export SUPPRESSION_PORT=${PORT[suppression]} TEMPLATES_PORT=${PORT[templates]} GATEWAY_PORT=${PORT[gateway]}
export BILLING_PORT=${PORT[billing]} REPUTATION_PORT=${PORT[reputation]} CONTACTS_PORT=${PORT[contacts]} ANALYTICS_PORT=${PORT[analytics]}
export TRANSACTIONAL_PORT=${PORT[transactional]} CAMPAIGNS_PORT=${PORT[campaigns]} AUTOMATIONS_PORT=${PORT[automations]}
export MAIL_POLICY_MAPS_PORT=$MAPS_PORT MAIL_POLICY_EXPORT_PORT=$EXPORT_PORT
export MAIL_AUTH_PORT=${PORT[mail-auth]} MAIL_AUTH_TLS_PORT=$AUTH_TLS_PORT
export API_ORIGIN="http://localhost:${PORT[gateway]}" PUBLIC_BASE_URL="http://localhost:${PORT[gateway]}"
# Direcciones internas: las que el gateway lee de routes.json por <SERVICIO>_HOST(_PORT) y
# las que los servicios usan entre si.
for s in identity access-control organization mail-directory mail-security domain-service suppression templates billing reputation contacts analytics transactional campaigns automations; do
  var="$(echo "$s" | tr 'a-z-' 'A-Z_')_HOST"
  export "$var=127.0.0.1" "${var}_PORT=${PORT[$s]}"
done
export WEB_HOST=127.0.0.1 WEB_HOST_PORT=1
export ACCESS_CONTROL_URL="http://127.0.0.1:${PORT[access-control]}"
export MAIL_DIRECTORY_URL="http://127.0.0.1:${PORT[mail-directory]}" MAIL_SECURITY_URL="http://127.0.0.1:${PORT[mail-security]}"
export SUPPRESSION_URL="http://127.0.0.1:${PORT[suppression]}" TEMPLATES_URL="http://127.0.0.1:${PORT[templates]}"
export BILLING_URL="http://127.0.0.1:${PORT[billing]}" REPUTATION_URL="http://127.0.0.1:${PORT[reputation]}"
# Umbrales y limites de reputation: los mismos que documenta .env.example. Billing en modo
# de produccion (una empresa sin suscripcion no tiene derechos).
while IFS= read -r linea; do export "$linea"; done < <(grep -E '^REPUTATION_(WINDOW|MIN_VOLUME|BOUNCE|COMPLAINT|DEFAULT)' .env.example)
export BILLING_ENFORCE=true
export TRANSACTIONAL_URL="http://127.0.0.1:${PORT[transactional]}" CONTACTS_URL="http://127.0.0.1:${PORT[contacts]}"
# SES sin credenciales: la prueba nunca llega a enviar (el remitente no esta verificado), y
# el cliente de AWS solo pide credenciales al enviar.
export SES_REGION=us-east-1 SES_CONFIG_SET_TRANSACTIONAL=cfm-transactional SES_CONFIG_SET_MARKETING=cfm-marketing
export PLATFORM_FROM_EMAIL=no-reply@platform.test PLATFORM_FROM_NAME="Core Force Mail" PLATFORM_FROM_ALLOW_UNVERIFIED=false
export CAMPAIGNS_TICK=1s
# El anuncio de caducidades de suppression cada segundo: la prueba de una exclusion manual que
# caduca espera segundos, no el minuto por defecto.
export SUPPRESSION_EXPIRY_SWEEP_INTERVAL=1s

# Los servicios de la celda arrancan con SU credencial y sin la de plataforma: un permiso que
# le falte al rol de la celda hace fallar las comprobaciones del correo de mas abajo.
SERVICIOS_DE_CELDA=" mail-directory mail-auth mail-security "

echo "== Plano de control"
arrancar organization
esperar_salud organization "${PORT[organization]}" || exit 1

echo "== Arranque de una plataforma vacia (ops/db/bootstrap-platform.sh)"
e2e_plataforma pe-01

ARRANQUE=(identity access-control mail-directory mail-auth domain-service mail-security templates suppression billing reputation contacts analytics transactional campaigns automations gateway)
for s in "${ARRANQUE[@]}"; do arrancar "$s"; done
for s in "${ARRANQUE[@]}"; do esperar_salud "$s" "${PORT[$s]}"; done

echo "== Credencial de la celda en uso"
expect "los servicios de la celda conectan con su rol" \
  "$(sql mail_registry "SELECT count(*) > 0 FROM pg_stat_activity WHERE usename = '$CELL_ROLE' AND datname = 'mail_cell_pe_01'")" "t"
# Con la de plataforma a mano pero sin la de la celda, en produccion no arranca.
env -u CELL_DB_PASSWORD ENVIRONMENT=production MAIL_DIRECTORY_PORT=$((BASE + 96)) \
  timeout 20 "$WORK/bin/mail-directory" >"$WORK/fail-closed.log" 2>&1
rc=$?
if [[ $rc -ne 0 && $rc -ne 124 ]] && grep -q 'CELL_DB_PASSWORD is required' "$WORK/fail-closed.log"; then
  ok "en produccion, sin credencial de celda mail-directory no arranca"
else
  mal "mail-directory en produccion sin credencial de celda (salida $rc): $(head -c 300 "$WORK/fail-closed.log")"
fi

echo "== Identidad y acceso"
L1=$(e2e_login "$ADMIN_EMAIL" "$ADMIN_PASS")
T1=$(echo "$L1" | jget data.access_token)
[[ -n "$T1" ]] && ok "login del superadmin" || { mal "login del superadmin: ${L1:0:200}"; exit 1; }
A1="Authorization: Bearer $T1"
contains "el superadmin lista celdas" "$(curl -s "$GW/cells" -H "$A1")" '"code":"pe-01"'

echo "== Firma del token de acceso"
cabecera() {
  python3 -c 'import sys, json, base64; h = sys.argv[1].split(".")[0]; print(json.dumps(json.loads(base64.urlsafe_b64decode(h + "=" * (-len(h) % 4))), sort_keys=True))' "$1" 2>/dev/null
}
expect "identity firma con EdDSA y el kid vigente" "$(cabecera "$T1")" "{\"alg\": \"EdDSA\", \"kid\": \"$JWT_SIGNING_KID\", \"typ\": \"at+jwt\"}"
# forjar <alg>: los mismos claims del superadmin sin la clave de identity. HS256 usa como
# secreto la clave PUBLICA, que conoce cualquiera: la confusion de algoritmo clasica.
forjar() {
  python3 - "$1" "$T1" <<'PY'
import base64, hashlib, hmac, json, os, sys
alg, real = sys.argv[1], sys.argv[2]
b64 = lambda b: base64.urlsafe_b64encode(b).rstrip(b"=").decode()
kid, publica = os.environ["JWT_PUBLIC_KEYS"].split(":", 1)
cuerpo = b64(json.dumps({"alg": alg, "kid": kid, "typ": "at+jwt"}).encode()) + "." + real.split(".")[1]
firma = "" if alg == "none" else b64(hmac.new(base64.b64decode(publica), cuerpo.encode(), hashlib.sha256).digest())
print(cuerpo + "." + firma)
PY
}
for alg in HS256 none; do
  expect "un token $alg con los claims del superadmin se rechaza" \
    "$(curl -s -o /dev/null -w '%{http_code}' "$GW/cells" -H "Authorization: Bearer $(forjar "$alg")")" "401"
done
# Sin su material de claves, identity (en produccion) y el gateway no arrancan.
falla_cerrado() {
  local nombre="$1" mensaje="$2" log="$WORK/fail-closed-$1.log"; shift 2
  env "$@" timeout 20 "$WORK/bin/$nombre" >"$log" 2>&1
  local rc=$?
  if [[ $rc -ne 0 && $rc -ne 124 ]] && grep -q "$mensaje" "$log"; then
    ok "sin claves $nombre no arranca"
  else
    mal "$nombre sin claves (salida $rc): $(head -c 300 "$log")"
  fi
}
falla_cerrado identity 'JWT_SIGNING_KEY is required' -u JWT_SIGNING_KEY -u JWT_SIGNING_KID ENVIRONMENT=production IDENTITY_PORT=$((BASE + 97))
falla_cerrado gateway 'JWT_PUBLIC_KEYS is required' -u JWT_PUBLIC_KEYS GATEWAY_PORT=$((BASE + 98))
# Fuera de desarrollo, el Redis de la plataforma en claro impide el arranque.
env -u REDIS_TLS -u REDIS_TLS_CA_FILE -u REDIS_TLS_SERVER_NAME ENVIRONMENT=production GATEWAY_PORT=$((BASE + 95)) \
  timeout 20 "$WORK/bin/gateway" >"$WORK/fail-closed-redis.log" 2>&1
rc=$?
if [[ $rc -ne 0 && $rc -ne 124 ]] && grep -q 'REDIS_TLS=true is required' "$WORK/fail-closed-redis.log"; then
  ok "en produccion, sin TLS hacia Redis el gateway no arranca"
else
  mal "gateway en produccion sin REDIS_TLS (salida $rc): $(head -c 300 "$WORK/fail-closed-redis.log")"
fi
# Solo development o test relajan algo (config.DeclaredDevelopmentOrTest): en staging o sin
# ENVIRONMENT, sin token interno ni el gateway ni domain-service arrancan.
sin_token_falla_cerrado() {
  local nombre="$1" entorno="$2" log="$WORK/fail-closed-token-$1.log"; shift 2
  env -u INTERNAL_GATEWAY_TOKEN "$@" GATEWAY_PORT=$((BASE + 94)) DOMAIN_SERVICE_PORT=$((BASE + 94)) \
    timeout 20 "$WORK/bin/$nombre" >"$log" 2>&1
  local rc=$?
  if [[ $rc -ne 0 && $rc -ne 124 ]] && grep -q 'INTERNAL_GATEWAY_TOKEN is required' "$log"; then
    ok "$entorno, sin token interno $nombre no arranca"
  else
    mal "$nombre $entorno sin INTERNAL_GATEWAY_TOKEN (salida $rc): $(head -c 300 "$log")"
  fi
}
for s in gateway domain-service; do
  sin_token_falla_cerrado "$s" "en staging" ENVIRONMENT=staging
  sin_token_falla_cerrado "$s" "sin ENVIRONMENT" -u ENVIRONMENT
done

TENANT_PASS="$(rand_hex 12)Aa1!"
ORG=$(e2e_alta_empresa "$T1" acme pe-01 admin@acme.test "$TENANT_PASS")
expect "alta de empresa en su celda" "$(echo "$ORG" | jget data.slug)" "acme"
expect "base de la empresa creada y migrada" "$(sql mail_registry "SELECT count(*) FROM pg_database WHERE datname = 'mail_tenant_acme'")" "1"
contains "organization la cierra: el rol de la celda no la abre" "$(como_celda mail_tenant_acme)" "permission denied for database"
expect "la saga de acme queda completada" \
  "$(sql mail_registry "SELECT s.state || '/' || s.step FROM organization.tenant_sagas s JOIN organization.tenants t ON t.id = s.tenant_id WHERE t.slug = 'acme'")" "completed/activated"
# Un alta que falla a mitad (identity rechaza al primer administrador) se deshace entera.
REJ=$(e2e_alta_empresa "$T1" rechazada pe-01 admin@rechazada.test solominusculas-sin-mas)
expect "identity rechaza la contrasena del primer administrador" "$(echo "$REJ" | jget error.code)" "PASSWORD_POLICY"
expect "la saga deshace su base" "$(sql mail_registry "SELECT count(*) FROM pg_database WHERE datname = 'mail_tenant_rechazada'")" "0"
expect "y no deja la empresa" "$(sql mail_registry "SELECT count(*) FROM organization.tenants WHERE slug = 'rechazada'")" "0"

L2=$(e2e_login admin@acme.test "$TENANT_PASS")
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
# El verificador que usa Dovecot (passwd-verify.lua) contra la contrasena que guardo
# mail-directory: es la costura entre el directorio y los motores.
dovecot_auth() {
  curl -sk -o /dev/null -w '%{http_code}' -X POST "https://127.0.0.1:$AUTH_TLS_PORT/" -H 'Content-Type: application/json' \
    -d "{\"username\":\"$1\",\"password\":\"$2\",\"real_rip\":\"10.20.0.5\",\"service\":\"$3\"}"
}
expect "Dovecot autentica el buzon por IMAP (mail-auth)" "$(dovecot_auth ana@acme.test "$MBX_PASS" imap)" "200"
expect "una contrasena incorrecta se rechaza" "$(dovecot_auth ana@acme.test "no-es-$MBX_PASS" imap)" "401"
MBID=$(echo "$MB" | jget data.id)
AP=$(curl -s -X POST "$GW/mailboxes/$MBID/app-passwords" -H "$A2" -H 'Content-Type: application/json' \
  -d '{"name":"movil","imap_access":false,"smtp_access":true}')
APP_PASS=$(echo "$AP" | jget data.password)
[[ -n "$APP_PASS" ]] && ok "contrasena de aplicacion generada y mostrada una vez" || mal "contrasena de aplicacion: ${AP:0:200}"
expect "la contrasena de aplicacion entra por SMTP" "$(dovecot_auth ana@acme.test "$APP_PASS" smtp)" "200"
expect "y no por IMAP, que no tiene concedido" "$(dovecot_auth ana@acme.test "$APP_PASS" imap)" "401"
expect "los inicios quedan en mail.sasl_logins" \
  "$(sql mail_cell_pe_01 "SELECT count(*) FROM mail.sasl_logins WHERE username = 'ana@acme.test'")" "2"
expect "otra empresa no ve el buzon (RLS)" "$(curl -s "$GW/mailboxes" -H "$A1" | jget data)" "[]"

dm=""
for _ in $(seq 1 20); do dm=$(docker exec "$E2E_PREFIX-redis" redis-cli HGET DOMAIN_MAP acme.test); [[ -n "$dm" ]] && break; sleep 0.5; done
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

echo "== Planes y derechos (billing)"
limites=""
for r in users domains mailboxes storage_bytes contacts transactional_messages marketing_messages; do
  limites+="${limites:+,}{\"resource\":\"$r\",\"included\":1000,\"hard_limit\":true,\"overage_unit_price\":null}"
done
plan() { echo "{\"code\":\"$1\",\"name\":\"Base\",\"description\":\"Plan de la prueba\",\"currency\":\"USD\",\"base_price\":\"49.00\",\"billing_period\":\"monthly\",\"limits\":[$limites]}"; }
codigo() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
expect "el superadmin crea un plan" \
  "$(codigo -X POST "$GW/billing/plans" -H "$A1" -H 'Content-Type: application/json' -d "$(plan e2e-base)")" "201"
expect "el tenant_admin no crea planes" \
  "$(codigo -X POST "$GW/billing/plans" -H "$A2" -H 'Content-Type: application/json' -d "$(plan e2e-otro)")" "403"
expect "el superadmin asigna el plan a la empresa" \
  "$(codigo -X PUT "$GW/billing/subscriptions/$TID" -H "$A1" -H 'Content-Type: application/json' -d '{"plan_code":"e2e-base"}')" "201"
expect "la empresa ve su plan" "$(curl -s "$GW/billing/subscription" -H "$A2" | jget data.plan_code)" "e2e-base"
interno() {
  curl -s -X POST "http://127.0.0.1:$1" -H "X-Gateway-Token: $INTERNAL_GATEWAY_TOKEN" -H "X-Tenant-ID: $TID" \
    -H 'Content-Type: application/json' -d "$2"
}
expect "billing concede un buzon mas dentro del plan" \
  "$(interno "${PORT[billing]}/internal/billing/entitlements/check" '{"resource":"mailboxes","quantity":1}' | jget data.allowed)" "True"
expect "y lo niega por encima del limite duro" \
  "$(interno "${PORT[billing]}/internal/billing/entitlements/check" '{"resource":"mailboxes","quantity":5000}' | jget data.allowed)" "False"

echo "== Autorizacion de envio (reputation)"
expect "reputation autoriza un envio transaccional con el derecho de billing" \
  "$(interno "${PORT[reputation]}/internal/reputation/authorize" '{"class":"transactional","count":1}' | jget data.allowed)" "True"
expect "el superadmin fija un limite por hora de marketing" \
  "$(codigo -X PUT "$GW/reputation/tenants/$TID/limits/marketing" -H "$A1" -H 'Content-Type: application/json' -d '{"hourly":3,"daily":null}')" "200"
expect "el tenant_admin no toca los limites de su propia reputacion" \
  "$(codigo -X PUT "$GW/reputation/tenants/$TID/limits/marketing" -H "$A2" -H 'Content-Type: application/json' -d '{"hourly":9000,"daily":null}')" "403"
expect "un lote dentro del limite pasa" \
  "$(interno "${PORT[reputation]}/internal/reputation/authorize" '{"class":"marketing","count":2}' | jget data.allowed)" "True"
R=$(interno "${PORT[reputation]}/internal/reputation/authorize" '{"class":"marketing","count":2}')
expect "el siguiente lote choca con el limite por hora" "$(echo "$R" | jget data.reason)" "rate_limited"
[[ "$(echo "$R" | jget data.retry_after_seconds)" =~ ^[1-9][0-9]*$ ]] && ok "y trae el tiempo de espera" || mal "sin retry_after_seconds: ${R:0:200}"

echo "== Alcance de permisos"
contains "el superadmin ve los permisos de plataforma" "$(curl -s "$GW/permissions" -H "$A1")" '"scope":"platform"'
CAT=$(curl -s "$GW/permissions" -H "$A2")
contains "la empresa ve su catalogo" "$CAT" '"scope":"tenant"'
[[ "$CAT" != *'"scope":"platform"'* ]] && ok "sin los permisos de plataforma" || mal "el tenant_admin ve permisos de plataforma"
RID=$(curl -s -X POST "$GW/roles" -H "$A2" -H 'Content-Type: application/json' -d '{"name":"finanzas","description":"Consulta de consumo"}' | jget data.id)
[[ -n "$RID" ]] && ok "la empresa crea un rol propio" || mal "alta de rol"
P_PLAT=$(sql mail_registry "SELECT id FROM access_control.permissions WHERE module = 'billing' AND resource = 'plans' AND action = 'create'")
P_EMP=$(sql mail_registry "SELECT id FROM access_control.permissions WHERE module = 'billing' AND resource = 'usage' AND action = 'read'")
expect "un rol de empresa no recibe permisos de plataforma" \
  "$(codigo -X PUT "$GW/roles/$RID/permissions" -H "$A2" -H 'Content-Type: application/json' -d "{\"permission_ids\":[\"$P_EMP\",\"$P_PLAT\"]}")" "403"
expect "y si los de empresa" \
  "$(codigo -X PUT "$GW/roles/$RID/permissions" -H "$A2" -H 'Content-Type: application/json' -d "{\"permission_ids\":[\"$P_EMP\"]}")" "200"
expect "el tenant_admin sembrado no tiene permisos de plataforma" \
  "$(sql mail_registry "SELECT count(*) FROM access_control.role_permissions rp JOIN access_control.permissions p ON p.id = rp.permission_id JOIN access_control.roles r ON r.id = rp.role_id WHERE r.tenant_id = '$TID' AND p.scope = 'platform'")" "0"

echo "== Contactos y audiencia (contacts)"
CT=$(curl -s -X POST "$GW/contacts" -H "$A2" -H 'Content-Type: application/json' \
  -d '{"email":"lucia@cliente.test","first_name":"Lucia","source":"api","consent":{"status":"granted","method":"api","source":"e2e"}}')
CTID=$(echo "$CT" | jget data.id)
[[ -n "$CTID" ]] && ok "alta de contacto con consentimiento" || mal "alta de contacto: ${CT:0:200}"
expect "la misma direccion en otra caja no crea otro contacto" \
  "$(codigo -X POST "$GW/contacts" -H "$A2" -H 'Content-Type: application/json' -d '{"email":"LUCIA@cliente.test","source":"api"}')" "409"
LID=$(curl -s -X POST "$GW/contacts/lists" -H "$A2" -H 'Content-Type: application/json' -d '{"name":"clientes"}' | jget data.id)
[[ -n "$LID" ]] && ok "alta de lista" || mal "alta de lista"
expect "el contacto entra en la lista" \
  "$(curl -s -X POST "$GW/contacts/lists/$LID/members" -H "$A2" -H 'Content-Type: application/json' -d "{\"contact_ids\":[\"$CTID\"]}" | jget data.added)" "1"
audiencia() { interno "${PORT[contacts]}/internal/contacts/audience" "{\"list_ids\":[\"$LID\"],\"limit\":100}"; }
contains "la audiencia de la lista trae al contacto enviable" "$(audiencia)" '"lucia@cliente.test"'
expect "previsualizacion de segmento (gateada como lectura)" \
  "$(curl -s -X POST "$GW/segments/preview" -H "$A2" -H 'Content-Type: application/json' -d '{"definition":{"match":"all","rules":[{"field":"status","op":"eq","value":"active"}]}}' | jget data.count)" "1"
expect "revocar el consentimiento queda como evidencia" \
  "$(codigo -X POST "$GW/contacts/$CTID/consent" -H "$A2" -H 'Content-Type: application/json' -d '{"status":"revoked","method":"api","source":"e2e"}')" "201"
[[ "$(audiencia)" != *lucia@cliente.test* ]] && ok "sin consentimiento sale de la audiencia" || mal "la audiencia conserva un contacto sin consentimiento"
expect "y no se vuelve a conceder sin doble opt-in" \
  "$(codigo -X POST "$GW/contacts/$CTID/consent" -H "$A2" -H 'Content-Type: application/json' -d '{"status":"granted","method":"api","source":"e2e"}')" "409"
# baja@otro.test tiene la exclusion manual de "Supresion"; la baja se registra por el endpoint
# interno de suppression, como la registraria el enlace de baja.
expect "un contacto nuevo con la direccion ya excluida entra excluded (el alta consulta suppression)" \
  "$(curl -s -X POST "$GW/contacts" -H "$A2" -H 'Content-Type: application/json' \
    -d '{"email":"baja@otro.test","source":"api","consent":{"status":"granted","method":"api","source":"e2e"}}' | jget data.status)" "excluded"
expect "suppression registra una baja" \
  "$(interno "${PORT[suppression]}/internal/suppression/add" '{"email":"se-fue@cliente.test","reason":"unsubscribe","source":"e2e"}' | jget data.added)" "True"
expect "el alta con consentimiento sin prueba sobre esa baja se rechaza" \
  "$(codigo -X POST "$GW/contacts" -H "$A2" -H 'Content-Type: application/json' -d '{"email":"se-fue@cliente.test","source":"api","consent":{"status":"granted","method":"api","source":"e2e"}}')" "409"
IMP=$(curl -s -X POST "$GW/contacts/imports" -H "$A2" -H 'Content-Type: application/json' \
  -d '{"rows":[{"email":"se-fue@cliente.test"},{"email":"nueva@cliente.test"}],"consent":{"status":"granted","basis":"e2e"}}')
expect "la importacion cuenta la baja entre los creados ya excluidos" "$(echo "$IMP" | jget data.suppressed.unsubscribed)" "1"
SEFUE=$(curl -s "$GW/contacts?search=se-fue" -H "$A2")
expect "y la deja unsubscribed aunque declare consentimiento" "$(echo "$SEFUE" | jget data.0.status)" "unsubscribed"
[[ "$(echo "$SEFUE" | jget data.0.consent_status)" != granted ]] && ok "sin el consentimiento de la importacion" || mal "la importacion concedio el consentimiento a una baja"
# Una exclusion manual que caduca: suppression anuncia la caducidad (suppression.entry.expired)
# y contacts devuelve el contacto a active con su consentimiento, sin barrido propio.
CADUCA=$(date -u -d '+8 seconds' +%Y-%m-%dT%H:%M:%SZ)
expect "exclusion manual con caducidad" "$(codigo -X POST "$GW/suppression/entries" -H "$A2" -H 'Content-Type: application/json' \
  -d "{\"email\":\"temporal@cliente.test\",\"reason\":\"manual\",\"expires_at\":\"$CADUCA\"}")" "201"
expect "el contacto de esa direccion entra excluded" \
  "$(curl -s -X POST "$GW/contacts" -H "$A2" -H 'Content-Type: application/json' \
    -d '{"email":"temporal@cliente.test","source":"api","consent":{"status":"granted","method":"api","source":"e2e"}}' | jget data.status)" "excluded"
estado=""
for _ in $(seq 1 60); do
  estado=$(curl -s "$GW/contacts?search=temporal" -H "$A2" | jget data.0.status)
  [[ "$estado" == active ]] && break
  sleep 0.5
done
expect "al caducar, el anuncio de suppression lo devuelve a active" "$estado" "active"
expect "con el consentimiento que tenia" "$(curl -s "$GW/contacts?search=temporal" -H "$A2" | jget data.0.consent_status)" "granted"

echo "== Campanas (campaigns -> contacts -> transactional)"
CT2=$(curl -s -X POST "$GW/contacts" -H "$A2" -H 'Content-Type: application/json' \
  -d '{"email":"marta@cliente.test","first_name":"Marta","source":"api","consent":{"status":"granted","method":"api","source":"e2e"}}' | jget data.id)
expect "un contacto enviable entra en la lista" \
  "$(curl -s -X POST "$GW/contacts/lists/$LID/members" -H "$A2" -H 'Content-Type: application/json' -d "{\"contact_ids\":[\"$CT2\"]}" | jget data.added)" "1"
TMID=$(curl -s -X POST "$GW/templates" -H "$A2" -H 'Content-Type: application/json' \
  -d '{"name":"novedades","kind":"marketing","subject":"Novedades","html":"<p>Novedades de Acme</p><p><a href=\"{{.unsubscribe_url}}\">Darse de baja</a></p>","variables":[]}' | jget data.id)
expect "plantilla de marketing publicada" "$(codigo -X POST "$GW/templates/$TMID/versions/1/publish" -H "$A2")" "200"
CP=$(curl -s -X POST "$GW/campaigns" -H "$A2" -H 'Content-Type: application/json' \
  -d "{\"name\":\"Lanzamiento\",\"template_id\":\"$TMID\",\"from_email\":\"hola@acme.test\",\"from_name\":\"Acme\",\"audience\":{\"list_ids\":[\"$LID\"]}}")
CPID=$(echo "$CP" | jget data.id)
expect "campana en borrador" "$(echo "$CP" | jget data.status)" "draft"
expect "el envio de prueba respeta el remitente sin verificar" \
  "$(curl -s -X POST "$GW/campaigns/$CPID/test" -H "$A2" -H 'Content-Type: application/json' -d '{"emails":["qa@cliente.test"],"template_version":1}' | jget error.code)" \
  "SENDING_DOMAIN_NOT_VERIFIED"
c=$(codigo -X POST "$GW/campaigns/$CPID/start" -H "$A2" -H 'Content-Type: application/json' -d '{"template_version":1}')
[[ "$c" =~ ^20[02]$ ]] && ok "la campana arranca" || mal "arranque de campana: $c"
estado=""
for _ in $(seq 1 40); do
  estado=$(curl -s "$GW/campaigns/$CPID" -H "$A2" | jget data.status)
  [[ "$estado" == failed || "$estado" == completed ]] && break; sleep 0.5
done
expect "el orquestador recorre la audiencia y transactional rechaza el lote" "$estado" "failed"
contains "con el motivo de transactional" "$(curl -s "$GW/campaigns/$CPID" -H "$A2" | jget data.failure_reason)" "SENDING_DOMAIN_NOT_VERIFIED"
expect "ningun mensaje de marketing llego a encolarse" \
  "$(sql mail_tenant_acme "SELECT count(*) FROM transactional.messages WHERE class = 'marketing'")" "0"

echo "== Doble opt-in (contacts -> automations -> transactional)"
TD=$(curl -s -X POST "$GW/templates" -H "$A2" -H 'Content-Type: application/json' \
  -d '{"name":"confirmacion","kind":"transactional","subject":"Confirma tu suscripcion","html":"<p><a href=\"{{.confirm_url}}\">Confirmar</a></p>","variables":[{"name":"confirm_url","type":"url","required":true}]}' | jget data.id)
expect "plantilla de confirmacion publicada" "$(codigo -X POST "$GW/templates/$TD/versions/1/publish" -H "$A2")" "200"
expect "ajustes del doble opt-in (la plantilla se comprueba al guardar)" \
  "$(codigo -X PUT "$GW/automations/double-opt-in" -H "$A2" -H 'Content-Type: application/json' -d "{\"enabled\":true,\"template_id\":\"$TD\",\"from_email\":\"hola@acme.test\",\"from_name\":\"Acme\"}")" "200"
CT3=$(curl -s -X POST "$GW/contacts" -H "$A2" -H 'Content-Type: application/json' -d '{"email":"pedro@cliente.test","first_name":"Pedro","source":"api"}' | jget data.id)
expect "la empresa pide el doble opt-in" \
  "$(codigo -X POST "$GW/contacts/$CT3/consent/request" -H "$A2" -H 'Content-Type: application/json' -d '{"source":"e2e"}')" "202"
estado=""; ENT=""
for _ in $(seq 1 40); do
  ENT=$(curl -s "$GW/automations/double-opt-in/deliveries" -H "$A2")
  estado=$(echo "$ENT" | jget data.0.status)
  [[ "$estado" == failed || "$estado" == sent ]] && break; sleep 0.5
done
expect "automations intenta el correo y transactional rechaza el remitente sin verificar" "$estado" "failed"
contains "con el motivo de transactional" "$(echo "$ENT" | jget data.0.reason)" "SENDING_DOMAIN_NOT_VERIFIED"
[[ "$ENT" != *confirm* ]] && ok "el historial no expone el enlace de confirmacion" || mal "el historial expone el enlace de confirmacion"

echo "== Analitica"
expect "el panel responde sin envios" "$(curl -s "$GW/analytics/overview" -H "$A2" | jget data.totals.sent)" "0"
expect "un rango invertido se rechaza" "$(codigo "$GW/analytics/overview?from=2026-09-10&to=2026-09-01" -H "$A2")" "422"

echo "== Cuentas borradas o desactivadas"
# El access token sigue firmado y vigente 5 minutos. El gateway lo rechaza en cuanto la cuenta
# deja de existir o de estar activa, tambien en las rutas de autoservicio que no gatea ningun
# modulo. Los tokens no se usan antes de la baja: la cache del gateway (60 s) retrasaria el
# rechazo.
cuenta_de_prueba() {
  local email="$1@acme.test" pass id
  pass="$(rand_hex 10)Aa1!"
  id=$(curl -s -X POST "$GW/users" -H "$A2" -H 'Content-Type: application/json' \
    -d "{\"email\":\"$email\",\"password\":\"$pass\",\"first_name\":\"Luis\",\"last_name\":\"Rios\"}" | jget data.id)
  echo "$id $(e2e_login "$email" "$pass" | jget data.access_token)"
}
# sesion <curl args>: "<codigo de error> <estado HTTP>".
sesion() { local r; r=$(curl -s -w ' %{http_code}' "$@"); echo "$(echo "${r% *}" | jget error.code) ${r##* }"; }
read -r U_VIGENTE T_VIGENTE <<<"$(cuenta_de_prueba vigente)"
read -r U_BORRADA T_BORRADA <<<"$(cuenta_de_prueba borrada)"
read -r U_INACTIVA T_INACTIVA <<<"$(cuenta_de_prueba inactiva)"
[[ -n "$T_VIGENTE" && -n "$T_BORRADA" && -n "$T_INACTIVA" ]] && ok "tres cuentas con sesion" || mal "alta o login de las cuentas de prueba"
expect "una cuenta vigente sin roles abre su ficha (autoservicio, sin modulo)" \
  "$(codigo "$GW/users/$U_VIGENTE" -H "Authorization: Bearer $T_VIGENTE")" "200"
expect "el tenant_admin borra una cuenta" "$(codigo -X DELETE "$GW/users/$U_BORRADA" -H "$A2")" "204"
expect "su token, aun vigente, ya no abre ni su ficha" \
  "$(sesion "$GW/users/$U_BORRADA" -H "Authorization: Bearer $T_BORRADA")" "SESSION_REVOKED 401"
expect "el tenant_admin desactiva otra" "$(codigo -X POST "$GW/users/$U_INACTIVA/deactivate" -H "$A2")" "200"
expect "su token ya no lista sus sesiones" \
  "$(sesion "$GW/sessions/mine" -H "Authorization: Bearer $T_INACTIVA")" "SESSION_REVOKED 401"
expect "ni pide un step-up" \
  "$(sesion -X POST "$GW/auth/step-up" -H "Authorization: Bearer $T_INACTIVA" -H 'Content-Type: application/json' -d '{"current_password":"x"}')" "SESSION_REVOKED 401"

echo "== Registros"
e2e_registros_sin_errores

echo
if [[ $fallos -gt 0 ]]; then echo "E2E: $fallos fallos" >&2; exit 1; fi
echo "E2E: OK"

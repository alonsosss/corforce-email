#!/usr/bin/env bash
# Prueba de punta a punta de la plataforma con binarios reales contra Postgres, NATS y
# Redis desechables. Recorre lo que ya esta integrado: arranque de una plataforma vacia,
# alta de celda y empresa, acceso por el gateway con sus tres capas, dominio y buzon en
# la celda, propagacion a Redis por eventos, mapas de los motores, plantillas, supresion,
# planes y derechos de billing, autorizacion de envio en reputation, alcance de permisos,
# contactos con su consentimiento y audiencia, campanas por la via de marketing de
# transactional (hasta su rechazo por remitente sin verificar, sin SES), el doble opt-in por
# automations, el panel de analitica, las claves de API en la API de envio y en el relay SMTP (STARTTLS y TLS
# implicito con AUTH, hasta transactional) y el rechazo del token de una cuenta borrada o desactivada. Los servicios de la celda corren con la credencial
# propia de la celda (ops/db/cell-service-role.sh), sin la de plataforma. domain-service verifica
# contra un DNS de la prueba (ops/e2e/dns_prueba.py), reclama cada dominio en el indice global de
# organization y lo activa en la celda de su empresa; el webmail de cada celda recibe a sus buzones
# por el gateway de las celdas.
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
    docker rm -f "$E2E_PREFIX-pg" "$E2E_PREFIX-nats" "$E2E_PREFIX-redis" "$E2E_PREFIX-redis-pe02" >/dev/null 2>&1
    rm -rf "$WORK"
  else
    echo "E2E_KEEP=1: contenedores $E2E_PREFIX-* y registros en $WORK/log"
  fi
  e2e_liberar
}
trap limpiar EXIT

echo "== Infraestructura desechable"
e2e_infra_up || exit 1

SERVICES=(organization identity access-control gateway mail-directory mail-auth domain-service mail-security templates suppression billing reputation contacts scheduler analytics transactional campaigns automations webmail observability smtp-relay)
echo "== Compilacion (${SERVICES[*]})"
e2e_compilar "${SERVICES[@]}" || exit 1

echo "== Bases de las celdas pe-01 y pe-02"
e2e_celda pe-01
e2e_celda pe-02

echo "== Credencial propia de cada celda (ops/db/cell-service-role.sh)"
CELL_ROLE=mail_cell_pe_01_svc
CELL_PASS="$(rand_hex 24)"
e2e_credencial_celda pe-01 "$CELL_PASS"
CELL2_PASS="$(rand_hex 24)"
e2e_credencial_celda pe-02 "$CELL2_PASS"
# Por el socket del contenedor: lo que se prueba es el privilegio CONNECT, no la contrasena
# (la prueban los servicios de la celda, que entran por TCP).
como_celda() { psql -U "$CELL_ROLE" -d "$1" -At -c 'SELECT current_user' 2>&1; }
expect "el rol de la celda entra en su base" "$(como_celda mail_cell_pe_01)" "$CELL_ROLE"
contains "pero no en el registro" "$(como_celda mail_registry)" "permission denied for database"
contains "ni en la base de otra celda" "$(como_celda mail_cell_pe_02)" "permission denied for database"

echo "== Credencial de los motores de cada celda (ops/db/cell-engine-role.sh)"
MAIL_PASS="$(rand_hex 24)"
MAIL2_PASS="$(rand_hex 24)"
e2e_credencial_motores pe-01 "$MAIL_PASS"
e2e_credencial_motores pe-02 "$MAIL2_PASS"
# Segunda fase: sin retirar el rol de login compartido, cada rol de celda HEREDA de
# mail_engine el CONNECT a las bases de las demas celdas (la membresia no distingue permisos
# de tabla de permisos de base). El aislamiento entre celdas se comprueba despues de esto.
PGHOST=127.0.0.1 MAIL_DB_PASSWORD="${MAIL_PASS}" \
  bash ops/db/cell-engine-role.sh --cell pe-01 --retire-shared >/dev/null ||
  mal "cell-engine-role.sh --retire-shared"
expect "el rol de motores compartido queda sin inicio de sesion" \
  "$(sql mail_registry "SELECT rolcanlogin FROM pg_roles WHERE rolname = 'mail_engine'")" "f"
como_motor() { psql -U "mail_cell_${1//-/_}_engine" -d "$2" -At -c "$3" 2>&1; }
expect "el motor de pe-01 lee el directorio de su celda" \
  "$(como_motor pe-01 mail_cell_pe_01 'SELECT count(*) FROM mail.mailboxes')" "0"
contains "y no las contrasenas de aplicacion" \
  "$(como_motor pe-01 mail_cell_pe_01 'SELECT 1 FROM mail.app_passwords LIMIT 1')" "permission denied"
contains "ni la base de la otra celda" \
  "$(como_motor pe-01 mail_cell_pe_02 'SELECT 1')" "permission denied for database"

# ── Entorno comun ────────────────────────────────────────────────────────────
e2e_entorno_comun || exit 1
export MAIL_REDIS_HOST=127.0.0.1 MAIL_REDIS_PORT="$REDIS_PORT"
# Repaso de claves DKIM de mail-security corto: la prueba lo ve retirar una clave huerfana.
export MAIL_DKIM_RECONCILE_INTERVAL=2s
export DEFAULT_CELL_CODE=pe-01 CELL_CODE=pe-01 CELL_DB_NAME=mail_cell_pe_01
export MAIL_HOSTNAME=mail.cfm.test MAIL_MX_HOSTNAME=mail.cfm.test MAIL_SPF_INCLUDE=include:spf.cfm.test MAIL_DMARC_RUA=dmarc@cfm.test
export CORS_ALLOWED_ORIGINS=http://localhost:3000
# Cupo estricto de autenticacion del gateway para la prueba, que inicia sesion muchas veces: el
# de produccion (30 por minuto e IP) la dejaba al borde del 429. Por debajo del limite propio de
# identity (60 por minuto e IP), para que la rafaga de "Limite de inicios de sesion" choque con
# el gateway y no con identity.
export AUTH_RATE_LIMIT_PER_MIN=50

declare -A PORT=(
  [identity]=$((BASE + 1)) [access-control]=$((BASE + 2)) [organization]=$((BASE + 3))
  [mail-directory]=$((BASE + 40)) [mail-auth]=$((BASE + 41)) [mail-security]=$((BASE + 42)) [domain-service]=$((BASE + 43)) [webmail]=$((BASE + 44))
  [suppression]=$((BASE + 46)) [templates]=$((BASE + 47)) [transactional]=$((BASE + 45)) [contacts]=$((BASE + 50)) [campaigns]=$((BASE + 52)) [automations]=$((BASE + 51)) [analytics]=$((BASE + 53)) [reputation]=$((BASE + 54)) [billing]=$((BASE + 55)) [scheduler]=$((BASE + 33))
  [gateway]=$((BASE + 80)) [observability]=$((BASE + 59)) [smtp-relay]=$((BASE + 71))
)
MAPS_PORT=$((BASE + 81))
AUTH_TLS_PORT=$((BASE + 82))
EXPORT_PORT=$((BASE + 91))
DNS_PORT=$((BASE + 65))
GW="http://127.0.0.1:${PORT[gateway]}/api/v1"
export IDENTITY_PORT=${PORT[identity]} ACCESS_CONTROL_PORT=${PORT[access-control]} ORGANIZATION_PORT=${PORT[organization]}
export MAIL_DIRECTORY_PORT=${PORT[mail-directory]} MAIL_SECURITY_PORT=${PORT[mail-security]} DOMAIN_SERVICE_PORT=${PORT[domain-service]}
export SUPPRESSION_PORT=${PORT[suppression]} TEMPLATES_PORT=${PORT[templates]} GATEWAY_PORT=${PORT[gateway]}
export BILLING_PORT=${PORT[billing]} REPUTATION_PORT=${PORT[reputation]} CONTACTS_PORT=${PORT[contacts]} ANALYTICS_PORT=${PORT[analytics]}
export TRANSACTIONAL_PORT=${PORT[transactional]} CAMPAIGNS_PORT=${PORT[campaigns]} AUTOMATIONS_PORT=${PORT[automations]} SCHEDULER_PORT=${PORT[scheduler]}
export OBSERVABILITY_PORT=${PORT[observability]}
export MAIL_POLICY_MAPS_PORT=$MAPS_PORT MAIL_POLICY_EXPORT_PORT=$EXPORT_PORT
export MAIL_AUTH_PORT=${PORT[mail-auth]} MAIL_AUTH_TLS_PORT=$AUTH_TLS_PORT
# domain-service verifica contra el DNS de la prueba: la zona es $WORK/zona.json, que la prueba
# escribe con los registros que pide domain-service. Sin zona, todo nombre es NXDOMAIN.
export MAIL_DNS_RESOLVER="127.0.0.1:$DNS_PORT"
# domain-service publica el DNS de los dominios en automatico en un Cloudflare falso
# (ops/e2e/cloudflare_prueba.py) que escribe lo publicado en esa misma zona. Los tokens se generan
# en cada ejecucion: el bueno ve nube.test y sesenta zonas de relleno (varias paginas), el
# deshabilitado y el que no puede leer zonas no sirven, y uno cualquiera no existe.
CF_PORT=$((BASE + 70))
CF_TOKEN="cfe2e_$(rand_hex 20)"
CF_TOKEN_DESHABILITADO="cfe2e_$(rand_hex 20)"
CF_TOKEN_SIN_PERMISO="cfe2e_$(rand_hex 20)"
CF_TOKEN_DESCONOCIDO="cfe2e_$(rand_hex 20)"
export CLOUDFLARE_API_URL="http://127.0.0.1:$CF_PORT"
export API_ORIGIN="http://localhost:${PORT[gateway]}" PUBLIC_BASE_URL="http://localhost:${PORT[gateway]}"
# Direcciones internas: las que el gateway lee de routes.json por <SERVICIO>_HOST(_PORT) y
# las que los servicios usan entre si.
for s in identity access-control organization mail-directory mail-security domain-service suppression templates billing reputation contacts scheduler analytics transactional campaigns automations webmail observability smtp-relay; do
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
# analytics cierra en el scheduler la ejecucion de la poda que este le despacha.
export SCHEDULER_URL="http://127.0.0.1:${PORT[scheduler]}"
# SES sin credenciales: la prueba nunca llega a enviar (el remitente no esta verificado), y
# el cliente de AWS solo pide credenciales al enviar.
export SES_REGION=us-east-1 SES_CONFIG_SET_TRANSACTIONAL=cfm-transactional SES_CONFIG_SET_MARKETING=cfm-marketing
# La prueba nunca habla con AWS: sin las credenciales del anfitrion (su ~/.aws, el rol de EC2) y con SES apuntado a un
# puerto cerrado, un mensaje que llegue a la cola (el del relay SMTP) falla como transitorio sin salir de la maquina.
unset AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN AWS_PROFILE
export AWS_SHARED_CREDENTIALS_FILE=/dev/null AWS_CONFIG_FILE=/dev/null AWS_EC2_METADATA_DISABLED=true
export AWS_ENDPOINT_URL_SESV2="http://127.0.0.1:$((BASE + 74))"
export PLATFORM_FROM_EMAIL=no-reply@platform.test PLATFORM_FROM_NAME="Core Force Mail" PLATFORM_FROM_ALLOW_UNVERIFIED=false
export CAMPAIGNS_TICK=1s
# El anuncio de caducidades de suppression cada segundo: la prueba de una exclusion manual que
# caduca espera segundos, no el minuto por defecto.
export SUPPRESSION_EXPIRY_SWEEP_INTERVAL=1s
# Formularios publicos: el tiempo minimo de rellenado a un segundo (la prueba espera, no rellena)
# y un cupo por IP pequeno para ver el 429 sin cientos de envios.
export CONTACTS_FORM_MIN_FILL=1s CONTACTS_FORM_SUBMITS_PER_IP=5

# Los servicios de la celda arrancan con SU credencial y sin la de plataforma: un permiso que
# le falte al rol de la celda hace fallar las comprobaciones del correo de mas abajo.
SERVICIOS_DE_CELDA=" mail-directory mail-auth mail-security "
# Los del plano de empresa abren el registro con el rol de enrutado, que solo puede leer
# organization.v_tenant_routing: si alguno necesitara otra cosa del registro (una tabla, la
# outbox), fallaria aqui y no en produccion. billing no entra, porque su almacen ES el
# registro y sigue con la credencial de plataforma.
SERVICIOS_DE_EMPRESA=" domain-service templates suppression reputation contacts analytics transactional campaigns automations "

cp ops/e2e/dns_prueba.py "$WORK/bin/" && python3 "$WORK/bin/dns_prueba.py" "$DNS_PORT" "$WORK/zona.json" >"$WORK/log/dns.log" 2>&1 &
# El cliente ya tiene en nube.test un SPF de otro proveedor y un TXT de verificacion ajeno: la
# publicacion no pisa el primero sin confirmacion ni toca el segundo.
cat >"$WORK/cloudflare.json" <<JSON
{"filler_zones": 60,
 "tokens": {"$CF_TOKEN": {"status": "active", "zones": ["nube.test"]},
            "$CF_TOKEN_DESHABILITADO": {"status": "disabled", "zones": ["nube.test"]},
            "$CF_TOKEN_SIN_PERMISO": {"status": "active", "forbidden": true}},
 "seed": [{"zone": "nube.test", "type": "TXT", "name": "nube.test", "content": "v=spf1 include:_spf.otro-proveedor.test ~all"},
          {"zone": "nube.test", "type": "TXT", "name": "nube.test", "content": "otro-proveedor-verification=e2e"}]}
JSON
cp ops/e2e/cloudflare_prueba.py "$WORK/bin/" &&
  python3 "$WORK/bin/cloudflare_prueba.py" "$CF_PORT" "$WORK/cloudflare.json" "$WORK/zona.json" >"$WORK/log/cloudflare.log" 2>&1 &

echo "== Plano de control"
# organization da de baja el correo de una empresa en el mail-directory de su celda: pe-01 en el
# destino base y pe-02 en la instancia que arranca mas abajo (MD2_PORT). Los demas servicios del
# host siguen sin celdas.
GATEWAY_BASE_CELL_CODE=pe-01 MAIL_DIRECTORY_CELL_HOSTS="pe-02=127.0.0.1:$((BASE + 60))" ORGANIZATION_SAGA_SWEEP_INTERVAL=1s arrancar organization
esperar_salud organization "${PORT[organization]}" || exit 1

echo "== Arranque de una plataforma vacia (ops/db/bootstrap-platform.sh)"
e2e_plataforma pe-01

# La base de la empresa de plataforma la crea y la migra organization, no el alta: el barrido de
# migraciones solo migra bases que ya existen, y sin ella los servicios que recorren las empresas
# activas la reintentaban en bucle. Se espera antes de arrancar el resto, para que ninguno la vea vacia.
plataforma_lista() { [[ "$(sql mail_tenant_platform "SELECT count(*) > 0 FROM public.schema_migrations" 2>/dev/null)" == t ]]; }
for _ in $(seq 1 60); do plataforma_lista && break; sleep 1; done
plataforma_lista && ok "organization crea y migra la base de la empresa de plataforma" || mal "organization crea y migra la base de la empresa de plataforma"
PLATFORM_ID="$(sql mail_registry "SELECT id FROM organization.tenants WHERE slug = 'platform'")"
expect "con la marca de su empresa, como las altas por API" \
  "$(sql mail_registry "SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname = 'mail_tenant_platform'")" \
  "core-force-mail tenant $PLATFORM_ID"
expect "cerrada a PUBLIC" \
  "$(sql mail_registry "SELECT has_database_privilege('public', 'mail_tenant_platform', 'CONNECT')")" "f"
expect "migrada por el runner y no por baseline" \
  "$(sql mail_tenant_platform "SELECT count(*) FROM public.schema_migrations WHERE name LIKE '%baseline%'")" "0"
# Se repite el alta con la MISMA contrasena (e2e_plataforma genera una nueva en cada llamada): es
# idempotente y no cambia la de un superadmin que ya existe; el login de abajo lo prueba.
PGHOST=127.0.0.1 PLATFORM_ADMIN_EMAIL="$ADMIN_EMAIL" PLATFORM_ADMIN_PASSWORD="$ADMIN_PASS" \
  bash ops/db/bootstrap-platform.sh --cell pe-01 --region sa-east-1 >/dev/null || mal "bootstrap-platform.sh repetido"

echo "== Credencial de enrutado de los servicios de empresa (ops/db/tenant-service-role.sh)"
# organization ya aplico las migraciones del registro al arrancar, asi que existen la vista
# publicada del enrutado y su rol de grupo.
ROUTER_PASS="$(rand_hex 24)"
e2e_credencial_enrutado "$ROUTER_PASS"
como_router() { psql -U mail_router -d "$1" -At -c "$2" 2>&1; }
expect "el rol de enrutado lee la vista publicada" \
  "$(como_router mail_registry 'SELECT count(*) > 0 FROM organization.v_tenant_routing')" "t"
contains "pero no la tabla de empresas" \
  "$(como_router mail_registry 'SELECT 1 FROM organization.tenants LIMIT 1')" "permission denied"
contains "ni las cuentas de identity" \
  "$(como_router mail_registry 'SELECT 1 FROM identity.users LIMIT 1')" "permission denied"

ARRANQUE=(identity access-control mail-directory mail-auth domain-service mail-security templates suppression billing reputation contacts scheduler analytics transactional campaigns automations observability gateway)
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

echo "== Limite de inicios de sesion por IP"
# La prueba sube el cupo estricto (AUTH_RATE_LIMIT_PER_MIN): una rafaga por encima tiene que
# seguir chocando con el, o la holgura esconderia un limitador roto. Sale desde una IP de
# documentacion por X-Real-IP, que el gateway acepta de loopback como de su proxy de borde
# (TRUSTED_PROXY_CIDRS por defecto): no gasta el cupo de 127.0.0.1 del resto de pasos, y el
# limite propio de identity, que cuenta por la IP que le pasa el gateway, la ve nueva. Cada
# intento lleva su propio correo: desde el quinto fallo del mismo, identity responde el bloqueo
# (403), tambien a un correo sin cuenta, y eso no es el limitador.
# rafaga <correo> <curl args>
rafaga() {
  local email="$1"
  shift
  curl -s "$@" -X POST "$GW/auth/login" -H 'X-Real-IP: 198.51.100.23' -H 'Content-Type: application/json' \
    -d "{\"email\":\"$email\",\"password\":\"no-es-la-contrasena\"}"
}
pasan=0
for i in $(seq 1 "$AUTH_RATE_LIMIT_PER_MIN"); do
  [[ "$(rafaga "nadie$i@rafaga.test" -o /dev/null -w '%{http_code}')" == 401 ]] && pasan=$((pasan + 1))
done
expect "los $AUTH_RATE_LIMIT_PER_MIN inicios del cupo llegan a identity (401)" "$pasan" "$AUTH_RATE_LIMIT_PER_MIN"
EXCESO=$(rafaga nadie@rafaga.test -o /dev/null -D -)
contains "el siguiente recibe 429 del gateway" "$EXCESO" " 429"
contains "con Retry-After" "$EXCESO" "Retry-After:"

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
# ENVIRONMENT, sin token interno no arrancan ni el gateway, ni domain-service ni organization.
sin_token_falla_cerrado() {
  local nombre="$1" entorno="$2" log="$WORK/fail-closed-token-$1.log"; shift 2
  env -u INTERNAL_GATEWAY_TOKEN "$@" GATEWAY_PORT=$((BASE + 94)) DOMAIN_SERVICE_PORT=$((BASE + 94)) ORGANIZATION_PORT=$((BASE + 94)) \
    timeout 20 "$WORK/bin/$nombre" >"$log" 2>&1
  local rc=$?
  if [[ $rc -ne 0 && $rc -ne 124 ]] && grep -q 'INTERNAL_GATEWAY_TOKEN is required' "$log"; then
    ok "$entorno, sin token interno $nombre no arranca"
  else
    mal "$nombre $entorno sin INTERNAL_GATEWAY_TOKEN (salida $rc): $(head -c 300 "$log")"
  fi
}
for s in gateway domain-service organization; do
  sin_token_falla_cerrado "$s" "en staging" ENVIRONMENT=staging
  sin_token_falla_cerrado "$s" "sin ENVIRONMENT" -u ENVIRONMENT
done

TENANT_PASS="$(rand_hex 12)Aa1!"
ORG=$(e2e_alta_empresa "$T1" acme pe-01 admin@acme.test "$TENANT_PASS")
expect "alta de empresa en su celda" "$(echo "$ORG" | jget data.slug)" "acme"
expect "base de la empresa creada y migrada" "$(sql mail_registry "SELECT count(*) FROM pg_database WHERE datname = 'mail_tenant_acme'")" "1"
contains "organization la cierra: el rol de la celda no la abre" "$(como_celda mail_tenant_acme)" "permission denied for database"

echo "== Credencial propia de un servicio de empresa (ops/db/tenant-service-role.sh)"
# Ya hay una base de empresa migrada, asi que existen los roles de grupo <esquema>_service.
CONTACTS_PASS="$(rand_hex 24)"
CONTACTS_DB_PASSWORD="${CONTACTS_PASS}" PGHOST=127.0.0.1 \
  bash ops/db/tenant-service-role.sh --service contacts >/dev/null 2>&1 ||
  mal "tenant-service-role.sh --service contacts"
como_contacts() { psql -U mail_svc_contacts -d "$1" -At -c "$2" 2>&1; }
expect "el rol de contacts lee su esquema en la base de la empresa" \
  "$(como_contacts mail_tenant_acme 'SELECT count(*) FROM contacts.contacts')" "0"
contains "pero no el esquema de otro servicio de la MISMA base" \
  "$(como_contacts mail_tenant_acme 'SELECT 1 FROM templates.templates LIMIT 1')" "permission denied"
contains "ni el registro" "$(como_contacts mail_registry 'SELECT 1')" "permission denied for database"
contains "ni crea tablas" \
  "$(como_contacts mail_tenant_acme 'CREATE TABLE contacts.prohibida (id int)')" "permission denied"
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

echo "== Visor de registros (observability, solo superadmin; sin Loki en esta prueba)"
# Loki no corre aqui y LOKI_URL queda vacia: el servicio arranca igual y el visor responde 503
# NOT_CONFIGURED (deploy-safe). La lista blanca y los permisos si se comprueban de punta a punta.
registros() { # registros <cabecera> <ruta>: deja REG_CODE y REG_BODY
  REG_CODE=$(curl -s -o "$WORK/registros.json" -w '%{http_code}' "$GW/observability/logs$2" -H "$1")
  REG_BODY=$(cat "$WORK/registros.json")
}
registros "$A1" "/services"
expect "el superadmin recibe la lista blanca de servicios" "$REG_CODE" "200"
contains "con los motores de correo" "$REG_BODY" '"postfix-mail"'
contains "y los servicios Go" "$REG_BODY" '"gateway"'
lacks "sin la base de datos, que no esta en la lista" "$REG_BODY" '"postgres-primary"'
registros "$A1" "?service=gateway&q=hola"
expect "sin LOKI_URL el visor responde 503 NOT_CONFIGURED" "$REG_CODE/$(echo "$REG_BODY" | jget error.code)" "503/NOT_CONFIGURED"
registros "$A1" "?service=postgres-primary"
expect "un servicio fuera de la lista blanca se rechaza antes de mirar Loki" "$REG_CODE" "422"
registros "$A1" "?service=gateway&since=ayer"
expect "y una fecha que no es RFC 3339 tambien" "$REG_CODE" "400"
registros "$A2" "/services"
expect "un administrador de empresa no ve la lista" "$REG_CODE" "403"
registros "$A2" "?service=gateway"
expect "ni consulta registros" "$REG_CODE" "403"
expect "sin sesion tampoco" "$(curl -s -o /dev/null -w '%{http_code}' "$GW/observability/logs/services")" "401"

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

# codigo <curl args>: solo el estado HTTP.
codigo() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

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
# El alta desde el editor (galeria o en blanco) crea la plantilla con su primer diseno.
TE=$(curl -s -X POST "$GW/templates" -H "$A2" -H 'Content-Type: application/json' \
  -d '{"name":"desde-el-editor","kind":"transactional","subject":"Hola","html":"<p>Hola</p>","variables":[],"editor":{"kind":"grapesjs-mjml","project":{"pages":[]},"mjml":"<mjml><mj-body></mj-body></mjml>"}}' | jget data.id)
expect "alta con el primer diseno del editor, sin paso intermedio" \
  "$(curl -s "$GW/templates/$TE/versions/1" -H "$A2" | jget data.editor.kind)" "grapesjs-mjml"

echo "== Envio de prueba de una plantilla y correo en el navegador (templates -> transactional)"
PRUEBA='{"from":{"email":"hola@acme.test","name":"Acme"},"to":["qa@cliente.test"],"variables":{}}'
expect "la prueba de una version respeta el remitente sin verificar (rechazo de transactional)" \
  "$(curl -s -X POST "$GW/templates/$TPID/versions/1/test-send" -H "$A2" -H 'Content-Type: application/json' -d "$PRUEBA" | jget error.code)" \
  "SENDING_DOMAIN_NOT_VERIFIED"
expect "mas de cinco destinatarios se rechazan antes de llamar a transactional" \
  "$(codigo -X POST "$GW/templates/$TPID/versions/1/test-send" -H "$A2" -H 'Content-Type: application/json' \
    -d '{"from":{"email":"hola@acme.test"},"to":["a@c.test","b@c.test","d@c.test","e@c.test","f@c.test","g@c.test"]}')" "422"
expect "ninguna prueba llego a encolarse" "$(sql mail_tenant_acme "SELECT count(*) FROM transactional.messages WHERE is_test")" "0"
# El enlace {{.view_in_browser_url}} lo firma transactional al renderizar; sin SES la prueba no
# llega a enviar, asi que se siembra un mensaje ya renderizado y el enlace se firma aqui con la
# clave de la ejecucion y la forma de domain.LinkSigner.SignView ("view", empresa, mensaje,
# caducidad en segundos Unix). El HTML lleva un script: el gateway no debe marcarlo con el nonce.
enlace_ver() {
  python3 - "$@" <<'PY'
import hashlib, hmac, os, sys, urllib.parse
empresa, mensaje, caducidad = sys.argv[1:]
firma = hmac.new(os.environ["MAIL_LINK_SIGNING_KEY"].encode(), "\n".join(("view", empresa, mensaje, caducidad)).encode(), hashlib.sha256).hexdigest()
print("/public/transactional/view?" + urllib.parse.urlencode({"t": empresa, "m": mensaje, "x": caducidad, "sig": firma}))
PY
}
ACME_TENANT=$(sql mail_registry "SELECT id FROM organization.tenants WHERE slug = 'acme'")
VMID=$(cat /proc/sys/kernel/random/uuid)
sql mail_tenant_acme "INSERT INTO transactional.messages (id, tenant_id, from_email, \"to\", subject, html, template_id, template_version, status)
  VALUES ('$VMID', '$ACME_TENANT', 'hola@acme.test', '[{\"email\":\"lucia@cliente.test\"}]', 'Hola', '<p>Hola desde el navegador</p><script>alert(1)</script>', '$TPID', 1, 'sent')"
VER=$(enlace_ver "$ACME_TENANT" "$VMID" "$(( $(date +%s) + 3600 ))")
VER_CAB=$(curl -s -D - -o "$WORK/ver.html" "$GW$VER")
contains "el correo se ve en el navegador tal como se guardo" "$(cat "$WORK/ver.html")" "Hola desde el navegador"
lacks "el gateway no marca los scripts del correo con el nonce" "$(cat "$WORK/ver.html")" "nonce="
contains "con la politica del servicio, que no ejecuta nada" "$VER_CAB" "default-src 'none'"
contains "que el buscador no indexa" "$VER_CAB" "X-Robots-Tag: noindex"
expect "un enlace alterado no abre el correo" "$(codigo "$GW${VER/sig=/sig=0}")" "403"
expect "un enlace caducado responde 410" "$(codigo "$GW$(enlace_ver "$ACME_TENANT" "$VMID" "$(( $(date +%s) - 60 ))")")" "410"
expect "un mensaje que no existe responde 404" "$(codigo "$GW$(enlace_ver "$ACME_TENANT" "$(cat /proc/sys/kernel/random/uuid)" "$(( $(date +%s) + 3600 ))")")" "404"

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

echo "== Claves de API y relay SMTP (access-control -> gateway / smtp-relay -> transactional)"
# La empresa crea sus claves en access-control por el gateway; el secreto solo sale en el alta. Con una clave
# se llama a la API de envio (lista cerrada de rutas del gateway) y se autentica en smtp-relay, que entrega
# el MIME a transactional. Sin SES real: un remitente sin verificar se rechaza (550) y uno de un dominio de
# envio sembrado en la proyeccion se encola con su MIME. Sin clamd en esta prueba: un adjunto recibe 451.
clave() {
  curl -s -X POST "$GW/access/api-keys/" -H "$A2" -H 'Content-Type: application/json' \
    -d "{\"name\":\"$1\",\"scopes\":[$2]}"
}
ENVIO='{"module":"transactional","resource":"messages","action":"create"}'
LECTURA='{"module":"transactional","resource":"messages","action":"read"}'
K1=$(clave "Tienda en linea" "$ENVIO,$LECTURA")
K1_SECRET=$(echo "$K1" | jget data.secret)
K1_PREFIX=$(echo "$K1" | jget data.key.prefix)
K1_ID=$(echo "$K1" | jget data.key.id)
[[ "$K1_SECRET" == cfm_"$K1_PREFIX"_* ]] && ok "alta de una clave de API: el secreto sale una vez" || mal "alta de clave: ${K1:0:300}"
K2_SECRET=$(clave "Solo lectura" "$LECTURA" | jget data.secret)
lacks "la lista nunca lleva el secreto" "$(curl -s "$GW/access/api-keys/" -H "$A2")" "$K1_SECRET"
expect "en la base solo queda su hash" \
  "$(sql mail_registry "SELECT count(*) FROM access_control.api_keys WHERE id = '$K1_ID' AND position(convert_to('${K1_SECRET##*_}', 'UTF8') in secret_hash) = 0")" "1"
expect "un permiso fuera del catalogo de claves se rechaza" \
  "$(clave x '{"module":"access","resource":"roles","action":"create"}' | jget error.code)" "SCOPE_NOT_GRANTABLE"
expect "el alta queda en la outbox para audit" \
  "$(sql mail_registry "SELECT count(*) FROM platform.event_outbox WHERE subject = 'access.api_key.created' AND payload->'data'->>'api_key_id' = '$K1_ID'")" "1"

K="Authorization: Bearer $K1_SECRET"
MSG='{"from":{"email":"hola@acme.test"},"to":[{"email":"qa@cliente.test"}],"subject":"Pedido","text":"Gracias"}'
expect "con la clave el gateway deja enviar y transactional aplica sus reglas (remitente sin verificar)" \
  "$(curl -s -X POST "$GW/transactional/messages" -H "$K" -H 'Content-Type: application/json' -d "$MSG" | jget error.code)" \
  "SENDING_DOMAIN_NOT_VERIFIED"
expect "la clave no abre rutas fuera de la lista cerrada" \
  "$(curl -s "$GW/transactional/stats" -H "$K" | jget error.code)" "API_KEY_ROUTE_NOT_ALLOWED"
expect "ni gestiona claves" "$(codigo "$GW/access/api-keys/" -H "$K")" "401"
expect "lee el estado de un mensaje" \
  "$(codigo "$GW/transactional/messages/$(cat /proc/sys/kernel/random/uuid)" -H "$K")" "404"
expect "una clave solo de lectura no envia" \
  "$(codigo -X POST "$GW/transactional/messages" -H "Authorization: Bearer $K2_SECRET" -H 'Content-Type: application/json' -d "$MSG")" "403"
expect "una clave alterada no autentica" \
  "$(curl -s -X POST "$GW/transactional/messages" -H "Authorization: Bearer ${K1_SECRET}x" -H 'Content-Type: application/json' -d "$MSG" | jget error.code)" \
  "API_KEY_INVALID"

SMTP_CERT="$WORK/smtp-relay"
mkdir -p "$SMTP_CERT"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 1 -subj /CN=smtp.cfm.test \
  -addext subjectAltName=DNS:smtp.cfm.test -keyout "$SMTP_CERT/key.pem" -out "$SMTP_CERT/cert.pem" >/dev/null 2>&1 ||
  mal "certificado de la prueba para smtp-relay"
SMTP_STARTTLS=$((BASE + 72))
SMTP_TLS=$((BASE + 73))
env -u JWT_SIGNING_KEY SMTP_RELAY_PORT="${PORT[smtp-relay]}" SMTP_RELAY_STARTTLS_PORT="$SMTP_STARTTLS" SMTP_RELAY_TLS_PORT="$SMTP_TLS" \
  SMTP_RELAY_HOSTNAME=smtp.cfm.test SMTP_RELAY_TLS_CERT_FILE="$SMTP_CERT/cert.pem" SMTP_RELAY_TLS_KEY_FILE="$SMTP_CERT/key.pem" \
  SMTP_RELAY_CLAMD_ADDR= "$WORK/bin/smtp-relay" >"$WORK/log/smtp-relay.log" 2>&1 &
esperar_salud smtp-relay "${PORT[smtp-relay]}"
relay() { python3 ops/e2e/smtp_relay_client.py "$1" 127.0.0.1 "$2" smtp.cfm.test "$SMTP_CERT/cert.pem" "${@:3}"; }

CLARO=$(relay claro "$SMTP_STARTTLS" "$K1_PREFIX" "$K1_SECRET")
contains "en claro el relay no anuncia AUTH" "$CLARO" "AUTH-ANUNCIADO no"
contains "ni la admite antes de STARTTLS" "$CLARO" "RECHAZO auth 523"
contains "ni acepta MAIL sin AUTH" "$CLARO" "RECHAZO mail 530"
expect "una clave solo de lectura no es credencial SMTP" \
  "$(relay enviar "$SMTP_STARTTLS" "$(echo "${K2_SECRET#cfm_}" | cut -d_ -f1)" "$K2_SECRET" hola@acme.test qa@cliente.test | cut -d' ' -f1-3)" \
  "RECHAZO auth 535"
expect "STARTTLS, AUTH PLAIN y un remitente sin verificar: 550 de transactional" \
  "$(relay enviar "$SMTP_STARTTLS" "$K1_PREFIX" "$K1_SECRET" hola@acme.test qa@cliente.test | cut -d' ' -f1-3)" "RECHAZO data 550"

# Un dominio de envio verificado en la proyeccion de transactional (lo alimenta domain-service; aqui se siembra).
sql mail_tenant_acme "INSERT INTO transactional.sending_domains (tenant_id, domain, status, purpose)
  VALUES ('$ACME_TENANT', 'envios.acme.test', 'verified', 'sending') ON CONFLICT DO NOTHING"
expect "TLS implicito, AUTH LOGIN y remitente verificado: 250" \
  "$(relay enviar "$SMTP_TLS" "$K1_PREFIX" "$K1_SECRET" pedidos@envios.acme.test qa@cliente.test,baja@otro.test --implicito --login | cut -d' ' -f1-2)" \
  "OK 250"
SMTP_MSG="transactional.messages WHERE origin = 'smtp' AND api_key_id = '$K1_ID'"
expect "el mensaje llega a transactional con su clave y su MIME" \
  "$(sql mail_tenant_acme "SELECT count(*) FROM $SMTP_MSG AND EXISTS (SELECT 1 FROM transactional.raw_contents r WHERE r.message_id = messages.id)")" "1"
expect "sin el destinatario suprimido (la supresion antes de encolar)" \
  "$(sql mail_tenant_acme "SELECT \"to\"::text FROM $SMTP_MSG")" '[{"email": "qa@cliente.test"}]'
expect "por el carril transaccional" "$(sql mail_tenant_acme "SELECT class || '/' || is_test FROM $SMTP_MSG")" "transactional/false"
# Sin SES real el envio no sale; se retira de la cola para que sus reintentos no acaben en un fallo definitivo.
sql mail_tenant_acme "UPDATE transactional.messages SET status = 'failed', error = 'e2e: sin SES'
  WHERE origin = 'smtp' AND api_key_id = '$K1_ID' AND status = 'queued'" >/dev/null
expect "un adjunto sin clamd no sale: 451" \
  "$(relay enviar "$SMTP_STARTTLS" "$K1_PREFIX" "$K1_SECRET" pedidos@envios.acme.test qa@cliente.test --adjunto-eicar | cut -d' ' -f1-3)" \
  "RECHAZO data 451"

expect "revocar la clave" "$(codigo -X POST "$GW/access/api-keys/$K1_ID/revoke" -H "$A2")" "200"
expect "la revocacion es inmediata en el gateway" \
  "$(curl -s -X POST "$GW/transactional/messages" -H "$K" -H 'Content-Type: application/json' -d "$MSG" | jget error.code)" "API_KEY_INVALID"
expect "y en el relay" \
  "$(relay enviar "$SMTP_STARTTLS" "$K1_PREFIX" "$K1_SECRET" pedidos@envios.acme.test qa@cliente.test | cut -d' ' -f1-3)" "RECHAZO auth 535"
expect "la revocacion queda en la outbox para audit" \
  "$(sql mail_registry "SELECT count(*) FROM platform.event_outbox WHERE subject = 'access.api_key.revoked' AND payload->'data'->>'api_key_id' = '$K1_ID'")" "1"

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
# El verificador de entregabilidad no deja publicar marketing sin la direccion fisica del remitente
# (docs/Plan_Editor_Correos.md, 3.5): se publica con el kit de marca y el pie legal en el cuerpo.
TSIN=$(curl -s -X POST "$GW/templates" -H "$A2" -H 'Content-Type: application/json' \
  -d '{"name":"sin-direccion","kind":"marketing","subject":"Novedades","html":"<p>Novedades de Acme</p><p><a href=\"{{.unsubscribe_url}}\">Darse de baja</a></p>","variables":[]}' | jget data.id)
expect "marketing sin direccion fisica no se publica" \
  "$(curl -s -X POST "$GW/templates/$TSIN/versions/1/publish" -H "$A2" | jget error.code)" "DELIVERABILITY_FAILED"
expect "kit de marca con la direccion del remitente" "$(codigo -X PUT "$GW/templates/brand-kit" -H "$A2" -H 'Content-Type: application/json' \
  -d '{"colors":["#0B5FFF"],"fonts":["Arial"],"footer":{"company":"Acme SAC","address":"Av. Principal 123, Lima, Peru"}}')" "200"
TMID=$(curl -s -X POST "$GW/templates" -H "$A2" -H 'Content-Type: application/json' \
  -d '{"name":"novedades","kind":"marketing","subject":"Novedades","html":"<p>Novedades de Acme</p><p>Acme SAC, Av. Principal 123, Lima, Peru</p><p><a href=\"{{.unsubscribe_url}}\">Darse de baja</a></p>","variables":[]}' | jget data.id)
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
# El objeto utm del lote (docs/Plan_Marketing_Avanzado.md, 1-B): se valida antes que el
# remitente, y uno valido deja el lote en el mismo rechazo por remitente sin verificar.
lote_utm() {
  interno "${PORT[transactional]}/internal/transactional/batch" "{\"class\":\"marketing\",\"campaign_id\":\"$CPID\",\"idempotency_key\":\"e2e-utm-$1\",\"from\":{\"email\":\"hola@acme.test\",\"name\":\"Acme\"},\"template_id\":\"$TMID\",\"template_version\":1,\"recipients\":[{\"email\":\"marta@cliente.test\",\"contact_id\":\"$CT2\"}],\"utm\":$2}"
}
contains "transactional rechaza un utm sin caracteres validos" "$(lote_utm invalido '{"source":"!!!"}' | jget error.message)" "utm.source"
expect "y acepta el objeto utm del lote hasta el remitente sin verificar" \
  "$(lote_utm valido '{"enabled":true,"source":"Boletin","campaign":"Lanzamiento de Otono","content":"cabecera"}' | jget error.code)" \
  "SENDING_DOMAIN_NOT_VERIFIED"

echo "== Campanas: prueba A/B, reenvio y envio por zona horaria"
# Veinte contactos: con una muestra del 50 % la probabilidad de que ninguno caiga en ella es
# de uno entre un millon, asi que la primera muestra siempre llega a transactional.
LAB=$(curl -s -X POST "$GW/contacts/lists" -H "$A2" -H 'Content-Type: application/json' -d '{"name":"prueba-ab"}' | jget data.id)
IDS=""
for i in $(seq -w 1 20); do
  id=$(curl -s -X POST "$GW/contacts" -H "$A2" -H 'Content-Type: application/json' \
    -d "{\"email\":\"ab$i@cliente.test\",\"source\":\"api\",\"timezone\":\"Europe/Madrid\",\"consent\":{\"status\":\"granted\",\"method\":\"api\",\"source\":\"e2e\"}}" | jget data.id)
  IDS="$IDS${IDS:+,}\"$id\""
done
expect "veinte contactos en la lista de la prueba" \
  "$(curl -s -X POST "$GW/contacts/lists/$LAB/members" -H "$A2" -H 'Content-Type: application/json' -d "{\"contact_ids\":[$IDS]}" | jget data.added)" "20"
expect "la meta publica los topes de la prueba A/B" \
  "$(curl -s "$GW/campaigns/meta" -H "$A2" | jget data.ab_test.max_variants)" "4"
expect "cinco variantes se rechazan" \
  "$(codigo -X POST "$GW/campaigns" -H "$A2" -H 'Content-Type: application/json' \
    -d "{\"name\":\"AB invalida\",\"template_id\":\"$TMID\",\"from_email\":\"hola@acme.test\",\"audience\":{\"list_ids\":[\"$LID\"]},\"ab_test\":{\"criterion\":\"opens\",\"sample_percent\":20,\"decision_window_minutes\":60,\"variants\":[{\"subject\":\"a\"},{\"subject\":\"b\"},{\"subject\":\"c\"},{\"subject\":\"d\"},{\"subject\":\"e\"}]}}")" "422"
AB=$(curl -s -X POST "$GW/campaigns" -H "$A2" -H 'Content-Type: application/json' \
  -d "{\"name\":\"Prueba AB\",\"template_id\":\"$TMID\",\"from_email\":\"hola@acme.test\",\"audience\":{\"list_ids\":[\"$LAB\"]},\"ab_test\":{\"criterion\":\"clicks\",\"sample_percent\":50,\"decision_window_minutes\":60,\"variants\":[{\"subject\":\"Asunto A\"},{\"subject\":\"Asunto B\"}]},\"resend\":{\"subject\":\"Recordatorio\",\"delay_minutes\":1440}}")
ABID=$(echo "$AB" | jget data.id)
expect "campana A/B con reenvio en borrador" "$(echo "$AB" | jget data.ab_test.variants.1.label)" "B"
expect "con su reenvio" "$(echo "$AB" | jget data.resend.subject)" "Recordatorio"
DIA=$(date -u -d '+3 days' +%Y-%m-%d)
expect "la prueba A/B no se programa por zona horaria" \
  "$(codigo -X POST "$GW/campaigns/$ABID/schedule" -H "$A2" -H 'Content-Type: application/json' \
    -d "{\"local_send_at\":\"${DIA}T09:00\",\"fallback_timezone\":\"America/Lima\",\"template_version\":1}")" "422"
c=$(codigo -X POST "$GW/campaigns/$ABID/start" -H "$A2" -H 'Content-Type: application/json' -d '{"template_version":1}')
[[ "$c" =~ ^20[02]$ ]] && ok "la campana A/B arranca y fija la version de cada variante" || mal "arranque de la campana A/B: $c"
estado=""
for _ in $(seq 1 40); do
  estado=$(curl -s "$GW/campaigns/$ABID" -H "$A2" | jget data.status)
  [[ "$estado" == failed || "$estado" == completed ]] && break; sleep 0.5
done
expect "la muestra llega a transactional, que rechaza el remitente" "$estado" "failed"
contains "con el motivo de transactional" "$(curl -s "$GW/campaigns/$ABID" -H "$A2" | jget data.failure_reason)" "SENDING_DOMAIN_NOT_VERIFIED"
PLAN=$(curl -s "$GW/campaigns/$ABID/phases" -H "$A2")
expect "el plan tiene la muestra de A como primera fase" "$(echo "$PLAN" | jget data.phases.0.kind)" "sample"
expect "seguida de la muestra de B" "$(echo "$PLAN" | jget data.phases.1.variant)" "1"
expect "y de la ganadora" "$(echo "$PLAN" | jget data.phases.2.kind)" "winner"
expect "la ganadora no se decide sin la muestra" "$(curl -s "$GW/campaigns/$ABID" -H "$A2" | jget data.ab_winner)" ""
TZ=$(curl -s -X POST "$GW/campaigns" -H "$A2" -H 'Content-Type: application/json' \
  -d "{\"name\":\"Por zona\",\"template_id\":\"$TMID\",\"from_email\":\"hola@acme.test\",\"audience\":{\"list_ids\":[\"$LID\"]}}")
TZID=$(echo "$TZ" | jget data.id)
expect "una zona de respaldo que no existe se rechaza" \
  "$(codigo -X POST "$GW/campaigns/$TZID/schedule" -H "$A2" -H 'Content-Type: application/json' \
    -d "{\"local_send_at\":\"${DIA}T09:00\",\"fallback_timezone\":\"Marte/Olimpo\",\"template_version\":1}")" "422"
PROG=$(curl -s -X POST "$GW/campaigns/$TZID/schedule" -H "$A2" -H 'Content-Type: application/json' \
  -d "{\"local_send_at\":\"${DIA}T09:00\",\"fallback_timezone\":\"America/Lima\",\"template_version\":1}")
expect "programada a la hora local de cada contacto" "$(echo "$PROG" | jget data.status)" "scheduled"
expect "guarda la hora de pared sin zona" "$(echo "$PROG" | jget data.timezone_delivery.local_send_at)" "${DIA}T09:00"
expect "arranca cuando la primera zona del mundo marca esa hora (UTC+14)" \
  "$(echo "$PROG" | jget data.scheduled_at)" "$(date -u -d "${DIA} 09:00 UTC - 14 hours" +%Y-%m-%dT%H:%M:%SZ)"
expect "cancelar la campana por zona" "$(codigo -X POST "$GW/campaigns/$TZID/cancel" -H "$A2")" "200"

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

echo "== Segmentos por comportamiento y automatizaciones con ramas y por fecha (2-E)"
# No hay SES: la apertura se siembra en la outbox de la empresa con el contrato de
# transactional.email.opened, y cualquier rele de la base la publica.
CAMP_OPEN=$(cat /proc/sys/kernel/random/uuid)
sql mail_tenant_acme "INSERT INTO platform.event_outbox (id, subject, tenant_id, payload) VALUES (gen_random_uuid(), 'transactional.email.opened', '$ACME_TENANT',
  jsonb_build_object('id', gen_random_uuid()::text, 'type', 'transactional.email.opened', 'source', 'e2e', 'tenant_id', '$ACME_TENANT',
    'timestamp', to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'),
    'data', jsonb_build_object('tenant_id', '$ACME_TENANT', 'message_id', gen_random_uuid()::text, 'email', 'marta@cliente.test',
      'class', 'marketing', 'campaign_id', '$CAMP_OPEN', 'contact_id', '$CT2', 'test', false,
      'occurred_at', to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'))))"
abrio() { curl -s -X POST "$GW/segments/preview" -H "$A2" -H 'Content-Type: application/json' -d "{\"definition\":{\"match\":\"all\",\"rules\":[$1]}}" | jget data.count; }
n=""
for _ in $(seq 1 60); do
  n=$(abrio "{\"field\":\"campaign\",\"op\":\"opened\",\"value\":\"$CAMP_OPEN\"}")
  [[ "$n" == 1 ]] && break; sleep 0.5
done
expect "la apertura llega a la proyeccion y el segmento 'abrio la campana' la encuentra" "$n" "1"
expect "la proyeccion solo guarda ids y horas" \
  "$(sql mail_tenant_acme "SELECT count(*) FROM contacts.engagement WHERE contact_id = '$CT2' AND campaign_id = '$CAMP_OPEN' AND last_opened_at IS NOT NULL AND last_clicked_at IS NULL")" "1"
expect "abrio en los ultimos 7 dias" "$(abrio '{"field":"last_days","op":"opened","value":7}')" "1"
expect "sin clic en la campana no entra en 'hizo clic'" "$(abrio "{\"field\":\"campaign\",\"op\":\"clicked\",\"value\":\"$CAMP_OPEN\"}")" "0"
expect "una regla de comportamiento fuera de su tope se rechaza" \
  "$(codigo -X POST "$GW/segments/preview" -H "$A2" -H 'Content-Type: application/json' -d '{"definition":{"match":"all","rules":[{"field":"last_campaigns","op":"opened","value":51}]}}')" "422"
contains "el catalogo publica las reglas de comportamiento" "$(curl -s "$GW/segments/meta" -H "$A2")" '"last_campaigns"'

# Rama por atributo: plan oro va a una lista y el resto a otra.
expect "atributo plan declarado" "$(codigo -X POST "$GW/contacts/attributes" -H "$A2" -H 'Content-Type: application/json' -d '{"key":"plan","type":"string"}')" "201"
LORO=$(curl -s -X POST "$GW/contacts/lists" -H "$A2" -H 'Content-Type: application/json' -d '{"name":"plan-oro"}' | jget data.id)
LRESTO=$(curl -s -X POST "$GW/contacts/lists" -H "$A2" -H 'Content-Type: application/json' -d '{"name":"plan-resto"}' | jget data.id)
RAMA=$(curl -s -X POST "$GW/automations/workflows" -H "$A2" -H 'Content-Type: application/json' -d "{\"name\":\"Rama por plan\",\"trigger\":{\"type\":\"contact.created\"},\"steps\":[
  {\"id\":\"rama\",\"type\":\"branch\",\"condition\":{\"kind\":\"attribute\",\"attribute\":\"plan\",\"op\":\"eq\",\"value\":\"oro\"},\"then\":\"oro\",\"else\":\"resto\"},
  {\"id\":\"oro\",\"type\":\"add_to_list\",\"list_id\":\"$LORO\"},{\"id\":\"resto\",\"type\":\"add_to_list\",\"list_id\":\"$LRESTO\"}]}")
RAMAID=$(echo "$RAMA" | jget data.id)
expect "flujo con rama en borrador" "$(echo "$RAMA" | jget data.steps.0.then)" "oro"
expect "un ciclo se rechaza en el dominio" \
  "$(codigo -X PATCH "$GW/automations/workflows/$RAMAID" -H "$A2" -H 'Content-Type: application/json' -d '{"steps":[{"id":"a","type":"wait","duration":"1d","next":"b"},{"id":"b","type":"wait","duration":"1d","next":"a"}]}')" "422"
expect "la rama se activa tras comprobar la condicion en contacts" "$(curl -s -X POST "$GW/automations/workflows/$RAMAID/activate" -H "$A2" | jget data.status)" "active"
curl -s -o /dev/null -X POST "$GW/contacts" -H "$A2" -H 'Content-Type: application/json' -d '{"email":"oro@cliente.test","source":"api","attributes":{"plan":"oro"}}'
curl -s -o /dev/null -X POST "$GW/contacts" -H "$A2" -H 'Content-Type: application/json' -d '{"email":"plata@cliente.test","source":"api","attributes":{"plan":"plata"}}'
oro=""; resto=""
for _ in $(seq 1 90); do
  oro=$(curl -s "$GW/contacts?list_id=$LORO" -H "$A2" | jget data.0.email)
  resto=$(curl -s "$GW/contacts?list_id=$LRESTO" -H "$A2" | jget data.0.email)
  [[ -n "$oro" && -n "$resto" ]] && break; sleep 0.5
done
expect "quien cumple la condicion sigue por then" "$oro" "oro@cliente.test"
expect "y quien no, por else" "$resto" "plata@cliente.test"
expect "archivar el flujo con rama" "$(codigo -X POST "$GW/automations/workflows/$RAMAID/archive" -H "$A2")" "200"

# Disparador por fecha: el aniversario de hoy (UTC) desde las 00:00 de la zona del contacto.
expect "atributo de fecha declarado" "$(codigo -X POST "$GW/contacts/attributes" -H "$A2" -H 'Content-Type: application/json' -d '{"key":"cumple","type":"date"}')" "201"
LCUMPLE=$(curl -s -X POST "$GW/contacts/lists" -H "$A2" -H 'Content-Type: application/json' -d '{"name":"cumpleanos"}' | jget data.id)
curl -s -o /dev/null -X POST "$GW/contacts" -H "$A2" -H 'Content-Type: application/json' \
  -d "{\"email\":\"cumple@cliente.test\",\"source\":\"api\",\"timezone\":\"UTC\",\"attributes\":{\"cumple\":\"1990-$(date -u +%m-%d)\"}}"
fecha_flujo() {
  curl -s -X POST "$GW/automations/workflows" -H "$A2" -H 'Content-Type: application/json' -d "{\"name\":\"$1\",\"re_entry\":true,
    \"trigger\":{\"type\":\"contact.date\",\"attribute\":\"$2\",\"hour\":0,\"timezone\":\"UTC\"},\"steps\":[{\"id\":\"regalo\",\"type\":\"add_to_list\",\"list_id\":\"$LCUMPLE\"}]}"
}
MALA=$(fecha_flujo "Aniversario de un texto" plan | jget data.id)
expect "un disparador por fecha sobre un atributo que no es de fecha no se activa" \
  "$(codigo -X POST "$GW/automations/workflows/$MALA/activate" -H "$A2")" "422"
FECHA=$(fecha_flujo "Cumpleanos" cumple)
FECHAID=$(echo "$FECHA" | jget data.id)
expect "el disparador por fecha guarda su hora local" "$(echo "$FECHA" | jget data.trigger.hour)" "0"
expect "el flujo por fecha se activa" "$(curl -s -X POST "$GW/automations/workflows/$FECHAID/activate" -H "$A2" | jget data.status)" "active"
cumple=""
for _ in $(seq 1 120); do
  cumple=$(curl -s "$GW/contacts?list_id=$LCUMPLE" -H "$A2" | jget data.0.email)
  [[ -n "$cumple" ]] && break; sleep 0.5
done
expect "el aniversario de hoy hace entrar al contacto" "$cumple" "cumple@cliente.test"
expect "una sola entrada este ano aunque el recorrido se repita" \
  "$(sql mail_tenant_acme "SELECT count(*) FROM automations.runs WHERE workflow_id = '$FECHAID' AND entry_key = 'date:$(date -u +%Y)'")" "1"

echo "== Captacion: formularios de suscripcion (contacts) y paginas de aterrizaje (templates)"
RAIZ="${GW%/api/v1}"
FLID=$(curl -s -X POST "$GW/contacts/lists" -H "$A2" -H 'Content-Type: application/json' -d '{"name":"suscritos-web"}' | jget data.id)
FORM=$(curl -s -X POST "$GW/contacts/forms" -H "$A2" -H 'Content-Type: application/json' -d "{\"name\":\"portada\",\"list_id\":\"$FLID\",
  \"fields\":[{\"key\":\"email\",\"label\":\"Correo\"},{\"key\":\"first_name\",\"label\":\"Nombre\"}],
  \"texts\":{\"title\":\"Boletin\",\"consent_text\":\"Acepto recibir el boletin de Acme\",\"success_message\":\"Revisa tu correo para confirmar\"},
  \"allowed_origins\":[\"https://acme.test\"]}")
FKEY=$(echo "$FORM" | jget data.embed.key)
[[ -n "$FKEY" ]] && ok "alta de un formulario con su clave publica" || mal "alta de formulario: ${FORM:0:300}"
expect "el formulario exige su permiso de lectura por el gateway" "$(codigo "$GW/contacts/forms" -H "$A2")" "200"
expect "la lista destino de un formulario no se borra" "$(codigo -X DELETE "$GW/contacts/lists/$FLID" -H "$A2")" "409"
FPUB="$GW/public/contacts/forms/$FKEY"
DEF_CAB=$(curl -s -D - -o "$WORK/form.json" "$FPUB" -H 'Origin: https://acme.test')
contains "la definicion publica lleva CORS para el origen declarado" "$DEF_CAB" "Access-Control-Allow-Origin: https://acme.test"
lacks "sin credenciales" "$DEF_CAB" "Access-Control-Allow-Credentials"
expect "un origen no declarado no usa el formulario" "$(codigo "$FPUB" -H 'Origin: https://evil.test')" "403"
PRE=$(curl -s -D - -o /dev/null -X OPTIONS "$FPUB/submit" -H 'Origin: https://acme.test' -H 'Access-Control-Request-Method: POST' -H 'Access-Control-Request-Headers: content-type')
contains "la comprobacion previa la contesta contacts" "$PRE" "Access-Control-Allow-Origin: https://acme.test"
lacks "y no el CORS del gateway" "$PRE" "Access-Control-Allow-Credentials"
EMB_CAB=$(curl -s -D - -o "$WORK/embed.html" "$FPUB/embed")
contains "el iframe se deja incrustar solo en la plataforma y en el origen declarado" "$EMB_CAB" "frame-ancestors 'self' https://acme.test"
lacks "sin el X-Frame-Options del borde" "$EMB_CAB" "X-Frame-Options"
lacks "el formulario no lleva scripts" "$(cat "$WORK/embed.html")" "<script"
contains "y lleva el campo trampa" "$(cat "$WORK/embed.html")" 'name="homepage"'
contains "el script para incrustar crea el iframe" "$(curl -s "$FPUB/embed.js")" "/embed"
# envio <ip> <cuerpo sin token> [token]: POST JSON desde una IP de documentacion (X-Real-IP, que el
# gateway acepta de loopback como de su proxy de borde), con un token recien servido.
token_formulario() { curl -s "$FPUB" | jget data.token; }
envio() {
  curl -s -o "$WORK/envio.json" -w '%{http_code}' -X POST "$FPUB/submit" -H 'Content-Type: application/json' \
    -H 'Origin: https://acme.test' -H "X-Real-IP: $1" -d "{\"token\":\"$3\",$2}"
}
TK=$(token_formulario); sleep 1.2
expect "un envio valido se acepta" "$(envio 198.51.100.21 '"fields":{"email":"web@cliente.test","first_name":"Wendy"},"consent":true,"homepage":""' "$TK")" "202"
WEB=$(curl -s "$GW/contacts?search=web@cliente" -H "$A2")
expect "el contacto entra pendiente de confirmar el doble opt-in" "$(echo "$WEB" | jget data.0.consent_status)" "pending"
expect "con el formulario como origen" "$(echo "$WEB" | jget data.0.source)" "form"
EVID=$(sql mail_tenant_acme "SELECT evidence->>'ip_prefix' || '|' || (evidence ? 'consent_text') FROM contacts.consents WHERE source LIKE 'form:%' ORDER BY occurred_at DESC LIMIT 1")
expect "la evidencia guarda la ip truncada y el texto aceptado" "$EVID" "198.51.100.0/24|true"
expect "el mismo token no vale dos veces" "$(envio 198.51.100.21 '"fields":{"email":"otra@cliente.test"},"consent":true' "$TK")" "400"
expect "una direccion ya existente recibe la misma respuesta" \
  "$(TK=$(token_formulario); sleep 1.2; envio 198.51.100.22 '"fields":{"email":"web@cliente.test","first_name":"Otro"},"consent":true' "$TK")" "202"
expect "y el envio publico no le cambia el nombre" "$(curl -s "$GW/contacts?search=web@cliente" -H "$A2" | jget data.0.first_name)" "Wendy"
expect "exclusion manual de una direccion" "$(codigo -X POST "$GW/suppression/entries" -H "$A2" -H 'Content-Type: application/json' \
  -d '{"email":"suprimida-web@cliente.test","reason":"manual"}')" "201"
expect "el envio de una suprimida recibe la misma respuesta" \
  "$(TK=$(token_formulario); sleep 1.2; envio 198.51.100.23 '"fields":{"email":"suprimida-web@cliente.test"},"consent":true' "$TK")" "202"
SUPR=$(curl -s "$GW/contacts?search=suprimida-web" -H "$A2")
expect "la suprimida queda excluida" "$(echo "$SUPR" | jget data.0.status)" "excluded"
expect "sin pedirle el doble opt-in (no recibe el correo de confirmacion)" \
  "$(sql mail_tenant_acme "SELECT count(*) FROM contacts.consents k JOIN contacts.contacts c ON c.id = k.contact_id WHERE c.email = 'suprimida-web@cliente.test'")" "0"
expect "el campo trampa recibe la misma respuesta" \
  "$(TK=$(token_formulario); sleep 1.2; envio 198.51.100.24 '"fields":{"email":"robot@cliente.test"},"consent":true,"homepage":"https://spam.test"' "$TK")" "202"
expect "y no crea nada" "$(curl -s "$GW/contacts?search=robot@cliente" -H "$A2" | jget data)" "[]"
expect "un envio antes del tiempo minimo se rechaza" \
  "$(envio 198.51.100.25 '"fields":{"email":"rapido@cliente.test"},"consent":true' "$(token_formulario)")" "400"
cupo=""
for _ in 1 2 3 4 5 6; do cupo=$(envio 198.51.100.26 '"fields":{"email":"cupo@cliente.test"},"consent":true' "x"); done
expect "por encima del cupo por IP responde 429" "$cupo" "429"
TK=$(token_formulario); sleep 1.2
HTMLRESP=$(curl -s -w '\n%{http_code}' -X POST "$FPUB/submit" -H "X-Real-IP: 198.51.100.27" \
  --data-urlencode "_token=$TK" --data-urlencode "field.email=html@cliente.test" --data-urlencode "consent=on")
contains "el envio desde el iframe responde la pagina de gracias" "$HTMLRESP" "Revisa tu correo para confirmar"
expect "con 200" "${HTMLRESP##*$'\n'}" "200"
expect "las estadisticas cuentan los envios validos" "$(curl -s "$GW/contacts/forms/$(echo "$FORM" | jget data.id)/stats" -H "$A2" | jget data.submitted)" "4"

PG=$(curl -s -X POST "$GW/templates/pages" -H "$A2" -H 'Content-Type: application/json' -d "{\"name\":\"Oferta\",\"slug\":\"oferta\",
  \"content\":{\"title\":\"Oferta de Acme\",\"description\":\"Descuento\",\"css\":\".h{color:#123}\",
  \"html\":\"<h1 class=\\\"h\\\" onclick=\\\"x()\\\">Oferta</h1><script>alert(1)</script><div data-cf-form=\\\"$FKEY\\\"></div>\",
  \"editor\":{\"kind\":\"grapesjs-web\",\"project\":{\"pages\":[]}}}}")
PGID=$(echo "$PG" | jget data.page.id)
[[ -n "$PGID" ]] && ok "alta de una pagina de aterrizaje con su primera version" || mal "alta de pagina: ${PG:0:300}"
lacks "el HTML se guarda saneado" "$(curl -s "$GW/templates/pages/$PGID/versions/1" -H "$A2" | jget data.html)" "script"
expect "un CSS que carga otra hoja se rechaza" "$(codigo -X POST "$GW/templates/pages/$PGID/versions" -H "$A2" -H 'Content-Type: application/json' \
  -d '{"title":"x","html":"<p>x</p>","css":"@import url(https://evil.test/a.css);"}')" "422"
expect "sin publicar no se sirve" "$(codigo "$RAIZ/p/acme/oferta")" "404"
expect "publicacion de la pagina" "$(codigo -X POST "$GW/templates/pages/$PGID/versions/1/publish" -H "$A2")" "200"
PAG_CAB=$(curl -s -D - -o "$WORK/pagina.html" "$RAIZ/p/acme/oferta")
contains "la pagina publicada se sirve en /p/<empresa>/<slug>" "$PAG_CAB" "200"
contains "con la politica del servicio, que no ejecuta nada" "$PAG_CAB" "default-src 'none'"
contains "sin indexar por defecto" "$PAG_CAB" "X-Robots-Tag: noindex"
lacks "sin scripts del usuario ni nonce del gateway" "$(cat "$WORK/pagina.html")" "<script"
contains "con el formulario de la empresa en su iframe" "$(cat "$WORK/pagina.html")" "/public/contacts/forms/$FKEY/embed"
expect "otra empresa no sirve la pagina" "$(codigo "$RAIZ/p/no-existe/oferta")" "404"
expect "retirar la pagina" "$(codigo -X POST "$GW/templates/pages/$PGID/unpublish" -H "$A2")" "200"
expect "retirada ya no se sirve" "$(codigo "$RAIZ/p/acme/oferta")" "404"

echo "== Analitica"
expect "el panel responde sin envios" "$(curl -s "$GW/analytics/overview" -H "$A2" | jget data.totals.sent)" "0"
expect "un rango invertido se rechaza" "$(codigo "$GW/analytics/overview?from=2026-09-10&to=2026-09-01" -H "$A2")" "422"
LNK=$(curl -s "$GW/analytics/campaigns/$CPID/links" -H "$A2")
expect "los clics por enlace de una campana sin clics salen en cero" "$(echo "$LNK" | jget data.total_clicks)/$(echo "$LNK" | jget data.links)" "0/[]"
expect "un tope de enlaces fuera de rango se rechaza" "$(codigo "$GW/analytics/campaigns/$CPID/links?limit=0" -H "$A2")" "422"
contains "el catalogo publica el tope de la lista de enlaces" "$(curl -s "$GW/analytics/meta" -H "$A2")" '"links":{"default_limit"'

echo "== Planificador (scheduler)"
# El catalogo de manejadores ya no esta vacio: analytics declara analytics.retention.prune y
# lo ejecuta. Con el catalogo vacio, crear un trabajo respondia 422 y nada se despachaba.
HAND=$(curl -s "$GW/scheduler/handlers" -H "$A2")
contains "el catalogo publica el manejador de la poda" "$HAND" '"analytics.retention.prune"'
contains "con el servicio que lo ejecuta" "$HAND" '"service":"analytics"'
# El trabajo de plataforma que siembra la migracion 05: la empresa lo ve y no lo puede tocar.
contains "la empresa ve el trabajo de plataforma sembrado" "$(curl -s "$GW/scheduler/jobs" -H "$A2")" '"code":"analytics-retention-prune"'
PJID=$(sql mail_tenant_acme "SELECT id FROM scheduler.job_definitions WHERE code = 'analytics-retention-prune'")
expect "que no es de ninguna empresa" "$(sql mail_tenant_acme "SELECT tenant_id IS NULL FROM scheduler.job_definitions WHERE id = '$PJID'")" "t"
expect "y la empresa no lo desactiva" "$(codigo -X POST "$GW/scheduler/jobs/$PJID/disable" -H "$A2")" "403"
expect "su primera pasada es la medianoche siguiente, no el propio despliegue" \
  "$(sql mail_tenant_acme "SELECT next_run_at > now() FROM scheduler.job_schedules WHERE job_id = '$PJID'")" "t"
# Un manejador que nadie declara se rechaza al crear, nombrando el campo y la regla.
NOJOB=$(curl -s -X POST "$GW/scheduler/jobs" -H "$A2" -H 'Content-Type: application/json' \
  -d '{"name":"Inventado","code":"e2e-inventado","job_type":"interval","interval_minutes":60,"handler":"no.existe"}')
expect "un manejador fuera del catalogo se rechaza" \
  "$(echo "$NOJOB" | jget error.details.field)/$(echo "$NOJOB" | jget error.details.rule)" "handler/not_allowed"
# Y uno declarado se crea: es lo que el catalogo vacio hacia imposible.
JOB=$(curl -s -X POST "$GW/scheduler/jobs" -H "$A2" -H 'Content-Type: application/json' \
  -d '{"name":"Poda de la analitica","code":"e2e-poda","job_type":"interval","interval_minutes":60,"handler":"analytics.retention.prune","timeout_seconds":120}')
JID=$(echo "$JOB" | jget data.id)
[[ -n "$JID" ]] && ok "la empresa crea un trabajo con un manejador declarado" || mal "crear el trabajo: ${JOB:0:300}"
# Lanzarlo a mano lo despacha por la outbox; analytics lo recoge y lo cierra por
# POST /internal/scheduler/executions/{id}/complete.
EJID=$(curl -s -X POST "$GW/scheduler/jobs/$JID/run" -H "$A2" | jget data.id)
[[ -n "$EJID" ]] && ok "la ejecucion nace despachada" || mal "lanzar el trabajo: $JID"
estado=""
for _ in $(seq 1 80); do
  estado=$(curl -s "$GW/scheduler/executions/$EJID" -H "$A2" | jget data.status)
  [[ "$estado" == completed || "$estado" == failed ]] && break; sleep 0.5
done
expect "analytics la ejecuta y la cierra con exito" "$estado" "completed"
contains "y deja lo que podo como resultado" "$(curl -s "$GW/scheduler/executions/$EJID" -H "$A2" | jget data.result)" "messages"

# Tareas puntuales: apuntan al mismo catalogo y ahora se despachan de verdad.
NOTASK=$(curl -s -X POST "$GW/scheduler/tasks" -H "$A2" -H 'Content-Type: application/json' \
  -d '{"name":"Inventada","trigger_at":"2026-01-01T00:00:00Z","handler":"no.existe"}')
expect "una tarea con un manejador fuera del catalogo se rechaza" \
  "$(echo "$NOTASK" | jget error.details.field)/$(echo "$NOTASK" | jget error.details.rule)" "handler/not_allowed"
TSK=$(curl -s -X POST "$GW/scheduler/tasks" -H "$A2" -H 'Content-Type: application/json' \
  -d "{\"name\":\"Poda puntual\",\"trigger_at\":\"$(date -u -d '-1 minute' +%Y-%m-%dT%H:%M:%SZ)\",\"handler\":\"analytics.retention.prune\"}")
TSKID=$(echo "$TSK" | jget data.id)
[[ -n "$TSKID" ]] && ok "la empresa programa una tarea puntual ya vencida" || mal "programar la tarea: ${TSK:0:300}"
estado=""
for _ in $(seq 1 120); do
  estado=$(sql mail_tenant_acme "SELECT status FROM scheduler.scheduled_tasks WHERE id = '$TSKID'")
  [[ "$estado" == executed ]] && break; sleep 0.5
done
expect "el barrido la marca ejecutada" "$estado" "executed"
# Lo que antes faltaba: la marca iba sola, sin despachar nada a nadie.
despachada() {
  local n=""
  for _ in $(seq 1 40); do
    n=$(sql mail_tenant_acme "SELECT count(*) FROM platform.event_outbox WHERE subject = 'scheduler.task.started'
      AND payload->'data'->>'task_id' = '$TSKID' AND published_at IS NOT NULL")
    [[ "$n" == 1 ]] && { echo si; return; }
    sleep 0.5
  done
  echo "no ($n)"
}
expect "y la outbox entrega su despacho a NATS" "$(despachada)" "si"

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
ROL_SISTEMA=$(sql mail_registry "SELECT r.id FROM access_control.roles r JOIN organization.tenants t ON t.id = r.tenant_id WHERE t.slug = 'acme' AND r.is_system")
expect "la cuenta que se va a borrar recibe un rol" \
  "$(codigo -X POST "$GW/user-roles/assign" -H "$A2" -H 'Content-Type: application/json' -d "{\"user_id\":\"$U_BORRADA\",\"role_id\":\"$ROL_SISTEMA\"}")" "200"
expect "el tenant_admin borra una cuenta" "$(codigo -X DELETE "$GW/users/$U_BORRADA" -H "$A2")" "204"
expect "su token, aun vigente, ya no abre ni su ficha" \
  "$(sesion "$GW/users/$U_BORRADA" -H "Authorization: Bearer $T_BORRADA")" "SESSION_REVOKED 401"
expect "el tenant_admin desactiva otra" "$(codigo -X POST "$GW/users/$U_INACTIVA/deactivate" -H "$A2")" "200"
expect "su token ya no lista sus sesiones" \
  "$(sesion "$GW/sessions/mine" -H "Authorization: Bearer $T_INACTIVA")" "SESSION_REVOKED 401"
expect "ni pide un step-up" \
  "$(sesion -X POST "$GW/auth/step-up" -H "Authorization: Bearer $T_INACTIVA" -H 'Content-Type: application/json' -d '{"current_password":"x"}')" "SESSION_REVOKED 401"

# identity encola identity.user.deleted con el borrado; su rele lo publica en IDENTITY y
# access-control retira las asignaciones de la cuenta.
roles_de_la_borrada() { sql mail_registry "SELECT count(*) FROM access_control.user_roles WHERE user_id = '$U_BORRADA'"; }
for _ in $(seq 1 40); do [[ "$(roles_de_la_borrada)" == 0 ]] && break; sleep 0.5; done
expect "access-control retira sus asignaciones al consumir identity.user.deleted" "$(roles_de_la_borrada)" "0"
expect "borrarla otra vez no la encuentra" "$(codigo -X DELETE "$GW/users/$U_BORRADA" -H "$A2")" "404"
expect "la baja se anuncio una sola vez, por la outbox del registro, con su empresa" \
  "$(sql mail_registry "SELECT count(*) FROM platform.event_outbox WHERE subject = 'identity.user.deleted' AND payload->'data'->>'user_id' = '$U_BORRADA' AND payload->'data'->>'tenant_id' = (SELECT id::text FROM organization.tenants WHERE slug = 'acme')")" "1"
# Una cuenta pending no inicia sesion: indicando su empresa responde como una inactiva y, sin
# empresa, igual que un correo desconocido. Su token anterior tampoco sirve.
pend_pass="$(rand_hex 10)Aa1!"
U_PEND=$(curl -s -X POST "$GW/users" -H "$A2" -H 'Content-Type: application/json' \
  -d "{\"email\":\"pendiente@acme.test\",\"password\":\"$pend_pass\",\"first_name\":\"Luis\",\"last_name\":\"Rios\"}" | jget data.id)
T_PEND=$(e2e_login pendiente@acme.test "$pend_pass" | jget data.access_token)
[[ -n "$U_PEND" && -n "$T_PEND" ]] && ok "cuenta que pasara a pending, con sesion" || mal "alta o login de la cuenta pending"
sql mail_registry "UPDATE identity.users SET status = 'pending' WHERE id = '$U_PEND'" >/dev/null
expect "pending no inicia sesion en su empresa, ni con la contrasena buena" \
  "$(sesion -X POST "$GW/auth/login" -H 'Content-Type: application/json' -d "{\"email\":\"pendiente@acme.test\",\"password\":\"$pend_pass\",\"tenant_slug\":\"acme\"}")" "ACCOUNT_INACTIVE 403"
expect "sin empresa responde igual que un correo desconocido" \
  "$(curl -s -w ' %{http_code}' -X POST "$GW/auth/login" -H 'Content-Type: application/json' -d "{\"email\":\"pendiente@acme.test\",\"password\":\"$pend_pass\"}")" \
  "$(curl -s -w ' %{http_code}' -X POST "$GW/auth/login" -H 'Content-Type: application/json' -d "{\"email\":\"nadie@acme.test\",\"password\":\"$pend_pass\"}")"
expect "su token anterior ya no abre su ficha" "$(sesion "$GW/users/$U_PEND" -H "Authorization: Bearer $T_PEND")" "SESSION_REVOKED 401"

echo "== Enumeracion de cuentas: recuperacion de contrasena y bloqueo"
# forgot-password responde lo mismo, estado y cuerpo, exista o no el correo: la peticion solo
# encola. Cada paso sale de su propia IP de documentacion por X-Real-IP, como la rafaga del
# limitador, para no gastar el cupo estricto de 127.0.0.1.
olvido() {
  curl -s -w ' %{http_code}' -X POST "$GW/auth/forgot-password" -H 'X-Real-IP: 198.51.100.31' \
    -H 'Content-Type: application/json' -d "{\"email\":\"$1\"}"
}
OLVIDO=$(olvido admin@acme.test)
contains "forgot-password con un correo registrado responde 200" "$OLVIDO" " 200"
expect "y un correo desconocido recibe lo mismo" "$(olvido nadie@acme.test)" "$OLVIDO"
# Un correo sin cuenta llega al bloqueo tras los mismos intentos que una cuenta real y con la
# misma respuesta, con la empresa y sin ella.
bloq_pass="$(rand_hex 10)Aa1!"
A2B="Authorization: Bearer $(e2e_login admin@acme.test "$TENANT_PASS" | jget data.access_token)"
expect "alta de la cuenta que se va a bloquear" "$(codigo -X POST "$GW/users" -H "$A2B" -H 'Content-Type: application/json' \
  -d "{\"email\":\"bloqueo@acme.test\",\"password\":\"$bloq_pass\",\"first_name\":\"Luis\",\"last_name\":\"Rios\"}")" "201"
# serie <ip> <email> [slug]: las respuestas de 8 intentos con una contrasena mala.
serie() {
  local slug="" out="" _
  [[ -n "${3:-}" ]] && slug=",\"tenant_slug\":\"$3\""
  for _ in $(seq 1 8); do
    out+="$(sesion -X POST "$GW/auth/login" -H "X-Real-IP: $1" -H 'Content-Type: application/json' \
      -d "{\"email\":\"$2\",\"password\":\"no-es-la-contrasena\"$slug}")|"
  done
  echo "$out"
}
REAL=$(serie 198.51.100.32 bloqueo@acme.test acme)
contains "la cuenta real llega al bloqueo" "$REAL" "ACCOUNT_LOCKED 403"
expect "un correo sin cuenta en la empresa recibe la misma serie" "$(serie 198.51.100.33 nadie-bloqueo@acme.test acme)" "$REAL"
expect "y sin empresa, tambien" "$(serie 198.51.100.34 nadie-bloqueo@acme.test)" "$REAL"

echo "== La misma direccion en dos empresas: la entrada la resuelve la credencial"
# El formulario no pide la empresa. Con la misma direccion dada de alta en dos, entra la cuenta
# cuya contrasena coincide; antes se resolvia la empresa por el correo y una de las dos no podia
# entrar nunca. Una contrasena que no es de ninguna sigue respondiendo lo que un correo sin
# cuenta. Cada inicio sale de su propia IP de documentacion para no gastar el cupo de 127.0.0.1.
T1C=$(e2e_login "$ADMIN_EMAIL" "$ADMIN_PASS" | jget data.access_token)
DOBLE_EMAIL=admin@dosempresas.test
DOBLE_PASS_1="$(rand_hex 12)Aa1!"
DOBLE_PASS_2="$(rand_hex 12)Aa1!"
expect "alta de la primera empresa con la direccion compartida" \
  "$(e2e_alta_empresa "$T1C" dosuna pe-01 "$DOBLE_EMAIL" "$DOBLE_PASS_1" | jget data.slug)" "dosuna"
expect "alta de la segunda con la MISMA direccion" \
  "$(e2e_alta_empresa "$T1C" dosdos pe-01 "$DOBLE_EMAIL" "$DOBLE_PASS_2" | jget data.slug)" "dosdos"
# entra_en <slug> <contrasena> <ip>: la empresa en que entra esa contrasena, sin indicarla.
entra_en() {
  expect "con la contrasena de $1 entra en $1" \
    "$(curl -s -X POST "$GW/auth/login" -H "X-Real-IP: $3" -H 'Content-Type: application/json' \
      -d "{\"email\":\"$DOBLE_EMAIL\",\"password\":\"$2\"}" | jget data.tenant_id)" \
    "$(sql mail_registry "SELECT id FROM organization.tenants WHERE slug = '$1'")"
}
entra_en dosuna "$DOBLE_PASS_1" 198.51.100.41
entra_en dosdos "$DOBLE_PASS_2" 198.51.100.42
MALA_DOBLE=$(sesion -X POST "$GW/auth/login" -H 'X-Real-IP: 198.51.100.43' -H 'Content-Type: application/json' \
  -d "{\"email\":\"$DOBLE_EMAIL\",\"password\":\"no-es-la-contrasena\"}")
expect "una contrasena que no es de ninguna de las dos responde como un correo sin cuenta" "$MALA_DOBLE" \
  "$(sesion -X POST "$GW/auth/login" -H 'X-Real-IP: 198.51.100.44' -H 'Content-Type: application/json' \
    -d '{"email":"nadie-dos@dosempresas.test","password":"no-es-la-contrasena"}')"

echo "== Rutas con sesion por celda (segundo gateway, dos celdas)"
# El gateway de arriba no declara celdas: todo va al destino base. Este segundo gateway declara
# la celda base (GATEWAY_BASE_CELL_CODE) y una instancia de mail-directory sobre mail_cell_pe_02:
# cada peticion con sesion va a la celda de su empresa, que pregunta a organization, y una celda
# sin instancia declarada no llega a ninguna. Tokens nuevos: los de arriba caducan a los 5 min.
MD2_PORT=$((BASE + 60)) GW2_PORT=$((BASE + 61)) MS2_PORT=$((BASE + 62)) REDIS2_PORT=$((BASE - 1620)) DS2_PORT=$((BASE + 66))
MA2_PORT=$((BASE + 67)) MA2_TLS_PORT=$((BASE + 68)) WM2_PORT=$((BASE + 69))
T1N=$(e2e_login "$ADMIN_EMAIL" "$ADMIN_PASS" | jget data.access_token)
A2N="Authorization: Bearer $(e2e_login admin@acme.test "$TENANT_PASS" | jget data.access_token)"
for c in pe-02 pe-03; do
  expect "el superadmin registra la celda $c" "$(codigo -X POST "$GW/cells" -H "Authorization: Bearer $T1N" -H 'Content-Type: application/json' \
    -d "{\"code\":\"$c\",\"region\":\"sa-east-1\",\"db_host\":\"127.0.0.1\",\"db_port\":5432}")" "201"
done
CELDAS_PASS="$(rand_hex 12)Aa1!"
expect "alta de beta en pe-02" "$(e2e_alta_empresa "$T1N" beta pe-02 admin@beta.test "$CELDAS_PASS" | jget data.slug)" "beta"
expect "alta de gamma en pe-03, celda sin instancias" "$(e2e_alta_empresa "$T1N" gamma pe-03 admin@gamma.test "$CELDAS_PASS" | jget data.slug)" "gamma"
LB=$(e2e_login admin@beta.test "$CELDAS_PASS")
BID=$(echo "$LB" | jget data.tenant_id)
AB="Authorization: Bearer $(echo "$LB" | jget data.access_token)"
AG="Authorization: Bearer $(e2e_login admin@gamma.test "$CELDAS_PASS" | jget data.access_token)"

ORG_INTERNA="http://127.0.0.1:${PORT[organization]}/internal/organization/tenants/$BID/cell"
expect "organization da la celda de beta con el token interno" \
  "$(curl -s "$ORG_INTERNA" -H "X-Gateway-Token: $INTERNAL_GATEWAY_TOKEN" | jget data.cell_code)" "pe-02"
expect "a una peticion con usuario no" \
  "$(codigo "$ORG_INTERNA" -H "X-Gateway-Token: $INTERNAL_GATEWAY_TOKEN" -H "X-User-ID: $U_VIGENTE")" "403"
expect "ni sin el token interno" "$(codigo "$ORG_INTERNA")" "401"

env -u POSTGRES_PASSWORD -u JWT_SIGNING_KEY CELL_CODE=pe-02 CELL_DB_NAME=mail_cell_pe_02 CELL_DB_PASSWORD="$CELL2_PASS" \
  MAIL_DIRECTORY_PORT="$MD2_PORT" "$WORK/bin/mail-directory" >"$WORK/log/mail-directory-pe-02.log" 2>&1 &
# mail-security de pe-02 con el Redis de los motores de su celda y sin NATS: aqui solo se opera su
# cortafuegos, y no debe llevarse los eventos del directorio que consume la instancia de pe-01.
docker rm -f "$E2E_PREFIX-redis-pe02" >/dev/null 2>&1
docker run -d --name "$E2E_PREFIX-redis-pe02" -p "127.0.0.1:$REDIS2_PORT:6379" redis:7.4.10-alpine >/dev/null || mal "redis de los motores de pe-02"
env -u POSTGRES_PASSWORD -u JWT_SIGNING_KEY CELL_CODE=pe-02 CELL_DB_NAME=mail_cell_pe_02 CELL_DB_PASSWORD="$CELL2_PASS" \
  MAIL_SECURITY_PORT="$MS2_PORT" MAIL_POLICY_MAPS_PORT=$((BASE + 63)) MAIL_POLICY_EXPORT_PORT=$((BASE + 64)) \
  MAIL_REDIS_PORT="$REDIS2_PORT" NATS_URL=nats://127.0.0.1:1 "$WORK/bin/mail-security" >"$WORK/log/mail-security-pe-02.log" 2>&1 &
# domain-service con las instancias de mail-directory por celda de este gateway, que le lleva /domains.
env -u JWT_SIGNING_KEY DOMAIN_SERVICE_PORT="$DS2_PORT" GATEWAY_BASE_CELL_CODE=pe-01 MAIL_DIRECTORY_CELL_HOSTS="pe-02=127.0.0.1:$MD2_PORT" \
  "$WORK/bin/domain-service" >"$WORK/log/domain-service-celdas.log" 2>&1 &
env -u JWT_SIGNING_KEY GATEWAY_PORT="$GW2_PORT" GATEWAY_BASE_CELL_CODE=pe-01 MAIL_DIRECTORY_CELL_HOSTS="pe-02=127.0.0.1:$MD2_PORT" \
  MAIL_SECURITY_CELL_HOSTS="pe-02=127.0.0.1:$MS2_PORT" WEBMAIL_CELL_HOSTS="pe-02=127.0.0.1:$WM2_PORT" \
  DOMAIN_SERVICE_HOST_PORT="$DS2_PORT" "$WORK/bin/gateway" >"$WORK/log/gateway-celdas.log" 2>&1 &
esperar_salud mail-directory-pe-02 "$MD2_PORT"
esperar_salud mail-security-pe-02 "$MS2_PORT"
esperar_salud domain-service-celdas "$DS2_PORT"
esperar_salud gateway-celdas "$GW2_PORT"
GW2="http://127.0.0.1:$GW2_PORT/api/v1"
en_pe02() { grep -c '"path":"/api/v1/mailboxes"' "$WORK/log/mail-directory-pe-02.log"; }
contains "acme (pe-01) va al destino base" "$(curl -s "$GW2/mailboxes" -H "$A2N")" '"ana@acme.test"'
expect "sin pasar por la instancia de pe-02" "$(en_pe02)" "0"
expect "beta (pe-02) va a la instancia de su celda" "$(curl -s "$GW2/mailboxes" -H "$AB" | jget data)" "[]"
expect "que es la que la atiende" "$(en_pe02)" "1"
expect "gamma (pe-03, sin instancia) no llega a ninguna" "$(sesion "$GW2/mailboxes" -H "$AG")" "CELL_UNAVAILABLE 503"
expect "ni a la de pe-02" "$(en_pe02)" "1"
expect "beta llega a mail-security de su celda" "$(codigo "$GW2/mail-security/quarantine" -H "$AB")" "200"
expect "gamma tampoco llega a mail-security" "$(sesion "$GW2/mail-security/quarantine" -H "$AG")" "CELL_UNAVAILABLE 503"
expect "acme si, en la celda base" "$(codigo "$GW2/mail-security/quarantine" -H "$A2N")" "200"
contains "el gateway cuenta la celda sin instancia" "$(curl -s "http://127.0.0.1:$GW2_PORT/metrics")" \
  'cell_routing_failures_total{cell_service="mail-directory",reason="not_served"} 1'

echo "== Celda destino explicita del superadmin (X-Target-Cell)"
# El superadmin es de la empresa de plataforma (pe-01): sin cabecera solo opera esa celda. Con
# X-Target-Cell el gateway por celda le lleva a la instancia de la celda que nombra, que solo le
# atiende sus rutas de plataforma (el cortafuegos) y nunca datos de una empresa.
A1N="Authorization: Bearer $T1N"
RED_PE02=192.0.2.0/24
expect "el superadmin anade una red al cortafuegos de pe-02" "$(codigo -X POST "$GW2/mail-security/firewall/networks" -H "$A1N" \
  -H 'X-Target-Cell: pe-02' -H 'Content-Type: application/json' -d "{\"list\":\"deny\",\"network\":\"$RED_PE02\",\"note\":\"e2e\"}")" "201"
expect "la red queda en la base de pe-02" "$(sql mail_cell_pe_02 "SELECT count(*) FROM mail_security.firewall_networks WHERE network = '$RED_PE02'")" "1"
expect "y no en la de pe-01" "$(sql mail_cell_pe_01 "SELECT count(*) FROM mail_security.firewall_networks WHERE network = '$RED_PE02'")" "0"
expect "llega al Redis de los motores de pe-02" "$(docker exec "$E2E_PREFIX-redis-pe02" redis-cli HEXISTS F2B_BLACKLIST "$RED_PE02")" "1"
expect "y no al de pe-01" "$(docker exec "$E2E_PREFIX-redis" redis-cli HEXISTS F2B_BLACKLIST "$RED_PE02")" "0"
contains "el superadmin lee el cortafuegos de pe-02" "$(curl -s "$GW2/mail-security/firewall/networks" -H "$A1N" -H 'X-Target-Cell: pe-02')" "\"$RED_PE02\""
FW=$(curl -s -w ' %{http_code}' "$GW2/mail-security/firewall/networks" -H "$A1N")
expect "sin cabecera lee el cortafuegos de la celda de su empresa" "${FW##* }" "200"
lacks "donde la red de pe-02 no esta" "$FW" "$RED_PE02"
FW=$(curl -s -w ' %{http_code}' "$GW2/mail-security/firewall/networks" -H "$A1N" -H 'X-Target-Cell: pe-01')
expect "con la celda base como destino tambien" "${FW##* }" "200"
lacks "y tampoco la ve" "$FW" "$RED_PE02"
expect "con celda destino no llega a los datos de una empresa" "$(sesion "$GW2/mail-security/quarantine" -H "$A1N" -H 'X-Target-Cell: pe-02')" "PLATFORM_SCOPE_ONLY 403"
expect "ni al directorio de pe-02, que no tiene rutas de plataforma" "$(sesion "$GW2/mailboxes" -H "$A1N" -H 'X-Target-Cell: pe-02')" "PLATFORM_SCOPE_ONLY 403"
expect "una celda sin instancia no cae en ninguna otra" "$(sesion "$GW2/mail-security/firewall/networks" -H "$A1N" -H 'X-Target-Cell: pe-03')" "CELL_UNAVAILABLE 503"
expect "una celda mal formada se rechaza" "$(sesion "$GW2/mail-security/firewall/networks" -H "$A1N" -H 'X-Target-Cell: PE-02')" "INVALID_TARGET_CELL 400"
expect "fuera de un servicio de celda no se admite" "$(sesion "$GW2/templates" -H "$A1N" -H 'X-Target-Cell: pe-02')" "TARGET_CELL_NOT_APPLICABLE 400"
expect "una empresa no elige celda destino" "$(sesion "$GW2/mail-security/quarantine" -H "$AB" -H 'X-Target-Cell: pe-01')" "TARGET_CELL_FORBIDDEN 403"
expect "la cabecera interna del cliente se borra: beta sigue en su celda" "$(codigo "$GW2/mail-security/quarantine" -H "$AB" -H 'X-Operator-Cell: pe-01')" "200"
expect "el gateway sin celdas no admite celda destino" "$(sesion "$GW/mail-security/firewall/networks" -H "$A1N" -H 'X-Target-Cell: pe-01')" "CELL_UNAVAILABLE 503"
M2=$(curl -s "http://127.0.0.1:$GW2_PORT/metrics")
contains "el gateway cuenta la empresa que pidio celda destino" "$M2" 'cell_target_refusals_total{reason="not_operator"} 1'
contains "y la celda destino sin instancia" "$M2" 'cell_target_refusals_total{reason="not_served"} 1'
contains "mail-security de pe-02 cuenta la ruta de empresa que rechazo al operador" "$(curl -s "http://127.0.0.1:$MS2_PORT/metrics")" \
  'cell_membership_refusals_total{reason="operator_tenant_route"} 1'

echo "== Segunda barrera: cada instancia solo atiende a las empresas de su celda"
# El primer gateway no declara celdas y lo lleva todo a pe-01, como uno al que le falta
# GATEWAY_BASE_CELL_CODE: beta (pe-02) llega a las instancias de pe-01, que preguntan a
# organization y la rechazan sin atenderla ni escribir nada.
expect "beta por el gateway sin celdas: mail-directory de pe-01 no la atiende" "$(sesion "$GW/mailboxes" -H "$AB")" "TENANT_NOT_IN_CELL 403"
expect "ni una escritura" "$(sesion -X POST "$GW/mail-routing/relayhosts" -H "$AB" -H 'Content-Type: application/json' \
  -d "{\"hostname\":\"smtp.beta.test:587\",\"username\":\"beta\",\"password\":\"$(rand_hex 12)\"}")" "TENANT_NOT_IN_CELL 403"
expect "mail-security de pe-01 tampoco" "$(sesion -X PUT "$GW/mail-security/quarantine-settings" -H "$AB" -H 'Content-Type: application/json' \
  -d '{"max_size_bytes":1048576,"max_age_days":30,"retention_size":10,"exclude_domains":[],"notify":{}}')" "TENANT_NOT_IN_CELL 403"
expect "ni la activacion interna de domain-service con la empresa de beta" \
  "$(sesion -X PUT "http://127.0.0.1:${PORT[mail-directory]}/internal/mail-directory/domains/beta.test/activation" \
    -H "X-Gateway-Token: $INTERNAL_GATEWAY_TOKEN" -H "X-Tenant-ID: $BID" -H 'Content-Type: application/json' -d '{"active":true}')" "TENANT_NOT_IN_CELL 403"
expect "la base de pe-01 no guarda nada de beta" \
  "$(sql mail_cell_pe_01 "SELECT (SELECT count(*) FROM mail.relayhosts WHERE tenant_id = '$BID') + (SELECT count(*) FROM mail_security.quarantine_settings WHERE tenant_id = '$BID') + (SELECT count(*) FROM mail.domains WHERE tenant_id = '$BID')")" "0"
ACME_ID=$(sql mail_registry "SELECT id FROM organization.tenants WHERE slug = 'acme'")
expect "y la instancia de pe-02 no atiende a acme aunque le llegue directa" \
  "$(sesion "http://127.0.0.1:$MD2_PORT/api/v1/mailboxes" -H "X-Gateway-Token: $INTERNAL_GATEWAY_TOKEN" \
    -H "X-Tenant-ID: $ACME_ID" -H "X-User-ID: $U_VIGENTE" -H 'X-User-Roles: tenant_admin')" "TENANT_NOT_IN_CELL 403"
contains "mail-directory de pe-01 cuenta sus tres rechazos" "$(curl -s "http://127.0.0.1:${PORT[mail-directory]}/metrics")" \
  'cell_membership_refusals_total{reason="foreign_tenant"} 3'
contains "y mail-security el suyo" "$(curl -s "http://127.0.0.1:${PORT[mail-security]}/metrics")" \
  'cell_membership_refusals_total{reason="foreign_tenant"} 1'
expect "acme sigue entrando por el gateway sin celdas" "$(codigo "$GW/mailboxes" -H "$A2N")" "200"

echo "== domain-service por la celda de cada empresa"
# beta (pe-02) da de alta beta.test y la prueba publica sus registros en el DNS de la prueba. El
# domain-service sin celdas (primer gateway) llama a las instancias de pe-01, que rechazan a beta:
# el paso falla como error de configuracion y pe-01 no guarda nada. El de las celdas (segundo
# gateway) activa el dominio en mail_cell_pe_02; mail-security no declara pe-02, asi que las
# claves DKIM no salen hacia ninguna instancia y el paso queda para el barrido.
AB="Authorization: Bearer $(e2e_login admin@beta.test "$CELDAS_PASS" | jget data.access_token)"
BDOM=$(curl -s -X POST "$GW2/domains" -H "$AB" -H 'Content-Type: application/json' -d '{"domain":"beta.test","purpose":"corporate"}')
BDOMID=$(echo "$BDOM" | jget data.id)
expect "alta de beta.test en domain-service (pending)" "$(echo "$BDOM" | jget data.status)" "pending"
echo "$BDOM" | jget data.dns_records >"$WORK/zona.json.tmp" && mv "$WORK/zona.json.tmp" "$WORK/zona.json"
# errores_de <fragmento>: de los integration_errors de la verificacion en stdin, cuantos hay y
# cuantos llevan el fragmento.
errores_de() {
  python3 -c 'import json, sys; e = json.load(sys.stdin)["data"]["integration_errors"]; print(len(e), sum(sys.argv[1] in x for x in e))' "$1" 2>/dev/null
}
en_pe01() { sql mail_cell_pe_01 "SELECT count(*) FROM mail.domains WHERE domain = 'beta.test' OR tenant_id = '$BID'"; }
V1=$(curl -s -X POST "$GW/domains/$BDOMID/verify" -H "$AB")
expect "domain-service sin celdas verifica beta.test contra el DNS de la prueba" "$(echo "$V1" | jget data.status)" "verified"
expect "la activacion y las claves no se dan por hechas: pe-01 no atiende a beta" "$(echo "$V1" | errores_de TENANT_NOT_IN_CELL)" "2 2"
expect "y pe-01 no guarda nada de beta.test" "$(en_pe01)" "0"
M_DS=$(curl -s "http://127.0.0.1:${PORT[domain-service]}/metrics")
# Tres llamadas a mail-directory rechazadas por la celda: la activacion y dos lecturas de la version de la
# politica MTA-STS para el TXT _mta-sts (estas ultimas no dan error de integracion: solo dejan ese TXT fuera).
contains "domain-service cuenta la instancia de otra celda en mail-directory" "$M_DS" 'cell_call_failures_total{cell_service="mail-directory",reason="not_in_cell"} 3'
contains "y en mail-security" "$M_DS" 'cell_call_failures_total{cell_service="mail-security",reason="not_in_cell"} 1'
V2=$(curl -s -X POST "$GW2/domains/$BDOMID/verify" -H "$AB")
expect "domain-service de las celdas lo verifica" "$(echo "$V2" | jget data.status)" "verified"
expect "y lo activa en mail_cell_pe_02 con la empresa de beta" \
  "$(sql mail_cell_pe_02 "SELECT active FROM mail.domains WHERE domain = 'beta.test' AND tenant_id = '$BID'")" "t"
expect "sin escribir nada en mail_cell_pe_01" "$(en_pe01)" "0"
expect "las claves DKIM no salen hacia ninguna instancia: mail-security no declara pe-02" \
  "$(echo "$V2" | errores_de 'celda pe-02: la celda de la empresa no tiene instancia declarada')" "1 1"
M_DS2=$(curl -s "http://127.0.0.1:$DS2_PORT/metrics")
contains "y lo cuenta como celda sin instancia" "$M_DS2" 'cell_call_failures_total{cell_service="mail-security",reason="not_served"} 1'
contains "sin llamar a ninguna instancia de otra celda" "$M_DS2" 'cell_call_failures_total{cell_service="mail-directory",reason="not_in_cell"} 0'

echo "== Webmail por celda (indice global de dominios y celda en el token)"
# domain-service reclamo beta.test en el indice global de organization al activarlo en pe-02. Un
# webmail por celda, cada uno con su mail-auth y su CELL_CODE; sin IMAP ni SMTP: aqui solo se abre,
# se usa y se cierra la sesion. Cada inicio sale de su propia IP de documentacion para no gastar el
# cupo estricto de 127.0.0.1.
ORG_HOST="http://127.0.0.1:${PORT[organization]}/internal/organization"
expect "beta.test queda en el indice global con la empresa de beta" \
  "$(sql mail_registry "SELECT tenant_id FROM organization.mail_domain_cells WHERE domain = 'beta.test'")" "$BID"
expect "organization da al gateway la celda de beta.test" \
  "$(curl -s "$ORG_HOST/mail-domains/beta.test/cell" -H "X-Gateway-Token: $INTERNAL_GATEWAY_TOKEN" | jget data.cell_code)" "pe-02"
expect "y no deja que otra empresa lo active" \
  "$(sesion -X PUT "$ORG_HOST/tenants/$ACME_ID/mail-domains/beta.test" -H "X-Gateway-Token: $INTERNAL_GATEWAY_TOKEN")" "MAIL_DOMAIN_CLAIMED 409"
env -u POSTGRES_PASSWORD -u JWT_SIGNING_KEY CELL_CODE=pe-02 CELL_DB_NAME=mail_cell_pe_02 CELL_DB_PASSWORD="$CELL2_PASS" \
  MAIL_AUTH_PORT="$MA2_PORT" MAIL_AUTH_TLS_PORT="$MA2_TLS_PORT" "$WORK/bin/mail-auth" >"$WORK/log/mail-auth-pe-02.log" 2>&1 &
WM_MASTER_USER="e2e-webmail@platform.local"
WM_MASTER_PASS=$(rand_hex 20)
# webmail_de <celda> <puerto> <puerto TLS de su mail-auth> <mail-directory de su celda>. El webmail
# no abre ninguna base, pero config.Load exige una credencial: conserva la de la prueba. Esta prueba
# no llega a IMAP, SMTP ni mail-dav: sus direcciones son obligatorias y quedan inalcanzables.
webmail_de() {
  env -u JWT_SIGNING_KEY CELL_CODE="$1" WEBMAIL_PORT="$2" MAIL_AUTH_URL="https://127.0.0.1:$3" \
    MAIL_DIRECTORY_URL="$4" MAIL_DAV_URL=http://127.0.0.1:1 WEBMAIL_IMAP_ADDR=127.0.0.1:1 WEBMAIL_IMAP_TLS=none WEBMAIL_SMTP_ADDR=127.0.0.1:1 \
    WEBMAIL_TLS_INSECURE_SKIP_VERIFY=true WEBMAIL_ALLOW_UNSCANNED_ATTACHMENTS=true NATS_URL=nats://127.0.0.1:1 \
    WEBMAIL_MASTER_USER="${WM_MASTER_USER}" WEBMAIL_MASTER_PASSWORD="${WM_MASTER_PASS}" \
    "$WORK/bin/webmail" >"$WORK/log/webmail-$1.log" 2>&1 &
}
webmail_de pe-01 "${PORT[webmail]}" "$AUTH_TLS_PORT" "$MAIL_DIRECTORY_URL"
webmail_de pe-02 "$WM2_PORT" "$MA2_TLS_PORT" "http://127.0.0.1:$MD2_PORT"
esperar_salud mail-auth-pe-02 "$MA2_PORT"
esperar_salud webmail-pe-01 "${PORT[webmail]}"
esperar_salud webmail-pe-02 "$WM2_PORT"

AB="Authorization: Bearer $(e2e_login admin@beta.test "$CELDAS_PASS" | jget data.access_token)"
EVA_PASS="$(rand_hex 10)Aa1!"
expect "alta del buzon eva@beta.test en pe-02 por el gateway de las celdas" "$(curl -s -X POST "$GW2/mailboxes" -H "$AB" -H 'Content-Type: application/json' \
  -d "{\"local_part\":\"eva\",\"domain\":\"beta.test\",\"password\":\"$EVA_PASS\",\"display_name\":\"Eva Rios\"}" | jget data.username)" "eva@beta.test"
# wm <url> <tarro> <metodo> [curl...]: codigo HTTP; el cuerpo queda en $WORK/wm.json.
wm() {
  local url="$1" tarro="$2" metodo="$3"
  shift 3
  curl -s -o "$WORK/wm.json" -w '%{http_code}' -b "$tarro" -c "$tarro" -X "$metodo" "$url" -H "Origin: $API_ORIGIN" -H 'X-Real-IP: 198.51.100.40' "$@"
}
# entrar_wm <gateway> <tarro> <buzon> <contrasena>
entrar_wm() { wm "$1/webmail/session" "$2" POST -H 'Content-Type: application/json' -d "{\"username\":\"$3\",\"password\":\"$4\"}"; }
celda_de_la_cookie() { awk '$6 == "cf_wm" {print $7}' "$1" | tail -1 | cut -d. -f1; }
TARRO_EVA="$WORK/eva.cookies"
expect "eva inicia sesion en el webmail por el gateway de las celdas" \
  "$(entrar_wm "$GW2" "$TARRO_EVA" eva@beta.test "$EVA_PASS")/$(jget data.username <"$WORK/wm.json")" "200/eva@beta.test"
expect "su cookie lleva la celda pe-02" "$(celda_de_la_cookie "$TARRO_EVA")" "pe-02"
contains "la abrio el webmail de pe-02" "$(cat "$WORK/log/webmail-pe-02.log")" '"username":"eva@beta.test"'
expect "y el gateway la lleva despues a su celda por el token" \
  "$(wm "$GW2/webmail/session" "$TARRO_EVA" GET)/$(jget data.username <"$WORK/wm.json")" "200/eva@beta.test"
sed 's/\tpe-02\./\tpe-01./' "$TARRO_EVA" >"$WORK/eva-cambiada.cookies"
CAMBIADA=$(curl -s -D - -o "$WORK/wm.json" -b "$WORK/eva-cambiada.cookies" "$GW2/webmail/session" -H 'X-Real-IP: 198.51.100.40')
expect "con la celda del token cambiada a pe-01 la sesion no existe" "$(jget error.code <"$WORK/wm.json") $(head -1 <<<"$CAMBIADA" | cut -d' ' -f2)" "SESSION_EXPIRED 401"
contains "y la cookie se borra" "$CAMBIADA" "cf_wm=;"
# El gateway sin celdas lo lleva todo a pe-01: el token real de pe-02 llega a la instancia de otra
# celda, que lo rechaza sin buscarlo (segunda barrera).
AJENA=$(curl -s -D - -o "$WORK/wm.json" -b "$TARRO_EVA" "$GW/webmail/session" -H 'X-Real-IP: 198.51.100.40')
expect "por el gateway sin celdas el token de pe-02 llega a pe-01, que no lo acepta" "$(jget error.code <"$WORK/wm.json") $(head -1 <<<"$AJENA" | cut -d' ' -f2)" "SESSION_EXPIRED 401"
contains "sin buscarlo en su almacen" "$(cat "$WORK/log/webmail-pe-01.log")" '"token_cell":"pe-02"'
entrar_wm "$GW2" "$WORK/nadie.cookies" nadie@desconocido.test "no-es-$EVA_PASS" >/dev/null
DESCONOCIDO=$(cat "$WORK/wm.json")
expect "una contrasena mala de eva recibe 401" "$(entrar_wm "$GW2" "$WORK/eva-mala.cookies" eva@beta.test "no-es-$EVA_PASS")" "401"
expect "y un dominio desconocido recibe exactamente la misma respuesta" "$DESCONOCIDO" "$(cat "$WORK/wm.json")"
TARRO_ANA="$WORK/ana.cookies"
expect "ana (acme.test, fuera del indice) entra en la celda base por el gateway de las celdas" \
  "$(entrar_wm "$GW2" "$TARRO_ANA" ana@acme.test "$MBX_PASS")" "200"
expect "con la celda pe-01 en su cookie" "$(celda_de_la_cookie "$TARRO_ANA")" "pe-01"
expect "por el gateway sin celdas eva no entra: la celda base no conoce su buzon" \
  "$(entrar_wm "$GW" "$WORK/eva-sin-celdas.cookies" eva@beta.test "$EVA_PASS")/$(jget error.code <"$WORK/wm.json")" "401/INVALID_CREDENTIALS"
expect "eva cierra sesion" "$(wm "$GW2/webmail/session" "$TARRO_EVA" DELETE)" "204"
expect "y su cookie ya no abre nada" "$(wm "$GW2/webmail/session" "$TARRO_EVA" GET)" "401"
M_GW2=$(curl -s "http://127.0.0.1:$GW2_PORT/metrics")
contains "el gateway de las celdas no dejo ningun inicio de sesion sin celda" "$M_GW2" 'cell_routing_failures_total{cell_service="webmail",reason="not_served"} 0'
contains "ni sin resolver, con las series del webmail a cero desde el arranque" "$M_GW2" 'cell_routing_failures_total{cell_service="webmail",reason="unresolved"} 0'

echo "== Claves DKIM solo de dominios activos en la celda"
# mail-security de pe-01 acepta claves solo de un dominio activo en el directorio de su celda, se
# las quita cuando el directorio lo desactiva (mail.domain.*) y su repaso retira las que queden de
# un dominio que la celda no sirve.
dkim_redis() { docker exec "$E2E_PREFIX-redis" redis-cli "$@"; }
dkim_activar() {
  curl -s -o /dev/null -w '%{http_code}' -X PUT "http://127.0.0.1:${PORT[mail-directory]}/internal/mail-directory/domains/$1/activation" \
    -H "X-Gateway-Token: $INTERNAL_GATEWAY_TOKEN" -H "X-Tenant-ID: $TID" -H 'Content-Type: application/json' -d "{\"active\":$2}"
}
dkim_publicar() {
  curl -s -w ' %{http_code}' -X PUT "http://127.0.0.1:${PORT[mail-security]}/internal/mail-security/dkim/$1" \
    -H "X-Gateway-Token: $INTERNAL_GATEWAY_TOKEN" -H "X-Tenant-ID: $TID" -H 'Content-Type: application/json' \
    -d '{"keys":[{"selector":"e2e1","private_key_pem":"-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"}]}'
}
# dkim_sin_claves <dominio>: si en 30 s no queda ni su clave ni su selector en los motores.
dkim_sin_claves() {
  for _ in $(seq 1 60); do
    [[ "$(dkim_redis HEXISTS DKIM_PRIV_KEYS "e2e1.$1")$(dkim_redis HEXISTS DKIM_SELECTORS "$1")" == "00" ]] && { echo si; return; }
    sleep 0.5
  done
  echo no
}
expect "claves.test activo en el directorio de pe-01" "$(dkim_activar claves.test true)" "200"
expect "mail-security acepta el juego de claves de un dominio activo" "$(dkim_publicar claves.test | tail -c 3)" "204"
expect "y lo deja firmando" "$(dkim_redis HGET DKIM_SELECTORS claves.test)" "e2e1"
contains "rechaza las de un dominio que la celda no sirve" "$(dkim_publicar fantasma.test)" '"code":"DKIM_DOMAIN_NOT_ACTIVE"'
expect "sin escribir nada" "$(dkim_redis HEXISTS DKIM_SELECTORS fantasma.test)" "0"
expect "claves.test desactivado en el directorio" "$(dkim_activar claves.test false)" "200"
expect "el evento del directorio le quita las claves" "$(dkim_sin_claves claves.test)" "si"
dkim_redis HSET DKIM_PRIV_KEYS e2e1.fantasma.test x >/dev/null
dkim_redis HSET DKIM_SELECTORS fantasma.test e2e1 >/dev/null
expect "el repaso retira la clave huerfana de un dominio fuera del directorio" "$(dkim_sin_claves fantasma.test)" "si"
# dkim_metricas: si en 10 s mail-security expone las dos series de retiradas, alguna not_served y
# una pasada completa.
dkim_metricas() {
  for _ in $(seq 1 20); do
    curl -s "http://127.0.0.1:${PORT[mail-security]}/metrics" | awk '
      $1 == "mail_security_dkim_reconcile_removals_total{reason=\"not_served\"}" && $2 >= 1 { r = 1 }
      $1 == "mail_security_dkim_reconcile_removals_total{reason=\"tenant_gone\"}" { g = 1 }
      $1 == "mail_security_dkim_reconcile_last_success_timestamp_seconds" && $2 > 0 { s = 1 }
      END { exit !(r && g && s) }' && { echo si; return; }
    sleep 0.5
  done
  echo no
}
expect "y lo cuenta en las metricas de mail-security, con su ultima pasada completa" "$(dkim_metricas)" "si"

echo "== Rotacion y revocacion de claves DKIM (domain-service)"
# acme da de alta firma.test, la prueba lo publica en su DNS y domain-service lo verifica: lo activa
# en el directorio de pe-01 y mail-security escribe su clave en el Redis de la prueba. Una rotacion
# programada sigue firmando con la anterior; revocar por compromiso deja en el Redis solo la clave
# nueva al volver la respuesta, con motivo y actor en el historial del dominio y en la auditoria.
AF="Authorization: Bearer $(e2e_login admin@acme.test "$TENANT_PASS" | jget data.access_token)"
FDOM=$(curl -s -X POST "$GW/domains" -H "$AF" -H 'Content-Type: application/json' -d '{"domain":"firma.test","purpose":"corporate"}')
FDOMID=$(echo "$FDOM" | jget data.id)
K1=$(echo "$FDOM" | jget data.dkim_selector)
python3 -c 'import json, sys
z = json.load(open(sys.argv[1])) + json.load(sys.stdin)["data"]["dns_records"]
json.dump(z, open(sys.argv[1] + ".tmp", "w"))' "$WORK/zona.json" <<<"$FDOM" && mv "$WORK/zona.json.tmp" "$WORK/zona.json"
FV=$(curl -s -X POST "$GW/domains/$FDOMID/verify" -H "$AF")
expect "domain-service verifica firma.test y lo activa en pe-01 sin errores de celda" "$(echo "$FV" | jget data.status)/$(echo "$FV" | errores_de x)" "verified/0 0"
expect "mail-security escribe su clave en los motores" "$(dkim_redis HGET DKIM_SELECTORS firma.test)" "$K1"
K2=$(curl -s -X POST "$GW/domains/$FDOMID/rotate-dkim" -H "$AF" | jget data.dkim_selector)
expect "rotacion programada: la nueva en los motores y firmando la anterior" \
  "$(dkim_redis HEXISTS DKIM_PRIV_KEYS "$K2.firma.test")/$(dkim_redis HGET DKIM_SELECTORS firma.test)" "1/$K1"
expect "con la anterior en gracia no se rota otra vez" "$(sesion -X POST "$GW/domains/$FDOMID/rotate-dkim" -H "$AF")" "DKIM_ROTATION_IN_PROGRESS 409"
MOTIVO="clave expuesta en la prueba $(rand_hex 4)"
revocar() {
  curl -s -X POST "$GW/domains/$FDOMID/revoke-dkim" -H "$AF" -H 'Content-Type: application/json' \
    -d "{\"current_selector\":\"$1\",\"reason\":\"$2\"}" "${@:3}"
}
expect "revocar exige el selector actual" "$(sesion -X POST "$GW/domains/$FDOMID/revoke-dkim" -H "$AF" -H 'Content-Type: application/json' \
  -d "{\"current_selector\":\"$K1\",\"reason\":\"$MOTIVO\"}")" "DKIM_SELECTOR_NOT_CURRENT 409"
expect "y un motivo" "$(sesion -X POST "$GW/domains/$FDOMID/revoke-dkim" -H "$AF" -H 'Content-Type: application/json' \
  -d "{\"current_selector\":\"$K2\",\"reason\":\"   \"}")" "VALIDATION_ERROR 422"
REV=$(revocar "$K2" "$MOTIVO")
K3=$(echo "$REV" | jget data.dkim_selector)
expect "revocacion confirmada por la celda" "$(echo "$REV" | jget data.engines_retired)" "True"
expect "al volver la respuesta el Redis de los motores ya no tiene ninguna clave revocada" \
  "$(dkim_redis HEXISTS DKIM_PRIV_KEYS "$K1.firma.test")$(dkim_redis HEXISTS DKIM_PRIV_KEYS "$K2.firma.test")" "00"
expect "y firma con la nueva" "$(dkim_redis HGET DKIM_SELECTORS firma.test)/$(dkim_redis HEXISTS DKIM_PRIV_KEYS "$K3.firma.test")" "$K3/1"
expect "pide retirar del DNS los dos TXT revocados" \
  "$(echo "$REV" | jget data.remove_dns_records.0.host) $(echo "$REV" | jget data.remove_dns_records.1.host)" \
  "$K2._domainkey.firma.test $K1._domainkey.firma.test"
expect "el reintento de la misma revocacion no genera otra clave" "$(revocar "$K2" "$MOTIVO" | jget data.dkim_selector)" "$K3"
YO=$(sql mail_registry "SELECT id FROM identity.users WHERE email = 'admin@acme.test'")
FICHA=$(curl -s "$GW/domains/$FDOMID" -H "$AF")
expect "el historial del dominio guarda la revocacion con su motivo y quien la pidio" \
  "$(echo "$FICHA" | jget data.dkim_rotations.0.kind)/$(echo "$FICHA" | jget data.dkim_rotations.0.reason)/$(echo "$FICHA" | jget data.dkim_rotations.0.actor_id)" \
  "compromised/$MOTIVO/$YO"
expect "y la rotacion programada de antes, sin repetir la revocacion" "$(echo "$FICHA" | jget data.dkim_rotations.1.kind)/$(echo "$FICHA" | jget data.dkim_rotations.2.kind)" "scheduled/"
# publicada: si en 30 s el rele de la outbox de acme entrego a NATS la revocacion con el usuario y
# el motivo. audit la guarda por AUDIT_SUBJECTS (domains.>); esta prueba no arranca audit.
publicada() {
  local n=""
  for _ in $(seq 1 60); do
    n=$(sql mail_tenant_acme "SELECT count(*) FROM platform.event_outbox WHERE subject = 'domains.domain.dkim_revoked'
      AND published_at IS NOT NULL AND payload->>'user_id' = '$YO' AND payload->'data'->>'reason' = '$MOTIVO'")
    [[ "$n" == 1 ]] && { echo si; return; }
    sleep 0.5
  done
  echo "no ($n)"
}
expect "la outbox de acme entrega a NATS una sola revocacion, con el usuario y el motivo" "$(publicada)" "si"
FVR=$(curl -s -X POST "$GW/domains/$FDOMID/verify" -H "$AF")
expect "verificar a mano antes de publicar el TXT nuevo no apaga el dominio" "$(echo "$FVR" | jget data.outcome)/$(echo "$FVR" | jget data.status)" "failed/verified"
expect "que sigue activo en el directorio de pe-01" "$(sql mail_cell_pe_01 "SELECT active FROM mail.domains WHERE domain = 'firma.test'")" "t"

echo "== Publicacion automatica del DNS con Cloudflare (domain-service)"
# acme da de alta nube.test, que como todo dominio publica su DNS a mano. Conecta Cloudflare una
# vez con un token que se valida contra el Cloudflare falso y se guarda cifrado sin volver a salir;
# pasa nube.test a automatico y publica: el SPF que ya tenia queda en conflicto hasta que confirma
# reemplazarlo, y entonces la verificacion real (DNS de la prueba) lo da por bueno. Rotar y revocar
# DKIM publican y retiran solas sus TXT; desconectar borra el token y devuelve el dominio a manual.
CF="$GW/domains/dns-providers/cloudflare"
cf_registros() { curl -s "http://127.0.0.1:$CF_PORT/__registros?zone=nube.test"; }
# en_zona <host>: cuantos TXT tiene ese nombre en la zona que sirve el DNS de la prueba.
en_zona() { python3 -c 'import json, sys; print(sum(1 for e in json.load(open(sys.argv[1])) if e["host"] == sys.argv[2] and e["type"] == "TXT"))' "$WORK/zona.json" "$1"; }
accion() { python3 -c 'import json, sys; d = json.load(sys.stdin)["data"]; p = d.get("dns_publication") or (d.get("dns_automation") or {}).get("publication") or {}; print(",".join(r["record"] + "=" + r["action"] for r in p.get("records", [])))' 2>/dev/null; }
NDOM=$(curl -s -X POST "$GW/domains" -H "$AF" -H 'Content-Type: application/json' -d '{"domain":"nube.test","purpose":"corporate"}')
NDOMID=$(echo "$NDOM" | jget data.id)
expect "un dominio nuevo publica su DNS a mano" "$(echo "$NDOM" | jget data.dns_mode)" "manual"
expect "acme no tiene Cloudflare conectado" "$(curl -s "$CF" -H "$AF" | jget data.connected)" "False"
expect "sin conexion no pasa a automatico" \
  "$(sesion -X POST "$GW/domains/$NDOMID/dns-mode" -H "$AF" -H 'Content-Type: application/json' -d '{"mode":"cloudflare"}')" "DNS_PROVIDER_NOT_CONNECTED 409"
expect "ni publica" "$(sesion -X POST "$GW/domains/$NDOMID/publish-dns" -H "$AF")" "DNS_MODE_MANUAL 409"
conectar() { curl -s -w ' %{http_code}' -X POST "$CF/connect" -H "$AF" -H 'Content-Type: application/json' -d "{\"api_token\":\"$1\"}"; }
for caso in "desconocido:$CF_TOKEN_DESCONOCIDO:DNS_PROVIDER_TOKEN_INVALID 422" "deshabilitado:$CF_TOKEN_DESHABILITADO:DNS_PROVIDER_TOKEN_INVALID 422" \
  "que no puede leer zonas:$CF_TOKEN_SIN_PERMISO:DNS_PROVIDER_PERMISSION_DENIED 422"; do
  IFS=: read -r nombre tok esperado <<<"$caso"
  r=$(conectar "$tok")
  expect "Cloudflare no acepta un token $nombre" "$(echo "${r% *}" | jget error.code) ${r##* }" "$esperado"
  lacks "y la respuesta no lo repite" "$r" "$tok"
done
expect "un token rechazado no se guarda" "$(sql mail_tenant_acme "SELECT count(*) FROM domains.dns_providers")" "0"
CONN=$(conectar "$CF_TOKEN")
expect "conecta con un token activo y ve todas sus zonas, en varias paginas" \
  "$(echo "${CONN% *}" | jget data.connected)/$(echo "${CONN% *}" | jget data.zones_visible)/$(echo "${CONN% *}" | jget data.token_hint) ${CONN##* }" \
  "True/61/${CF_TOKEN: -4} 200"
lacks "la respuesta de conectar no lleva el token" "$CONN" "$CF_TOKEN"
ESTADO=$(curl -s "$CF" -H "$AF")
expect "el estado dice quien la conecto" "$(echo "$ESTADO" | jget data.connected_by)" "$YO"
lacks "y tampoco lleva el token" "$ESTADO" "$CF_TOKEN"
expect "el token se guarda cifrado: ninguna columna lo contiene en claro" \
  "$(sql mail_tenant_acme "SELECT count(*) FROM domains.dns_providers p WHERE position(convert_to('$CF_TOKEN', 'UTF8') IN p.api_token_enc) = 0 AND (p.*)::text NOT LIKE '%$CF_TOKEN%'")" "1"
expect "la conexion se anuncia por la outbox sin el token" \
  "$(sql mail_tenant_acme "SELECT count(*) FROM platform.event_outbox WHERE subject = 'domains.dns_provider.connected' AND payload->>'user_id' = '$YO' AND payload::text NOT LIKE '%$CF_TOKEN%'")" "1"
lacks "ningun registro de domain-service lleva el token" "$(cat "$WORK/log/domain-service.log" "$WORK/log/gateway.log" "$WORK/log/cloudflare.log")" "$CF_TOKEN"
expect "firma.test no esta en ninguna zona que vea el token" \
  "$(sesion -X POST "$GW/domains/$FDOMID/dns-mode" -H "$AF" -H 'Content-Type: application/json' -d '{"mode":"cloudflare"}')" "DNS_ZONE_NOT_FOUND 409"
expect "nube.test pasa a automatico" \
  "$(curl -s -X POST "$GW/domains/$NDOMID/dns-mode" -H "$AF" -H 'Content-Type: application/json' -d '{"mode":"cloudflare"}' | jget data.dns_mode)" "cloudflare"
P1=$(curl -s -X POST "$GW/domains/$NDOMID/publish-dns" -H "$AF" -H 'Content-Type: application/json' -d '{}')
expect "publicar crea lo que falta y deja en conflicto el SPF del cliente" "$(echo "$P1" | accion)" \
  "ownership_txt=created,mx=created,spf=conflict,dkim=created,dmarc=created"
contains "mostrando su valor" "$(echo "$P1" | jget data.dns_publication.records.2.existing.0)" "_spf.otro-proveedor.test"
expect "sin tocarlo, y la verificacion lo ve" "$(en_zona nube.test)/$(echo "$P1" | jget data.outcome)/$(echo "$P1" | jget data.status)" "2/failed/failed"
P2=$(curl -s -X POST "$GW/domains/$NDOMID/publish-dns" -H "$AF" -H 'Content-Type: application/json' -d '{"replace":["spf"]}')
expect "con la confirmacion reemplaza el SPF y el resto sigue igual" "$(echo "$P2" | accion)" \
  "ownership_txt=unchanged,mx=unchanged,spf=replaced,dkim=unchanged,dmarc=unchanged"
expect "y la verificacion real contra el DNS de la prueba pasa" \
  "$(echo "$P2" | jget data.outcome)/$(echo "$P2" | jget data.status)/$(echo "$P2" | errores_de x)" "verified/verified/0 0"
expect "el TXT ajeno del mismo nombre sigue en la zona" "$(en_zona nube.test)" "2"
expect "nube.test queda activo en el directorio de pe-01" "$(sql mail_cell_pe_01 "SELECT active FROM mail.domains WHERE domain = 'nube.test'")" "t"
expect "todo lo que publico la plataforma lleva su marca" \
  "$(cf_registros | python3 -c 'import json, sys; r = json.load(sys.stdin)["result"]; print(sum(1 for x in r if x["comment"] == "cfm-managed"), len(r))')" "5 6"
P3=$(curl -s -X POST "$GW/domains/$NDOMID/publish-dns" -H "$AF")
expect "publicar otra vez no toca nada" "$(echo "$P3" | accion)" \
  "ownership_txt=unchanged,mx=unchanged,spf=unchanged,dkim=unchanged,dmarc=unchanged"
N1=$(echo "$NDOM" | jget data.dkim_selector)
ROT=$(curl -s -X POST "$GW/domains/$NDOMID/rotate-dkim" -H "$AF")
N2=$(echo "$ROT" | jget data.dkim_selector)
expect "rotar en automatico publica sola la clave nueva y conserva la anterior" \
  "$(echo "$ROT" | accion)/$(echo "$ROT" | jget data.dns_automation.error_code)/$(en_zona "$N2._domainkey.nube.test")/$(en_zona "$N1._domainkey.nube.test")" \
  "dkim=created,dkim_previous=unchanged//1/1"
REVN=$(curl -s -X POST "$GW/domains/$NDOMID/revoke-dkim" -H "$AF" -H 'Content-Type: application/json' \
  -d "{\"current_selector\":\"$N2\",\"reason\":\"prueba de publicacion automatica\"}")
N3=$(echo "$REVN" | jget data.dkim_selector)
expect "revocar en automatico retira solos los dos TXT revocados y publica el nuevo" \
  "$(en_zona "$N1._domainkey.nube.test")/$(en_zona "$N2._domainkey.nube.test")/$(en_zona "$N3._domainkey.nube.test")/$(echo "$REVN" | jget data.dns_automation.publication.removed.1)" \
  "0/0/1/$N1._domainkey.nube.test"
expect "con el TXT nuevo ya publicado el dominio verifica" "$(curl -s -X POST "$GW/domains/$NDOMID/verify" -H "$AF" | jget data.outcome)" "verified"
expect "cada publicacion sale por la outbox" \
  "$(sql mail_tenant_acme "SELECT count(*) FROM platform.event_outbox WHERE subject = 'domains.domain.dns_published' AND payload->'data'->>'domain' = 'nube.test'")" "5"
DIS=$(curl -s -X POST "$CF/disconnect" -H "$AF")
expect "desconectar borra el token y devuelve nube.test a manual" \
  "$(echo "$DIS" | jget data.disconnected)/$(echo "$DIS" | jget data.domains_reset)/$(sql mail_tenant_acme "SELECT count(*) FROM domains.dns_providers")/$(curl -s "$GW/domains/$NDOMID" -H "$AF" | jget data.dns_mode)" \
  "True/1/0/manual"
expect "y deja lo publicado en la zona" "$(en_zona "$N3._domainkey.nube.test")" "1"
expect "rotar en manual ya no escribe en Cloudflare" \
  "$(curl -s -X POST "$GW/domains/$NDOMID/rotate-dkim" -H "$AF" | jget data.dns_automation)/$(cf_registros | python3 -c 'import json, sys; print(len(json.load(sys.stdin)["result"]))')" "/6"

echo "== Baja de una empresa con correo en su celda"
# La saga de baja de organization da de baja a beta en el mail-directory de pe-02, la instancia de
# su celda, antes de retirarla del registro, y despues suelta beta.test del indice global. El buzon
# de eva deja de autenticar en el mail-auth de su celda y el dominio se puede volver a reclamar.
eva_en_pe02() {
  curl -sk -o /dev/null -w '%{http_code}' -X POST "https://127.0.0.1:$MA2_TLS_PORT/" -H 'Content-Type: application/json' \
    -d "{\"username\":\"eva@beta.test\",\"password\":\"$EVA_PASS\",\"real_rip\":\"10.20.0.6\",\"service\":\"imap\"}"
}
expect "antes de la baja eva autentica por IMAP en el mail-auth de pe-02" "$(eva_en_pe02)" "200"
AS="Authorization: Bearer $(e2e_login "$ADMIN_EMAIL" "$ADMIN_PASS" | jget data.access_token)"
expect "una empresa activa no se borra" "$(codigo -X DELETE "$GW/organizations/$BID" -H "$AS")" "409"
expect "el superadmin deja a beta inactiva" \
  "$(curl -s -X PATCH "$GW/organizations/$BID" -H "$AS" -H 'Content-Type: application/json' -d '{"status":"inactive"}' | jget data.status)" "inactive"
expect "y la borra" "$(codigo -X DELETE "$GW/organizations/$BID" -H "$AS")" "204"
expect "beta sale del registro" "$(sql mail_registry "SELECT count(*) FROM organization.tenants WHERE id = '$BID'")" "0"
expect "y su base se conserva" "$(sql mail_registry "SELECT count(*) FROM pg_database WHERE datname = 'mail_tenant_beta'")" "1"
expect "beta.test queda inactivo en el directorio de pe-02" "$(sql mail_cell_pe_02 "SELECT active FROM mail.domains WHERE domain = 'beta.test'")" "f"
expect "y el buzon de eva tambien" "$(sql mail_cell_pe_02 "SELECT active FROM mail.mailboxes WHERE username = 'eva@beta.test'")" "0"
expect "la baja queda registrada en la celda" "$(sql mail_cell_pe_02 "SELECT count(*) FROM mail.tenant_retirements WHERE tenant_id = '$BID'")" "1"
expect "y se anuncia por la outbox de pe-02" \
  "$(sql mail_cell_pe_02 "SELECT count(*) FROM platform.event_outbox WHERE tenant_id = '$BID' AND subject IN ('mail.domain.updated', 'mail.mailbox.updated') AND payload->'data'->>'active' IN ('false', '0')")" "2"
expect "sin tocar el directorio de pe-01" "$(sql mail_cell_pe_01 "SELECT active FROM mail.domains WHERE domain = 'acme.test'")" "t"
expect "eva ya no autentica en el mail-auth de su celda" "$(eva_en_pe02)" "401"
# La instancia de pe-02 rechaza la activacion con la baja (409 TENANT_RETIRED) o, si su cache de
# celdas ya caduco, porque organization no conoce a beta (403 TENANT_NOT_IN_CELL): nunca la hace.
REACT=$(sesion -X PUT "http://127.0.0.1:$MD2_PORT/internal/mail-directory/domains/beta.test/activation" \
  -H "X-Gateway-Token: $INTERNAL_GATEWAY_TOKEN" -H "X-Tenant-ID: $BID" -H 'Content-Type: application/json' -d '{"active":true}')
[[ "$REACT" == "TENANT_RETIRED 409" || "$REACT" == "TENANT_NOT_IN_CELL 403" ]] &&
  ok "la activacion de domain-service ya no enciende beta.test ($REACT)" || mal "reactivar beta.test tras la baja: $REACT"
expect "y beta.test sigue inactivo" "$(sql mail_cell_pe_02 "SELECT active FROM mail.domains WHERE domain = 'beta.test'")" "f"
expect "beta.test sale del indice global" "$(sql mail_registry "SELECT count(*) FROM organization.mail_domain_cells WHERE domain = 'beta.test'")" "0"
expect "y otra empresa puede reclamarlo" \
  "$(curl -s -X PUT "$ORG_HOST/tenants/$ACME_ID/mail-domains/beta.test" -H "X-Gateway-Token: $INTERNAL_GATEWAY_TOKEN" | jget data.cell_code)" "pe-01"
expect "(la prueba lo suelta)" "$(codigo -X DELETE "$ORG_HOST/tenants/$ACME_ID/mail-domains/beta.test" -H "X-Gateway-Token: $INTERNAL_GATEWAY_TOKEN")" "204"

echo "== Registros"
# Los unicos errores esperados son los que la prueba provoca a proposito: los pasos de
# domain-service para beta que no llegan a la instancia de su celda.
e2e_registros_sin_errores "\"tenant_id\":\"$BID\".*(TENANT_NOT_IN_CELL|no tiene instancia declarada)"

echo
if [[ $fallos -gt 0 ]]; then echo "E2E: $fallos fallos" >&2; exit 1; fi
echo "E2E: OK"

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
# de un buzon a otro del mismo Dovecot, con ClamAV real en el camino, y CardDAV y CalDAV: mail-dav (binario del host,
# con la credencial propia de su esquema) autentica cada peticion contra el listener TLS de mail-auth y el
# gateway lo expone sin JWT; se prueban PROPFIND, PUT, REPORT, MKCALENDAR y DELETE con la contrasena de aplicacion, el
# aislamiento entre buzones, las politicas de fila y dav_access. Tambien el maildir de un buzon borrado: la marca
# de baja, el barrido de Dovecot (maildir_reconcile.sh) que lo mueve a _garbage, su fail-closed y que el buzon
# recreado con el mismo nombre nace vacio. Y el webmail de la fase 2 (docs/Plan_Webmail_Innovador.md): conversaciones,
# bandeja inteligente, escudo antifraude con correo entregado al MX, baja RFC 8058, posponer, seguimiento, respuestas
# rapidas, zona horaria y apariciones sueltas, invitaciones iTIP entre dos buzones, disponibilidad, la pagina publica
# de citas, los ficheros grandes por enlace (mail-files con ClamAV real y MinIO) y el asistente apagado sin clave.
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
  [mail-dav]=$((BASE + 58)) [mail-files]=$((BASE + 61))
)
# Listener TLS de mail-auth publicado en el host: mail-dav, que corre en el host, verifica con el a cada buzon.
AUTH_TLS_PORT=$((BASE + 45))
# API del ejecutor de mail-migration: el ejecutor, en su contenedor, la alcanza por host.docker.internal.
MIGRATION_RUNNER_PORT=$((BASE + 57))
# clamd y MinIO publicados en el host para mail-files, que corre en el host (bloque C4 del webmail).
CLAMD_PORT=$((BASE + 62)) MINIO_PORT=$((BASE + 63))
e2e_fuera_del_rango_efimero "$PG_PORT" "$NATS_PORT" "$REDIS_PORT" "$DNS_PORT" "${PORT[@]}" "$MIGRATION_RUNNER_PORT" "$AUTH_TLS_PORT" "$CLAMD_PORT" "$MINIO_PORT"

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
# Red propia del analisis antivirus de templates (la de produccion se llama mail-scan).
export E2E_SCAN_NETWORK="${E2E_SCAN_NETWORK:-cfm-e2e-mail-scan}"
export E2E_SCAN_BRIDGE="${E2E_SCAN_BRIDGE:-br-cfme2escan}"
export MAIL_SCAN_IPV4_NETWORK="${E2E_MAIL_SCAN_IPV4_NETWORK:-172.30.31}"
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
    RSPAMD_CONTROLLER_PASSWORD="${RSPAMD_PASS}" RSPAMD_CONTROLLER_ENABLE_PASSWORD="${RSPAMD_LEARN_PASS}" \
    MAIL_MIGRATION_RUNNER_KEY="${MIGRATION_KEY}" DOVECOT_MIGRATION_MASTER_USER="${MIGRATION_MASTER_USER}" DOVECOT_MIGRATION_MASTER_PASS="${MIGRATION_MASTER_PASS}" \
    E2E_PORT_MAIL_MIGRATION_RUNNER="$MIGRATION_RUNNER_PORT" \
    docker compose -p "$PROYECTO" -f "$MAILDIR/docker-compose.mail.yml" -f "$MAILDIR/docker-compose.e2e.yml" "$@"
}

restos() {
  local ids
  ids=$(docker ps -aq --filter "label=com.docker.compose.project=$PROYECTO")
  # shellcheck disable=SC2086
  docker rm -f $ids "$E2E_PREFIX-pg" "$E2E_PREFIX-nats" "$E2E_PREFIX-redis" "$E2E_PREFIX-dns" >/dev/null 2>&1
  docker network rm "$E2E_MAIL_NETWORK" "$E2E_MIGRATION_NETWORK" "$E2E_SCAN_NETWORK" >/dev/null 2>&1
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
e2e_puertos_libres "$PG_PORT" "$NATS_PORT" "$REDIS_PORT" "$DNS_PORT" "${PORT[@]}" "$MIGRATION_RUNNER_PORT" "$CLAMD_PORT" "$MINIO_PORT"
docker network inspect $(docker network ls -q) --format '{{.Name}} {{range .IPAM.Config}}{{.Subnet}} {{end}}' 2>/dev/null |
  python3 -c '
import ipaddress, sys
mias = [ipaddress.ip_network(a) for a in sys.argv[1:]]
for i, a in enumerate(mias):
    for b in mias[i + 1:]:
        if a.overlaps(b):
            sys.exit(f"E2E: las subredes {a} y {b} se pisan; usa E2E_MAIL_IPV4_NETWORK, E2E_MAIL_MIGRATION_IPV4_NETWORK o E2E_MAIL_SCAN_IPV4_NETWORK")
for linea in sys.stdin:
    nombre, *redes = linea.split()
    for r in redes:
        try:
            for mia in mias:
                if ipaddress.ip_network(r, strict=False).overlaps(mia):
                    sys.exit(f"E2E: la subred {mia} choca con la red {nombre} ({r}); usa E2E_MAIL_IPV4_NETWORK, E2E_MAIL_MIGRATION_IPV4_NETWORK o E2E_MAIL_SCAN_IPV4_NETWORK")
        except ValueError:
            pass' "$IPV4_NETWORK.0/24" "$MAIL_MIGRATION_IPV4_NETWORK.0/24" "$MAIL_SCAN_IPV4_NETWORK.0/24" || exit 2

# Copia de deploy/mail con lo versionado y lo nuevo no ignorado: los entrypoints reescriben
# ficheros de sus bind mounts (main.cf, mapas con la credencial) y el repositorio no se toca.
mkdir -p "$WORK/repo" "$WORK/log"
git ls-files -z --cached --others --exclude-standard -- deploy/mail | tar --null -T - -cf - | tar -xf - -C "$WORK/repo" \
  || { echo "no se pudo copiar deploy/mail" >&2; exit 1; }

# CA de la prueba: certificado del servidor de correo (Postfix y Dovecot, nombre MAIL_HOSTNAME)
# y de mail-auth (nombre mail-auth, y 127.0.0.1 para mail-dav, que lo alcanza desde el host), los dos
# verificados por el webmail, por mail-dav y por el cliente.
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
certificado mail-auth "$TLS/mail-auth-key.pem" "$TLS/mail-auth.pem" "IP:127.0.0.1" || { echo "openssl: certificado de mail-auth" >&2; exit 1; }
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
# Contrasenas del controller de Rspamd, como en produccion: las reciben rspamd-mail (que al arrancar guarda
# solo sus hashes, rspamd/controller-password.sh) y mail-security. La de lectura sirve la pantalla del
# superadmin; la de escritura, el aprendizaje desde la cuarentena.
RSPAMD_PASS="$(rand_hex 32)"
RSPAMD_LEARN_PASS="$(rand_hex 32)"
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
export MAIL_DAV_PORT=${PORT[mail-dav]}
for s in identity access-control organization domain-service mail-directory mail-auth mail-security webmail mail-migration mail-dav mail-files; do
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
export E2E_PORT_MAIL_SECURITY=${PORT[mail-security]} E2E_PORT_WEBMAIL=${PORT[webmail]} E2E_PORT_MAIL_AUTH_TLS=$AUTH_TLS_PORT
# mail-migration corre en el host; mail-auth le pregunta desde la red de los motores si una credencial de
# destino de un trabajo sigue viva.
export E2E_PORT_MAIL_MIGRATION=${PORT[mail-migration]}
# mail-dav corre en el host; el webmail (contenedor) le pide contactos y eventos por la pasarela de docker.
export E2E_PORT_MAIL_DAV=${PORT[mail-dav]}
# mail-files (host) y MinIO (perfil ficheros del compose de prueba) arrancan en el bloque C4 del webmail.
export E2E_PORT_MAIL_FILES=${PORT[mail-files]} E2E_PORT_CLAMD=$CLAMD_PORT E2E_PORT_MINIO=$MINIO_PORT E2E_MINIO_BUCKET=cfm-e2e-ficheros
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
# MinIO del perfil ficheros: la imagen propia (docs/adr/0016), que el compose pide con pull_policy: never.
t0=$SECONDS
if scripts/imagen-minio.sh --construir >"$WORK/log/build-minio.log" 2>&1; then ok "imagen de MinIO $(scripts/imagen-minio.sh --referencia) ($((SECONDS - t0))s)"; else
  mal "scripts/imagen-minio.sh --construir"; tail -30 "$WORK/log/build-minio.log" >&2; exit 1
fi
e2e_compilar organization identity access-control gateway domain-service mail-migration mail-dav mail-files || exit 1

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
  MAIL_MIGRATION_ALLOW_PRIVATE_SOURCES=true MAIL_MIGRATION_JOB_CREDENTIALS=true arrancar mail-migration
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
# Un buzon migrado del dominio en coexistencia: en esta plataforma un buzon NO deja una fila de alias
# con su propia direccion, asi que el transporte local tiene que mirar mail.mailboxes. Sin eso, todo
# buzon ya migrado se reenviaba al proveedor anterior y no se entregaba nunca aqui.
MIGRADO_PASS="$(rand_hex 10)Aa1!"
creado "buzon migrado del dominio en coexistencia" POST /mailboxes \
  "{\"local_part\":\"migrado\",\"domain\":\"respaldo.test\",\"password\":\"$MIGRADO_PASS\"}"
MIGRADOID=$(echo "$API_BODY" | jget data.id)

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
mapa pgsql_relay_ne migrado@respaldo.test "lmtp:inet:dovecot:24"
mapa pgsql_relay_ne nomigrado@respaldo.test ""
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
contains "la de enlace no valido, servida por mail-security" "$(cat "$WORK/q-firma.html")" "Enlace no válido"
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
echo "== Avisos en tiempo real (webmail: SSE por el gateway, alimentado por IMAP IDLE de Dovecot)"
SSE="$WORK/sse-bea.txt"; : >"$SSE"
sin_cookie=$(curl -s -o /dev/null -w '%{http_code}' -H "Origin: $API_ORIGIN" "$WM/events")
expect "el flujo de avisos exige sesion" "$sin_cookie" "401"
otro_origen=$(curl -s -o /dev/null -w '%{http_code}' -b "$TARRO_BEA" -H "Origin: https://malo.example" "$WM/events")
expect "y no se abre desde otro origen" "$otro_origen" "403"
curl -sN --max-time 60 -b "$TARRO_BEA" -H "Origin: $API_ORIGIN" -H 'Accept: text/event-stream' "$WM/events" >"$SSE" 2>/dev/null &
SSE_PID=$!
sse_listo() { grep -q '^event: ready' "$SSE"; }
esperar "el flujo de bea se abre (evento ready) sin que el gateway lo acumule" 15 sse_listo
T0=$(date +%s.%N)
enviar_wm ana@acme.test "$TOKEN-tiempo-real" -H "Idempotency-Key: $(rand_hex 16)"
expect "ana envia a bea por el webmail" "$WM_CODE" "202"
sse_aviso() { grep -q '^event: mailbox' "$SSE"; }
esperar "bea recibe el aviso de correo nuevo por el flujo, sin recargar" 20 sse_aviso
T1=$(date +%s.%N)
LAT=$(python3 -c "print(round(($T1) - ($T0), 1))")
python3 -c "import sys; sys.exit(0 if $LAT <= 12 else 1)" && ok "del envio al aviso pasaron ${LAT} s (tope de la prueba: 12 s)" || mal "el aviso tardo ${LAT} s"
contains "el aviso no lleva contenido del mensaje" "$(grep -A1 '^event: mailbox' "$SSE" | tr '\n' ' ')" '"folder":"INBOX"'
lacks "ni el asunto" "$(cat "$SSE")" "$TOKEN-tiempo-real"
contains "y por el sondeo la carpeta ya lo cuenta" "$(wm "$TARRO_BEA" GET /folders/INBOX/messages; echo "$WM_BODY")" "$TOKEN-tiempo-real"
kill "$SSE_PID" 2>/dev/null; wait "$SSE_PID" 2>/dev/null

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

# ── CardDAV y CalDAV (docs/adr/0004) ────────────────────────────────────────────────────────────────────────
# mail-dav corre en el host con la credencial PROPIA de su esquema (mail_svc_mail_dav, sujeta a las politicas
# de fila) y verifica a cada buzon contra el listener TLS de mail-auth, con la CA de la prueba. Todo pasa por
# el gateway: prefijo autenticado por el servicio (sin JWT), con HTTP Basic y la contrasena de aplicacion.
echo "== CardDAV: contactos por el gateway con la contrasena de aplicacion (mail-dav)"
DAV_DB_PASS="$(rand_hex 24)"
MAIL_DAV_DB_PASSWORD="${DAV_DB_PASS}" PGHOST=127.0.0.1 bash ops/db/tenant-service-role.sh --service mail-dav >"$WORK/log/dav-role.log" 2>&1 ||
  { mal "tenant-service-role.sh --service mail-dav"; tail -5 "$WORK/log/dav-role.log" >&2; }
MAIL_AUTH_URL="https://127.0.0.1:$AUTH_TLS_PORT" MAIL_DAV_TLS_CA_FILE="$TLS/ca.pem" MAIL_DAV_MAX_VCARD_BYTES=2048 MAIL_DAV_MAX_EVENT_BYTES=2048 \
  MAIL_DAV_AUTH_CACHE_TTL=2s \
  TENANT_DB_USER=mail_svc_mail_dav TENANT_DB_PASSWORD="$DAV_DB_PASS" arrancar mail-dav
esperar_salud mail-dav "${PORT[mail-dav]}" && ok "mail-dav responde"
expect "el gateway no exige JWT al prefijo dav: el desafio Basic es de mail-dav" \
  "$(curl -s -D - -o /dev/null -X PROPFIND "$GW/dav/" | grep -ci '^www-authenticate: basic')" "1"

# dav <usuario:contrasena> <metodo> <ruta> [opciones de curl]: deja DAV_CODE, DAV_BODY y DAV_HDR.
dav() {
  local cred="$1" metodo="$2" ruta="$3"
  shift 3
  DAV_CODE=$(curl -s -o "$WORK/dav.out" -D "$WORK/dav.hdr" -w '%{http_code}' -u "$cred" -X "$metodo" "$GW/dav$ruta" "$@")
  DAV_BODY=$(cat "$WORK/dav.out")
  DAV_HDR=$(cat "$WORK/dav.hdr")
}
CARDDAV_NS='xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav" xmlns:cs="http://calendarserver.org/ns/"'
REDIR=$(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' "http://127.0.0.1:${PORT[gateway]}/.well-known/carddav")
expect "/.well-known/carddav redirige al prefijo de mail-dav (RFC 6764)" "$REDIR" "301 http://127.0.0.1:${PORT[gateway]}/api/v1/dav/"

dav "ana@acme.test:$ANA_PASS" OPTIONS /
contains "OPTIONS anuncia CardDAV" "$DAV_HDR" "addressbook"
dav "ana@acme.test:$(rand_hex 6)" PROPFIND / -H 'Depth: 0'
expect "una contrasena incorrecta es un 401 con el desafio Basic" "$DAV_CODE/$(grep -ci '^www-authenticate: basic' <<<"$DAV_HDR")" "401/1"
dav "ana@acme.test:$ANA_PASS" PROPFIND / -H 'Depth: 0' -H 'Content-Type: application/xml' \
  --data "<d:propfind $CARDDAV_NS><d:prop><d:current-user-principal/></d:prop></d:propfind>"
expect "la contrasena principal de ana abre DAV (dav_access esta encendido)" "$DAV_CODE" "207"
contains "y el principal es el suyo" "$DAV_BODY" "/api/v1/dav/principals/ana@acme.test/"

# Contrasena de aplicacion solo para DAV: ni IMAP ni SMTP ni Sieve.
api POST "/mailboxes/$ANAID/app-passwords" '{"name":"contactos-e2e","imap_access":false,"pop3_access":false,"smtp_access":false,"sieve_access":false,"dav_access":true}'
ANA_DAV=$(echo "$API_BODY" | jget data.password)
ANA_DAV_ID=$(echo "$API_BODY" | jget data.app_password.id)
expect "alta de una contrasena de aplicacion de ana solo con dav_access" "$API_CODE/${ANA_DAV:+clave}" "201/clave"
ANA_APP="ana@acme.test:$ANA_DAV"
contains "no abre IMAP (mail-auth le comprueba imap_access)" "$(cliente login ana@acme.test "$ANA_DAV")" "NO"
dav "$ANA_APP" PROPFIND / -H 'Depth: 0' -H 'Content-Type: application/xml' \
  --data "<d:propfind $CARDDAV_NS><d:prop><d:current-user-principal/></d:prop></d:propfind>"
expect "pero si abre DAV" "$DAV_CODE" "207"
expect "el inicio de DAV queda en mail.sasl_logins (service dav) con la contrasena de aplicacion" \
  "$(sql mail_cell_pe_01 "SELECT count(*) > 0 FROM mail.sasl_logins WHERE username = 'ana@acme.test' AND service = 'dav' AND app_password_id IS NOT NULL")" "t"

# Descubrimiento como lo hace DAVx5: principal, home set y libretas (la de por defecto se crea sola).
dav "$ANA_APP" PROPFIND /principals/ana@acme.test/ -H 'Depth: 0' -H 'Content-Type: application/xml' \
  --data "<d:propfind $CARDDAV_NS><d:prop><c:addressbook-home-set/></d:prop></d:propfind>"
contains "el home set del principal" "$DAV_BODY" "/api/v1/dav/addressbooks/ana@acme.test/"
dav "$ANA_APP" PROPFIND /addressbooks/ana@acme.test/ -H 'Depth: 1' -H 'Content-Type: application/xml' \
  --data "<d:propfind $CARDDAV_NS><d:prop><d:resourcetype/><d:displayname/><cs:getctag/><d:sync-token/></d:prop></d:propfind>"
expect "el home lista su libreta de contactos" "$DAV_CODE" "207"
contains "la libreta es un addressbook con ctag y sync-token" "$DAV_BODY" "/addressbooks/ana@acme.test/contacts/"
contains "y tiene sync-token" "$DAV_BODY" "urn:mail-dav:sync:"

BOOK=/addressbooks/ana@acme.test/contacts
CARD_UID="e2e-$(rand_hex 6)"
CARD_URL="$BOOK/$CARD_UID.vcf"
tarjeta() { printf 'BEGIN:VCARD\r\nVERSION:3.0\r\nUID:%s\r\nFN:%s\r\nN:Contacto;%s;;;\r\nEMAIL;TYPE=WORK:%s@ejemplo.test\r\nEND:VCARD\r\n' "$CARD_UID" "$1" "$1" "$CARD_UID"; }
tarjeta "Carla Prueba" >"$WORK/tarjeta1.vcf"
dav "$ANA_APP" PUT "$CARD_URL" -H 'Content-Type: text/vcard; charset=utf-8' -H 'If-None-Match: *' --data-binary "@$WORK/tarjeta1.vcf"
expect "PUT crea el contacto (If-None-Match: *)" "$DAV_CODE" "201"
ETAG1=$(grep -i '^etag:' <<<"$DAV_HDR" | tr -d '\r' | sed 's/^[^:]*: *//')
[[ "$ETAG1" == \"*\" ]] && ok "y devuelve su ETag" || mal "PUT sin ETag valido: '$ETAG1'"
dav "$ANA_APP" PUT "$CARD_URL" -H 'Content-Type: text/vcard; charset=utf-8' -H 'If-None-Match: *' --data-binary "@$WORK/tarjeta1.vcf"
expect "crearlo otra vez con If-None-Match: * es un 412" "$DAV_CODE" "412"
dav "$ANA_APP" GET "$CARD_URL"
expect "GET devuelve exactamente lo guardado" "$(cmp -s "$WORK/dav.out" "$WORK/tarjeta1.vcf" && echo igual)" "igual"
tarjeta "Carla Editada" >"$WORK/tarjeta2.vcf"
dav "$ANA_APP" PUT "$CARD_URL" -H 'Content-Type: text/vcard; charset=utf-8' -H 'If-Match: "0000"' --data-binary "@$WORK/tarjeta2.vcf"
expect "editar con un If-Match viejo es un 412" "$DAV_CODE" "412"
dav "$ANA_APP" PUT "$CARD_URL" -H 'Content-Type: text/vcard; charset=utf-8' -H "If-Match: $ETAG1" --data-binary "@$WORK/tarjeta2.vcf"
expect "y con el ETag vigente es un 204" "$DAV_CODE" "204"
dav "$ANA_APP" PUT "$BOOK/invalido.vcf" -H 'Content-Type: text/vcard; charset=utf-8' --data 'esto no es un vCard'
expect "un cuerpo que no es un vCard es un 403 (valid-address-data)" "$DAV_CODE" "403"
head -c 4096 /dev/zero | tr '\0' 'A' >"$WORK/enorme.vcf"
dav "$ANA_APP" PUT "$BOOK/enorme.vcf" -H 'Content-Type: text/vcard; charset=utf-8' --data-binary "@$WORK/enorme.vcf"
expect "un vCard mas grande que el limite (2048 bytes en esta prueba) se rechaza (413)" "$DAV_CODE" "413"

# Sincronizacion como iOS y DAVx5: sync-collection inicial, multiget y consulta.
dav "$ANA_APP" REPORT "$BOOK/" -H 'Depth: 0' -H 'Content-Type: application/xml' \
  --data "<d:sync-collection $CARDDAV_NS><d:sync-token/><d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>"
expect "sync-collection inicial" "$DAV_CODE" "207"
contains "lista el contacto" "$DAV_BODY" "$CARD_URL"
SYNC_TOKEN=$(grep -o 'urn:mail-dav:sync:[0-9a-f-]*:[0-9]*' <<<"$DAV_BODY" | head -1)
[[ -n "$SYNC_TOKEN" ]] && ok "y da un token de sincronizacion" || mal "sin sync-token"
dav "$ANA_APP" REPORT "$BOOK/" -H 'Depth: 1' -H 'Content-Type: application/xml' \
  --data "<c:addressbook-multiget $CARDDAV_NS><d:prop><d:getetag/><c:address-data/></d:prop><d:href>/api/v1/dav$CARD_URL</d:href></c:addressbook-multiget>"
contains "multiget trae el vCard editado" "$DAV_BODY" "Carla Editada"
dav "$ANA_APP" REPORT "$BOOK/" -H 'Depth: 1' -H 'Content-Type: application/xml' \
  --data "<c:addressbook-query $CARDDAV_NS><d:prop><d:getetag/></d:prop><c:filter><c:prop-filter name=\"FN\"><c:text-match match-type=\"contains\">editada</c:text-match></c:prop-filter></c:filter></c:addressbook-query>"
contains "addressbook-query filtra por FN" "$DAV_BODY" "$CARD_URL"

# Aislamiento: bea, de la misma empresa, con su propia contrasena de aplicacion, no ve nada de ana.
api POST "/mailboxes/$BEAID/app-passwords" '{"name":"contactos-e2e","imap_access":false,"pop3_access":false,"smtp_access":false,"sieve_access":false,"dav_access":true}'
BEA_DAV=$(echo "$API_BODY" | jget data.password)
BEA_APP="bea@acme.test:$BEA_DAV"
dav "$BEA_APP" GET "$CARD_URL"
expect "bea no lee el contacto de ana por su URL" "$DAV_CODE" "404"
dav "$BEA_APP" PROPFIND /addressbooks/ana@acme.test/ -H 'Depth: 1'
expect "ni lista el home de ana" "$DAV_CODE" "404"
dav "$BEA_APP" PUT "$CARD_URL" -H 'Content-Type: text/vcard; charset=utf-8' --data-binary "@$WORK/tarjeta2.vcf"
expect "ni escribe en su libreta" "$DAV_CODE" "404"
dav "$BEA_APP" PROPFIND /addressbooks/bea@acme.test/ -H 'Depth: 1'
contains "bea tiene su propia libreta, creada al descubrirla" "$DAV_BODY" "/addressbooks/bea@acme.test/contacts/"
dav "$BEA_APP" REPORT /addressbooks/bea@acme.test/contacts/ -H 'Depth: 1' -H 'Content-Type: application/xml' \
  --data "<c:addressbook-query $CARDDAV_NS><d:prop><d:getetag/></d:prop><c:filter/></c:addressbook-query>"
lacks "y en ella no hay contactos de ana" "$DAV_BODY" "$CARD_UID"
expect "ana sigue viendo el suyo" "$(dav "$ANA_APP" GET "$CARD_URL"; echo "$DAV_CODE")" "200"
DAV_SESION="$(PGPASSWORD="$DAV_DB_PASS" PGHOST=127.0.0.1 psql -U mail_svc_mail_dav -d mail_tenant_acme -At -c 'SELECT count(*) FROM mail_dav.contacts' 2>&1)"
expect "el rol de mail-dav sin sesion de buzon no ve ningun contacto aunque hay uno (RLS fail-closed)" "$DAV_SESION" "0"
expect "y el dueno de la base si lo ve" "$(sql mail_tenant_acme "SELECT count(*) FROM mail_dav.contacts WHERE uid = '$CARD_UID'")" "1"

# Borrado y sincronizacion incremental con el token de antes.
dav "$ANA_APP" DELETE "$CARD_URL" -H 'If-Match: "0000"'
expect "borrar con un If-Match viejo es un 412" "$DAV_CODE" "412"
dav "$ANA_APP" DELETE "$CARD_URL"
expect "DELETE borra el contacto" "$DAV_CODE" "204"
dav "$ANA_APP" REPORT "$BOOK/" -H 'Depth: 0' -H 'Content-Type: application/xml' \
  --data "<d:sync-collection $CARDDAV_NS><d:sync-token>$SYNC_TOKEN</d:sync-token><d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>"
contains "sync-collection con el token anterior informa el borrado" "$DAV_BODY" "404 Not Found"
dav "$ANA_APP" REPORT "$BOOK/" -H 'Depth: 0' -H 'Content-Type: application/xml' \
  --data "<d:sync-collection $CARDDAV_NS><d:sync-token>urn:mail-dav:sync:basura</d:sync-token><d:sync-level>1</d:sync-level></d:sync-collection>"
expect "un token que no se puede resolver obliga a sincronizar de nuevo (403 valid-sync-token)" "$DAV_CODE/$(grep -c valid-sync-token <<<"$DAV_BODY")" "403/1"

# dav_access se aplica en cuanto vence la cache de verificaciones correctas de mail-dav (MAIL_DAV_AUTH_CACHE_TTL,
# 2s en esta prueba y 10s por defecto): dentro de ese plazo un acierto reciente se atiende sin preguntar a mail-auth.
api PATCH "/mailboxes/$ANAID/app-passwords/$ANA_DAV_ID" '{"dav_access":false}'
expect "mail-directory apaga dav_access de la contrasena de aplicacion" "$API_CODE/$(echo "$API_BODY" | jget data.dav_access)" "200/False"
sleep 3
dav "$ANA_APP" PROPFIND / -H 'Depth: 0'
expect "esa contrasena deja de abrir DAV pasado el TTL de la cache" "$DAV_CODE" "401"
api PATCH "/mailboxes/$ANAID/app-passwords/$ANA_DAV_ID" '{"dav_access":true}'
dav "$ANA_APP" PROPFIND / -H 'Depth: 0'
expect "y vuelve a abrirlo al reactivarlo" "$DAV_CODE" "207"
api PATCH "/mailboxes/$ANAID" '{"dav_access":false}'
expect "mail-directory apaga dav_access del buzon" "$API_CODE/$(echo "$API_BODY" | jget data.dav_access)" "200/False"
sleep 3
dav "$ANA_APP" PROPFIND / -H 'Depth: 0'
expect "ni la contrasena de aplicacion ni la principal abren DAV sin el flag del buzon (la de aplicacion)" "$DAV_CODE" "401"
dav "ana@acme.test:$ANA_PASS" PROPFIND / -H 'Depth: 0'
expect "(la principal)" "$DAV_CODE" "401"
expect "y ana sigue entrando por IMAP: el flag es solo de DAV" "$(cliente login ana@acme.test "$ANA_PASS")" "OK"
contains "mail-auth lo rechaza por el protocolo, no por la contrasena" \
  "$(docker logs "$(c mail-auth)" 2>&1 | grep '"username":"ana@acme.test"' | grep '"service":"dav"')" "protocolo deshabilitado para el buzon"
api PATCH "/mailboxes/$ANAID" '{"dav_access":true}'
expect "ana recupera dav_access" "$API_CODE/$(echo "$API_BODY" | jget data.dav_access)" "200/True"
dav "$ANA_APP" PROPFIND / -H 'Depth: 0'
expect "y DAV vuelve a abrir" "$DAV_CODE" "207"

# Metodos y rutas que el servicio no admite.
dav "$ANA_APP" PROPPATCH "$BOOK/" -H 'Content-Type: application/xml' --data "<d:propertyupdate $CARDDAV_NS/>"
expect "PROPPATCH no se admite (405)" "$DAV_CODE" "405"
dav "$ANA_APP" GET "/addressbooks/ana@acme.test/contacts/..%2f..%2fbea@acme.test/x.vcf"
expect "una ruta con ..%2f no llega a ningun recurso" "$([[ $DAV_CODE == 404 || $DAV_CODE == 400 ]] && echo bien || echo "$DAV_CODE")" "bien"
dav "ana@acme.test:$ANA_PASS" PROPFIND / -H 'Depth: infinity'
expect "PROPFIND con Depth infinity se rechaza (403)" "$DAV_CODE" "403"

# ── CalDAV (docs/adr/0004, fase 2): calendarios y eventos por el mismo prefijo y las mismas credenciales ──
CALDAV_NS='xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:cs="http://calendarserver.org/ns/"'
echo "== CalDAV: calendarios y eventos por el gateway con la contrasena de aplicacion (mail-dav)"
REDIR=$(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' "http://127.0.0.1:${PORT[gateway]}/.well-known/caldav")
expect "/.well-known/caldav redirige al prefijo de mail-dav (RFC 6764)" "$REDIR" "301 http://127.0.0.1:${PORT[gateway]}/api/v1/dav/"
dav "$ANA_APP" OPTIONS /calendars/ana@acme.test/
contains "OPTIONS anuncia calendar-access" "$DAV_HDR" "calendar-access"
contains "y admite MKCALENDAR" "$DAV_HDR" "MKCALENDAR"

# Descubrimiento como DAVx5: el principal da el calendar-home-set y el home lista el calendario por defecto.
dav "$ANA_APP" PROPFIND /principals/ana@acme.test/ -H 'Depth: 0' -H 'Content-Type: application/xml' \
  --data "<d:propfind $CALDAV_NS><d:prop><c:calendar-home-set/></d:prop></d:propfind>"
contains "el calendar-home-set del principal" "$DAV_BODY" "/api/v1/dav/calendars/ana@acme.test/"
dav "$ANA_APP" PROPFIND /calendars/ana@acme.test/ -H 'Depth: 1' -H 'Content-Type: application/xml' \
  --data "<d:propfind $CALDAV_NS><d:prop><d:resourcetype/><d:displayname/><cs:getctag/><d:sync-token/><c:supported-calendar-component-set/></d:prop></d:propfind>"
expect "el home lista su calendario" "$DAV_CODE" "207"
CAL="/calendars/ana@acme.test/calendar"
contains "el calendario por defecto se crea al descubrirlo" "$DAV_BODY" "$CAL/"
contains "es un calendario que admite VEVENT" "$DAV_BODY" 'name="VEVENT"'
contains "y tiene sync-token" "$DAV_BODY" "urn:mail-dav:sync:"

# Un evento de una vez, PUT / GET con If-None-Match e If-Match como los clientes.
EVT_UID="e2e-$(rand_hex 6)"
EVT_URL="$CAL/$EVT_UID.ics"
evento() { printf 'BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//e2e//ES\r\nBEGIN:VEVENT\r\nUID:%s\r\nDTSTAMP:20300101T000000Z\r\nSUMMARY:%s\r\nDTSTART:%s\r\nDTEND:%s\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n' "$EVT_UID" "$1" "$2" "$3"; }
evento "Reunion e2e" 20300304T100000Z 20300304T110000Z >"$WORK/evento1.ics"
dav "$ANA_APP" PUT "$EVT_URL" -H 'Content-Type: text/calendar; charset=utf-8' -H 'If-None-Match: *' --data-binary "@$WORK/evento1.ics"
expect "PUT crea el evento (If-None-Match: *)" "$DAV_CODE" "201"
EVT_ETAG1=$(grep -i '^etag:' <<<"$DAV_HDR" | tr -d '\r' | sed 's/^[^:]*: *//')
[[ "$EVT_ETAG1" == \"*\" ]] && ok "y devuelve su ETag" || mal "PUT de evento sin ETag valido: '$EVT_ETAG1'"
dav "$ANA_APP" PUT "$EVT_URL" -H 'Content-Type: text/calendar; charset=utf-8' -H 'If-None-Match: *' --data-binary "@$WORK/evento1.ics"
expect "crearlo otra vez con If-None-Match: * es un 412" "$DAV_CODE" "412"
dav "$ANA_APP" GET "$EVT_URL"
expect "GET devuelve exactamente lo guardado" "$(cmp -s "$WORK/dav.out" "$WORK/evento1.ics" && echo igual)" "igual"
contains "con su tipo de contenido" "$DAV_HDR" "text/calendar"
evento "Reunion e2e editada" 20300304T100000Z 20300304T110000Z >"$WORK/evento2.ics"
dav "$ANA_APP" PUT "$EVT_URL" -H 'Content-Type: text/calendar; charset=utf-8' -H 'If-Match: "0000"' --data-binary "@$WORK/evento2.ics"
expect "editar con un If-Match viejo es un 412" "$DAV_CODE" "412"
dav "$ANA_APP" PUT "$EVT_URL" -H 'Content-Type: text/calendar; charset=utf-8' -H "If-Match: $EVT_ETAG1" --data-binary "@$WORK/evento2.ics"
expect "y con el ETag vigente es un 204" "$DAV_CODE" "204"
dav "$ANA_APP" PUT "$CAL/mal.ics" -H 'Content-Type: text/calendar; charset=utf-8' --data 'esto no es un iCalendar'
expect "un cuerpo que no es iCalendar es un 403" "$DAV_CODE/$(grep -c valid-calendar-data <<<"$DAV_BODY")" "403/1"
dav "$ANA_APP" PUT "$CAL/tarea.ics" -H 'Content-Type: text/calendar; charset=utf-8' \
  --data-binary $'BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VTODO\r\nUID:tarea-e2e\r\nEND:VTODO\r\nEND:VCALENDAR\r\n'
expect "una tarea (VTODO) no se admite: supported-calendar-component" "$DAV_CODE/$(grep -c supported-calendar-component <<<"$DAV_BODY")" "403/1"
head -c 4096 /dev/zero | tr '\0' 'A' >"$WORK/enorme.ics"
dav "$ANA_APP" PUT "$CAL/enorme.ics" -H 'Content-Type: text/calendar; charset=utf-8' --data-binary "@$WORK/enorme.ics"
expect "un evento mas grande que el limite (2048 bytes en esta prueba) es un 403 max-resource-size" "$DAV_CODE/$(grep -c max-resource-size <<<"$DAV_BODY")" "403/1"
dav "$ANA_APP" PUT "$CAL/otro-nombre.ics" -H 'Content-Type: text/calendar; charset=utf-8' --data-binary "@$WORK/evento2.ics"
expect "otro recurso con el mismo UID es un 409 no-uid-conflict" "$DAV_CODE/$(grep -c no-uid-conflict <<<"$DAV_BODY")" "409/1"

# Una serie semanal con una excepcion, para la consulta por rango con recurrencias.
SERIE_UID="serie-$(rand_hex 6)"
printf 'BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//e2e//ES\r\nBEGIN:VEVENT\r\nUID:%s\r\nDTSTAMP:20300101T000000Z\r\nSUMMARY:Semanal e2e\r\nDTSTART:20300107T090000Z\r\nDTEND:20300107T093000Z\r\nRRULE:FREQ=WEEKLY;BYDAY=MO\r\nEXDATE:20300121T090000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n' "$SERIE_UID" >"$WORK/serie.ics"
dav "$ANA_APP" PUT "$CAL/$SERIE_UID.ics" -H 'Content-Type: text/calendar; charset=utf-8' --data-binary "@$WORK/serie.ics"
expect "PUT de una serie con RRULE y EXDATE" "$DAV_CODE" "201"
calendar_query() {
  dav "$ANA_APP" REPORT "$CAL/" -H 'Depth: 1' -H 'Content-Type: application/xml' \
    --data "<c:calendar-query $CALDAV_NS><d:prop><d:getetag/></d:prop><c:filter><c:comp-filter name=\"VCALENDAR\"><c:comp-filter name=\"VEVENT\">$1</c:comp-filter></c:comp-filter></c:filter></c:calendar-query>"
}
calendar_query "<c:time-range start=\"20300304T000000Z\" end=\"20300305T000000Z\"/>"
expect "calendar-query por rango: el dia del evento" "$DAV_CODE" "207"
contains "trae el evento de ese dia" "$DAV_BODY" "$EVT_URL"
contains "y la serie semanal, que cae ese lunes (el 4 de marzo de 2030)" "$DAV_BODY" "$SERIE_UID.ics"
calendar_query "<c:time-range start=\"20300305T000000Z\" end=\"20300306T000000Z\"/>"
lacks "un dia sin evento no lo trae" "$DAV_BODY" "$EVT_URL"
calendar_query "<c:time-range start=\"20300114T000000Z\" end=\"20300115T000000Z\"/>"
contains "la serie semanal aparece un lunes de su recurrencia (expansion en el servidor solo para decidir)" "$DAV_BODY" "$SERIE_UID.ics"
calendar_query "<c:time-range start=\"20300121T000000Z\" end=\"20300122T000000Z\"/>"
lacks "y no el lunes que excluye su EXDATE" "$DAV_BODY" "$SERIE_UID.ics"
calendar_query "<c:time-range start=\"20300115T000000Z\" end=\"20300116T000000Z\"/>"
lacks "ni un martes" "$DAV_BODY" "$SERIE_UID.ics"
calendar_query "<c:prop-filter name=\"SUMMARY\"><c:text-match collation=\"i;ascii-casemap\">semanal</c:text-match></c:prop-filter>"
contains "calendar-query por texto de SUMMARY" "$DAV_BODY" "$SERIE_UID.ics"
lacks "y no trae el que no coincide" "$DAV_BODY" "$EVT_UID.ics"
calendar_query "<c:prop-filter name=\"ATTENDEE\"><c:param-filter name=\"PARTSTAT\"/></c:prop-filter>"
expect "un filtro que no se sabe evaluar (param-filter) es un 403 supported-filter, no se ignora" "$DAV_CODE/$(grep -c supported-filter <<<"$DAV_BODY")" "403/1"
dav "$ANA_APP" REPORT "$CAL/" -H 'Depth: 1' -H 'Content-Type: application/xml' \
  --data "<c:calendar-query $CALDAV_NS><d:prop><c:calendar-data><c:expand start=\"20300101T000000Z\" end=\"20300201T000000Z\"/></c:calendar-data></d:prop><c:filter><c:comp-filter name=\"VCALENDAR\"/></c:filter></c:calendar-query>"
expect "expand no se aplica en el servidor: 403 supported-calendar-data" "$DAV_CODE/$(grep -c supported-calendar-data <<<"$DAV_BODY")" "403/1"

# sync-collection y multiget como DAVx5 e iOS.
dav "$ANA_APP" REPORT "$CAL/" -H 'Depth: 0' -H 'Content-Type: application/xml' \
  --data "<d:sync-collection $CALDAV_NS><d:sync-token/><d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>"
expect "sync-collection inicial del calendario" "$DAV_CODE" "207"
contains "lista el evento" "$DAV_BODY" "$EVT_URL"
CAL_SYNC_TOKEN=$(grep -o 'urn:mail-dav:sync:[0-9a-f-]*:[0-9]*' <<<"$DAV_BODY" | head -1)
[[ -n "$CAL_SYNC_TOKEN" ]] && ok "y da un token de sincronizacion" || mal "sin sync-token en el calendario"
dav "$ANA_APP" REPORT "$CAL/" -H 'Depth: 1' -H 'Content-Type: application/xml' \
  --data "<c:calendar-multiget $CALDAV_NS><d:prop><d:getetag/><c:calendar-data/></d:prop><d:href>/api/v1/dav$EVT_URL</d:href></c:calendar-multiget>"
contains "multiget trae el evento editado" "$DAV_BODY" "Reunion e2e editada"

# MKCALENDAR como iOS: un calendario mas, con propiedades que el servidor no guarda.
dav "$ANA_APP" MKCALENDAR /calendars/ana@acme.test/trabajo/ -H 'Content-Type: application/xml' \
  --data "<c:mkcalendar $CALDAV_NS><d:set><d:prop><d:displayname>Trabajo</d:displayname><c:supported-calendar-component-set><c:comp name=\"VEVENT\"/></c:supported-calendar-component-set></d:prop></d:set></c:mkcalendar>"
expect "MKCALENDAR crea un calendario" "$DAV_CODE" "201"
dav "$ANA_APP" MKCALENDAR /calendars/ana@acme.test/trabajo/ -H 'Content-Type: application/xml'
expect "repetirlo es un 405" "$DAV_CODE" "405"
dav "$ANA_APP" MKCALENDAR /calendars/ana@acme.test/tareas/ -H 'Content-Type: application/xml' \
  --data "<c:mkcalendar $CALDAV_NS><d:set><d:prop><c:supported-calendar-component-set><c:comp name=\"VTODO\"/></c:supported-calendar-component-set></d:prop></d:set></c:mkcalendar>"
expect "un calendario de tareas no se admite (403)" "$DAV_CODE" "403"
dav "$ANA_APP" DELETE /calendars/ana@acme.test/trabajo/
expect "DELETE borra el calendario" "$DAV_CODE" "204"

# Aislamiento: bea, de la misma empresa, no ve nada de los calendarios de ana.
dav "$BEA_APP" GET "$EVT_URL"
expect "bea no lee el evento de ana por su URL" "$DAV_CODE" "404"
dav "$BEA_APP" PROPFIND /calendars/ana@acme.test/ -H 'Depth: 1'
expect "ni lista el home de calendarios de ana" "$DAV_CODE" "404"
dav "$BEA_APP" PUT "$EVT_URL" -H 'Content-Type: text/calendar; charset=utf-8' --data-binary "@$WORK/evento2.ics"
expect "ni escribe en su calendario" "$DAV_CODE" "404"
dav "$BEA_APP" PROPFIND /calendars/bea@acme.test/ -H 'Depth: 1'
contains "bea tiene su propio calendario, creado al descubrirlo" "$DAV_BODY" "/calendars/bea@acme.test/calendar/"
calendar_query_bea() {
  dav "$BEA_APP" REPORT /calendars/bea@acme.test/calendar/ -H 'Depth: 1' -H 'Content-Type: application/xml' \
    --data "<c:calendar-query $CALDAV_NS><d:prop><d:getetag/></d:prop><c:filter><c:comp-filter name=\"VCALENDAR\"><c:comp-filter name=\"VEVENT\"><c:time-range start=\"20300101T000000Z\" end=\"20301231T000000Z\"/></c:comp-filter></c:comp-filter></c:filter></c:calendar-query>"
}
calendar_query_bea
lacks "y en el no hay eventos de ana, ni con una consulta por rango" "$DAV_BODY" "$EVT_UID"
CAL_SESION="$(PGPASSWORD="$DAV_DB_PASS" PGHOST=127.0.0.1 psql -U mail_svc_mail_dav -d mail_tenant_acme -At -c 'SELECT count(*) FROM mail_dav.events' 2>&1)"
expect "el rol de mail-dav sin sesion de buzon no ve ningun evento aunque hay (RLS fail-closed)" "$CAL_SESION" "0"
expect "y el dueno de la base ve los de ana" "$(sql mail_tenant_acme "SELECT count(*) FROM mail_dav.events WHERE uid IN ('$EVT_UID', '$SERIE_UID')")" "2"
expect "el evento guardado lleva su intervalo indexado" "$(sql mail_tenant_acme "SELECT first_start = '2030-03-04T10:00:00Z' AND last_end = '2030-03-04T11:00:00Z' FROM mail_dav.events WHERE uid = '$EVT_UID'")" "t"
expect "y la serie sin fin no tiene cota superior" "$(sql mail_tenant_acme "SELECT last_end IS NULL FROM mail_dav.events WHERE uid = '$SERIE_UID'")" "t"

# Borrado y sincronizacion incremental con el token de antes.
dav "$ANA_APP" DELETE "$EVT_URL" -H 'If-Match: "0000"'
expect "borrar un evento con un If-Match viejo es un 412" "$DAV_CODE" "412"
dav "$ANA_APP" DELETE "$EVT_URL"
expect "DELETE borra el evento" "$DAV_CODE" "204"
dav "$ANA_APP" REPORT "$CAL/" -H 'Depth: 0' -H 'Content-Type: application/xml' \
  --data "<d:sync-collection $CALDAV_NS><d:sync-token>$CAL_SYNC_TOKEN</d:sync-token><d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>"
contains "sync-collection con el token anterior informa el borrado" "$DAV_BODY" "404 Not Found"
dav "$ANA_APP" REPORT "$CAL/" -H 'Depth: 0' -H 'Content-Type: application/xml' \
  --data "<d:sync-collection $CALDAV_NS><d:sync-token>urn:mail-dav:sync:basura</d:sync-token><d:sync-level>1</d:sync-level></d:sync-collection>"
expect "un token que no se puede resolver obliga a sincronizar de nuevo (403 valid-sync-token)" "$DAV_CODE/$(grep -c valid-sync-token <<<"$DAV_BODY")" "403/1"
dav "$ANA_APP" GET "/calendars/ana@acme.test/calendar/..%2f..%2fbea@acme.test/x.ics"
expect "una ruta de calendarios con ..%2f no llega a ningun recurso" "$([[ $DAV_CODE == 404 || $DAV_CODE == 400 ]] && echo bien || echo "$DAV_CODE")" "bien"

# Ninguna contrasena en el registro de mail-dav.
if grep -qF -e "$ANA_DAV" -e "$BEA_DAV" -e "$ANA_PASS" -e "$DAV_DB_PASS" "$WORK/log/mail-dav.log"; then
  mal "una contrasena aparece en el registro de mail-dav"
else
  ok "el registro de mail-dav no contiene ninguna contrasena"
fi

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

echo "== Webmail completo: carpetas, lote, spam, firma, reglas, reenvio, programado, contactos, calendario y contrasena"
# docs/Plan_Webmail_Competitivo.md: todo por el gateway, contra Dovecot, Postfix, Rspamd y mail-dav reales.
# enviar_wm escribe como ana con TARRO_ANA: la sesion anterior se cerro arriba.
TARRO_ANA="$WORK/ana2.cookies"; TARRO_ANA2="$TARRO_ANA"; TARRO_BEA2="$WORK/bea2.cookies"
wm "$TARRO_ANA2" POST /session -H 'Content-Type: application/json' -d "{\"username\":\"ana@acme.test\",\"password\":\"$ANA_PASS\"}"
expect "ana vuelve a entrar al webmail" "$WM_CODE" "200"
# La contrasena de bea acaba de cambiar: el webmail revoca hasta 2 s despues del cambio (revocationMargin), asi que
# una sesion abierta en ese margen cae con el evento. Se espera a una que sobreviva, como haria una persona.
bea_sesion_estable() {
  wm "$TARRO_BEA2" POST /session -H 'Content-Type: application/json' -d "{\"username\":\"bea@acme.test\",\"password\":\"$BEA_PASS\"}"
  [[ $WM_CODE == 200 ]] || return 1
  sleep 3
  wm "$TARRO_BEA2" GET /folders
  [[ $WM_CODE == 200 ]]
}
esperar "bea vuelve a entrar al webmail con una sesion que sobrevive a la revocacion anterior" 30 bea_sesion_estable
wm "$TARRO_ANA2" GET /meta
expect "/meta sirve el tope del lote y los dias de programado" "$WM_CODE/$(echo "$WM_BODY" | jget data.limits.max_batch_uids)/$(echo "$WM_BODY" | jget data.limits.max_scheduled_days)" "200/500/365"
contains "y el papel scheduled" "$WM_BODY" '"scheduled"'

# Carpetas propias.
CARPETA="Clientes-$(rand_hex 3)"
wm "$TARRO_ANA2" POST /folders -H 'Content-Type: application/json' -d "{\"name\":\"$CARPETA\"}"
expect "ana crea una carpeta propia en Dovecot" "$WM_CODE/$(echo "$WM_BODY" | jget data.name)" "201/$CARPETA"
wm "$TARRO_ANA2" POST /folders -H 'Content-Type: application/json' -d "{\"name\":\"$CARPETA\"}"
expect "la misma otra vez es FOLDER_EXISTS" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "409/FOLDER_EXISTS"
wm "$TARRO_ANA2" POST /folders -H 'Content-Type: application/json' -d "{\"name\":\"$CARPETA/2026\"}"
expect "y una subcarpeta" "$WM_CODE" "201"
wm "$TARRO_ANA2" DELETE "/folders/$CARPETA"
expect "una carpeta con subcarpetas no se borra" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "409/FOLDER_HAS_CHILDREN"
wm "$TARRO_ANA2" DELETE /folders/INBOX
expect "INBOX esta protegida" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "409/FOLDER_PROTECTED"
wm "$TARRO_ANA2" PATCH "/folders/$CARPETA%2F2026" -H 'Content-Type: application/json' -d "{\"name\":\"$CARPETA/2027\"}"
expect "renombrar la subcarpeta" "$WM_CODE" "200"
wm "$TARRO_ANA2" DELETE "/folders/$CARPETA%2F2027"
expect "borrar la subcarpeta" "$WM_CODE" "204"
wm "$TARRO_ANA2" DELETE "/folders/$CARPETA"
expect "y despues la carpeta" "$WM_CODE" "204"

# Lote y spam: dos mensajes a bea, marcados y movidos a Junk de una vez; imapsieve se lo ensena a Rspamd.
for n in 1 2; do enviar_wm ana@acme.test "$TOKEN-lote-$n" -H "Idempotency-Key: $(rand_hex 16)"; done
contains "bea recibe los dos del lote" "$(cliente buscar bea@acme.test "$BEA_PASS" "$TOKEN-lote-2")" "OK 1"
wm "$TARRO_BEA2" GET "/folders/INBOX/messages?subject=$TOKEN-lote"
UIDS=$(echo "$WM_BODY" | python3 -c 'import json, sys; print(",".join(str(m["uid"]) for m in json.load(sys.stdin)["data"]))' 2>/dev/null)
expect "la busqueda por asunto encuentra justo los dos" "$(tr ',' '\n' <<<"$UIDS" | grep -c .)" "2"
wm "$TARRO_BEA2" POST /folders/INBOX/messages/batch -H 'Content-Type: application/json' -d "{\"uids\":[$UIDS],\"action\":\"flags\",\"add\":[\"\\\\Seen\",\"\\\\Flagged\"]}"
expect "el lote los marca leidos y destacados" "$WM_CODE/$(echo "$WM_BODY" | jget data.affected)" "200/2"
wm "$TARRO_BEA2" GET "/folders/INBOX/messages?subject=$TOKEN-lote&flagged=true"
expect "y la busqueda de destacados los ve" "$(echo "$WM_BODY" | python3 -c 'import json, sys; print(len(json.load(sys.stdin)["data"]))')" "2"
APRENDIDOS_ANTES=$(docker logs "$(c rspamd-mail)" 2>&1 | grep -ci 'learn')
wm "$TARRO_BEA2" GET /folders
SPAM=$(echo "$WM_BODY" | python3 -c 'import json, sys; print(next((f["name"] for f in json.load(sys.stdin)["data"] if f["role"] == "junk"), ""))' 2>/dev/null)
PAPELERA=$(echo "$WM_BODY" | python3 -c 'import json, sys; print(next((f["name"] for f in json.load(sys.stdin)["data"] if f["role"] == "trash"), ""))' 2>/dev/null)
wm "$TARRO_BEA2" POST /folders/INBOX/messages/batch -H 'Content-Type: application/json' -d "{\"uids\":[$UIDS],\"action\":\"move\",\"to\":\"$SPAM\"}"
expect "marcar como spam mueve los dos a $SPAM" "$WM_CODE/$(echo "$WM_BODY" | jget data.affected)" "200/2"
aprendido() { (( $(docker logs "$(c rspamd-mail)" 2>&1 | grep -ci 'learn') > APRENDIDOS_ANTES )); }
esperar "y Rspamd recibe el aprendizaje de imapsieve aunque entra el usuario maestro" 20 aprendido
wm "$TARRO_BEA2" POST "/folders/$SPAM/empty"
expect "vaciar Spam los borra para siempre" "$WM_CODE/$(echo "$WM_BODY" | jget data.removed)" "200/2"
wm "$TARRO_BEA2" POST "/folders/INBOX/empty"
expect "INBOX no se vacia de golpe" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "409/FOLDER_NOT_EMPTIABLE"
wm "$TARRO_BEA2" GET "/folders/INBOX/messages?subject=$TOKEN-webmail-ventas"
UID_ORIG=$(echo "$WM_BODY" | jget data.0.uid)
ORIG=$(curl -s -b "$TARRO_BEA2" -H "Origin: $API_ORIGIN" -D "$WORK/raw.hdr" "$WM/folders/INBOX/messages/$UID_ORIG/raw")
contains "ver original descarga el .eml completo" "$ORIG" "Subject: $TOKEN-webmail-ventas"
contains "como adjunto message/rfc822" "$(cat "$WORK/raw.hdr")" "message/rfc822"

# Firma.
wm "$TARRO_ANA2" PUT /signature -H 'Content-Type: application/json' -d '{"enabled":true,"html":"<p>Ana <b>Acme</b><script>x()</script></p>","on_replies":true}'
expect "ana guarda su firma" "$WM_CODE/$(echo "$WM_BODY" | jget data.enabled)" "200/True"
lacks "y el webmail la guarda saneada" "$WM_BODY" "<script>"
contains "con su version en texto" "$(echo "$WM_BODY" | jget data.text)" "Ana"

# Reglas y reenvio: Sieve generado, leido por Dovecot en sieve_before3.
REGLA="$TOKEN-regla"
wm "$TARRO_BEA2" PUT /filters -H 'Content-Type: application/json' -d "{\"rules\":[{\"id\":\"\",\"name\":\"A proyectos\",\"enabled\":true,\"match\":\"all\",\"conditions\":[{\"field\":\"subject\",\"op\":\"contains\",\"value\":\"$REGLA\"}],\"actions\":[{\"type\":\"move\",\"folder\":\"Proyectos\"},{\"type\":\"flag\"}],\"stop\":false}],\"forwarding\":{\"enabled\":true,\"addresses\":[\"ana@acme.test\"],\"keep_copy\":true}}"
expect "bea guarda una regla y un reenvio con copia" "$WM_CODE" "200"
expect "el script queda en la vista que lee Dovecot, solo para bea" "$(sql mail_cell_pe_01 "SELECT string_agg(username, ',') FROM mail.v_sieve_user WHERE username IN ('ana@acme.test','bea@acme.test')")" "bea@acme.test"
wm "$TARRO_BEA2" PUT /filters -H 'Content-Type: application/json' -d '{"rules":[],"forwarding":{"enabled":true,"addresses":["bea@acme.test"],"keep_copy":true}}'
expect "reenviar al propio buzon es un 422 en su campo" "$WM_CODE/$(echo "$WM_BODY" | jget error.details.field)" "422/forwarding.addresses[0]"
wm "$TARRO_BEA2" PUT /filters -H 'Content-Type: application/json' -d "{\"rules\":[{\"id\":\"\",\"name\":\"A proyectos\",\"enabled\":true,\"match\":\"all\",\"conditions\":[{\"field\":\"subject\",\"op\":\"contains\",\"value\":\"$REGLA\"}],\"actions\":[{\"type\":\"move\",\"folder\":\"Proyectos\"},{\"type\":\"flag\"}],\"stop\":false}],\"forwarding\":{\"enabled\":true,\"addresses\":[\"ana@acme.test\"],\"keep_copy\":true}}"
enviar_wm ana@acme.test "$REGLA" -H "Idempotency-Key: $(rand_hex 16)"
expect "ana envia a bea un correo que cumple la regla" "$WM_CODE" "202"
en_proyectos() { wm "$TARRO_BEA2" GET /folders/Proyectos/messages; [[ "$WM_BODY" == *"$REGLA"* ]]; }
esperar "Dovecot aplica la regla: el correo acaba en Proyectos (fileinto :create)" 30 en_proyectos
contains "y destacado por la misma regla" "$WM_BODY" '\\Flagged'
contains "el reenvio con copia llega a ana" "$(cliente buscar ana@acme.test "$ANA_PASS" "$REGLA")" "OK"
wm "$TARRO_BEA2" PUT /filters -H 'Content-Type: application/json' -d '{"rules":[],"forwarding":{"enabled":false,"addresses":[],"keep_copy":true}}'
expect "sin reglas ni reenvio, bea sale de la vista de Dovecot" "$WM_CODE/$(sql mail_cell_pe_01 "SELECT count(*) FROM mail.v_sieve_user WHERE username = 'bea@acme.test'")" "200/0"

# Envio programado: el mensaje espera en Scheduled y el trabajador lo entrega a su hora.
programar() { # programar <asunto> <segundos>
  wm "$TARRO_ANA2" POST /send -H "Idempotency-Key: $(rand_hex 16)" -F from=ana@acme.test -F to=bea@acme.test -F "subject=$1" \
    -F "text=Programado." -F "send_at=$(date -u -d "+$2 seconds" +%Y-%m-%dT%H:%M:%SZ)"
}
programar "$TOKEN-programado" 65
PROG_ID=$(echo "$WM_BODY" | jget data.scheduled.id)
expect "ana programa un envio para dentro de un minuto" "$WM_CODE/${PROG_ID:+id}" "202/id"
wm "$TARRO_ANA2" GET /folders/Scheduled/messages
contains "espera en su carpeta Scheduled" "$WM_BODY" "$TOKEN-programado"
wm "$TARRO_ANA2" GET /scheduled
contains "y en la lista de programados" "$WM_BODY" "$PROG_ID"
expect "la fila durable esta en la celda, pendiente" "$(sql mail_cell_pe_01 "SELECT status FROM mail.scheduled_sends WHERE id = '$PROG_ID'")" "pending"
lacks "bea todavia no lo tiene" "$(cliente buscar bea@acme.test "$BEA_PASS" "$TOKEN-programado")" "OK 1"
programar "$TOKEN-cancelado" 600
CANC_ID=$(echo "$WM_BODY" | jget data.scheduled.id)
wm "$TARRO_ANA2" DELETE "/scheduled/$CANC_ID"
expect "cancelar un programado" "$WM_CODE" "204"
wm "$TARRO_ANA2" GET /folders
BORRADORES=$(echo "$WM_BODY" | python3 -c 'import json, sys; print(next((f["name"] for f in json.load(sys.stdin)["data"] if f["role"] == "drafts"), ""))' 2>/dev/null)
wm "$TARRO_ANA2" GET "/folders/$BORRADORES/messages?subject=$TOKEN-cancelado"
contains "y vuelve a Borradores" "$WM_BODY" "$TOKEN-cancelado"
enviado() { [[ "$(sql mail_cell_pe_01 "SELECT status FROM mail.scheduled_sends WHERE id = '$PROG_ID'")" == sent ]]; }
esperar "el trabajador del webmail lo envia a su hora" 120 enviado
contains "bea lo recibe una vez" "$(cliente buscar bea@acme.test "$BEA_PASS" "$TOKEN-programado" --estable 5)" "OK 1"
wm "$TARRO_ANA2" GET /folders/Scheduled/messages
lacks "y ya no esta en Scheduled" "$WM_BODY" "$TOKEN-programado"
wm "$TARRO_ANA2" GET "/folders/$ENVIADOS/messages?subject=$TOKEN-programado"
contains "sino en enviados" "$WM_BODY" "$TOKEN-programado"

# Contactos y calendario por el webmail, sobre el mismo almacen que CardDAV y CalDAV.
wm "$TARRO_ANA2" POST /contacts -H 'Content-Type: application/json' -d '{"name":"Lucia Web","given_name":"Lucia","family_name":"Web","emails":[{"value":"lucia@ejemplo.test","type":"work"}],"phones":[{"value":"+51 999 000 111","type":"mobile"}],"organization":"Acme","title":"","notes":"Linea uno\nlinea dos, con coma; y punto y coma","birthday":""}'
CONTACTO=$(echo "$WM_BODY" | jget data.id)
expect "ana crea un contacto desde el webmail" "$WM_CODE/${CONTACTO:+id}" "201/id"
wm "$TARRO_ANA2" GET "/contacts?q=lucia"
contains "la busqueda lo encuentra" "$WM_BODY" "lucia@ejemplo.test"
dav "ana@acme.test:$ANA_PASS" GET "$BOOK/$CONTACTO.vcf"
expect "CardDAV devuelve la misma tarjeta" "$DAV_CODE" "200"
contains "como vCard con su correo" "$DAV_BODY" "lucia@ejemplo.test"
contains "y la nota escapada segun RFC 6350" "$DAV_BODY" 'Linea uno\nlinea dos\, con coma\; y punto y coma'
wm "$TARRO_ANA2" PUT "/contacts/$CONTACTO" -H 'If-Match: "0000"' -H 'Content-Type: application/json' -d '{"name":"Lucia Cambiada","emails":[{"value":"lucia@ejemplo.test","type":"work"}]}'
expect "un If-Match viejo es 412" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "412/PRECONDITION_FAILED"
EXPORT=$(curl -s -b "$TARRO_ANA2" -H "Origin: $API_ORIGIN" "$WM/contacts/export")
contains "exportar devuelve vCard" "$EXPORT" "BEGIN:VCARD"
DESDE=$(date -u -d 'tomorrow 10:00' +%Y-%m-%dT%H:%M:%SZ); HASTA=$(date -u -d 'tomorrow 11:00' +%Y-%m-%dT%H:%M:%SZ)
wm "$TARRO_ANA2" POST /calendar/events -H 'Content-Type: application/json' -d "{\"title\":\"Reunion web\",\"start\":\"$DESDE\",\"end\":\"$HASTA\",\"all_day\":false,\"location\":\"Sala 1\",\"description\":\"\",\"recurrence\":{\"freq\":\"weekly\",\"interval\":1,\"count\":3,\"until\":null,\"by_day\":[]},\"reminder_minutes\":15}"
EVENTO=$(echo "$WM_BODY" | jget data.id)
expect "ana crea un evento semanal de tres veces" "$WM_CODE/${EVENTO:+id}" "201/id"
wm "$TARRO_ANA2" GET "/calendar/events?start=$(date -u +%Y-%m-%dT00:00:00Z)&end=$(date -u -d '+30 days' +%Y-%m-%dT00:00:00Z)"
expect "la ventana de 30 dias trae sus tres apariciones" "$(echo "$WM_BODY" | python3 -c "import json, sys; print(sum(1 for o in json.load(sys.stdin)['data'] if o['id'] == '$EVENTO'))")" "3"
dav "ana@acme.test:$ANA_PASS" GET "$CAL/$EVENTO.ics"
expect "CalDAV devuelve el mismo evento" "$DAV_CODE" "200"
contains "con su regla de repeticion" "$DAV_BODY" "RRULE:FREQ=WEEKLY"
contains "y su aviso" "$DAV_BODY" "TRIGGER:-PT15M"
wm "$TARRO_ANA2" DELETE "/calendar/events/$EVENTO"
expect "borrar el evento" "$WM_CODE" "204"
wm "$TARRO_ANA2" DELETE "/contacts/$CONTACTO"
expect "y el contacto" "$WM_CODE" "204"

# Cambiar la contrasena: la actual se comprueba en mail-auth y el cambio revoca las sesiones.
ANA_NUEVA="Nueva-$(rand_hex 10)"
wm "$TARRO_ANA2" POST /password -H 'Content-Type: application/json' -d "{\"current_password\":\"no-$ANA_PASS\",\"new_password\":\"$ANA_NUEVA\"}"
expect "con la contrasena actual mala no cambia nada" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "401/INVALID_CREDENTIALS"
wm "$TARRO_ANA2" POST /password -H 'Content-Type: application/json' -d "{\"current_password\":\"$ANA_PASS\",\"new_password\":\"$ANA_NUEVA\"}"
expect "ana cambia su contrasena desde el webmail" "$WM_CODE" "204"
wm "$TARRO_ANA2" GET /folders
expect "y su sesion queda revocada" "$WM_CODE" "401"
esperar "IMAP acepta la nueva" 20 login_aceptado ana@acme.test "$ANA_NUEVA"
# Como con bea arriba: una sesion abierta dentro del margen de revocacion del cambio cae con su evento.
ana_sesion_estable() {
  wm "$TARRO_ANA2" POST /session -H 'Content-Type: application/json' -d "{\"username\":\"ana@acme.test\",\"password\":\"$ANA_NUEVA\"}"
  [[ $WM_CODE == 200 ]] || return 1
  sleep 3
  wm "$TARRO_ANA2" GET /folders
  [[ $WM_CODE == 200 ]]
}
esperar "ana entra con la nueva en una sesion que sobrevive a la revocacion" 30 ana_sesion_estable
wm "$TARRO_ANA2" POST /password -H 'Content-Type: application/json' -d "{\"current_password\":\"$ANA_NUEVA\",\"new_password\":\"$ANA_PASS\"}"
expect "y vuelve a la anterior para el resto de la prueba" "$WM_CODE" "204"
esperar "IMAP acepta otra vez la anterior" 20 login_aceptado ana@acme.test "$ANA_PASS"
# Esta seccion espera al envio programado y alarga la prueba: los access tokens de la plataforma caducarian antes de las siguientes.
T1=$(e2e_login "$ADMIN_EMAIL" "$ADMIN_PASS" | jget data.access_token)
T2=$(e2e_login admin@acme.test "$TENANT_PASS" | jget data.access_token)
A2="Authorization: Bearer $T2"
[[ -n "$T1" && -n "$T2" ]] && ok "se renuevan los access tokens de la plataforma" || mal "no se pudieron renovar los access tokens"

# ── Webmail innovador (docs/Plan_Webmail_Innovador.md, fase 2) ─────────────────────────────────────────────
# Cada bloque contra los motores reales: lo que calcula el webmail sale de las cabeceras que dejan Postfix y
# Rspamd, lo durable de la celda (mail-directory) o de la base de la empresa (mail-dav, mail-files).
# jpy <expresion> [args...]: evalua la expresion de Python sobre el JSON de la entrada (d; los argumentos en a).
jpy() { python3 -c 'import json, sys; d = json.load(sys.stdin); a = sys.argv[2:]; print(eval(sys.argv[1]))' "$@" 2>/dev/null; }
# fila <tarro> <carpeta> <asunto> <expresion>: del listado de la carpeta, la expresion sobre el mensaje (m) cuyo
# asunto es exactamente ese; vacio si no esta.
fila() {
  wm "$1" GET "/folders/$2/messages?subject=$(python3 -c 'import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1]))' "$3")"
  echo "$WM_BODY" | python3 -c '
import json, sys
m = next((m for m in json.load(sys.stdin)["data"] if m["subject"] == sys.argv[1]), None)
print("" if m is None else eval(sys.argv[2]))' "$3" "$4" 2>/dev/null
}
echo "== Webmail C1: conversaciones, bandeja inteligente, escudo antifraude y baja RFC 8058"
# Volver a la contrasena anterior cerro la sesion de ana: una nueva, fuera del margen de revocacion del cambio.
ana_sesion_final() {
  wm "$TARRO_ANA2" POST /session -H 'Content-Type: application/json' -d "{\"username\":\"ana@acme.test\",\"password\":\"$ANA_PASS\"}"
  [[ $WM_CODE == 200 ]] || return 1
  sleep 3
  wm "$TARRO_ANA2" GET /folders
  [[ $WM_CODE == 200 ]]
}
esperar "ana vuelve a entrar al webmail con su contrasena de siempre" 30 ana_sesion_final
HILO="$TOKEN-hilo"
enviar_wm ana@acme.test "$HILO" -H "Idempotency-Key: $(rand_hex 16)"
expect "ana escribe a bea por el webmail" "$WM_CODE" "202"
contains "bea lo recibe" "$(cliente buscar bea@acme.test "$BEA_PASS" "$HILO")" "OK 1"
HILO_BEA=$(fila "$TARRO_BEA2" INBOX "$HILO" 'm["uid"]')
wm "$TARRO_BEA2" POST /send -H "Idempotency-Key: $(rand_hex 16)" -F from=bea@acme.test -F to=ana@acme.test \
  -F "subject=Re: $HILO" -F "text=Respuesta de bea." -F "in_reply_to=$HILO_BEA" -F in_reply_to_folder=INBOX
expect "bea responde desde el webmail (in_reply_to)" "$WM_CODE/$(echo "$WM_BODY" | jget data.saved_to_sent)" "202/True"
contains "ana recibe la respuesta" "$(cliente buscar ana@acme.test "$ANA_PASS" "Re: $HILO")" "OK 1"
RESP_ANA=$(fila "$TARRO_ANA2" INBOX "Re: $HILO" 'm["uid"]')
wm "$TARRO_ANA2" GET "/threads?folder=INBOX&uid=$RESP_ANA"
expect "ana abre la conversacion: dos mensajes, su envio (de $ENVIADOS) y la respuesta (de INBOX)" \
  "$WM_CODE/$(echo "$WM_BODY" | jpy '" ".join(m["folder"] + ":" + m["subject"] for m in d["data"])')" "200/$ENVIADOS:$HILO INBOX:Re: $HILO"
contains "cada uno con su Message-ID" "$(echo "$WM_BODY" | jpy 'all(m["message_id"] for m in d["data"])')" "True"
wm "$TARRO_BEA2" GET "/threads?folder=INBOX&uid=$HILO_BEA"
expect "bea ve la misma conversacion desde el original, con su propia respuesta" \
  "$(echo "$WM_BODY" | jpy '" ".join(m["folder"] + ":" + m["subject"] for m in d["data"])')" "INBOX:$HILO $ENVIADOS:Re: $HILO"
wm "$TARRO_ANA2" POST /send -H "Idempotency-Key: $(rand_hex 16)" -F from=ana@acme.test -F to=bea@acme.test \
  -F "subject=Re: $HILO" -F "text=Segunda de ana." -F "in_reply_to=$RESP_ANA" -F in_reply_to_folder=INBOX
expect "ana contesta a la respuesta" "$WM_CODE" "202"
contains "y bea la recibe" "$(cliente buscar bea@acme.test "$BEA_PASS" "Re: $HILO")" "OK 1"
wm "$TARRO_BEA2" GET "/folders/INBOX/messages?view=threads&subject=$HILO"
expect "la vista por conversaciones de bea agrupa los dos recibidos en una fila (THREAD de Dovecot)" \
  "$WM_CODE/$(echo "$WM_BODY" | jpy 'len(d["data"])')/$(echo "$WM_BODY" | jpy 'd["data"][0]["thread"]["size"]')" "200/1/2"
expect "la fila muestra el ultimo mensaje" "$(echo "$WM_BODY" | jpy 'd["data"][0]["subject"]')" "Re: $HILO"
ULTIMO_BEA=$(echo "$WM_BODY" | jpy 'd["data"][0]["uid"]')
wm "$TARRO_BEA2" GET "/threads?folder=INBOX&uid=$ULTIMO_BEA"
expect "y abierta desde el ultimo son tres, en orden" \
  "$(echo "$WM_BODY" | jpy '" ".join(m["folder"] for m in d["data"])')" "INBOX $ENVIADOS INBOX"

# Bandeja inteligente: un boletin (List-Id y List-Unsubscribe, por submission con cabeceras propias) cae en
# newsletters; un correo normal, en primary.
BOLETIN="$TOKEN-boletin"
contains "ana envia un boletin a bea con List-Id y baja en un clic hacia una IP privada" \
  "$(cliente enviar ana@acme.test "$ANA_PASS" ana@acme.test bea@acme.test "$BOLETIN" \
    --cabecera "List-Id: Novedades <novedades.acme.test>" --cabecera "List-Unsubscribe: <https://10.20.30.40/baja?u=1>" \
    --cabecera "List-Unsubscribe-Post: List-Unsubscribe=One-Click")" "OK 250"
contains "bea lo recibe" "$(cliente buscar bea@acme.test "$BEA_PASS" "$BOLETIN")" "OK 1"
expect "el listado lo clasifica como boletin" "$(fila "$TARRO_BEA2" INBOX "$BOLETIN" 'm["category"]')" "newsletters"
expect "y el correo de la conversacion como principal" "$(fila "$TARRO_BEA2" INBOX "$HILO" 'm["category"]')" "primary"
wm "$TARRO_BEA2" GET "/folders/INBOX/messages?category=newsletters"
contains "la pestana Boletines lo filtra en el servidor" "$WM_BODY" "$BOLETIN"
lacks "sin el correo principal" "$WM_BODY" "\"subject\":\"$HILO\""
wm "$TARRO_BEA2" GET "/folders/INBOX/messages?category=primary"
lacks "y la principal no lo trae" "$WM_BODY" "$BOLETIN"
contains "pero si el correo normal" "$WM_BODY" "\"subject\":\"$HILO\""
BOLETIN_UID=$(fila "$TARRO_BEA2" INBOX "$BOLETIN" 'm["uid"]')
wm "$TARRO_BEA2" GET "/sender-insight?folder=INBOX&uid=$BOLETIN_UID"
expect "la ficha del boletin: pestana, baja en un clic y su destino" \
  "$WM_CODE/$(echo "$WM_BODY" | jpy 'd["data"]["category"]')/$(echo "$WM_BODY" | jpy 'd["data"]["unsubscribe"]["method"]')/$(echo "$WM_BODY" | jpy 'd["data"]["unsubscribe"]["target"]')" \
  "200/newsletters/one_click/10.20.30.40"
wm "$TARRO_BEA2" POST /unsubscribe -H 'Content-Type: application/json' -d "{\"folder\":\"INBOX\",\"uid\":$BOLETIN_UID}"
expect "la baja en un clic hacia una IP privada la rechaza la proteccion SSRF" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "422/UNSUBSCRIBE_TARGET_REFUSED"
contains "y el webmail lo registra sin la URL" "$(docker logs "$(c webmail)" 2>&1 | grep 'baja en un clic rechazada' | tail -1)" '"host":"10.20.30.40"'
BOLETIN_LOCAL="$TOKEN-boletin-local"
contains "otro boletin con la baja en loopback por nombre" \
  "$(cliente enviar ana@acme.test "$ANA_PASS" ana@acme.test bea@acme.test "$BOLETIN_LOCAL" \
    --cabecera "List-Unsubscribe: <https://localhost/baja>" --cabecera "List-Unsubscribe-Post: List-Unsubscribe=One-Click")" "OK 250"
contains "bea lo recibe" "$(cliente buscar bea@acme.test "$BEA_PASS" "$BOLETIN_LOCAL")" "OK 1"
wm "$TARRO_BEA2" POST /unsubscribe -H 'Content-Type: application/json' -d "{\"folder\":\"INBOX\",\"uid\":$(fila "$TARRO_BEA2" INBOX "$BOLETIN_LOCAL" 'm["uid"]')}"
expect "un nombre que resuelve a loopback tambien se rechaza" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "422/UNSUBSCRIBE_TARGET_REFUSED"
BOLETIN_MAILTO="$TOKEN-boletin-mailto"
contains "un boletin cuya baja es solo por correo (mailto:)" \
  "$(cliente enviar ana@acme.test "$ANA_PASS" ana@acme.test bea@acme.test "$BOLETIN_MAILTO" \
    --cabecera "List-Unsubscribe: <mailto:ana@acme.test?subject=baja-$TOKEN>")" "OK 250"
contains "bea lo recibe" "$(cliente buscar bea@acme.test "$BEA_PASS" "$BOLETIN_MAILTO")" "OK 1"
wm "$TARRO_BEA2" POST /unsubscribe -H 'Content-Type: application/json' -d "{\"folder\":\"INBOX\",\"uid\":$(fila "$TARRO_BEA2" INBOX "$BOLETIN_MAILTO" 'm["uid"]')}"
expect "la baja sale por correo desde el propio buzon" "$WM_CODE/$(echo "$WM_BODY" | jget data.method)/$(echo "$WM_BODY" | jget data.target)" "200/mailto/ana@acme.test"
contains "y llega al remitente del boletin por Postfix" "$(cliente buscar ana@acme.test "$ANA_PASS" "baja-$TOKEN")" "OK 1"

# Escudo antifraude y ficha del remitente. Un companero no lleva aviso; desde fuera, por el MX (25, sin
# autenticarse), un dominio que imita al propio y el nombre visible de un companero son peligro.
wm "$TARRO_BEA2" GET "/sender-insight?folder=INBOX&uid=$HILO_BEA"
expect "un correo de una companera: interno y sin aviso" \
  "$(echo "$WM_BODY" | jpy 'd["data"]["shield"]["level"]')/$(echo "$WM_BODY" | jpy 'd["data"]["shield"]["external"]')/$(echo "$WM_BODY" | jpy 'len(d["data"]["shield"]["reasons"])')/$(echo "$WM_BODY" | jpy 'd["data"]["sender"]["email"]')" \
  "none/False/0/ana@acme.test"
PARECIDO="$TOKEN-parecido"
contains "un servidor de fuera entrega al MX un correo de facturas@acrne.test (rn por m)" \
  "$(cliente enviar - - facturas@acrne.test bea@acme.test "$PARECIDO" --mx)" "OK 250"
contains "bea lo recibe" "$(cliente buscar bea@acme.test "$BEA_PASS" "$PARECIDO")" "OK 1"
wm "$TARRO_BEA2" GET "/sender-insight?folder=INBOX&uid=$(fila "$TARRO_BEA2" INBOX "$PARECIDO" 'm["uid"]')"
expect "el escudo lo marca como peligro: externo y dominio que se confunde con acme.test" \
  "$(echo "$WM_BODY" | jpy 'd["data"]["shield"]["level"]')/$(echo "$WM_BODY" | jpy 'd["data"]["shield"]["external"]')/$(echo "$WM_BODY" | jpy '" ".join(sorted(r["code"] for r in d["data"]["shield"]["reasons"]))')" \
  "danger/True/external_sender homoglyph_domain"
expect "y dice a que dominio se parece" "$(echo "$WM_BODY" | jpy 'next(r["params"]["resembles"] for r in d["data"]["shield"]["reasons"] if r["code"] == "homoglyph_domain")')" "acme.test"
wm "$TARRO_BEA2" GET "/address-book?q=ana@acme.test"
ANA_NOMBRE=$(echo "$WM_BODY" | jpy 'd["data"][0]["display_name"]')
SUPLANTA="$TOKEN-suplanta"
contains "desde fuera, un correo con el nombre visible de ana ($ANA_NOMBRE) y otra direccion" \
  "$(cliente enviar - - ceo.urgente@correo-externo.test bea@acme.test "$SUPLANTA" --mx --de-visible "\"$ANA_NOMBRE\" <ceo.urgente@correo-externo.test>")" "OK 250"
contains "bea lo recibe" "$(cliente buscar bea@acme.test "$BEA_PASS" "$SUPLANTA")" "OK 1"
wm "$TARRO_BEA2" GET "/sender-insight?folder=INBOX&uid=$(fila "$TARRO_BEA2" INBOX "$SUPLANTA" 'm["uid"]')"
expect "el escudo avisa de la suplantacion de una companera (peligro)" \
  "$(echo "$WM_BODY" | jpy 'd["data"]["shield"]["level"]')/$(echo "$WM_BODY" | jpy 'next((r["params"]["address"] for r in d["data"]["shield"]["reasons"] if r["code"] == "colleague_name"), "")')" \
  "danger/ana@acme.test"
expect "con el directorio de la empresa completo (no parcial)" "$(echo "$WM_BODY" | jpy 'd["data"]["shield"]["partial"]')" "False"

echo "== Webmail C2: posponer, seguimiento y respuestas rapidas (mail-directory: mail.mailbox_reminders)"
# Posponer: el mensaje va a Snoozed y a su hora (65 s; WEBMAIL_REMINDERS_POLL_INTERVAL es 2 s en la prueba)
# vuelve a INBOX sin \Seen. La vuelta se comprueba al final del bloque C3 para no esperar parados.
POSPONER="$TOKEN-posponer"
enviar_wm ana@acme.test "$POSPONER" -H "Idempotency-Key: $(rand_hex 16)"
contains "bea recibe un correo que va a posponer" "$(cliente buscar bea@acme.test "$BEA_PASS" "$POSPONER")" "OK 1"
POSPONER_UID=$(fila "$TARRO_BEA2" INBOX "$POSPONER" 'm["uid"]')
wm "$TARRO_BEA2" POST /folders/INBOX/messages/batch -H 'Content-Type: application/json' -d "{\"uids\":[$POSPONER_UID],\"action\":\"flags\",\"add\":[\"\\\\Seen\"]}"
expect "lo lee" "$WM_CODE/$(fila "$TARRO_BEA2" INBOX "$POSPONER" '"\\Seen" in m["flags"]')" "200/True"
wm "$TARRO_BEA2" POST /snooze -H 'Content-Type: application/json' \
  -d "{\"folder\":\"INBOX\",\"uids\":[$POSPONER_UID],\"until\":\"$(date -u -d '+30 seconds' +%Y-%m-%dT%H:%M:%SZ)\"}"
expect "posponer menos de un minuto se rechaza" "$WM_CODE" "422"
wm "$TARRO_BEA2" POST /snooze -H 'Content-Type: application/json' \
  -d "{\"folder\":\"INBOX\",\"uids\":[$POSPONER_UID],\"until\":\"$(date -u -d '+65 seconds' +%Y-%m-%dT%H:%M:%SZ)\"}"
POSPONER_ID=$(echo "$WM_BODY" | jpy 'd["data"]["snoozed"][0]["id"]')
expect "bea lo pospone 65 s" "$WM_CODE/${POSPONER_ID:+id}/$(echo "$WM_BODY" | jpy 'd["data"]["snoozed"][0]["return_folder"]')/$(echo "$WM_BODY" | jpy 'len(d["data"]["failed"])')" "200/id/INBOX/0"
expect "sale de INBOX" "$(fila "$TARRO_BEA2" INBOX "$POSPONER" 'm["uid"]')" ""
expect "y espera en Snoozed" "$(fila "$TARRO_BEA2" Snoozed "$POSPONER" 'm["subject"]')" "$POSPONER"
expect "la fila durable esta en la celda, pendiente" \
  "$(sql mail_cell_pe_01 "SELECT kind || '/' || status FROM mail.mailbox_reminders WHERE id = '$POSPONER_ID'")" "snooze/pending"
wm "$TARRO_BEA2" GET /snooze
contains "y en la lista de pospuestos" "$WM_BODY" "$POSPONER_ID"
CANCELAR_POS="$TOKEN-posponer-cancelado"
enviar_wm ana@acme.test "$CANCELAR_POS" -H "Idempotency-Key: $(rand_hex 16)"
contains "bea recibe otro" "$(cliente buscar bea@acme.test "$BEA_PASS" "$CANCELAR_POS")" "OK 1"
wm "$TARRO_BEA2" POST /snooze -H 'Content-Type: application/json' \
  -d "{\"folder\":\"INBOX\",\"uids\":[$(fila "$TARRO_BEA2" INBOX "$CANCELAR_POS" 'm["uid"]')],\"until\":\"$(date -u -d '+1 hour' +%Y-%m-%dT%H:%M:%SZ)\"}"
CANCELAR_POS_ID=$(echo "$WM_BODY" | jpy 'd["data"]["snoozed"][0]["id"]')
expect "lo pospone una hora" "$WM_CODE/${CANCELAR_POS_ID:+id}" "200/id"
wm "$TARRO_BEA2" DELETE "/snooze/$CANCELAR_POS_ID"
expect "y lo saca a mano" "$WM_CODE" "204"
expect "vuelve a INBOX al momento" "$(fila "$TARRO_BEA2" INBOX "$CANCELAR_POS" 'm["subject"]')" "$CANCELAR_POS"
expect "y la fila queda cancelada" "$(sql mail_cell_pe_01 "SELECT status FROM mail.mailbox_reminders WHERE id = '$CANCELAR_POS_ID'")" "canceled"

# Seguimiento: registrar y cancelar; y dos que vencen (se adelanta due_at en la celda, que es lo que
# haria el reloj): sin respuesta, el enviado vuelve a INBOX destacado y sin leer; con respuesta, se cierra.
seguimiento() { # seguimiento <asunto> <dias>
  wm "$TARRO_ANA2" POST /send -H "Idempotency-Key: $(rand_hex 16)" -F from=ana@acme.test -F to=bea@acme.test \
    -F "subject=$1" -F "text=Espero respuesta." -F "follow_up_days=$2"
}
seguimiento "$TOKEN-seguimiento-0" 0
expect "un seguimiento de 0 dias se rechaza sin enviar" "$WM_CODE/$(echo "$WM_BODY" | jget error.details.field)" "422/follow_up_days"
lacks "y bea no recibe nada" "$(cliente buscar bea@acme.test "$BEA_PASS" "$TOKEN-seguimiento-0" --espera 3)" "OK 1"
seguimiento "$TOKEN-seguimiento" 3
SEG_ID=$(echo "$WM_BODY" | jget data.follow_up.id)
expect "ana envia con seguimiento a 3 dias" "$WM_CODE/${SEG_ID:+id}/$(echo "$WM_BODY" | jget data.follow_up_error)" "202/id/"
wm "$TARRO_ANA2" GET /follow-ups
expect "aparece en sus seguimientos, pendiente" "$(echo "$WM_BODY" | jpy 'next((f["status"] for f in d["data"] if f["id"] == a[0]), "")' "$SEG_ID")" "pending"
expect "con vencimiento a 3 dias en la celda" \
  "$(sql mail_cell_pe_01 "SELECT kind || '/' || (due_at - now() BETWEEN interval '71 hours' AND interval '73 hours') FROM mail.mailbox_reminders WHERE id = '$SEG_ID'")" "follow_up/true"
wm "$TARRO_ANA2" DELETE "/follow-ups/$SEG_ID"
expect "ana lo cancela" "$WM_CODE" "204"
expect "y queda cancelado en la celda" "$(sql mail_cell_pe_01 "SELECT status FROM mail.mailbox_reminders WHERE id = '$SEG_ID'")" "canceled"
wm "$TARRO_ANA2" DELETE "/follow-ups/$SEG_ID"
expect "cancelarlo otra vez no falla (idempotente)" "$WM_CODE" "204"
SIN_RESP="$TOKEN-sin-respuesta"
seguimiento "$SIN_RESP" 1
SIN_RESP_ID=$(echo "$WM_BODY" | jget data.follow_up.id)
CON_RESP="$TOKEN-con-respuesta"
seguimiento "$CON_RESP" 1
CON_RESP_ID=$(echo "$WM_BODY" | jget data.follow_up.id)
[[ -n "$SIN_RESP_ID" && -n "$CON_RESP_ID" ]] && ok "ana envia otros dos con seguimiento" || mal "seguimientos: '$SIN_RESP_ID' '$CON_RESP_ID'"
contains "bea recibe el que va a contestar" "$(cliente buscar bea@acme.test "$BEA_PASS" "$CON_RESP")" "OK 1"
wm "$TARRO_BEA2" POST /send -H "Idempotency-Key: $(rand_hex 16)" -F from=bea@acme.test -F to=ana@acme.test \
  -F "subject=Re: $CON_RESP" -F "text=Contestado." -F "in_reply_to=$(fila "$TARRO_BEA2" INBOX "$CON_RESP" 'm["uid"]')" -F in_reply_to_folder=INBOX
expect "y lo contesta" "$WM_CODE" "202"
contains "la respuesta llega a ana" "$(cliente buscar ana@acme.test "$ANA_PASS" "Re: $CON_RESP")" "OK 1"
expect "se adelantan los dos vencimientos" \
  "$(sql mail_cell_pe_01 "UPDATE mail.mailbox_reminders SET due_at = now() WHERE id IN ('$SIN_RESP_ID', '$CON_RESP_ID') AND status = 'pending' RETURNING 1" | wc -l)" "2"
recordatorio() { [[ "$(sql mail_cell_pe_01 "SELECT status || '/' || result FROM mail.mailbox_reminders WHERE id = '$1'")" == "done/$2" ]]; }
esperar "el trabajador del webmail cierra el que tuvo respuesta (replied)" 60 recordatorio "$CON_RESP_ID" replied
esperar "y el que no, lo recuerda (reminded)" 60 recordatorio "$SIN_RESP_ID" reminded
expect "el enviado sin respuesta vuelve al INBOX de ana destacado y sin leer" \
  "$(fila "$TARRO_ANA2" INBOX "$SIN_RESP" '"\\Flagged" in m["flags"] and "\\Seen" not in m["flags"]')" "True"
expect "el contestado no se copia" "$(fila "$TARRO_ANA2" INBOX "$CON_RESP" 'm["uid"]')" ""
wm "$TARRO_ANA2" DELETE "/follow-ups/$CON_RESP_ID"
expect "uno ya resuelto no se cancela" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "409/REMINDER_NOT_PENDING"

# Respuestas rapidas: CRUD del buzon en la celda, HTML saneado y las variables tal cual.
wm "$TARRO_BEA2" POST /quick-replies -H 'Content-Type: application/json' \
  -d '{"name":"Saludo","html":"<p>Hola {nombre}, gracias por escribir a {empresa}.</p><script>x()</script>"}'
RAPIDA=$(echo "$WM_BODY" | jget data.id)
expect "bea crea una respuesta rapida" "$WM_CODE/${RAPIDA:+id}" "201/id"
lacks "guardada saneada" "$WM_BODY" "<script>"
contains "con sus variables tal cual en el texto" "$(echo "$WM_BODY" | jget data.text)" "Hola {nombre}, gracias por escribir a {empresa}."
wm "$TARRO_BEA2" POST /quick-replies -H 'Content-Type: application/json' -d '{"name":"saludo","html":"<p>Otra</p>"}'
expect "el nombre es unico en el buzon sin distinguir mayusculas" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "409/QUICK_REPLY_EXISTS"
wm "$TARRO_BEA2" POST /quick-replies -H 'Content-Type: application/json' -d '{"name":"Vacia","html":""}'
expect "sin contenido es un 422 en su campo" "$WM_CODE/$(echo "$WM_BODY" | jget error.details.field)" "422/html"
wm "$TARRO_BEA2" PUT "/quick-replies/$RAPIDA" -H 'Content-Type: application/json' -d '{"name":"Saludo formal","html":"<p>Estimado {nombre}:</p>"}'
expect "la edita" "$WM_CODE/$(echo "$WM_BODY" | jget data.name)" "200/Saludo formal"
wm "$TARRO_BEA2" GET /quick-replies
expect "la lista la trae con sus topes" "$(echo "$WM_BODY" | jpy '[i["name"] for i in d["data"]["items"]]')/$(echo "$WM_BODY" | jpy 'd["data"]["limits"]["max_items"] > 0')" "['Saludo formal']/True"
expect "es del buzon de bea en la celda" "$(sql mail_cell_pe_01 "SELECT count(*) FROM mail.mailbox_quick_replies WHERE id = '$RAPIDA'")" "1"
wm "$TARRO_ANA2" GET /quick-replies
lacks "ana no la ve" "$WM_BODY" "Saludo formal"
wm "$TARRO_ANA2" DELETE "/quick-replies/$RAPIDA"
expect "ni la puede borrar" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "404/QUICK_REPLY_NOT_FOUND"
wm "$TARRO_BEA2" DELETE "/quick-replies/$RAPIDA"
expect "bea la borra" "$WM_CODE" "204"
wm "$TARRO_BEA2" GET /quick-replies
expect "y ya no esta" "$(echo "$WM_BODY" | jpy 'len(d["data"]["items"])')" "0"

echo "== Webmail C3: zona horaria, apariciones sueltas, invitaciones iTIP, disponibilidad y citas (mail-dav)"
# Una serie semanal en Europe/Madrid: CalDAV la guarda con su VTIMEZONE y DTSTART con TZID, y una aparicion
# editada sola queda como un VEVENT con RECURRENCE-ID en la zona de la serie.
DIA_SERIE=$(date -u -d '+10 days' +%Y-%m-%d)
madrid() { TZ=Europe/Madrid date -d "$1" +%Y-%m-%dT%H:%M:%S%:z; }
wm "$TARRO_ANA2" POST "/calendar/events?notify=false" -H 'Content-Type: application/json' -d "{\"title\":\"Comite $TOKEN\",\"start\":\"$(madrid "$DIA_SERIE 09:00")\",\"end\":\"$(madrid "$DIA_SERIE 10:00")\",\"all_day\":false,\"timezone\":\"Europe/Madrid\",\"location\":\"\",\"description\":\"\",\"recurrence\":{\"freq\":\"weekly\",\"interval\":1,\"count\":3,\"until\":null,\"by_day\":[]}}"
SERIE=$(echo "$WM_BODY" | jget data.id)
expect "ana crea una serie semanal de tres en Europe/Madrid" "$WM_CODE/${SERIE:+id}/$(echo "$WM_BODY" | jget data.timezone)" "201/id/Europe/Madrid"
VENTANA="start=$(date -u -d "$DIA_SERIE -1 day" +%Y-%m-%dT00:00:00Z)&end=$(date -u -d "$DIA_SERIE +20 days" +%Y-%m-%dT00:00:00Z)"
wm "$TARRO_ANA2" GET "/calendar/events?$VENTANA"
RIDS=$(echo "$WM_BODY" | jpy '" ".join(o["recurrence_id"] for o in d["data"] if o["id"] == a[0])' "$SERIE")
expect "sus tres apariciones" "$(wc -w <<<"$RIDS")" "3"
expect "a las 09:00 de Madrid, en UTC" "$(echo "$WM_BODY" | jpy 'next(o["start"] for o in d["data"] if o["id"] == a[0])' "$SERIE")" \
  "$(date -u -d "$(madrid "$DIA_SERIE 09:00")" +%Y-%m-%dT%H:%M:%SZ)"
RID2=$(awk '{print $2}' <<<"$RIDS") RID3=$(awk '{print $3}' <<<"$RIDS")
DIA2=$(date -u -d "$RID2" +%Y-%m-%d)
wm "$TARRO_ANA2" PUT "/calendar/events/$SERIE/occurrences/$RID2?notify=false" -H 'Content-Type: application/json' -d "{\"title\":\"Comite movido $TOKEN\",\"start\":\"$(madrid "$DIA2 11:00")\",\"end\":\"$(madrid "$DIA2 12:00")\",\"all_day\":false,\"timezone\":\"Europe/Madrid\",\"location\":\"Sala 2\",\"description\":\"\"}"
expect "ana mueve solo la segunda a las 11:00" "$WM_CODE" "200"
wm "$TARRO_ANA2" DELETE "/calendar/events/$SERIE/occurrences/$RID3?notify=false"
expect "y borra solo la tercera" "$WM_CODE" "200"
wm "$TARRO_ANA2" GET "/calendar/events?$VENTANA"
expect "quedan dos apariciones: la original y la movida" \
  "$(echo "$WM_BODY" | jpy '" | ".join(o["title"] + " " + o["start"] for o in d["data"] if o["id"] == a[0])' "$SERIE")" \
  "Comite $TOKEN $(date -u -d "$(madrid "$DIA_SERIE 09:00")" +%Y-%m-%dT%H:%M:%SZ) | Comite movido $TOKEN $(date -u -d "$(madrid "$DIA2 11:00")" +%Y-%m-%dT%H:%M:%SZ)"
dav "ana@acme.test:$ANA_PASS" GET "$CAL/$SERIE.ics"
expect "CalDAV devuelve la serie" "$DAV_CODE" "200"
contains "con la zona como VTIMEZONE" "$DAV_BODY" "TZID:Europe/Madrid"
contains "y DTSTART en hora de Madrid" "$DAV_BODY" "DTSTART;TZID=Europe/Madrid:${DIA_SERIE//-/}T090000"
contains "la aparicion editada es un VEVENT con RECURRENCE-ID en la zona de la serie" "$DAV_BODY" "RECURRENCE-ID;TZID=Europe/Madrid:${DIA2//-/}T090000"
contains "con su nuevo titulo" "$DAV_BODY" "SUMMARY:Comite movido $TOKEN"
contains "y la borrada es un EXDATE" "$DAV_BODY" "EXDATE;TZID=Europe/Madrid:$(date -u -d "$RID3" +%Y%m%d)T090000"
wm "$TARRO_ANA2" DELETE "/calendar/events/$SERIE?notify=false"
expect "se borra la serie" "$WM_CODE" "204"

# Invitacion iTIP/iMIP: ana invita a bea; llega un text/calendar con METHOD:REQUEST, bea acepta desde el webmail
# (su calendario gana el evento y sale un REPLY), ana aplica el REPLY y ve a bea aceptada.
REUNION="Reunion $TOKEN"
INV_INI=$(date -u -d 'tomorrow 15:00' +%Y-%m-%dT%H:%M:%SZ) INV_FIN=$(date -u -d 'tomorrow 16:00' +%Y-%m-%dT%H:%M:%SZ)
wm "$TARRO_ANA2" POST /calendar/events -H 'Content-Type: application/json' -d "{\"title\":\"$REUNION\",\"start\":\"$INV_INI\",\"end\":\"$INV_FIN\",\"all_day\":false,\"timezone\":\"UTC\",\"location\":\"Sala 3\",\"description\":\"Orden del dia\",\"attendees\":[{\"email\":\"bea@acme.test\",\"name\":\"Bea\"}]}"
INV=$(echo "$WM_BODY" | jget data.id)
expect "ana crea una reunion con bea como invitada y el webmail envia la invitacion" \
  "$WM_CODE/${INV:+id}/$(echo "$WM_BODY" | jget data.invitations.method)/$(echo "$WM_BODY" | jget data.invitations.recipients)/$(echo "$WM_BODY" | jget data.invitations.sent)" "201/id/REQUEST/1/True"
expect "ana es la organizadora" "$(echo "$WM_BODY" | jget data.organizer.email)" "ana@acme.test"
# El asunto es "Invitación: <titulo>": IMAP SEARCH busca por el titulo, unico, para no depender de la tilde;
# fila, por el API del webmail, compara el asunto entero.
contains "bea recibe la invitacion por Postfix y Dovecot" "$(cliente buscar bea@acme.test "$BEA_PASS" "$REUNION")" "OK 1"
INV_UID=$(fila "$TARRO_BEA2" INBOX "Invitación: $REUNION" 'm["uid"]')
INV_RAW=$(curl -s -b "$TARRO_BEA2" -H "Origin: $API_ORIGIN" "$WM/folders/INBOX/messages/$INV_UID/raw")
contains "el mensaje lleva la parte text/calendar con method=REQUEST" "$INV_RAW" "text/calendar; charset=utf-8; method=REQUEST"
wm "$TARRO_BEA2" GET "/invitations/INBOX/$INV_UID"
expect "el webmail la interpreta: REQUEST de ana, bea invitada, sin responder y aun fuera de su calendario" \
  "$WM_CODE/$(echo "$WM_BODY" | jget data.method)/$(echo "$WM_BODY" | jget data.organizer.email)/$(echo "$WM_BODY" | jget data.attendee)/$(echo "$WM_BODY" | jpy 'd["data"]["attendees"][0]["partstat"]')/$(echo "$WM_BODY" | jget data.event_id)" \
  "200/REQUEST/ana@acme.test/bea@acme.test/NEEDS-ACTION/"
wm "$TARRO_BEA2" POST "/invitations/INBOX/$INV_UID/respond" -H 'Content-Type: application/json' -d '{"response":"ACCEPTED"}'
INV_BEA=$(echo "$WM_BODY" | jget data.event_id)
expect "bea acepta desde el webmail y sale la respuesta" "$WM_CODE/${INV_BEA:+id}/$(echo "$WM_BODY" | jget data.reply_sent)" "200/id/True"
wm "$TARRO_BEA2" GET "/calendar/events/$INV_BEA"
expect "la reunion queda en el calendario de bea" "$WM_CODE/$(echo "$WM_BODY" | jget data.title)/$(echo "$WM_BODY" | jget data.start)" "200/$REUNION/$INV_INI"
wm "$TARRO_BEA2" GET "/invitations/INBOX/$INV_UID"
expect "y la invitacion ya muestra su respuesta y su evento" "$(echo "$WM_BODY" | jget data.partstat)/$(echo "$WM_BODY" | jget data.event_id)" "ACCEPTED/$INV_BEA"
contains "ana recibe el REPLY" "$(cliente buscar ana@acme.test "$ANA_PASS" "Aceptada: $REUNION")" "OK 1"
REPLY_UID=$(fila "$TARRO_ANA2" INBOX "Aceptada: $REUNION" 'm["uid"]')
wm "$TARRO_ANA2" GET "/invitations/INBOX/$REPLY_UID"
expect "es un METHOD:REPLY de bea sobre su reunion" \
  "$(echo "$WM_BODY" | jget data.method)/$(echo "$WM_BODY" | jget data.event_id)/$(echo "$WM_BODY" | jpy 'd["data"]["attendees"][0]["partstat"]')" "REPLY/$INV/ACCEPTED"
wm "$TARRO_ANA2" POST "/invitations/INBOX/$REPLY_UID/apply"
expect "ana lo aplica a su calendario" "$WM_CODE/$(echo "$WM_BODY" | jget data.method)/$(echo "$WM_BODY" | jget data.changed)/$(echo "$WM_BODY" | jget data.event_id)" "200/REPLY/True/$INV"
wm "$TARRO_ANA2" GET "/calendar/events/$INV"
expect "y su evento muestra a bea aceptada" \
  "$(echo "$WM_BODY" | jpy '" ".join(x["email"] + "=" + x["partstat"] for x in d["data"]["attendees"])')" "bea@acme.test=ACCEPTED"
dav "ana@acme.test:$ANA_PASS" GET "$CAL/$INV.ics"
contains "CalDAV lo guarda en el ATTENDEE" "$(tr -d '\r\n ' <<<"$DAV_BODY")" "PARTSTAT=ACCEPTED"
lacks "sin METHOD en el objeto guardado" "$DAV_BODY" "METHOD:"

# Disponibilidad: bea ve cuando esta ocupada ana, solo el intervalo, nunca el contenido.
wm "$TARRO_BEA2" GET "/availability?addresses=ana@acme.test&start=$(date -u -d 'tomorrow 00:00' +%Y-%m-%dT%H:%M:%SZ)&end=$(date -u -d 'tomorrow 23:59' +%Y-%m-%dT%H:%M:%SZ)"
expect "bea ve a ana ocupada durante la reunion" \
  "$WM_CODE/$(echo "$WM_BODY" | jpy 'd["data"][0]["known"]')/$(echo "$WM_BODY" | jpy '" ".join(b["start"] + "-" + b["end"] for b in d["data"][0]["busy"])')" "200/True/$INV_INI-$INV_FIN"
expect "solo con inicio y fin" "$(echo "$WM_BODY" | jpy 'sorted(set(k for b in d["data"][0]["busy"] for k in b))')" "['end', 'start']"
lacks "sin el titulo" "$WM_BODY" "$REUNION"
lacks "ni el lugar" "$WM_BODY" "Sala 3"

# Pagina publica de citas: ana la configura; un visitante sin sesion lista huecos, reserva y el hueco desaparece.
wm "$TARRO_ANA2" PUT /booking -H 'Content-Type: application/json' -d '{"title":"Citas con ana","description":"Treinta minutos","duration_minutes":30,"buffer_minutes":0,"min_notice_minutes":0,"max_advance_days":30,"daily_limit":5,"timezone":"UTC","weekly":{"MO":[{"start":"00:00","end":"24:00"}],"TU":[{"start":"00:00","end":"24:00"}],"WE":[{"start":"00:00","end":"24:00"}],"TH":[{"start":"00:00","end":"24:00"}],"FR":[{"start":"00:00","end":"24:00"}],"SA":[{"start":"00:00","end":"24:00"}],"SU":[{"start":"00:00","end":"24:00"}]},"active":true,"regenerate_link":false}'
PAGINA=$(echo "$WM_BODY" | jget data.public_id)
expect "ana configura su pagina de citas" "$WM_CODE/${PAGINA:+id}/$(echo "$WM_BODY" | jget data.cell)/$(echo "$WM_BODY" | jget data.tenant_id)" "200/id/pe-01/$TID"
CITAS="$GW/public/booking/pe-01/$TID/$PAGINA"
CITA_DIA=$(date -u -d '+3 days' +%Y-%m-%d)
CITA_VENTANA="start=${CITA_DIA}T00:00:00Z&end=$(date -u -d "$CITA_DIA +1 day" +%Y-%m-%d)T00:00:00Z"
publico() { # publico <metodo> <url> [curl...]: deja PUB_CODE y PUB_BODY
  local metodo="$1" url="$2"
  shift 2
  PUB_CODE=$(curl -s -o "$WORK/pub.out" -w '%{http_code}' -X "$metodo" "$url" "$@")
  PUB_BODY=$(cat "$WORK/pub.out")
}
publico GET "$CITAS?$CITA_VENTANA"
HUECO=$(echo "$PUB_BODY" | jpy 'd["data"]["slots"][4]["start"]')
expect "sin sesion, por el gateway, la pagina lista sus huecos de 30 minutos" \
  "$PUB_CODE/$(echo "$PUB_BODY" | jpy 'd["data"]["title"]')/$(echo "$PUB_BODY" | jpy 'len(d["data"]["slots"])')/$HUECO" "200/Citas con ana/48/${CITA_DIA}T02:00:00Z"
lacks "sin la direccion de ana" "$PUB_BODY" "ana@acme.test"
publico GET "$GW/public/booking/pe-01/$TID/$(rand_hex 16)?$CITA_VENTANA"
expect "una pagina que no existe es 404" "$PUB_CODE" "404"
reservar() { publico POST "$CITAS" -H "Origin: $API_ORIGIN" -H 'Content-Type: application/json' -d "$1"; }
reservar "{\"start\":\"$HUECO\",\"name\":\"Robot\",\"email\":\"carla@acme.test\",\"note\":\"\",\"website\":\"https://spam.example\"}"
expect "la trampa para robots responde como si reservara" "$PUB_CODE" "201"
publico GET "$CITAS?$CITA_VENTANA"
contains "pero el hueco sigue libre" "$PUB_BODY" "\"start\":\"$HUECO\""
reservar "{\"start\":\"$HUECO\",\"name\":\"Carla Cliente\",\"email\":\"carla@acme.test\",\"note\":\"Quiero una demo\",\"website\":\"\"}"
expect "un visitante reserva el hueco" "$PUB_CODE/$(echo "$PUB_BODY" | jget data.start)/$(echo "$PUB_BODY" | jget data.confirmation_sent)" "201/$HUECO/True"
publico GET "$CITAS?$CITA_VENTANA"
lacks "el hueco ya no aparece" "$PUB_BODY" "\"start\":\"$HUECO\""
expect "y quedan los demas" "$(echo "$PUB_BODY" | jpy 'len(d["data"]["slots"])')" "47"
reservar "{\"start\":\"$HUECO\",\"name\":\"Otra\",\"email\":\"bea@acme.test\",\"note\":\"\",\"website\":\"\"}"
expect "reservar el mismo hueco es SLOT_UNAVAILABLE" "$PUB_CODE/$(echo "$PUB_BODY" | jget error.code)" "409/SLOT_UNAVAILABLE"
wm "$TARRO_ANA2" GET "/calendar/events?start=${CITA_DIA}T00:00:00Z&end=${CITA_DIA}T23:59:59Z"
CITA=$(echo "$WM_BODY" | jpy 'next(o["id"] for o in d["data"] if o["start"] == a[0])' "$HUECO")
expect "la cita esta en el calendario de ana" "$(echo "$WM_BODY" | jpy 'next(o["title"] for o in d["data"] if o["start"] == a[0])' "$HUECO")" "Citas con ana"
wm "$TARRO_ANA2" GET "/calendar/events/$CITA"
expect "con ana aceptada y el visitante invitado" \
  "$(echo "$WM_BODY" | jpy '" ".join(sorted(x["email"] + "=" + x["partstat"] for x in d["data"]["attendees"]))')" "ana@acme.test=ACCEPTED carla@acme.test=NEEDS-ACTION"
contains "y quien reservo en la descripcion" "$(echo "$WM_BODY" | jget data.description)" "Carla Cliente <carla@acme.test>"
contains "la confirmacion llega al visitante" "$(cliente buscar carla@acme.test "$CARLA_PASS" "Cita confirmada: Citas con ana")" "OK 1"
contains "y a ana en copia" "$(cliente buscar ana@acme.test "$ANA_PASS" "Cita confirmada: Citas con ana")" "OK 1"
wm "$TARRO_ANA2" DELETE "/calendar/events/$CITA?notify=false"
wm "$TARRO_ANA2" DELETE "/calendar/events/$INV?notify=false"
expect "se retiran la cita y la reunion" "$WM_CODE" "204"

echo "== Webmail C2: el mensaje pospuesto vuelve a su hora"
volvio() { [[ "$(sql mail_cell_pe_01 "SELECT status || '/' || result FROM mail.mailbox_reminders WHERE id = '$POSPONER_ID'")" == done/returned ]]; }
esperar "el trabajador del webmail lo devuelve (returned)" 120 volvio
expect "vuelve a INBOX sin \\Seen" "$(fila "$TARRO_BEA2" INBOX "$POSPONER" '"\\Seen" in m["flags"]')" "False"
expect "y sale de Snoozed" "$(fila "$TARRO_BEA2" Snoozed "$POSPONER" 'm["uid"]')" ""
wm "$TARRO_BEA2" GET /snooze
lacks "ni sigue en la lista de pospuestos" "$WM_BODY" "$POSPONER_ID"

echo "== Webmail C4: ficheros grandes por enlace (mail-files con ClamAV real y MinIO, enlace publico por el gateway)"
# mail-files es del plano de EMPRESA y corre en el host, como mail-dav: la base de la empresa se alcanza por el
# host de Postgres que el registro guarda para la celda (127.0.0.1 en la prueba), que un contenedor no ve. Entra
# con su credencial propia (tenant-service-role.sh) y el rol de enrutado, analiza con el clamd de los motores y
# guarda en un MinIO desechable con la imagen, el arranque y la inicializacion del perfil autoalojado
# (selfhosted/minio: bucket privado y usuario de servicio con politica acotada).
ROUTER_PASS="$(rand_hex 24)"
e2e_credencial_enrutado "$ROUTER_PASS"
FILES_DB_PASS="$(rand_hex 24)"
MAIL_FILES_DB_PASSWORD="${FILES_DB_PASS}" PGHOST=127.0.0.1 bash ops/db/tenant-service-role.sh --service mail-files >"$WORK/log/files-role.log" 2>&1 &&
  ok "credencial propia de mail-files en la base de la empresa (tenant-service-role.sh)" ||
  { mal "tenant-service-role.sh --service mail-files"; tail -5 "$WORK/log/files-role.log" >&2; }
MINIO_RAIZ=e2e-raiz MINIO_ROOT_PASS="$(rand_hex 20)" MINIO_SVC_KEY="svc$(rand_hex 8)" MINIO_SVC_SECRET="$(rand_hex 18)"
MINIO_ROOT_USER="${MINIO_RAIZ}" MINIO_ROOT_PASSWORD="$MINIO_ROOT_PASS" MINIO_ACCESS_KEY="$MINIO_SVC_KEY" MINIO_SECRET_KEY="$MINIO_SVC_SECRET" \
  compose --profile ficheros up -d minio minio-init >"$WORK/log/compose-ficheros.log" 2>&1 ||
  { mal "compose up de MinIO"; tail -20 "$WORK/log/compose-ficheros.log" >&2; }
minio_listo() { [[ "$(docker inspect -f '{{.State.Status}}/{{.State.ExitCode}}' "$(c minio-init)" 2>/dev/null)" == exited/0 ]]; }
esperar "MinIO sano y minio-init deja el bucket privado y el usuario de servicio" 90 minio_listo
mkdir -m 700 "$WORK/spool-ficheros"
SERVICIOS_DE_EMPRESA=" mail-files " MAIL_FILES_PORT="${PORT[mail-files]}" TENANT_DB_USER=mail_svc_mail_files TENANT_DB_PASSWORD="$FILES_DB_PASS" \
  MAIL_FILES_CLAMD_ADDR="127.0.0.1:$CLAMD_PORT" MAIL_FILES_SPOOL_DIR="$WORK/spool-ficheros" \
  MINIO_ENDPOINT="127.0.0.1:$MINIO_PORT" MINIO_USE_SSL=false MINIO_BUCKET="$E2E_MINIO_BUCKET" MINIO_ACCESS_KEY="$MINIO_SVC_KEY" MINIO_SECRET_KEY="$MINIO_SVC_SECRET" \
  arrancar mail-files
esperar_salud mail-files "${PORT[mail-files]}" && ok "mail-files responde"
minio_objeto() { docker exec -e MC_HOST_local="http://$MINIO_RAIZ:$MINIO_ROOT_PASS@127.0.0.1:9000" "$(c minio)" mc --config-dir /tmp/mc --quiet stat "local/$E2E_MINIO_BUCKET/$1" >/dev/null 2>&1; }
wm "$TARRO_ANA2" GET /large-files
expect "el webmail ofrece la funcion con los topes de mail-files" \
  "$WM_CODE/$(echo "$WM_BODY" | jget data.enabled)/$(echo "$WM_BODY" | jpy 'd["data"]["limits"]["max_file_bytes"]')" "200/True/104857600"
head -c $((3 << 20)) /dev/urandom >"$WORK/plano.bin"
PLANO_SHA=$(sha256sum "$WORK/plano.bin" | cut -d' ' -f1)
wm "$TARRO_ANA2" POST "/large-files?expires_in_days=2&max_downloads=3" -F "file=@$WORK/plano.bin;filename=plano-obra.bin;type=application/octet-stream"
FICHERO=$(echo "$WM_BODY" | jget data.id) ENLACE=$(echo "$WM_BODY" | jget data.url)
expect "ana sube un fichero de 3 MiB: ClamAV lo analiza y queda activo" \
  "$WM_CODE/${FICHERO:+id}/$(echo "$WM_BODY" | jget data.state)/$(echo "$WM_BODY" | jget data.size_bytes)/$(echo "$WM_BODY" | jget data.sha256)" \
  "201/id/active/$((3 << 20))/$PLANO_SHA"
expect "con un enlace publico del gateway firmado y con caducidad" \
  "$(python3 -c 'import sys, urllib.parse as u; p = u.urlsplit(sys.argv[1]); q = u.parse_qs(p.query); print(p.scheme + "://" + p.netloc + p.path, sorted(q), len(q["s"][0]))' "$ENLACE" 2>/dev/null)" \
  "$PUBLIC_BASE_URL/api/v1/public/files/$TID/$FICHERO ['s', 'x'] 64"
expect "el objeto esta en el espacio privado del almacen" "$(minio_objeto "private/$TID/mail-files/$FICHERO" && echo si)" "si"
expect "y la fila en la base de la empresa, lista" "$(sql mail_tenant_acme "SELECT status FROM mail_files.shared_files WHERE id = '$FICHERO'")" "ready"
publico GET "$ENLACE" -D "$WORK/pub.hdr"
expect "el enlace sin sesion muestra la ficha del fichero (HTML)" "$PUB_CODE/$(grep -ci '^content-type: text/html' "$WORK/pub.hdr")" "200/1"
contains "con su nombre" "$PUB_BODY" "plano-obra.bin"
wm "$TARRO_ANA2" GET /large-files
expect "ver la ficha no cuenta como descarga" "$(echo "$WM_BODY" | jpy 'next(i["downloads"] for i in d["data"]["items"] if i["id"] == a[0])' "$FICHERO")" "0"
PUB_CODE=$(curl -s -o "$WORK/descarga.bin" -D "$WORK/pub.hdr" -w '%{http_code}' -X POST "$ENLACE" -H "Origin: $API_ORIGIN")
expect "la descarga (POST) entrega el mismo fichero" "$PUB_CODE/$(sha256sum "$WORK/descarga.bin" | cut -d' ' -f1)" "200/$PLANO_SHA"
contains "como adjunto" "$(grep -i '^content-disposition:' "$WORK/pub.hdr")" 'attachment; filename="plano-obra.bin"'
contains "con nosniff" "$(grep -i '^x-content-type-options:' "$WORK/pub.hdr")" "nosniff"
contains "y como octet-stream, nunca con el tipo que dijo quien lo subio" "$(grep -i '^content-type:' "$WORK/pub.hdr")" "application/octet-stream"
wm "$TARRO_ANA2" GET /large-files
expect "cuenta una descarga" "$(echo "$WM_BODY" | jpy 'next(str(i["downloads"]) + "/" + str(i["remaining_downloads"]) for i in d["data"]["items"] if i["id"] == a[0])' "$FICHERO")" "1/2"
publico GET "$(python3 -c 'import re, sys; print(re.sub(r"([?&]s=)[0-9a-f]+", r"\g<1>" + sys.argv[2], sys.argv[1]))' "$ENLACE" "$(rand_hex 32)")"
expect "una firma alterada es 404" "$PUB_CODE" "404"
wm "$TARRO_ANA2" POST /large-files -F "file=@$WORK/eicar.com;filename=factura.exe;type=application/octet-stream"
expect "un fichero con EICAR lo rechaza ClamAV antes de guardarlo" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "422/FILE_INFECTED"
expect "sin fila lista para el" "$(sql mail_tenant_acme "SELECT count(*) FROM mail_files.shared_files WHERE file_name = 'factura.exe' AND status = 'ready'")" "0"
wm "$TARRO_BEA2" DELETE "/large-files/$FICHERO"
expect "bea no puede revocar el enlace de ana" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "404/FILE_NOT_FOUND"
wm "$TARRO_ANA2" DELETE "/large-files/$FICHERO"
expect "ana revoca el enlace" "$WM_CODE/$(echo "$WM_BODY" | jget data.state)/$(echo "$WM_BODY" | jget data.url)" "200/revoked/"
publico GET "$ENLACE"
expect "el enlace revocado es 404" "$PUB_CODE" "404"
publico POST "$ENLACE" -H "Origin: $API_ORIGIN"
expect "y no descarga" "$PUB_CODE" "404"
expect "el objeto se borra del almacen al revocar (con la politica del usuario de servicio)" \
  "$(minio_objeto "private/$TID/mail-files/$FICHERO" && echo sigue || echo borrado)" "borrado"
expect "y la fila lo anota" "$(sql mail_tenant_acme "SELECT status || '/' || (object_deleted_at IS NOT NULL) FROM mail_files.shared_files WHERE id = '$FICHERO'")" "revoked/true"

echo "== Webmail C5: asistente sin ANTHROPIC_API_KEY (apagado)"
wm "$TARRO_ANA2" GET /assistant
expect "sin clave el asistente dice que no esta disponible y por que" \
  "$WM_CODE/$(echo "$WM_BODY" | jget data.available)/$(echo "$WM_BODY" | jget data.reason)" "200/False/not_configured"
wm "$TARRO_ANA2" POST /assistant/summarize -H 'Content-Type: application/json' -d "{\"messages\":[{\"folder\":\"INBOX\",\"uid\":$RESP_ANA}]}"
expect "resumir responde 503 ASSISTANT_NOT_CONFIGURED" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "503/ASSISTANT_NOT_CONFIGURED"
wm "$TARRO_ANA2" POST /assistant/reply -H 'Content-Type: application/json' -d "{\"folder\":\"INBOX\",\"uid\":$RESP_ANA,\"instructions\":\"breve\"}"
expect "proponer respuesta tambien" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "503/ASSISTANT_NOT_CONFIGURED"
wm "$TARRO_ANA2" POST /assistant/tone -H 'Content-Type: application/json' -d '{"text":"hola, te escribo por lo de ayer","tone":"formal"}'
expect "cambiar el tono tambien" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "503/ASSISTANT_NOT_CONFIGURED"
wm "$TARRO_ANA2" POST /assistant/extract -H 'Content-Type: application/json' -d "{\"folder\":\"INBOX\",\"uid\":$RESP_ANA,\"today\":\"$(date -u +%Y-%m-%d)\"}"
expect "y extraer tareas y fechas" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "503/ASSISTANT_NOT_CONFIGURED"
# Los bloques de la fase 2 esperan a los motores varias veces: se renuevan los access tokens de la plataforma.
T1=$(e2e_login "$ADMIN_EMAIL" "$ADMIN_PASS" | jget data.access_token)
T2=$(e2e_login admin@acme.test "$TENANT_PASS" | jget data.access_token)
A2="Authorization: Bearer $T2"
[[ -n "$T1" && -n "$T2" ]] && ok "se renuevan los access tokens de la plataforma" || mal "no se pudieron renovar los access tokens"

echo "== Maildir de un buzon borrado: marca de baja, barrido en Dovecot y buzon recreado limpio"
# Borrar un buzon quita su fila, pero su maildir sigue en el volumen de Dovecot y quien reciba despues la
# misma direccion heredaria el correo del titular anterior. mail-directory deja una marca de baja
# (mail.mailbox_deletions) en la transaccion del borrado y retiene la direccion (409) mientras la marca
# viva; maildir_reconcile.sh (cron de Dovecot, cada 5 min) mueve el maildir a _garbage y consume la marca
# (deploy/mail/README.md, "Maildir de un buzon borrado"). Aqui la pasada se fuerza con la gracia a cero.
DANI_PASS="$(rand_hex 10)Aa1!"
creado "buzon dani@acme.test" POST /mailboxes "{\"local_part\":\"dani\",\"domain\":\"acme.test\",\"password\":\"$DANI_PASS\"}"
DANIID=$(echo "$API_BODY" | jget data.id)
contains "ana escribe a dani" "$(cliente enviar ana@acme.test "$ANA_PASS" ana@acme.test dani@acme.test "$TOKEN-dani")" "OK 250"
contains "y dani lo lee por IMAP" "$(cliente buscar dani@acme.test "$DANI_PASS" "$TOKEN-dani")" "OK 1"
maildir_de() { en dovecot-mail test -d "/var/vmail/acme.test/$1"; }
maildir_de dani && ok "el maildir /var/vmail/acme.test/dani existe en el volumen de Dovecot" || mal "sin maildir de dani en el volumen"
# barrido [opciones de docker exec]: una pasada de maildir_reconcile.sh (se reejecuta como vmail) con la gracia a cero.
barrido() {
  BARRIDO_OUT=$(docker exec -e MAILDIR_RECONCILE_GRACE=0 "$@" "$(c dovecot-mail)" /usr/local/bin/maildir_reconcile.sh 2>&1)
  BARRIDO_RC=$?
}
marcas_de_dani() { sql mail_cell_pe_01 "SELECT count(*) FROM mail.mailbox_deletions WHERE username = 'dani@acme.test'"; }
en_garbage() { en dovecot-mail sh -c 'ls /var/vmail/_garbage/' 2>/dev/null | grep -E "^[0-9]{10}_$1\$"; }
barrido
expect "una pasada con todos los buzones vivos termina bien" "$BARRIDO_RC" "0"
contains "y no mueve nada" "$BARRIDO_OUT" " 0 movidos a _garbage"
maildir_de dani && maildir_de bea && ok "los maildir de dani y bea (con correo) siguen en su sitio" || mal "una pasada sin huerfanos movio un buzon vivo"

K_DANI=$(revocaciones kick)
api DELETE "/mailboxes/$DANIID"
expect "mail-directory borra a dani" "$API_CODE" "204"
# El borrado echa de Dovecot las sesiones de dani por un evento asincrono. Aqui el barrido se fuerza al
# momento y dani se recrea en menos de un segundo, asi que sin esta espera la expulsion del dani borrado
# alcanzaba la sesion del dani nuevo ("Server shutting down"). En produccion no pasa: la direccion queda
# retenida hasta la siguiente pasada del barrido, cada 5 minutos.
dani_expulsado() { (( $(revocaciones kick) > K_DANI )); }
esperar "mail-security echa de Dovecot las sesiones del dani borrado" 30 dani_expulsado
expect "y deja su marca de baja en mail.mailbox_deletions" "$(marcas_de_dani)" "1"
api POST /mailboxes "{\"local_part\":\"dani\",\"domain\":\"acme.test\",\"password\":\"$DANI_PASS\"}"
expect "recrear dani con la marca viva se rechaza (409): su maildir sigue en el disco" "$API_CODE" "409"
contains "con el codigo ADDRESS_RECENTLY_DELETED" "$API_BODY" "ADDRESS_RECENTLY_DELETED"
maildir_de dani && ok "el maildir de dani sigue en el volumen tras el borrado (nadie lo toca desde Go)" || mal "el maildir de dani desaparecio sin barrido"

# Fail-closed: sin acceso a la base la pasada no mueve nada ni consume la marca.
barrido -e MAIL_DB_PASSWORD=incorrecta
[[ "$BARRIDO_RC" -ne 0 ]] && ok "sin acceso a la base la pasada termina con error" || mal "sin acceso a la base la pasada termino en 0"
contains "y anuncia que aborta sin mover nada" "$BARRIDO_OUT" "ABORTADA sin mover nada"
maildir_de dani && ok "el maildir de dani sigue ahi" || mal "una pasada sin base movio el maildir de dani"
expect "y la marca sigue viva" "$(marcas_de_dani)" "1"

barrido
expect "la pasada con acceso a la base termina bien" "$BARRIDO_RC" "0"
contains "mueve el maildir de dani a _garbage con su marca de tiempo" "$BARRIDO_OUT" "movido acme.test/dani -> _garbage/"
contains "anotando su tamano y el motivo" "$BARRIDO_OUT" "KiB, baja marcada en @"
contains "y consume la marca" "$BARRIDO_OUT" "maildir de acme.test/dani movido"
[[ -n "$(en_garbage acme.test_dani)" ]] && ok "el maildir esta en /var/vmail/_garbage/<epoch>_acme.test_dani (lo purga maildir_gc.sh a los MAILDIR_GC_TIME min)" || mal "sin <epoch>_acme.test_dani en _garbage: $(en dovecot-mail sh -c 'ls /var/vmail/_garbage/')"
maildir_de dani && mal "el maildir de dani sigue en /var/vmail/acme.test tras el barrido" || ok "y ya no esta en /var/vmail/acme.test"
expect "la marca de baja se consumio" "$(marcas_de_dani)" "0"
maildir_de bea && ok "el maildir de bea (buzon vivo con correo) no se movio" || mal "el barrido movio el maildir de bea"

DANI_PASS2="$(rand_hex 10)Aa1!"
creado "dani@acme.test se vuelve a crear en cuanto la marca se consumio" POST /mailboxes "{\"local_part\":\"dani\",\"domain\":\"acme.test\",\"password\":\"$DANI_PASS2\"}"
expect "el dani nuevo entra por IMAP" "$(cliente login dani@acme.test "$DANI_PASS2")" "OK"
expect "y nace vacio: no hereda el mensaje del dani anterior" "$(cliente buscar dani@acme.test "$DANI_PASS2" "$TOKEN-dani" --espera 3)" "NO 0"
contains "ana escribe al dani nuevo" "$(cliente enviar ana@acme.test "$ANA_PASS" ana@acme.test dani@acme.test "$TOKEN-dani2")" "OK 250"
contains "y lo lee" "$(cliente buscar dani@acme.test "$DANI_PASS2" "$TOKEN-dani2")" "OK 1"
barrido
expect "otra pasada con el dani nuevo vivo termina bien" "$BARRIDO_RC" "0"
contains "y no mueve nada" "$BARRIDO_OUT" " 0 movidos a _garbage"
maildir_de dani && ok "el maildir del dani nuevo sigue en su sitio" || mal "el barrido movio el maildir del dani recreado"

echo "== Verificacion en dos pasos, contrasenas de aplicacion y reenvio externo (docs/Plan_Webmail_Seguridad.md)"
# erika es un buzon propio de esta seccion: activar la verificacion le cambia la credencial de IMAP.
ERIKA_PASS="$(rand_hex 10)Aa1!"
creado "erika@acme.test se crea para la seccion" POST /mailboxes "{\"local_part\":\"erika\",\"domain\":\"acme.test\",\"password\":\"$ERIKA_PASS\"}"
ERIKAID=$(echo "$API_BODY" | jget data.id)
expect "erika entra por IMAP con su contrasena" "$(cliente login erika@acme.test "$ERIKA_PASS")" "OK"
# totp <secreto>: codigo TOTP (RFC 6238, SHA-1, 6 cifras, 30 s) del paso actual.
totp() {
  python3 - "$1" <<'PY'
import base64, hashlib, hmac, struct, sys, time
key = base64.b32decode(sys.argv[1].upper() + "=" * (-len(sys.argv[1]) % 8))
mac = hmac.new(key, struct.pack(">Q", int(time.time()) // 30), hashlib.sha1).digest()
off = mac[-1] & 0x0F
print("%06d" % ((struct.unpack(">I", mac[off:off + 4])[0] & 0x7FFFFFFF) % 1000000))
PY
}
# paso_nuevo: espera al siguiente paso de 30 s. Cada codigo vale una sola vez (anti-repeticion).
paso_nuevo() { sleep $(( 31 - $(date +%s) % 30 )); }
TARRO_ERIKA="$WORK/erika.cookies"
wm "$TARRO_ERIKA" POST /session -H 'Content-Type: application/json' -d "{\"username\":\"erika@acme.test\",\"password\":\"$ERIKA_PASS\"}"
expect "sin verificacion, erika entra al webmail en un paso" "$WM_CODE/$(echo "$WM_BODY" | jget data.username)" "200/erika@acme.test"
wm "$TARRO_ERIKA" POST /security/mfa/setup -H 'Content-Type: application/json' -d '{"current_password":"no-es-la-suya"}'
expect "preparar la verificacion exige la contrasena actual" "$WM_CODE" "401"
wm "$TARRO_ERIKA" POST /security/mfa/setup -H 'Content-Type: application/json' -d "{\"current_password\":\"$ERIKA_PASS\"}"
SECRETO=$(echo "$WM_BODY" | jget data.secret)
contains "con la contrasena se prepara: secreto y URI otpauth" "$WM_CODE $(echo "$WM_BODY" | jget data.provisioning_uri)" "200 otpauth://totp/"
wm "$TARRO_ERIKA" POST /security/mfa/activate -H 'Content-Type: application/json' -d "{\"secret\":\"$SECRETO\",\"code\":\"000000\"}"
expect "un codigo malo no la activa" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "422/INVALID_MFA_CODE"
wm "$TARRO_ERIKA" POST /security/mfa/activate -H 'Content-Type: application/json' -d "{\"secret\":\"$SECRETO\",\"code\":\"$(totp "$SECRETO")\"}"
RECUPERACION=$(echo "$WM_BODY" | python3 -c 'import json, sys; print(" ".join(json.load(sys.stdin)["data"]["recovery_codes"]))' 2>/dev/null)
expect "con el codigo de la aplicacion se activa y entrega 10 codigos de recuperacion" "$WM_CODE/$(wc -w <<<"$RECUPERACION")" "200/10"
expect "el secreto queda cifrado en mail.mailbox_mfa, no en claro" \
  "$(sql mail_cell_pe_01 "SELECT mfa_enabled AND position(convert_to('$SECRETO','UTF8') in secret_enc) = 0 FROM mail.mailboxes m JOIN mail.mailbox_mfa f ON f.mailbox_id = m.id WHERE m.username = 'erika@acme.test'")" "t"
expect "y mail-directory deja el evento mail.mailbox.mfa_enabled en su outbox" \
  "$(sql mail_cell_pe_01 "SELECT count(*) FROM platform.event_outbox WHERE subject = 'mail.mailbox.mfa_enabled' AND payload::text LIKE '%erika@acme.test%'")" "1"
imap_principal_rechazada() { login_rechazado erika@acme.test "$ERIKA_PASS"; }
esperar "con la verificacion activa, IMAP rechaza la contrasena principal (mail-security vacia la cache de Dovecot)" 30 imap_principal_rechazada
contains "mail-auth explica en su registro que hace falta una contrasena de aplicacion" \
  "$(docker logs "$(c mail-auth)" 2>&1 | grep '"username":"erika@acme.test"' | grep '"service":"imap"')" "hace falta una contrasena de aplicacion"

paso_nuevo
wm "$TARRO_ERIKA" POST /security/app-passwords -H 'Content-Type: application/json' -d "{\"name\":\"Thunderbird\",\"imap\":true,\"smtp\":true,\"current_password\":\"$ERIKA_PASS\"}"
expect "crear una contrasena de aplicacion con verificacion activa exige el codigo" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "403/MFA_REQUIRED"
wm "$TARRO_ERIKA" POST /security/app-passwords -H 'Content-Type: application/json' -d "{\"name\":\"Thunderbird\",\"imap\":true,\"smtp\":true,\"current_password\":\"$ERIKA_PASS\",\"code\":\"$(totp "$SECRETO")\"}"
ERIKA_APP=$(echo "$WM_BODY" | jget data.password)
expect "con contrasena y codigo el webmail la crea y la ensena una vez" "$WM_CODE/$(echo "$WM_BODY" | jget data.imap_access)" "201/True"
expect "IMAP acepta la contrasena de aplicacion" "$(cliente login erika@acme.test "$ERIKA_APP")" "OK"
wm "$TARRO_ERIKA" GET /security
expect "la pestana de seguridad la lista con el tope del directorio" "$(echo "$WM_BODY" | jget data.mfa.enabled)/$(echo "$WM_BODY" | jget data.app_passwords.0.name)/$(echo "$WM_BODY" | jget data.app_passwords_max)" "True/Thunderbird/25"

TARRO_ERIKA2="$WORK/erika2.cookies"
wm "$TARRO_ERIKA2" POST /session -H 'Content-Type: application/json' -d "{\"username\":\"erika@acme.test\",\"password\":\"$ERIKA_PASS\"}"
expect "con verificacion, la contrasena sola no abre sesion: pide el codigo" "$WM_CODE/$(echo "$WM_BODY" | jget data.mfa_required)" "200/True"
contains "con la cookie del desafio (cf_wm_mfa)" "$(cat "$TARRO_ERIKA2")" "cf_wm_mfa"
lacks "y sin cookie de sesion" "$(grep -v cf_wm_mfa "$TARRO_ERIKA2")" "cf_wm"
wm "$TARRO_ERIKA2" GET /folders
expect "a medias no se lee el buzon" "$WM_CODE" "401"
wm "$TARRO_ERIKA2" POST /session/mfa -H 'Content-Type: application/json' -d '{"code":"000000"}'
expect "un codigo malo en el segundo paso" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "422/INVALID_MFA_CODE"
paso_nuevo
CODIGO=$(totp "$SECRETO")
wm "$TARRO_ERIKA2" POST /session/mfa -H 'Content-Type: application/json' -d "{\"code\":\"$CODIGO\"}"
expect "con el codigo del paso se abre la sesion" "$WM_CODE/$(echo "$WM_BODY" | jget data.username)" "200/erika@acme.test"
wm "$TARRO_ERIKA2" GET /folders
expect "y lee sus carpetas" "$WM_CODE" "200"
TARRO_ERIKA3="$WORK/erika3.cookies"
wm "$TARRO_ERIKA3" POST /session -H 'Content-Type: application/json' -d "{\"username\":\"erika@acme.test\",\"password\":\"$ERIKA_PASS\"}"
wm "$TARRO_ERIKA3" POST /session/mfa -H 'Content-Type: application/json' -d "{\"code\":\"$CODIGO\"}"
expect "el mismo codigo no vale dos veces (anti-repeticion)" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "422/INVALID_MFA_CODE"
PRIMERO=$(awk '{print $1}' <<<"$RECUPERACION")
wm "$TARRO_ERIKA3" POST /session/mfa -H 'Content-Type: application/json' -d "{\"code\":\"$PRIMERO\"}"
expect "un codigo de recuperacion abre la sesion" "$WM_CODE/$(echo "$WM_BODY" | jget data.username)" "200/erika@acme.test"
TARRO_ERIKA4="$WORK/erika4.cookies"
wm "$TARRO_ERIKA4" POST /session -H 'Content-Type: application/json' -d "{\"username\":\"erika@acme.test\",\"password\":\"$ERIKA_PASS\"}"
wm "$TARRO_ERIKA4" POST /session/mfa -H 'Content-Type: application/json' -d "{\"code\":\"$PRIMERO\"}"
expect "y se gasta: el mismo codigo de recuperacion ya no vale" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "422/INVALID_MFA_CODE"
for _ in 1 2 3; do wm "$TARRO_ERIKA4" POST /session/mfa -H 'Content-Type: application/json' -d '{"code":"000000"}'; done
wm "$TARRO_ERIKA4" POST /session/mfa -H 'Content-Type: application/json' -d '{"code":"000000"}'
expect "el quinto fallo aun es un codigo malo" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "422/INVALID_MFA_CODE"
wm "$TARRO_ERIKA4" POST /session/mfa -H 'Content-Type: application/json' -d '{"code":"000000"}'
expect "pero borra el desafio: lo siguiente pide volver a empezar" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "401/MFA_CHALLENGE_EXPIRED"

FUERA='{"rules":[],"forwarding":{"enabled":true,"addresses":["fuera@ejemplo.test"],"keep_copy":true}}'
wm "$TARRO_ERIKA2" PUT /filters -H 'Content-Type: application/json' -d "$FUERA"
expect "reenviar fuera de la empresa pide volver a autenticarse" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "403/REAUTH_REQUIRED"
contains "y dice a que direcciones" "$WM_BODY" "fuera@ejemplo.test"
expect "sin guardar nada" "$(sql mail_cell_pe_01 "SELECT count(*) FROM mail.v_sieve_user WHERE username = 'erika@acme.test'")" "0"
wm "$TARRO_ERIKA2" PUT /filters -H 'Content-Type: application/json' -d '{"rules":[],"forwarding":{"enabled":true,"addresses":["ana@acme.test"],"keep_copy":true}}'
expect "reenviar dentro de la empresa no la pide" "$WM_CODE" "200"
paso_nuevo
wm "$TARRO_ERIKA2" PUT /filters -H 'Content-Type: application/json' \
  -d "{\"rules\":[],\"forwarding\":{\"enabled\":true,\"addresses\":[\"ana@acme.test\",\"fuera@ejemplo.test\"],\"keep_copy\":true},\"current_password\":\"$ERIKA_PASS\",\"code\":\"$(totp "$SECRETO")\"}"
expect "con contrasena y codigo el reenvio externo se guarda" "$WM_CODE" "200"
contains "y el Sieve de erika redirige fuera" "$(sql mail_cell_pe_01 "SELECT script_data FROM mail.v_sieve_user WHERE username = 'erika@acme.test'")" "fuera@ejemplo.test"
expect "con el evento mail.mailbox.forwarding_changed" \
  "$(sql mail_cell_pe_01 "SELECT count(*) > 0 FROM platform.event_outbox WHERE subject = 'mail.mailbox.forwarding_changed' AND payload::text LIKE '%fuera@ejemplo.test%'")" "t"

api PUT /mail-directory/mail-policy '{"external_forwarding_allowed":false}'
expect "el administrador prohibe el reenvio externo de la empresa" "$API_CODE/$(echo "$API_BODY" | jget data.external_forwarding_allowed)" "200/False"
lacks "y el reenvio ya guardado de erika deja de salir fuera" "$(sql mail_cell_pe_01 "SELECT script_data FROM mail.v_sieve_user WHERE username = 'erika@acme.test'")" "fuera@ejemplo.test"
contains "conservando el interno" "$(sql mail_cell_pe_01 "SELECT script_data FROM mail.v_sieve_user WHERE username = 'erika@acme.test'")" "ana@acme.test"
paso_nuevo
wm "$TARRO_ERIKA2" PUT /filters -H 'Content-Type: application/json' \
  -d "{\"rules\":[],\"forwarding\":{\"enabled\":true,\"addresses\":[\"fuera@ejemplo.test\"],\"keep_copy\":true},\"current_password\":\"$ERIKA_PASS\",\"code\":\"$(totp "$SECRETO")\"}"
expect "con la politica apagada no se guarda aunque se reautentique" "$WM_CODE/$(echo "$WM_BODY" | jget error.code)" "422/EXTERNAL_FORWARDING_DISABLED"
api PUT /mail-directory/mail-policy '{"external_forwarding_allowed":true}'
expect "el administrador la vuelve a permitir" "$API_CODE" "200"

api GET "/mailboxes/$ERIKAID"
expect "la ficha del buzon en la consola dice que tiene verificacion" "$(echo "$API_BODY" | jget data.mfa_enabled)" "True"
api DELETE "/mailboxes/$ERIKAID/mfa"
expect "el administrador la restablece (dispositivo perdido)" "$API_CODE" "204"
webmail_erika_cerrada() { wm "$TARRO_ERIKA2" GET /folders; [[ $WM_CODE == 401 ]]; }
esperar "y las sesiones del webmail de erika se cierran (credentials_changed, credential mfa)" 30 webmail_erika_cerrada
imap_principal_vuelve() { login_aceptado erika@acme.test "$ERIKA_PASS"; }
esperar "IMAP vuelve a aceptar la contrasena principal" 30 imap_principal_vuelve
wm "$TARRO_ERIKA3" POST /session -H 'Content-Type: application/json' -d "{\"username\":\"erika@acme.test\",\"password\":\"$ERIKA_PASS\"}"
expect "y el webmail vuelve a entrar en un paso" "$WM_CODE/$(echo "$WM_BODY" | jget data.username)" "200/erika@acme.test"

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

echo "== Lectura del antispam (controller de Rspamd -> mail-security -> gateway, solo superadmin)"
# Rspamd ya analizo correo real mas arriba (rspamc y los envios por Postfix): sus contadores y su historial
# (history_redis en redis-mail) tienen que reflejarlo. Solo lectura: no hay ruta que escriba en el controller.
antispam() { # antispam <cabecera> <ruta>: deja AS_CODE y AS_BODY
  AS_CODE=$(curl -s -o "$WORK/antispam.json" -w '%{http_code}' "$GW/mail-security/rspamd$2" -H "$1")
  AS_BODY=$(cat "$WORK/antispam.json")
}
antispam "$A1" "/stats"
expect "el superadmin lee las estadisticas del controller" "$AS_CODE" "200"
AS_SCANNED=$(echo "$AS_BODY" | jget data.scanned)
[[ "$AS_SCANNED" =~ ^[0-9]+$ && "$AS_SCANNED" -gt 0 ]] && ok "con mensajes analizados ($AS_SCANNED)" || mal "mensajes analizados: '$AS_SCANNED'"
contains "los veredictos" "$AS_BODY" '"actions"'
contains "y el clasificador bayesiano" "$AS_BODY" '"statfiles"'
lacks "sin la contrasena del controller en la respuesta" "$AS_BODY" "$RSPAMD_PASS"
antispam "$A1" "/history?limit=5"
expect "y el historial reciente acotado" "$AS_CODE" "200"
AS_ROWS=$(echo "$AS_BODY" | python3 -c 'import json, sys; d = json.load(sys.stdin)["data"]; print(len(d["rows"]), d["total"] >= len(d["rows"]))')
[[ "$AS_ROWS" == *" True" && "${AS_ROWS%% *}" -le 5 && "${AS_ROWS%% *}" -ge 1 ]] && ok "con como mucho 5 filas ($AS_ROWS)" || mal "filas del historial: '$AS_ROWS'"
contains "cada fila lleva el sobre y el veredicto" "$AS_BODY" '"required_score"'
contains "con el remitente de la prueba" "$AS_BODY" "ana@acme.test"
lacks "y nunca las opciones de los simbolos ni el cuerpo" "$AS_BODY" '"options"'
antispam "$A1" "/history?limit=0"
expect "un limite que no es positivo se rechaza" "$AS_CODE" "400"
antispam "$A2" "/stats"
expect "un administrador de empresa no lee las estadisticas" "$AS_CODE" "403"
antispam "$A2" "/history"
expect "ni el historial" "$AS_CODE" "403"
# Desde fuera del bucle local (secure_ip) el controller sigue exigiendo la contrasena: la lectura del
# superadmin no la ha abierto a nadie. Rspamd responde 401 o 403 segun la version.
AS_SIN_CLAVE=$(en rspamd-mail wget -q -O /dev/null -S "http://$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$(c rspamd-mail)"):11334/stat" 2>&1 | awk '/HTTP\// { print $2; exit }')
[[ "$AS_SIN_CLAVE" == 401 || "$AS_SIN_CLAVE" == 403 ]] && ok "el controller sigue exigiendo contrasena fuera del bucle local ($AS_SIN_CLAVE)" || mal "controller sin contrasena: '$AS_SIN_CLAVE'"
# Una contrasena por permiso: la de lectura lee y no puede entrenar a Rspamd; la de escritura si. Rspamd deja
# que la lectura escriba si falta enable_password, asi que esto prueba que el arranque la pone.
controller_codigo() { # controller_codigo <contrasena> <ruta> [cuerpo]: codigo HTTP desde fuera del bucle local
  local ip; ip="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$(c rspamd-mail)")"
  en rspamd-mail wget -q -O /dev/null -S --header "Password: $1" ${3:+--post-data "$3"} "http://$ip:11334$2" 2>&1 |
    awk '/HTTP\// { print $2; exit }'
}
MENSAJE_APRENDER=$'From: e2e@acme.test\r\nTo: ana@acme.test\r\nSubject: aprendizaje\r\n\r\nmensaje de prueba del aprendizaje'
expect "la contrasena de lectura lee las estadisticas" "$(controller_codigo "$RSPAMD_PASS" /stat)" "200"
AS_APRENDE_LECTURA="$(controller_codigo "$RSPAMD_PASS" /learnspam "$MENSAJE_APRENDER")"
[[ "$AS_APRENDE_LECTURA" == 401 || "$AS_APRENDE_LECTURA" == 403 ]] && ok "y no puede entrenar a Rspamd ($AS_APRENDE_LECTURA)" ||
  mal "la contrasena de lectura entrena a Rspamd: '$AS_APRENDE_LECTURA'"
AS_APRENDE_ESCRITURA="$(controller_codigo "$RSPAMD_LEARN_PASS" /learnspam "$MENSAJE_APRENDER")"
[[ -n "$AS_APRENDE_ESCRITURA" && "$AS_APRENDE_ESCRITURA" != 401 && "$AS_APRENDE_ESCRITURA" != 403 ]] &&
  ok "la de escritura si esta autorizada a entrenar ($AS_APRENDE_ESCRITURA)" || mal "la contrasena de escritura no esta autorizada: '$AS_APRENDE_ESCRITURA'"
AS_FICHERO="$(en rspamd-mail cat /etc/rspamd/override.d/worker-controller-password.inc)"
lacks "el fichero del controller no guarda la contrasena de lectura en claro" "$AS_FICHERO" "$RSPAMD_PASS"
lacks "ni la de escritura" "$AS_FICHERO" "$RSPAMD_LEARN_PASS"
contains "guarda sus hashes" "$AS_FICHERO" 'enable_password = "$'
contains "y el arranque dice que activo las dos" "$(docker logs "$(c rspamd-mail)" 2>&1)" "controller-password: lectura activada; aprendizaje activado"

echo "== Puntuacion antispam de una plantilla (templates -> mail-security -> /checkv2 del controller)"
# La ruta interna que usa el verificador de templates: token de gateway, sin sesion, con la contrasena de
# lectura. El mensaje no deja huella en Rspamd: ni en su historial ni en el registro del motor.
SPAM_MARCA="plantilla-$(rand_hex 6)"
SPAM_MIME=$(printf 'From: Tienda <ventas@acme.test>\r\nTo: cliente@acme.test\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>Oferta <a href="https://bit.ly/x">aqui</a></p>\r\n' "$SPAM_MARCA")
spam_check() { # spam_check <cuerpo JSON>: deja SC_CODE y SC_BODY
  SC_CODE=$(curl -s -o "$WORK/spam-check.json" -w '%{http_code}' -X POST "http://127.0.0.1:${PORT[mail-security]}/internal/mail-security/spam-check" \
    -H "X-Gateway-Token: $INTERNAL_GATEWAY_TOKEN" -H 'Content-Type: application/json' --data-binary "$1")
  SC_BODY=$(cat "$WORK/spam-check.json")
}
spam_check "$(python3 -c 'import json, sys; print(json.dumps({"message": sys.argv[1]}))' "$SPAM_MIME")"
expect "mail-security puntua la plantilla con Rspamd" "$SC_CODE" "200"
SC_RESUMEN=$(echo "$SC_BODY" | python3 -c 'import json, sys
d = json.load(sys.stdin)["data"]
s = [x["score"] for x in d["symbols"]]
print(isinstance(d["score"], (int, float)), d["required"] == 15, bool(d["action"]), len(s) > 0, s == sorted(s, reverse=True))' 2>/dev/null)
expect "con puntuacion, umbral de rechazo, accion y simbolos ordenados" "$SC_RESUMEN" "True True True True True"
lacks "sin las opciones de los simbolos" "$SC_BODY" '"options"'
antispam "$A1" "/history?limit=200"
lacks "la plantilla no entra en el historial de Rspamd (no_log)" "$AS_BODY" "$SPAM_MARCA"
lacks "ni en el registro del motor" "$(docker logs "$(c rspamd-mail)" 2>&1)" "$SPAM_MARCA"
lacks "ni en el de mail-security" "$(docker logs "$(c mail-security)" 2>&1)" "$SPAM_MARCA"
spam_check '{"message":""}'
expect "un mensaje vacio se rechaza (400)" "$SC_CODE/$(echo "$SC_BODY" | jget error.code)" "400/MESSAGE_REQUIRED"
SC_SIN_TOKEN=$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:${PORT[mail-security]}/internal/mail-security/spam-check" -d '{"message":"x"}')
[[ "$SC_SIN_TOKEN" == 401 || "$SC_SIN_TOKEN" == 403 ]] && ok "sin el token interno no se atiende ($SC_SIN_TOKEN)" || mal "spam-check sin token: '$SC_SIN_TOKEN'"
contains "la metrica cuenta la puntuacion" "$(curl -s "http://127.0.0.1:${PORT[mail-security]}/metrics")" 'mail_security_spam_checks_total{outcome="scanned"} 1'
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
expect "el agente no acepta TLS 1.2 (su unico cliente habla 1.3)" "$(agente_http --tls-max 1.2 -H "Authorization: Bearer $QUEUE_KEY" https://127.0.0.1:8590/v1/queue)" "000"

# Un fallo del agente no puede detener Postfix: stop-supervisor.sh ignora su salida y supervisord lo reinicia.
reinicios_postfix() { docker inspect -f '{{.RestartCount}}' "$(c postfix-mail)"; }
agente_de_vuelta() { [[ "$(agente_http -H "Authorization: Bearer $QUEUE_KEY" https://127.0.0.1:8590/v1/queue)" == "200" ]]; }
REINICIOS_POSTFIX=$(reinicios_postfix)
docker exec "$(c postfix-mail)" sh -c 'kill -9 $(pidof queue-agent)'
esperar "el agente vuelve tras matarlo (supervisord lo reinicia)" 60 agente_de_vuelta
expect "y el contenedor de Postfix no se reinicio" "$(reinicios_postfix)" "$REINICIOS_POSTFIX"
expect "ni Postfix dejo de correr" "$(docker exec "$(c postfix-mail)" sh -c 'pidof master >/dev/null && echo si')" "si"

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

echo "== Credencial de destino por trabajo (mail-migration -> mail-auth -> Dovecot real)"
# Con MAIL_MIGRATION_JOB_CREDENTIALS cada trabajo reclamado trae su propia credencial de destino: abre solo el
# buzon del trabajo, solo desde la red del ejecutor y solo mientras el trabajo esta en curso. El ejecutor la
# prefiere al maestro compartido, asi que los trabajos de arriba ya entraron por ella.
expect "los trabajos anteriores entraron al destino con su credencial (inicio service migration desde la red del ejecutor)" \
  "$(sql mail_cell_pe_01 "SELECT count(*) > 0 FROM mail.sasl_logins WHERE service = 'migration' AND username = 'destino1@acme.test' AND host(remote_ip) LIKE '$MAIL_MIGRATION_IPV4_NETWORK.%'")" "t"
DEST3_PASS="$(rand_hex 10)Aa1!"; DEST4_PASS="$(rand_hex 10)Aa1!"; DEST5_PASS="$(rand_hex 10)Aa1!"
creado "buzon destino3@acme.test (destino de la prueba de credenciales)" POST /mailboxes "{\"local_part\":\"destino3\",\"domain\":\"acme.test\",\"password\":\"$DEST3_PASS\"}"
DEST3_ID=$(echo "$API_BODY" | jget data.id)
creado "buzon destino4@acme.test (otro buzon de la misma celda)" POST /mailboxes "{\"local_part\":\"destino4\",\"domain\":\"acme.test\",\"password\":\"$DEST4_PASS\"}"
DEST4_ID=$(echo "$API_BODY" | jget data.id)
creado "buzon destino5@acme.test (se borrara con un trabajo en curso)" POST /mailboxes "{\"local_part\":\"destino5\",\"domain\":\"acme.test\",\"password\":\"$DEST5_PASS\"}"
DEST5_ID=$(echo "$API_BODY" | jget data.id)

# El ejecutor se detiene para reclamar a mano con su misma clave y ver la credencial que recibe.
docker stop "$RUNNER" >/dev/null
reclamar() { curl -s -X POST "http://127.0.0.1:$MIGRATION_RUNNER_PORT/v1/claim" -H "Authorization: Bearer $MIGRATION_KEY" -H 'Content-Type: application/json' -d "{\"runner_id\":\"$1\"}"; }
latido() { curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:$MIGRATION_RUNNER_PORT/v1/tenants/$1/jobs/$2/heartbeat" -H "Authorization: Bearer $MIGRATION_KEY" -H 'Content-Type: application/json' -d "{\"lease_id\":\"$3\",\"phase\":\"initial\",\"progress\":{}}"; }
cerrar() { curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:$MIGRATION_RUNNER_PORT/v1/tenants/$1/jobs/$2/complete" -H "Authorization: Bearer $MIGRATION_KEY" -H 'Content-Type: application/json' -d "{\"lease_id\":\"$3\",\"outcome\":\"${4:-succeeded}\",\"progress\":{}}"; }

migrar "$DEST3_ID" origen1@acme.test "$ORIG1_PASS"; JOB_A=$MIG_JOB
CLAIM_A=$(reclamar e2e-a)
TOKEN_A=$(echo "$CLAIM_A" | jget data.destination.password); TENANT_A=$(echo "$CLAIM_A" | jget data.tenant_id); LEASE_A=$(echo "$CLAIM_A" | jget data.lease_id)
expect "el reclamo entrega una credencial de destino del trabajo (cfmj1...)" "$([[ "$TOKEN_A" == cfmj1.* ]] && echo si)" "si"
expect "y es del trabajo reclamado" "$(echo "$CLAIM_A" | jget data.job_id)" "$JOB_A"
expect "la base de la empresa solo guarda su hash (32 bytes) y nada de la credencial en claro" \
  "$(sql mail_tenant_acme "SELECT octet_length(destination_credential_hash) FROM mail_migration.jobs WHERE id = '$JOB_A'")" "32"
expect "sin el token en ninguna columna del trabajo" \
  "$(sql mail_tenant_acme "SELECT count(*) FROM mail_migration.jobs j WHERE j::text LIKE '%${TOKEN_A##*.}%'")" "0"

migrar "$DEST4_ID" origen1@acme.test "$ORIG1_PASS"; JOB_B=$MIG_JOB
CLAIM_B=$(reclamar e2e-b)
TOKEN_B=$(echo "$CLAIM_B" | jget data.destination.password); LEASE_B=$(echo "$CLAIM_B" | jget data.lease_id)
expect "otro trabajo trae otra credencial" "$([[ -n "$TOKEN_B" && "$TOKEN_B" != "$TOKEN_A" ]] && echo si)" "si"

expect "la credencial del trabajo abre el buzon destino desde la red del ejecutor" \
  "$(cliente_migracion login destino3@acme.test "$TOKEN_A")" "OK"
contains "no abre otro buzon de la celda" "$(cliente_migracion login destino4@acme.test "$TOKEN_A")" "NO"
contains "ni el buzon de un tercero" "$(cliente_migracion login bea@acme.test "$TOKEN_A")" "NO"
contains "la de otro trabajo no abre este buzon" "$(cliente_migracion login destino3@acme.test "$TOKEN_B")" "NO"
expect "la de ese otro trabajo abre el suyo" "$(cliente_migracion login destino4@acme.test "$TOKEN_B")" "OK"
contains "desde la red de los motores se rechaza (mail-auth solo la acepta desde la red del ejecutor)" \
  "$(cliente login destino3@acme.test "$TOKEN_A")" "NO"
contains "y no vale por el maestro: buzon*maestro con esa credencial no abre" \
  "$(cliente_migracion login destino3@acme.test "$TOKEN_A" --maestro "$MAESTRO_MIGRACION")" "NO"
contains "una contrasena cualquiera que empiece por el prefijo se rechaza" \
  "$(cliente_migracion login destino3@acme.test "cfmj1.$(rand_hex 20)")" "NO"
expect "la contrasena normal del buzon sigue abriendo por su passdb, tambien desde la red del ejecutor" \
  "$(cliente_migracion login destino3@acme.test "$DEST3_PASS")" "OK"
for servicio in "$(c dovecot-mail)" "$(c mail-auth)"; do
  lacks "la credencial no aparece en el registro de $servicio" "$(docker logs "$servicio" 2>&1)" "${TOKEN_A##*.}"
done
lacks "ni en el de mail-migration" "$(cat "$WORK/log/mail-migration.log")" "${TOKEN_A##*.}"
lacks "ni en el de mail-directory" "$(docker logs "$(c mail-directory)" 2>&1)" "${TOKEN_A##*.}"

# Cancelar el trabajo la revoca en el acto: Dovecot no la cachea (su clave de cache lleva la sesion).
expect "la credencial abre antes de cancelar" "$(cliente_migracion login destino3@acme.test "$TOKEN_A")" "OK"
api POST "/mail-migration/jobs/$JOB_A/cancel"
contains "cancelar el trabajo (200)" "$API_CODE" "200"
contains "tras cancelar, la credencial deja de abrir el buzon en el acto" "$(cliente_migracion login destino3@acme.test "$TOKEN_A")" "NO"
expect "el ejecutor cierra el trabajo cancelado" "$(cerrar "$TENANT_A" "$JOB_A" "$LEASE_A" cancelled)" "200"

expect "el trabajo B sigue con su credencial viva (latido aceptado)" "$(latido "$TENANT_A" "$JOB_B" "$LEASE_B")" "200"
expect "y la credencial de B abre su buzon" "$(cliente_migracion login destino4@acme.test "$TOKEN_B")" "OK"
expect "cerrar el trabajo B (completado)" "$(cerrar "$TENANT_A" "$JOB_B" "$LEASE_B")" "200"
contains "tras cerrar el trabajo la credencial no sirve" "$(cliente_migracion login destino4@acme.test "$TOKEN_B")" "NO"
expect "y la base ya no guarda su hash" \
  "$(sql mail_tenant_acme "SELECT count(*) FROM mail_migration.jobs WHERE id = '$JOB_B' AND destination_credential_hash IS NOT NULL")" "0"

migrar "$DEST5_ID" origen1@acme.test "$ORIG1_PASS"; JOB_C=$MIG_JOB
CLAIM_C=$(reclamar e2e-c)
TOKEN_C=$(echo "$CLAIM_C" | jget data.destination.password)
expect "una credencial nueva abre el buzon destino5" "$(cliente_migracion login destino5@acme.test "$TOKEN_C")" "OK"
api DELETE "/mailboxes/$DEST5_ID"
contains "borrar el buzon (204)" "$API_CODE" "204"
contains "borrado el buzon, la credencial del trabajo deja de abrir" "$(cliente_migracion login destino5@acme.test "$TOKEN_C")" "NO"
trabajo_c_retirado() { [[ "$(sql mail_tenant_acme "SELECT count(*) FROM mail_migration.jobs WHERE id = '$JOB_C'")" == 0 ]]; }
esperar "el consumidor de buzones borrados retira el trabajo del buzon" 30 trabajo_c_retirado

docker start "$RUNNER" >/dev/null
esperar "el ejecutor vuelve a arrancar" 90 docker exec "$RUNNER" /usr/local/bin/migration-runner healthcheck

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

echo "== Dovecot caido: el correo entrante queda en cola y se entrega al volver, sin rebotar"
# Con el contenedor parado, el nombre "dovecot" deja de resolverse por el DNS de Docker. Postfix lo toma por un
# error permanente y rebota (dsn 5.4.4, comprobado en produccion): se resuelve por extra_hosts y lmtp_host_lookup.
docker stop "$(c dovecot-mail)" >/dev/null
printf 'Subject: %s-caida\n\ncuerpo durante la caida\n' "$TOKEN" |
  docker exec -i -e MAIL_CONFIG=/opt/postfix/conf "$(c postfix-mail)" sendmail -f "caida@cfm.test" ana@acme.test
diferido_ana() { docker exec "$(c postfix-mail)" postqueue -j | python3 -c 'import json, sys
for l in sys.stdin:
    m = json.loads(l)
    if m["queue_name"] in ("deferred", "active") and any(r["address"] == "ana@acme.test" for r in m["recipients"]): print(m["queue_id"])' | grep -q .; }
esperar "el mensaje para ana queda en la cola de Postfix y no se rebota" 40 diferido_ana
expect "sin ningun rebote permanente por el nombre de Dovecot" \
  "$(docker logs "$(c postfix-mail)" 2>&1 | grep -c 'Name service error for name=dovecot')" "0"
docker start "$(c dovecot-mail)" >/dev/null
esperar "Dovecot vuelve" 180 dovecot_sano
docker exec "$(c postfix-mail)" postqueue -f >/dev/null 2>&1
contains "y el mensaje llega al buzon de ana" "$(cliente buscar ana@acme.test "$ANA_PASS" "$TOKEN-caida" --espera 60)" "OK 1"
expect "la cola de Postfix queda vacia" "$(docker exec "$(c postfix-mail)" postqueue -j | wc -l)" "0"

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
for s in unbound-mail redis-mail clamd-mail rspamd-mail dovecot-mail postfix-mail postfix-tlspol-mail olefy-mail mail-directory mail-auth mail-security webmail mail-migration-runner minio; do
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

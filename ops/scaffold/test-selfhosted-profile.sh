#!/usr/bin/env bash
# Prueba con docker del perfil de produccion autoalojada: levanta con docker-compose.yml +
# docker-compose.selfhosted.yml, en ENVIRONMENT=production y con certificados emitidos por
# ops/security/internal-tls.sh, postgres-primary, pgbouncer, redis, nats, access-control,
# mail-auth, el gateway y el proxy de borde, y comprueba cada control del perfil con clientes
# reales:
#   - PgBouncer llega a Postgres con verify-full, y verify-full rechaza un nombre fuera del SAN;
#   - Postgres rechaza TCP sin TLS y TLS por debajo de 1.2, y acepta TLS con contrasena;
#   - Redis no habla en claro, exige contrasena por TLS, no la muestra en la lista de procesos y
#     los servicios Go le hablan por TLS (sin avisos de Redis caido y con conexiones abiertas);
#   - mail-auth sirve en 9082 el certificado de la CA interna, verificable como mail-auth desde la
#     red de los motores (no por IP ni con otra CA), corre sin root y otro uid no lee su clave;
#   - el proxy de borde sirve HTTPS del host publico, rechaza otro SNI, redirige 80 a 443, pone
#     HSTS, limita el cuerpo, solo admite Cloudflare y solo toma CF-Connecting-IP de sus rangos
#     (una red de Docker en 173.245.48.0/24 hace de Cloudflare y otra privada de visitante
#     directo; por el puerto publicado del host no se juzga el origen, porque Docker lo entrega
#     desde la puerta de enlace de cualquiera de las redes del contenedor), y reenvia al gateway;
#   - la renovacion (internal-tls.sh --forzar y --solo-recargar) cambia el certificado que sirven
#     Postgres, Redis y mail-auth sin reiniciarlos y PgBouncer sigue verificando.
#
#   bash ops/scaffold/test-selfhosted-profile.sh     # SELFHOSTED_KEEP=1 deja todo en pie
#
# Puertos altos solo en 127.0.0.1 (SELFHOSTED_TEST_PORT_BASE, por defecto 47400) y proyecto de
# compose propio (SELFHOSTED_TEST_PROJECT): no toca ningun otro despliegue de la maquina. Una sola
# ejecucion a la vez: otra sale con 3. Los secretos son aleatorios de la ejecucion.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
PROYECTO="${SELFHOSTED_TEST_PROJECT:-cfm-autoalojado-prueba}"
BASE="${SELFHOSTED_TEST_PORT_BASE:-47400}"
HOST_PUBLICO=app.prueba-autoalojada.test
IMG_UTIL_PG="$(awk '/^  postgres-primary:/{d=1;next} d&&/^    image:/{print $2;exit}' "$ROOT/docker-compose.yml")"
IMG_UTIL_REDIS="$(awk '/^  redis:/{d=1;next} d&&/^    image:/{print $2;exit}' "$ROOT/docker-compose.yml")"
IMG_BORDE="$(awk '/^  edge-proxy:/{d=1;next} d&&/^    image:/{print $2;exit}' "$ROOT/docker-compose.selfhosted.yml")"

CERROJO="${TMPDIR:-/tmp}/$PROYECTO.lock"
exec 9>"$CERROJO"
if ! flock -n 9; then
  echo "test-selfhosted-profile: otra ejecucion (pid $(cat "$CERROJO.pid" 2>/dev/null || echo '?')) usa el proyecto $PROYECTO" >&2
  exit 3
fi
echo $$ >"$CERROJO.pid"

W="$(mktemp -d "${TMPDIR:-/tmp}/$PROYECTO.XXXXXX")"
APP="$W/app"
RED_MOTORES="$PROYECTO-engines"
RED_CF="$PROYECTO-cf"
RED_EXTERNA="$PROYECTO-externa"
VOL_SSL="$PROYECTO-mail-ssl"
FALLOS=0
ok() { echo "  OK: $*"; }
mal() { echo "  FALLA: $*" >&2; FALLOS=1; }

compose() { docker compose -p "$PROYECTO" --project-directory "$APP" --env-file "$APP/.env" \
  -f "$APP/docker-compose.yml" -f "$APP/docker-compose.selfhosted.yml" -f "$APP/prueba.yml" "$@"; }

limpiar() {
  local rc=$?
  if [[ "${SELFHOSTED_KEEP:-0}" == 1 ]]; then
    echo "SELFHOSTED_KEEP=1: se deja en pie el proyecto $PROYECTO ($APP)"
  else
    compose down -v --remove-orphans >/dev/null 2>&1 || true
    docker network rm "$RED_MOTORES" "$RED_CF" "$RED_EXTERNA" >/dev/null 2>&1 || true
    docker volume rm "$VOL_SSL" >/dev/null 2>&1 || true
    docker rmi "$PROYECTO-gateway:prueba" "$PROYECTO-access-control:prueba" "$PROYECTO-mail-auth:prueba" >/dev/null 2>&1 || true
    # Los certificados llevan el dueno de cada imagen: se borran como root desde un contenedor.
    docker run --rm -v "$W:/w" --entrypoint sh "$IMG_UTIL_PG" -c 'rm -rf /w/app /w/tls /w/borde' >/dev/null 2>&1 || true
    rm -rf "$W"
  fi
  rm -f "$CERROJO.pid"
  exit $rc
}
trap limpiar EXIT

aleatorio() { openssl rand -hex "$1"; }

# --- subredes libres ------------------------------------------------------------------------------
usadas="$(docker network ls -q | xargs docker network inspect --format '{{range .IPAM.Config}}{{.Subnet}} {{end}}' 2>/dev/null | tr ' ' '\n' | grep -v '^$' || true)"
libre() {
  python3 - "$1" <<PY
import ipaddress, sys
cand = ipaddress.ip_network(sys.argv[1])
for u in """$usadas""".split():
    try:
        if cand.overlaps(ipaddress.ip_network(u, strict=False)):
            sys.exit(1)
    except ValueError:
        pass
PY
}
SUBRED_BORDE=""
for c in 172.31.255.0/28 172.31.254.0/28 172.31.253.0/28 10.254.254.0/28; do
  if libre "$c"; then SUBRED_BORDE="$c"; break; fi
done
[[ -n "$SUBRED_BORDE" ]] || { echo "sin subred libre para la red edge" >&2; exit 1; }
libre 173.245.48.0/24 || { echo "173.245.48.0/24 ya esta en uso en docker: no se puede simular Cloudflare" >&2; exit 1; }

echo "== Preparacion (proyecto $PROYECTO, red edge $SUBRED_BORDE) =="
mkdir -p "$APP"
cp "$ROOT/docker-compose.yml" "$ROOT/docker-compose.selfhosted.yml" "$APP/"
cp -r "$ROOT/pgbouncer" "$ROOT/selfhosted" "$APP/"
rm -f "$APP/pgbouncer/userlist.txt"

for svc in gateway access-control mail-auth; do
  docker build -q -f "$ROOT/services/$svc/Dockerfile" -t "$PROYECTO-$svc:prueba" "$ROOT" >/dev/null
done
ok "imagenes de gateway, access-control y mail-auth construidas desde el arbol"

POSTGRES_PASSWORD="$(aleatorio 24)"
REDIS_PASSWORD="$(aleatorio 24)"
CELL_DB_NAME=mail_cell_prueba
CLAVE_CELDA="$(aleatorio 24)"
jwt="$(bash "$ROOT/ops/security/jwt-keygen.sh" --privada "$W/jwt-privada")"
JWT_KID="$(sed -n 's/^JWT_SIGNING_KID=//p' <<<"$jwt")"
JWT_PUB="$(sed -n 's/^JWT_PUBLIC_KEY_ENTRY=//p' <<<"$jwt")"
umask 077
cat >"$APP/.env" <<EOF
ENVIRONMENT=production
TZ=UTC
DEPLOY_PROFILE=selfhosted
POSTGRES_HOST=postgres
POSTGRES_PORT=5432
POSTGRES_USER=mail_admin
POSTGRES_PASSWORD=${POSTGRES_PASSWORD}
POSTGRES_DB=mail_registry
REDIS_HOST=redis
REDIS_PORT=6379
REDIS_PASSWORD=${REDIS_PASSWORD}
REDIS_TLS=false
CELL_CODE=prueba
CELL_DB_NAME=$CELL_DB_NAME
CELL_DB_PASSWORD=${CLAVE_CELDA}
NATS_URL=nats://nats:4222
JWT_SIGNING_KID=$JWT_KID
JWT_PUBLIC_KEYS=$JWT_PUB
INTERNAL_GATEWAY_TOKEN=$(aleatorio 24)
API_KEY_HASH_KEY=$(aleatorio 32)
PUBLIC_BASE_URL=https://$HOST_PUBLICO
CORS_ALLOWED_ORIGINS=https://$HOST_PUBLICO
INTERNAL_TLS_DIR=$W/tls
MAIL_SSL_VOLUME=$VOL_SSL
MAIL_ENGINES_NETWORK=$RED_MOTORES
EDGE_BIND_ADDRESS=127.0.0.1
EDGE_HTTP_PORT=$((BASE + 80))
EDGE_HTTPS_PORT=$((BASE + 43))
EDGE_NETWORK_SUBNET=$SUBRED_BORDE
EOF
umask 022
# userlist de la prueba: legible por el uid 70 del pooler sin poder cambiar su grupo.
printf '"mail_admin" "%s"\n"%s_svc" "%s"\n' "$POSTGRES_PASSWORD" "$CELL_DB_NAME" "$CLAVE_CELDA" >"$APP/pgbouncer/userlist.txt"
chmod 0644 "$APP/pgbouncer/userlist.txt"

# Los puertos de docker-compose.yml chocarian con otros despliegues de la maquina; env_file sin el
# fichero de secretos del servidor, por si esta maquina lo tuviera; y la red que simula Cloudflare.
cat >"$APP/prueba.yml" <<EOF
services:
  postgres-primary:
    ports: !reset []
  redis:
    ports: !reset []
  nats:
    ports: !reset []
  gateway:
    image: $PROYECTO-gateway:prueba
    ports: !reset []
    env_file: !override [.env]
  access-control:
    image: $PROYECTO-access-control:prueba
    ports: !reset []
    env_file: !override [.env]
  mail-auth:
    image: $PROYECTO-mail-auth:prueba
    ports: !reset []
    env_file: !override [.env]
  edge-proxy:
    networks:
      cloudflare:
        ipv4_address: 173.245.48.2
      externa: {}
networks:
  cloudflare:
    external: true
    name: $RED_CF
  externa:
    external: true
    name: $RED_EXTERNA
EOF

docker network create "$RED_MOTORES" >/dev/null
docker network create --subnet 173.245.48.0/24 "$RED_CF" >/dev/null
docker network create "$RED_EXTERNA" >/dev/null
docker volume create "$VOL_SSL" >/dev/null

# --- TLS interno con el guion del repositorio -----------------------------------------------------
duenos="$(bash "$ROOT/ops/security/internal-tls.sh" --solo-detectar | paste -sd,)"
[[ "$duenos" =~ ^postgres=[0-9]+:[0-9]+,redis=[0-9]+:[0-9]+,mail-auth=[1-9][0-9]*:[1-9][0-9]*$ ]] || { echo "deteccion de duenos inesperada: $duenos" >&2; exit 1; }
ok "uid:gid leidos de las imagenes fijadas: $duenos"
tls() { docker run --rm -v "$ROOT:/repo:ro" -v "$W:/w" "$IMG_UTIL_PG" bash /repo/ops/security/internal-tls.sh --dir /w/tls --proyecto "$PROYECTO" --duenos "$duenos" "$@"; }
tls >/dev/null
# El guion ve /w/tls; el compose lo monta desde $W/tls: mismo directorio.
segunda="$(tls)"
if grep -q 'emitido' <<<"$segunda" || ! grep -q 'vigente hasta' <<<"$segunda"; then
  mal "internal-tls.sh reemite en la segunda pasada: $(tr '\n' ' ' <<<"$segunda")"
else
  ok "internal-tls.sh es idempotente (segunda pasada sin reemitir)"
fi
permisos="$(docker run --rm -v "$W:/w" --entrypoint sh "$IMG_UTIL_PG" -c 'stat -c "%n %a %u:%g" /w/tls/ca/ca.key /w/tls/postgres/server.key /w/tls/redis/server.key /w/tls/mail-auth /w/tls/mail-auth/server.key /w/tls/publico/ca.crt')"
dueno_ma="$(sed -E 's/.*mail-auth=([0-9:]+)$/\1/' <<<"$duenos")"
for esperado in "ca/ca.key 600 0:0" "postgres/server.key 600 $(sed -E 's/postgres=([0-9:]+),.*/\1/' <<<"$duenos")" \
  "redis/server.key 600 $(sed -E 's/.*redis=([0-9:]+),.*/\1/' <<<"$duenos")" "mail-auth 700 $dueno_ma" \
  "mail-auth/server.key 600 $dueno_ma" "publico/ca.crt 644 0:0"; do
  grep -q "/w/tls/$esperado$" <<<"$permisos" || mal "permisos: se esperaba $esperado ($(tr '\n' ';' <<<"$permisos"))"
done
[[ $FALLOS -eq 0 ]] && ok "claves 0600 con el dueno de cada imagen y CA privada solo de root"

# Certificado publico de prueba en el volumen de acme: CA propia de la ejecucion, clave 0600 de root.
docker run --rm -v "$VOL_SSL:/ssl" -v "$W:/w" --entrypoint bash "$IMG_UTIL_PG" -c "
  set -e; umask 077; mkdir -p /w/borde; cd /w/borde
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 2 -subj '/CN=CA de prueba del borde' -keyout ca.key -out ca.crt 2>/dev/null
  openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj '/CN=$HOST_PUBLICO' -keyout /ssl/key.pem -out s.csr 2>/dev/null
  printf 'subjectAltName=DNS:$HOST_PUBLICO\n' > ext
  openssl x509 -req -in s.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 2 -extfile ext -out /ssl/cert.pem 2>/dev/null
  chmod 0644 ca.crt; chmod 0755 /w/borde"
cp "$W/borde/ca.crt" "$W/ca-borde.crt"

# --- infraestructura ------------------------------------------------------------------------------
echo "== Infraestructura: postgres-primary, redis, pgbouncer, nats =="
compose up -d postgres-primary redis pgbouncer nats >/dev/null 2>"$W/up-infra.log" || { cat "$W/up-infra.log" >&2; exit 1; }
if bash "$ROOT/ops/maintenance/esperar-sanos.sh" --proyecto "$PROYECTO" --plazo 180 postgres-primary redis pgbouncer nats; then
  ok "esperar-sanos: postgres-primary, redis y pgbouncer sanos con el perfil"
else
  mal "la infraestructura del perfil no arranco"
  exit 1
fi
PG="$(compose ps -q postgres-primary)"
PGB="$(compose ps -q pgbouncer)"
RDS="$(compose ps -q redis)"
RED_INTERNA="$(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$PG" | tr ' ' '\n' | grep mail-internal)"

# --- PgBouncer: verify-full -----------------------------------------------------------------------
ini="$(docker exec "$PGB" cat /tmp/pgbouncer.ini)"
grep -qE '^server_tls_sslmode[[:space:]]*=[[:space:]]*verify-full$' <<<"$ini" &&
  grep -qE '^server_tls_ca_file[[:space:]]*=[[:space:]]*/run/core-force-mail/tls/publico/ca.crt$' <<<"$ini" &&
  ok "pgbouncer renderiza verify-full con la CA interna" || mal "pgbouncer no renderiza verify-full con la CA interna"
por_pgb="$(PGPASSWORD="$POSTGRES_PASSWORD" docker exec -e PGPASSWORD "$PGB" psql -h 127.0.0.1 -p 5432 -U mail_admin -d mail_registry -Atc \
  "select ssl, version from pg_stat_ssl where pid = pg_backend_pid()" 2>&1)"
[[ "$por_pgb" =~ ^t\|TLSv1\.[23]$ ]] && ok "consulta por pgbouncer: la conexion del pooler a Postgres va cifrada ($por_pgb)" ||
  mal "consulta por pgbouncer: $por_pgb"
ip_pg="$(docker inspect -f "{{(index .NetworkSettings.Networks \"$RED_INTERNA\").IPAddress}}" "$PG")"
# Un cliente con la CA interna que apunta a la IP: el certificado es valido pero no para ese nombre.
sin_nombre="$(PGPASSWORD="$POSTGRES_PASSWORD" docker run --rm --network "$RED_INTERNA" -e PGPASSWORD -v "$W/tls/publico:/ca:ro" "$IMG_UTIL_PG" \
  psql "host=$ip_pg port=5432 user=mail_admin dbname=mail_registry sslmode=verify-full sslrootcert=/ca/ca.crt" -Atc 'select 1' 2>&1 || true)"
grep -qi 'does not match\|no coincide' <<<"$sin_nombre" && ok "verify-full rechaza el servidor por un nombre fuera del SAN (IP $ip_pg)" ||
  mal "verify-full contra la IP no fallo por el nombre: $sin_nombre"
con_nombre="$(PGPASSWORD="$POSTGRES_PASSWORD" docker run --rm --network "$RED_INTERNA" -e PGPASSWORD -v "$W/tls/publico:/ca:ro" "$IMG_UTIL_PG" \
  psql "host=postgres-primary port=5432 user=mail_admin dbname=mail_registry sslmode=verify-full sslrootcert=/ca/ca.crt" -Atc 'select 1' 2>&1 || true)"
[[ "$con_nombre" == 1 ]] && ok "verify-full acepta postgres-primary con la CA interna" || mal "verify-full contra postgres-primary: $con_nombre"

# --- Postgres: TCP sin TLS y TLS antiguo ----------------------------------------------------------
claro="$(PGPASSWORD="$POSTGRES_PASSWORD" docker run --rm --network "$RED_INTERNA" -e PGPASSWORD "$IMG_UTIL_PG" \
  psql "host=postgres-primary port=5432 user=mail_admin dbname=mail_registry sslmode=disable" -Atc 'select 1' 2>&1 || true)"
grep -q 'no encryption\|sin cifrado' <<<"$claro" && ok "Postgres rechaza una conexion TCP sin TLS ($(grep -o 'pg_hba.conf[^"]*' <<<"$claro" | head -1 | cut -c1-90))" ||
  mal "Postgres no rechazo TCP sin TLS: $claro"
bucle="$(docker exec "$PG" psql "host=127.0.0.1 user=mail_admin dbname=mail_registry sslmode=disable" -Atc 'select 1' 2>&1 || true)"
grep -q 'no encryption\|sin cifrado' <<<"$bucle" && ok "tambien por 127.0.0.1 dentro del contenedor (sin el trust del pg_hba de la imagen)" ||
  mal "Postgres admite TCP sin TLS por 127.0.0.1: $bucle"
tls11="$(docker run --rm --network "$RED_INTERNA" --entrypoint openssl "$IMG_UTIL_PG" s_client -connect postgres-primary:5432 -starttls postgres -tls1_1 </dev/null 2>&1 || true)"
tls12="$(docker run --rm --network "$RED_INTERNA" --entrypoint openssl "$IMG_UTIL_PG" s_client -connect postgres-primary:5432 -starttls postgres -tls1_2 </dev/null 2>&1 || true)"
if grep -q 'New, (NONE), Cipher is (NONE)' <<<"$tls11" && grep -q 'alert protocol version' <<<"$tls11" && grep -q '^New, TLSv1.2' <<<"$tls12"; then
  ok "Postgres no negocia TLS 1.1 y si TLS 1.2"
else
  mal "Postgres y la version minima de TLS: $(grep -m1 -i 'protocol\|alert' <<<"$tls11")"
fi

# --- Redis ----------------------------------------------------------------------------------------
redis_cli() { docker run --rm --network "$RED_INTERNA" -e REDISCLI_AUTH -v "$W/tls/publico:/ca:ro" --entrypoint redis-cli "$IMG_UTIL_REDIS" -h redis -p 6379 "$@" 2>&1 || true; }
en_claro="$(REDISCLI_AUTH="$REDIS_PASSWORD" redis_cli ping)"
[[ "$en_claro" != *PONG* ]] && ok "Redis no responde en claro ($(tr '\n' ' ' <<<"$en_claro" | cut -c1-60))" || mal "Redis responde en claro"
sin_clave="$(REDISCLI_AUTH='' redis_cli --tls --cacert /ca/ca.crt ping)"
[[ "$sin_clave" == *NOAUTH* ]] && ok "Redis por TLS sin contrasena: NOAUTH" || mal "Redis por TLS sin contrasena: $sin_clave"
con_clave="$(REDISCLI_AUTH="$REDIS_PASSWORD" redis_cli --tls --cacert /ca/ca.crt ping)"
[[ "$con_clave" == PONG ]] && ok "Redis por TLS con contrasena: PONG" || mal "Redis por TLS con contrasena: $con_clave"
otra_ca="$(REDISCLI_AUTH="$REDIS_PASSWORD" docker run --rm --network "$RED_INTERNA" -e REDISCLI_AUTH -v "$W/ca-borde.crt:/ca.crt:ro" --entrypoint redis-cli "$IMG_UTIL_REDIS" -h redis -p 6379 --tls --cacert /ca.crt ping 2>&1 || true)"
[[ "$otra_ca" != *PONG* ]] && ok "un cliente con otra CA no verifica el certificado de Redis" || mal "Redis verificado con una CA ajena"
if docker exec "$RDS" sh -c 'cat /proc/[0-9]*/cmdline 2>/dev/null | tr "\0" " "' | grep -qFf <(printf '%s\n' "$REDIS_PASSWORD"); then
  mal "la contrasena de Redis aparece en la linea de ordenes de algun proceso"
else
  ok "la contrasena de Redis no aparece en la linea de ordenes de ningun proceso del contenedor"
fi

# --- servicios Go ---------------------------------------------------------------------------------
echo "== Servicios Go en ENVIRONMENT=production =="
compose up -d --no-deps access-control gateway >/dev/null 2>"$W/up-go.log" || { cat "$W/up-go.log" >&2; exit 1; }
if bash "$ROOT/ops/maintenance/esperar-sanos.sh" --proyecto "$PROYECTO" --plazo 180 access-control gateway; then
  ok "access-control y gateway sanos en production con el perfil"
else
  mal "los servicios Go no arrancaron"
fi
compose logs access-control gateway >"$W/go.log" 2>&1 || true
if grep -qiE 'redis unavailable|Redis no disponible|REDIS_TLS=true is required' "$W/go.log"; then
  mal "un servicio Go no llego a Redis por TLS: $(grep -iE 'redis' "$W/go.log" | head -2)"
else
  ok "ningun servicio Go avisa de Redis caido ni exige TLS (REDIS_TLS=false del .env lo pisa el perfil)"
fi
clientes="$(REDISCLI_AUTH="$REDIS_PASSWORD" redis_cli --tls --cacert /ca/ca.crt client list | grep -c 'cmd=' || true)"
((clientes >= 2)) && ok "Redis tiene $clientes conexiones TLS abiertas (servicios + este cliente)" || mal "Redis sin conexiones de los servicios ($clientes)"

# --- mail-auth ------------------------------------------------------------------------------------
echo "== mail-auth con el certificado de la CA interna =="
# Base de celda minima: mail-auth abre su base al arrancar con su rol propio (sin tablas: aqui solo
# se juzga el TLS). La contrasena entra por el entorno del contenedor, no por argumentos.
CLAVE_CELDA="$CLAVE_CELDA" docker exec -i -e CLAVE_CELDA "$PG" psql -q -v ON_ERROR_STOP=1 -U mail_admin -d mail_registry >/dev/null <<SQL
\\getenv clave CLAVE_CELDA
CREATE ROLE ${CELL_DB_NAME}_svc LOGIN PASSWORD :'clave';
CREATE DATABASE $CELL_DB_NAME OWNER ${CELL_DB_NAME}_svc;
SQL
compose up -d --no-deps mail-auth >/dev/null 2>"$W/up-auth.log" || { cat "$W/up-auth.log" >&2; exit 1; }
if bash "$ROOT/ops/maintenance/esperar-sanos.sh" --proyecto "$PROYECTO" --plazo 120 mail-auth; then
  ok "mail-auth sano en production con el perfil"
else
  compose logs mail-auth 2>&1 | tail -20 >&2
  mal "mail-auth no arranco"
  exit 1
fi
MA="$(compose ps -q mail-auth)"
compose logs mail-auth >"$W/mail-auth.log" 2>&1 || true
grep -q 'autofirmado' "$W/mail-auth.log" && mal "mail-auth arranco con el certificado autofirmado en memoria" ||
  ok "mail-auth carga MAIL_AUTH_TLS_CERT/MAIL_AUTH_TLS_KEY del montaje (sin aviso de autofirmado)"
usuario_ma="$(docker inspect -f '{{.Config.User}}' "$MA")"
[[ "$usuario_ma" == "$dueno_ma" ]] && ok "mail-auth corre como $usuario_ma, el dueno de su clave, sin root" || mal "mail-auth corre como '$usuario_ma', su clave es de $dueno_ma"
# motores <orden>: un cliente en la red de los motores, donde Dovecot y el webmail buscan a mail-auth.
motores() { docker run --rm --network "$RED_MOTORES" -v "$W/tls/publico:/ca:ro" -v "$W/ca-borde.crt:/ca-ajena.crt:ro" --entrypoint sh "$IMG_BORDE" -c "$1" 2>&1 || true; }
verificado="$(motores "curl -sS -o /dev/null -w '%{http_code} %{ssl_verify_result}' --cacert /ca/ca.crt https://mail-auth:9082/")"
[[ "$verificado" =~ ^[0-9]{3}\ 0$ ]] && ok "mail-auth:9082 verificado con la CA interna como mail-auth, como lo hace el webmail (HTTP ${verificado%% *})" ||
  mal "TLS de mail-auth con la CA interna: $verificado"
version="$(docker run --rm --network "$RED_MOTORES" --entrypoint bash "$IMG_UTIL_PG" -c "openssl s_client -connect mail-auth:9082 -servername mail-auth -tls1_1 </dev/null 2>&1" || true)"
grep -q 'New, (NONE), Cipher is (NONE)' <<<"$version" && ok "mail-auth no negocia TLS 1.1" || mal "mail-auth negocia TLS 1.1"
ip_ma="$(docker inspect -f "{{(index .NetworkSettings.Networks \"$RED_MOTORES\").IPAddress}}" "$MA")"
por_ip="$(motores "curl -sS -o /dev/null --cacert /ca/ca.crt https://$ip_ma:9082/")"
grep -qi 'no alternative certificate subject name\|does not match' <<<"$por_ip" && ok "por IP ($ip_ma) el nombre no casa con el SAN y se rechaza" ||
  mal "mail-auth por IP no fallo por el nombre: $por_ip"
ajena="$(motores "curl -sS -o /dev/null --cacert /ca-ajena.crt https://mail-auth:9082/")"
grep -qi 'unable to get local issuer\|self.signed\|certificate verify\|unknown ca' <<<"$ajena" && ok "con otra CA el certificado de mail-auth no se verifica" ||
  mal "mail-auth verificado con una CA ajena: $ajena"
ajeno="$(docker run --rm --user 4242:4242 -v "$W/tls/mail-auth:/k:ro" --entrypoint cat "$IMG_UTIL_REDIS" /k/server.key 2>&1 || true)"
grep -qi 'permission denied' <<<"$ajeno" && ok "otro uid no lee la clave de mail-auth" || mal "otro uid lee la clave de mail-auth"

# --- proxy de borde -------------------------------------------------------------------------------
echo "== Proxy de borde =="
compose up -d --no-deps edge-proxy >/dev/null 2>"$W/up-borde.log" || { cat "$W/up-borde.log" >&2; exit 1; }
if bash "$ROOT/ops/maintenance/esperar-sanos.sh" --proyecto "$PROYECTO" --plazo 90 edge-proxy; then
  ok "esperar-sanos: edge-proxy sano"
else
  mal "el proxy de borde no arranco"
  exit 1
fi
BORDE="$(compose ps -q edge-proxy)"
HTTPS=$((BASE + 43))
HTTP=$((BASE + 80))
ip_externa="$(docker inspect -f "{{(index .NetworkSettings.Networks \"$RED_EXTERNA\").IPAddress}}" "$BORDE")"
# fuera <orden>: un visitante directo (red privada, fuera de los rangos de Cloudflare).
fuera() { docker run --rm --network "$RED_EXTERNA" -v "$W/ca-borde.crt:/ca.crt:ro" --entrypoint sh "$IMG_BORDE" -c "$1" 2>&1 || true; }
cabeceras="$(fuera "curl -sS -o /dev/null -D - --cacert /ca.crt --resolve $HOST_PUBLICO:443:$ip_externa https://$HOST_PUBLICO/health")"
grep -q '^HTTP/[0-9.]* 403' <<<"$cabeceras" && ok "visitante directo, fuera de Cloudflare: HTTPS verificado con el certificado del volumen de acme y 403" ||
  mal "desde fuera de Cloudflare: $(head -1 <<<"$cabeceras")"
grep -qi '^strict-transport-security: max-age=31536000; includeSubDomains' <<<"$cabeceras" && ok "HSTS tambien en las respuestas del borde" || mal "sin HSTS"
grep -qi '^server: nginx/' <<<"$cabeceras" && mal "el borde anuncia su version" || ok "sin version de nginx en Server"
otro_sni="$(curl -sS -o /dev/null -k --resolve "otro.test:$HTTPS:127.0.0.1" "https://otro.test:$HTTPS/" 2>&1 || true)"
[[ "$(curl -sS -o /dev/null -w '%{http_code}' --cacert "$W/ca-borde.crt" --resolve "$HOST_PUBLICO:$HTTPS:127.0.0.1" "https://$HOST_PUBLICO:$HTTPS/health" 2>&1 || true)" =~ ^[0-9]{3}$ ]] &&
  ok "HTTPS por el puerto publicado del host ($HTTPS, solo 127.0.0.1)" || mal "HTTPS por el puerto publicado del host"
grep -qi 'alert\|handshake\|SSL' <<<"$otro_sni" && ok "otro nombre (SNI): saludo TLS rechazado sin certificado" || mal "otro SNI: $otro_sni"
redir="$(curl -sS -o /dev/null -w '%{http_code} %{redirect_url}' -H "Host: $HOST_PUBLICO" "http://127.0.0.1:$HTTP/ruta?x=1" 2>&1 || true)"
[[ "$redir" == "301 https://$HOST_PUBLICO/ruta?x=1" ]] && ok "80 redirige a https://$HOST_PUBLICO" || mal "redireccion de 80: $redir"

# Cliente desde un rango de Cloudflare (red de docker 173.245.48.0/24) contra la IP del borde.
cf() { docker run --rm --network "$RED_CF" --ip "173.245.48.$1" -v "$W/ca-borde.crt:/ca.crt:ro" --entrypoint sh "$IMG_BORDE" -c "$2" 2>&1 || true; }
salud="$(cf 10 "curl -sS --cacert /ca.crt --resolve $HOST_PUBLICO:443:173.245.48.2 -H 'CF-Connecting-IP: 203.0.113.7' https://$HOST_PUBLICO/health")"
[[ "$salud" == *'"status":"ok"'* ]] && ok "desde Cloudflare: el borde reenvia al gateway (/health: $salud)" || mal "desde Cloudflare /health: $salud"
cf 11 "curl -sS -o /dev/null --cacert /ca.crt --resolve $HOST_PUBLICO:443:173.245.48.2 -H 'CF-Connecting-IP: 203.0.113.7' -X POST https://$HOST_PUBLICO/api/v1/auth/refresh" >/dev/null
claves="$(REDISCLI_AUTH="$REDIS_PASSWORD" redis_cli --tls --cacert /ca/ca.crt --scan --pattern 'rl:gateway:*')"
grep -q ':ip:203.0.113.7$' <<<"$claves" && ok "el gateway cuenta el cupo por la IP del visitante que dio Cloudflare (rl:...:ip:203.0.113.7), en Redis por TLS" ||
  mal "el gateway no recibio la IP de CF-Connecting-IP: $(tr '\n' ' ' <<<"$claves")"
grande="$(cf 12 "head -c 106954753 /dev/zero | curl -sS -o /dev/null -w '%{http_code}' --cacert /ca.crt --resolve $HOST_PUBLICO:443:173.245.48.2 -X POST --data-binary @- https://$HOST_PUBLICO/api/v1/webmail/drafts")"
[[ "$grande" == 413 ]] && ok "cuerpo de 102 MiB: 413 en el borde (limite 101m, el del webmail)" || mal "cuerpo mayor que el limite: $grande"

# Sin exigir Cloudflare: una conexion ajena no puede fijar su IP con CF-Connecting-IP.
sed -i '/^EDGE_REQUIRE_CLOUDFLARE=/d' "$APP/.env" && echo 'EDGE_REQUIRE_CLOUDFLARE=false' >>"$APP/.env"
compose up -d --no-deps edge-proxy >/dev/null 2>&1
bash "$ROOT/ops/maintenance/esperar-sanos.sh" --proyecto "$PROYECTO" --plazo 90 edge-proxy >/dev/null || mal "el borde no arranco con EDGE_REQUIRE_CLOUDFLARE=false"
BORDE="$(compose ps -q edge-proxy)"
ip_externa="$(docker inspect -f "{{(index .NetworkSettings.Networks \"$RED_EXTERNA\").IPAddress}}" "$BORDE")"
# 502: el gateway no tiene identity detras en esta prueba; lo que se mira es la IP con que cuenta.
ajena="$(fuera "curl -sS -o /dev/null -w '%{http_code}' --cacert /ca.crt --resolve $HOST_PUBLICO:443:$ip_externa -H 'CF-Connecting-IP: 198.51.100.9' -H 'X-Real-IP: 198.51.100.9' -H 'X-Forwarded-For: 198.51.100.9' -X POST https://$HOST_PUBLICO/api/v1/auth/refresh")"
claves="$(REDISCLI_AUTH="$REDIS_PASSWORD" redis_cli --tls --cacert /ca/ca.crt --scan --pattern 'rl:gateway:*')"
if [[ "$ajena" =~ ^[0-9]{3}$ && "$ajena" != 403 ]] && ! grep -q '198.51.100.9' <<<"$claves"; then
  ok "sin exigir Cloudflare, una conexion ajena con CF-Connecting-IP/X-Real-IP falsos cuenta por su IP real ($(grep -v 203.0.113.7 <<<"$claves" | sed 's/.*:ip://' | tr '\n' ' '))"
else
  mal "IP suplantada desde fuera de Cloudflare (HTTP $ajena): $(tr '\n' ' ' <<<"$claves")"
fi

# --- renovacion -----------------------------------------------------------------------------------
echo "== Renovacion del TLS interno =="
serie() { docker run --rm --network "$RED_INTERNA" --entrypoint bash "$IMG_UTIL_PG" -c "openssl s_client -connect $1 $2 </dev/null 2>/dev/null | openssl x509 -noout -serial"; }
pg_antes="$(serie postgres-primary:5432 '-starttls postgres')"
arranques_antes="$(docker inspect -f '{{.State.StartedAt}}' "$PG" "$RDS" "$MA")"
rd_antes="$(serie redis:6379 '')"
serie_ma() { docker run --rm --network "$RED_MOTORES" --entrypoint bash "$IMG_UTIL_PG" -c "openssl s_client -connect mail-auth:9082 -servername mail-auth </dev/null 2>/dev/null | openssl x509 -noout -serial"; }
ma_antes="$(serie_ma)"
tls --forzar | sed 's/^/    /'
bash "$ROOT/ops/security/internal-tls.sh" --solo-recargar --proyecto "$PROYECTO" | sed 's/^/    /' || mal "--solo-recargar fallo"
sleep 2
pg_despues="$(serie postgres-primary:5432 '-starttls postgres')"
rd_despues="$(serie redis:6379 '')"
[[ -n "$pg_antes" && "$pg_antes" != "$pg_despues" ]] && ok "Postgres sirve el certificado renovado sin reiniciar ($pg_antes -> $pg_despues)" || mal "Postgres no tomo el certificado renovado ($pg_antes / $pg_despues)"
[[ -n "$rd_antes" && "$rd_antes" != "$rd_despues" ]] && ok "Redis sirve el certificado renovado sin reiniciar ($rd_antes -> $rd_despues)" || mal "Redis no tomo el certificado renovado ($rd_antes / $rd_despues)"
# mail-auth mira sus ficheros cada 15 s (certReloadInterval): se le da un margen holgado.
for _ in $(seq 1 30); do
  ma_despues="$(serie_ma)"
  [[ -n "$ma_despues" && "$ma_despues" != "$ma_antes" ]] && break
  sleep 2
done
[[ -n "$ma_antes" && "$ma_antes" != "$ma_despues" ]] && ok "mail-auth sirve el certificado renovado sin reiniciar ($ma_antes -> $ma_despues)" || mal "mail-auth no tomo el certificado renovado ($ma_antes / $ma_despues)"
[[ "$(motores "curl -sS -o /dev/null -w '%{ssl_verify_result}' --cacert /ca/ca.crt https://mail-auth:9082/")" == 0 ]] &&
  ok "el certificado renovado de mail-auth se verifica con la misma CA" || mal "el certificado renovado de mail-auth no se verifica"
[[ "$(docker inspect -f '{{.State.StartedAt}}' "$PG" "$RDS" "$MA")" == "$arranques_antes" ]] && ok "ni Postgres, ni Redis, ni mail-auth se reiniciaron para renovar" || mal "la renovacion reinicio Postgres, Redis o mail-auth"
PGPASSWORD="$POSTGRES_PASSWORD" docker exec -e PGPASSWORD "$PGB" psql -h 127.0.0.1 -p 5432 -U mail_admin -d pgbouncer -Atqc 'RECONNECT' >/dev/null 2>&1 || true
sleep 1
tras="$(PGPASSWORD="$POSTGRES_PASSWORD" docker exec -e PGPASSWORD "$PGB" psql -h 127.0.0.1 -p 5432 -U mail_admin -d mail_registry -Atc \
  "select ssl from pg_stat_ssl where pid = pg_backend_pid()" 2>&1)"
[[ "$tras" == t ]] && ok "pgbouncer verifica el certificado renovado y sigue conectando" || mal "pgbouncer tras la renovacion: $tras"
REDISCLI_AUTH="$REDIS_PASSWORD" redis_cli --tls --cacert /ca/ca.crt ping | grep -q PONG && ok "Redis responde por TLS con el certificado renovado" || mal "Redis tras la renovacion"

# --- consumo --------------------------------------------------------------------------------------
echo "== Memoria en reposo =="
docker stats --no-stream --format '  {{.Name}}: {{.MemUsage}}' "$PG" "$RDS" "$PGB" "$BORDE" "$MA" "$(compose ps -q gateway)" "$(compose ps -q access-control)"

if [[ $FALLOS -ne 0 ]]; then
  echo "test-selfhosted-profile: FALLA" >&2
  exit 1
fi
echo "test-selfhosted-profile: OK"

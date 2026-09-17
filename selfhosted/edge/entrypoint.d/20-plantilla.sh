#!/bin/sh
# Valida la configuracion del proxy de borde y renderiza su plantilla. Cada variable se comprueba
# antes de llegar a nginx: un nombre de host con espacios o un tamano ilegible acabarian como una
# directiva distinta de la que se pretendia, no como un error.
set -eu

plantilla="${EDGE_TEMPLATE:-/etc/core-force-mail/edge/templates/core-force-mail.conf.template}"
destino=/etc/nginx/conf.d/10-core-force-mail.conf

falla() { echo "edge: $*" >&2; exit 1; }
cumple() { printf '%s' "$1" | grep -Eq "$2"; }

# El host publico es el de PUBLIC_BASE_URL, la direccion con la que la plataforma se anuncia en
# sus enlaces; EDGE_PUBLIC_HOST solo hace falta si la web se sirve en otro nombre.
if [ -z "${EDGE_PUBLIC_HOST:-}" ]; then
  case "${PUBLIC_BASE_URL:-}" in
    https://*) EDGE_PUBLIC_HOST=$(printf '%s' "${PUBLIC_BASE_URL#https://}" | sed -e 's|/$||') ;;
    *) falla "sin EDGE_PUBLIC_HOST, PUBLIC_BASE_URL debe ser https://<host> (valor: '${PUBLIC_BASE_URL:-}')" ;;
  esac
fi
EDGE_UPSTREAM="${EDGE_UPSTREAM:-gateway:8080}"
EDGE_TLS_CERT_FILE="${EDGE_TLS_CERT_FILE:-/etc/ssl/mail/cert.pem}"
EDGE_TLS_KEY_FILE="${EDGE_TLS_KEY_FILE:-/etc/ssl/mail/key.pem}"
EDGE_MAX_BODY_SIZE="${EDGE_MAX_BODY_SIZE:-101m}"
EDGE_HSTS_MAX_AGE="${EDGE_HSTS_MAX_AGE:-31536000}"
EDGE_REQUIRE_CLOUDFLARE="${EDGE_REQUIRE_CLOUDFLARE:-true}"

host='^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$'
cumple "$EDGE_PUBLIC_HOST" "$host" || falla "EDGE_PUBLIC_HOST no es un nombre de host valido: '$EDGE_PUBLIC_HOST'"
cumple "$EDGE_UPSTREAM" '^[a-z0-9]([a-z0-9.-]*[a-z0-9])?:[0-9]{1,5}$' || falla "EDGE_UPSTREAM debe ser host:puerto: '$EDGE_UPSTREAM'"
cumple "$EDGE_MAX_BODY_SIZE" '^[1-9][0-9]*[km]?$' || falla "EDGE_MAX_BODY_SIZE debe ser un tamano de nginx (p. ej. 101m): '$EDGE_MAX_BODY_SIZE'"
cumple "$EDGE_HSTS_MAX_AGE" '^[0-9]{1,9}$' || falla "EDGE_HSTS_MAX_AGE debe ser un numero de segundos: '$EDGE_HSTS_MAX_AGE'"
for f in "$EDGE_TLS_CERT_FILE" "$EDGE_TLS_KEY_FILE"; do
  cumple "$f" '^/[A-Za-z0-9._/-]+$' || falla "ruta de certificado no valida: '$f'"
  [ -r "$f" ] || falla "no se puede leer $f (volumen de certificados de acme montado?)"
done
case "$EDGE_REQUIRE_CLOUDFLARE" in
  true) EDGE_REQUIRE_CLOUDFLARE_FLAG=1 ;;
  false) EDGE_REQUIRE_CLOUDFLARE_FLAG=0 ;;
  *) falla "EDGE_REQUIRE_CLOUDFLARE debe ser true o false: '$EDGE_REQUIRE_CLOUDFLARE'" ;;
esac

export EDGE_PUBLIC_HOST EDGE_UPSTREAM EDGE_TLS_CERT_FILE EDGE_TLS_KEY_FILE EDGE_MAX_BODY_SIZE \
  EDGE_HSTS_MAX_AGE EDGE_REQUIRE_CLOUDFLARE_FLAG
# Lista explicita: las variables propias de nginx ($host, $remote_addr...) no se tocan.
envsubst '${EDGE_PUBLIC_HOST} ${EDGE_UPSTREAM} ${EDGE_TLS_CERT_FILE} ${EDGE_TLS_KEY_FILE} ${EDGE_MAX_BODY_SIZE} ${EDGE_HSTS_MAX_AGE} ${EDGE_REQUIRE_CLOUDFLARE_FLAG}' \
  <"$plantilla" >"$destino"
nginx -t -q
echo "edge: configuracion para $EDGE_PUBLIC_HOST -> $EDGE_UPSTREAM (cuerpo maximo $EDGE_MAX_BODY_SIZE, solo Cloudflare: $EDGE_REQUIRE_CLOUDFLARE)"

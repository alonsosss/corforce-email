#!/bin/sh
# Vigila el certificado publico que renueva acme (volumen ssl de deploy/mail) y, cuando cambia,
# recarga nginx sin cortar conexiones. Sin esto el proxy seguiria sirviendo el certificado viejo
# hasta que alguien lo recreara, y lo descubririan los navegadores al caducar.
set -eu

intervalo="${EDGE_CERT_CHECK_INTERVAL:-3600}"
case "$intervalo" in
  '' | *[!0-9]*) echo "edge: EDGE_CERT_CHECK_INTERVAL debe ser un numero de segundos: '$intervalo'" >&2; exit 1 ;;
esac
[ "$intervalo" -ge 60 ] || { echo "edge: EDGE_CERT_CHECK_INTERVAL minimo 60 s" >&2; exit 1; }

cert="${EDGE_TLS_CERT_FILE:-/etc/ssl/mail/cert.pem}"
key="${EDGE_TLS_KEY_FILE:-/etc/ssl/mail/key.pem}"
huella() { cat "$cert" "$key" 2>/dev/null | sha256sum | cut -d' ' -f1; }

(
  actual=$(huella)
  while sleep "$intervalo"; do
    nueva=$(huella)
    [ "$nueva" = "$actual" ] && continue
    if nginx -t -q 2>/dev/null && nginx -s reload; then
      echo "edge: certificado renovado; nginx recargado"
      actual="$nueva"
    else
      echo "edge: el certificado cambio pero la configuracion no pasa nginx -t; se sigue con el anterior" >&2
    fi
  done
) </dev/null &

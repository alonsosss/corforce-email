#!/bin/sh
# Genera la configuracion de Alertmanager desde su plantilla con las variables ALERT_* del entorno.
#
#   sh render.sh <plantilla> <destino>
#
# Corre dentro de la imagen de Alertmanager (busybox, sin envsubst) y en check-alertas.sh con la misma imagen.
# Los valores entran entre comillas simples en YAML, asi que solo se admiten caracteres de una direccion o de
# un host:puerto: un valor con comilla o salto de linea cambiaria la configuracion en vez de rellenarla.
set -eu

[ $# -eq 2 ] || { echo "uso: render.sh <plantilla> <destino>" >&2; exit 2; }

for nombre in ALERT_EMAIL_TO ALERT_SMTP_HOST ALERT_SMTP_USER; do
  eval "valor=\${$nombre:-}"
  [ -n "$valor" ] || { echo "render: falta $nombre" >&2; exit 1; }
  case "$valor" in
    *[!A-Za-z0-9._%+@:,-]*) echo "render: $nombre tiene caracteres no admitidos" >&2; exit 1 ;;
  esac
done

sed -e "s|__ALERT_EMAIL_TO__|$ALERT_EMAIL_TO|g" \
    -e "s|__ALERT_SMTP_HOST__|$ALERT_SMTP_HOST|g" \
    -e "s|__ALERT_SMTP_USER__|$ALERT_SMTP_USER|g" "$1" >"$2"

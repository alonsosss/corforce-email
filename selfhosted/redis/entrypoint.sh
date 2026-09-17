#!/bin/sh
# Arranque de Redis en la produccion autoalojada (docker-compose.selfhosted.yml).
#
# La contrasena no va en la linea de ordenes (docker-compose.yml la pasa con --requirepass, y
# cualquiera que liste procesos en el host la lee): se le da a redis-server por su entrada
# estandar como fichero de configuracion ("-"). Los demas parametros (TLS, memoria) llegan como
# argumentos. Se delega en el entrypoint de la imagen, que baja los privilegios al usuario redis.
set -eu

if [ -z "${REDIS_PASSWORD:-}" ]; then
  echo "redis: falta REDIS_PASSWORD (almacen de secretos); sin ella Redis quedaria abierto en la red interna" >&2
  exit 1
fi
case "$REDIS_PASSWORD" in
  *'
'* | *"$(printf '\r')"*)
    echo "redis: REDIS_PASSWORD no puede llevar saltos de linea" >&2
    exit 1
    ;;
esac

# Cadena entre comillas dobles del formato de configuracion de Redis: se escapan \ y ".
clave=$(printf '%s' "$REDIS_PASSWORD" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g')
unset REDIS_PASSWORD

exec docker-entrypoint.sh redis-server - "$@" <<EOF
requirepass "$clave"
EOF

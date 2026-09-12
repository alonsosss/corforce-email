#!/bin/sh
# Renderiza pgbouncer.ini desde la plantilla inyectando el upstream por entorno
# y arranca pgbouncer. El endpoint del Postgres real no se fija en el repo:
#   - dev local: DB_UPSTREAM_HOST=postgres-primary (contenedor)
#   - producción: DB_UPSTREAM_HOST=<endpoint RDS>
set -eu

DB_UPSTREAM_HOST="${DB_UPSTREAM_HOST:-postgres-primary}"
DB_UPSTREAM_PORT="${DB_UPSTREAM_PORT:-5432}"

template="/etc/pgbouncer/pgbouncer.ini.template"
# Se renderiza a una ruta escribible (la imagen puede correr como usuario
# no-root sin permiso de escritura en /etc/pgbouncer).
rendered="/tmp/pgbouncer.ini"

sed \
  -e "s|__DB_UPSTREAM_HOST__|${DB_UPSTREAM_HOST}|g" \
  -e "s|__DB_UPSTREAM_PORT__|${DB_UPSTREAM_PORT}|g" \
  "$template" > "$rendered"

exec pgbouncer "$rendered"

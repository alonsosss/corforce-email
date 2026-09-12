#!/usr/bin/env bash
# Renueva las conexiones de pgbouncer al servidor con RECONNECT (consola de administracion).
#
# Cuando hace falta: tras una migracion que cambia el tipo de una columna. pgx prepara
# sentencias en las conexiones al servidor y pgbouncer (pool_mode = transaction) las
# conserva; Postgres se niega a ejecutarlas con el tipo nuevo ("cached plan must not
# change result type") y reiniciar el servicio no ayuda porque el plan no vive ahi.
# RECONNECT es gradual: cada conexion se cierra al quedar libre, ninguna transaccion se
# corta. Reiniciar pgbouncer, en cambio, tira todas las conexiones de la plataforma a la vez.
#
# Uso (en el servidor, desde cualquier directorio):
#   ops/maintenance/pgbouncer-reconnect.sh [--wait-migrations]
# --wait-migrations espera (hasta 20 min) a que organization anuncie el fin de su barrido
# de migraciones antes de renovar, para no renovar ANTES del ALTER que motiva la renovacion.
set -euo pipefail

APP_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$APP_DIR"
PGB_CONTAINER="${PGB_CONTAINER:-app-pgbouncer-1}"
ORG_CONTAINER="${ORG_CONTAINER:-app-organization-1}"
WAIT_MIGRATIONS=0
[[ "${1:-}" == "--wait-migrations" ]] && WAIT_MIGRATIONS=1

if [[ "$WAIT_MIGRATIONS" == "1" ]]; then
  started="$(docker inspect --format '{{.State.StartedAt}}' "$ORG_CONTAINER" 2>/dev/null || true)"
  if [[ -z "$started" ]]; then
    echo ">> pgbouncer-reconnect: no encuentro $ORG_CONTAINER; renuevo sin esperar" >&2
  else
    for _ in $(seq 1 240); do
      if docker logs --since "$started" "$ORG_CONTAINER" 2>&1 | grep -q "barrido de migraciones de tenants completado"; then
        break
      fi
      sleep 5
    done
    if ! docker logs --since "$started" "$ORG_CONTAINER" 2>&1 | grep -q "barrido de migraciones de tenants completado"; then
      echo ">> pgbouncer-reconnect: organization no anuncio el fin de las migraciones en 20 min; renuevo igualmente" >&2
    fi
  fi
fi

POSTGRES_USER="$(grep -E '^POSTGRES_USER=' .env | head -1 | cut -d= -f2- | tr -d "\"'" || true)"
POSTGRES_USER="${POSTGRES_USER:-mail_admin}"
# La contrasena viene del almacen de secretos (tmpfs), nunca del repositorio.
APP_DIR="$APP_DIR" . ops/security/secrets/load.sh
: "${POSTGRES_PASSWORD:?POSTGRES_PASSWORD no esta en el almacen de secretos}"

docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" "$PGB_CONTAINER" \
  psql -h 127.0.0.1 -p 5432 -U "$POSTGRES_USER" -d pgbouncer -Atqc "RECONNECT" >/dev/null
echo ">> pgbouncer: conexiones al servidor renovadas (RECONNECT)"

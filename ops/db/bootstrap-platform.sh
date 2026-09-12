#!/usr/bin/env bash
# Arranque de una plataforma vacia: la celda inicial, la empresa de plataforma y su primer
# superadmin. Es lo unico que ningun servicio puede hacer por API, porque para llamar a la
# API hace falta ya un superadmin.
#
# Uso:
#   PLATFORM_ADMIN_EMAIL=root@example.com PLATFORM_ADMIN_PASSWORD='...' \
#     ops/db/bootstrap-platform.sh [--cell pe-01 --region sa-east-1 --db-host <host> --db-port 5432]
#
# La contrasena viaja SOLO por variable de entorno (nunca por argumento: quedaria en el
# historial y en `ps`). El hash lo hace Postgres con pgcrypto (bcrypt, coste 12), el mismo
# formato que verifica identity. Idempotente: si la celda, la empresa o el usuario ya
# existen, no los toca y no cambia ninguna contrasena.
#
# Credenciales de la base: como el resto de ops/, por ops/db/pg-credentials.sh (el almacen
# de secretos en el servidor; POSTGRES_* en desarrollo).
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

CELL="${DEFAULT_CELL_CODE:-}"; REGION=""; CELL_DB_HOST=""; CELL_DB_PORT="5432"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --cell) CELL="$2"; shift 2 ;;
    --region) REGION="$2"; shift 2 ;;
    --db-host) CELL_DB_HOST="$2"; shift 2 ;;
    --db-port) CELL_DB_PORT="$2"; shift 2 ;;
    *) echo "argumento desconocido: $1" >&2; exit 2 ;;
  esac
done

: "${PLATFORM_ADMIN_EMAIL:?PLATFORM_ADMIN_EMAIL es obligatoria}"
: "${PLATFORM_ADMIN_PASSWORD:?PLATFORM_ADMIN_PASSWORD es obligatoria (por entorno, nunca por argumento)}"
[[ -n "$CELL" ]] || { echo "indica la celda con --cell o DEFAULT_CELL_CODE" >&2; exit 2; }
[[ "$CELL" =~ ^[a-z][a-z0-9-]*$ ]] || { echo "codigo de celda invalido: $CELL" >&2; exit 2; }
[[ ${#PLATFORM_ADMIN_PASSWORD} -ge 12 ]] || { echo "la contrasena debe tener al menos 12 caracteres" >&2; exit 2; }

if [[ -f "$SCRIPT_DIR/pg-credentials.sh" && -z "${PGHOST:-}" ]]; then
  # shellcheck source=/dev/null
  . "$SCRIPT_DIR/pg-credentials.sh"
fi
export PGHOST="${PGHOST:-${POSTGRES_HOST:-127.0.0.1}}" PGPORT="${PGPORT:-${POSTGRES_PORT:-5432}}"
export PGUSER="${PGUSER:-${POSTGRES_USER:-mail_admin}}" PGPASSWORD="${PGPASSWORD:-${POSTGRES_PASSWORD:-}}"
REGISTRY_DB="${POSTGRES_DB:-mail_registry}"
CELL_DB_HOST="${CELL_DB_HOST:-$PGHOST}"

# Los valores entran como variables de psql (-v) y se citan con :'x': nunca se interpolan
# en el texto SQL desde bash.
psql -v ON_ERROR_STOP=1 -q -d "$REGISTRY_DB" \
  -v cell="$CELL" -v region="$REGION" -v cell_host="$CELL_DB_HOST" -v cell_port="$CELL_DB_PORT" \
  -v email="$PLATFORM_ADMIN_EMAIL" -v password="$PLATFORM_ADMIN_PASSWORD" <<'SQL'
INSERT INTO organization.cells (code, region, status, db_host, db_port)
VALUES (:'cell', :'region', 'active', :'cell_host', (:'cell_port')::int)
ON CONFLICT (code) DO NOTHING;

INSERT INTO organization.tenants (slug, name, db_name, status, cell_id)
SELECT 'platform', 'Core Force Mail', 'mail_tenant_platform', 'active', c.id
  FROM organization.cells c WHERE c.code = :'cell'
ON CONFLICT (slug) DO NOTHING;

INSERT INTO access_control.roles (tenant_id, name, description, is_system, status)
SELECT t.id, 'superadmin', 'Operador de la plataforma', true, 'active'
  FROM organization.tenants t WHERE t.slug = 'platform'
ON CONFLICT (tenant_id, name) DO NOTHING;

INSERT INTO identity.users (tenant_id, email, password_hash, first_name, last_name, status)
SELECT t.id, lower(:'email'), crypt(:'password', gen_salt('bf', 12)), 'Platform', 'Admin', 'active'
  FROM organization.tenants t WHERE t.slug = 'platform'
ON CONFLICT (tenant_id, email) DO NOTHING;

INSERT INTO access_control.user_roles (user_id, role_id)
SELECT u.id, r.id
  FROM identity.users u
  JOIN organization.tenants t ON t.id = u.tenant_id AND t.slug = 'platform'
  JOIN access_control.roles r ON r.tenant_id = t.id AND r.name = 'superadmin'
 WHERE u.email = lower(:'email')
ON CONFLICT DO NOTHING;
SQL

echo "plataforma lista: celda '$CELL', empresa 'platform', superadmin '$PLATFORM_ADMIN_EMAIL'"
echo "la base de la empresa de plataforma (mail_tenant_platform) la crea organization en su barrido de migraciones"

#!/usr/bin/env bash
# Prueba que los respaldos SIRVEN, no que existen.
#
# Un respaldo que nadie restauró nunca es una suposición. Esto toma el volcado más
# reciente de una empresa, lo restaura en una base desechable, comprueba que trae datos
# de negocio de verdad (no solo el esquema) y la borra.
#
# Pensado para correr semanalmente y dejar rastro: si falla, el respaldo estaba roto y
# hay tiempo de arreglarlo antes de necesitarlo.
#
# Uso: ops/backup/verify-restore.sh [base]   (por defecto, la de mayor tamaño)
set -uo pipefail

APP_DIR="${APP_DIR:-/opt/core-force-mail/app}"
BACKUP_DIR="${BACKUP_DIR:-/opt/core-force-mail/backups}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Credenciales por el resolvedor comun: la contrasena sale del almacen de secretos, el host
# y el usuario del .env.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
. "$SCRIPT_DIR/../db/pg-credentials.sh" || exit 1
# shellcheck source=/dev/null
. "$SCRIPT_DIR/report-metric.sh"

DB="${1:-}"
if [[ -z "$DB" ]]; then
  DB="$(ls -1S "$BACKUP_DIR"/*/mail_tenant_*.dump 2>/dev/null | head -1 | xargs -r basename | sed 's/\.dump$//')"
fi
[[ -z "$DB" ]] && { echo "FALLA: no hay respaldos que verificar en $BACKUP_DIR" >&2; exit 1; }

SCRATCH="verify_restore_$(date -u +%Y%m%d%H%M%S)"
echo "Verificando el respaldo de $DB restaurándolo en $SCRATCH"

cleanup() {
  psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d postgres -q \
    -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='$SCRATCH' AND pid<>pg_backend_pid()" >/dev/null 2>&1
  psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d postgres -q \
    -c "DROP DATABASE IF EXISTS \"$SCRATCH\"" >/dev/null 2>&1
}
trap cleanup EXIT

if ! bash "$HERE/restore-tenant.sh" "$DB" --into "$SCRATCH" > /tmp/verify-restore.log 2>&1; then
  echo "FALLA: el respaldo de $DB no se pudo restaurar" >&2
  tail -15 /tmp/verify-restore.log >&2
  exit 1
fi

# Que el esquema exista no prueba nada: una base recién creada también lo tendría. Lo que
# se comprueba es que haya filas de negocio. La tabla testigo depende del tipo de base:
# identity vive en el registro global, el catálogo vive en la base de cada empresa.
if [[ "$DB" == "mail_registry" ]]; then
  witness="identity.users"
else
  witness="master.products"
fi
rows="$(psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$SCRATCH" -At \
  -c "SELECT count(*) FROM $witness" 2>/dev/null)"

echo "  $witness: ${rows:-0} filas"
if [[ "${rows:-0}" -lt 1 ]]; then
  cf_publicar_metrica verificacion_respaldo 1 0
  echo "FALLA: la base restaurada no tiene filas en $witness; el respaldo está vacío" >&2
  exit 1
fi
cf_publicar_metrica verificacion_respaldo 0 "${rows:-0}"
echo "VERIFICACIÓN OK: el respaldo de $DB restaura y trae datos ($witness: $rows filas)"

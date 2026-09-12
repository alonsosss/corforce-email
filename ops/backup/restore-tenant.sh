#!/usr/bin/env bash
# Restaura UNA empresa desde su respaldo, sin tocar a las demás.
#
# Por defecto restaura a una base NUEVA (`<db>_restore_<fecha>`) en vez de sobrescribir la
# viva: casi siempre lo que se necesita es mirar el pasado o rescatar unas filas, no
# reemplazar la operación en curso. Sobrescribir exige --force y confirmación escrita,
# porque es irreversible.
#
# Uso:
#   ops/backup/restore-tenant.sh mail_tenant_demo                      # último respaldo local
#   ops/backup/restore-tenant.sh mail_tenant_demo /ruta/al.dump
#   ops/backup/restore-tenant.sh mail_tenant_demo s3://bucket/clave.dump
#   ops/backup/restore-tenant.sh mail_tenant_demo --into mail_prueba
#   ops/backup/restore-tenant.sh mail_tenant_demo --force              # SOBRESCRIBE la viva
set -uo pipefail

APP_DIR="${APP_DIR:-/opt/core-force-mail/app}"
BACKUP_DIR="${BACKUP_DIR:-/opt/core-force-mail/backups}"

usage() { sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'; exit 1; }
[[ $# -lt 1 ]] && usage

SRC_DB="$1"; shift
DUMP=""
TARGET=""
FORCE=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --into) TARGET="${2:-}"; shift 2 ;;
    --force) FORCE=1; shift ;;
    -h|--help) usage ;;
    *) DUMP="$1"; shift ;;
  esac
done

# Credenciales por el resolvedor comun: la contrasena sale del almacen de secretos, el host
# y el usuario del .env.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
. "$SCRIPT_DIR/../db/pg-credentials.sh" || exit 1

psql_() { psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$1" -v ON_ERROR_STOP=1 "${@:2}"; }

# Origen del volcado: explícito, de S3, o el más reciente en disco.
tmp=""
cleanup() { [[ -n "$tmp" ]] && rm -f "$tmp"; }
trap cleanup EXIT

if [[ -z "$DUMP" ]]; then
  DUMP="$(ls -1dt "$BACKUP_DIR"/*/"$SRC_DB".dump 2>/dev/null | head -1)"
  [[ -z "$DUMP" ]] && { echo "FALLA: no hay respaldo local de $SRC_DB en $BACKUP_DIR" >&2; exit 1; }
  echo "Respaldo más reciente: $DUMP"
elif [[ "$DUMP" == s3://* ]]; then
  tmp="$(mktemp /tmp/restore-XXXXXX.dump)"
  aws s3 cp "$DUMP" "$tmp" --only-show-errors || { echo "FALLA: no se pudo bajar $DUMP" >&2; exit 1; }
  DUMP="$tmp"
fi
[[ -f "$DUMP" ]] || { echo "FALLA: no existe $DUMP" >&2; exit 1; }

if [[ $FORCE -eq 1 ]]; then
  TARGET="${TARGET:-$SRC_DB}"
else
  TARGET="${TARGET:-${SRC_DB}_restore_$(date -u +%Y%m%d%H%M)}"
fi

exists="$(psql_ postgres -At -c "SELECT 1 FROM pg_database WHERE datname='$TARGET'" 2>/dev/null)"
if [[ "$exists" == "1" ]]; then
  if [[ $FORCE -ne 1 ]]; then
    echo "FALLA: $TARGET ya existe. Usa --into con otro nombre, o --force para reemplazarla." >&2
    exit 1
  fi
  echo
  echo "  Vas a REEMPLAZAR la base '$TARGET'. Todo lo que tenga ahora se pierde."
  echo "  Origen: $DUMP"
  read -r -p "  Escribe el nombre de la base para confirmar: " confirm
  [[ "$confirm" == "$TARGET" ]] || { echo "Cancelado."; exit 1; }
  # Cortar las sesiones abiertas: sin esto el DROP se queda esperando indefinidamente.
  psql_ postgres -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='$TARGET' AND pid<>pg_backend_pid()" >/dev/null
  psql_ postgres -c "DROP DATABASE \"$TARGET\"" || exit 1
fi

psql_ postgres -c "CREATE DATABASE \"$TARGET\"" || exit 1
echo "Restaurando $SRC_DB -> $TARGET ..."

# --no-owner: el rol dueño del volcado puede no existir en el destino, y eso no debe
# abortar una recuperación. Los errores reales sí se muestran.
log="$(mktemp)"
pg_restore -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$TARGET" --no-owner --no-privileges \
  -j 2 "$DUMP" 2>"$log"
rc=$?
real="$(grep -i 'error' "$log" | grep -viE 'already exists|does not exist, skipping' | head -5)"
rm -f "$log"

if [[ -n "$real" ]]; then
  echo "  Errores durante la restauración:"; printf '%s\n' "$real" | sed 's/^/      /'
fi

# Informe de cordura: una restauración que "terminó" pero dejó la base vacía es un fallo
# silencioso. Aquí se ve el tamaño real de lo recuperado.
echo
echo "Contenido restaurado en $TARGET:"
psql_ "$TARGET" -c "
  SELECT n.nspname AS esquema, count(*) AS tablas,
         pg_size_pretty(sum(pg_total_relation_size(c.oid))) AS tamano
    FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
   WHERE c.relkind='r' AND n.nspname NOT IN ('pg_catalog','information_schema')
   GROUP BY n.nspname ORDER BY sum(pg_total_relation_size(c.oid)) DESC LIMIT 10"

tables="$(psql_ "$TARGET" -At -c "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.relkind='r' AND n.nspname NOT IN ('pg_catalog','information_schema')")"
if [[ "${tables:-0}" -lt 10 ]]; then
  echo "FALLA: la base restaurada tiene solo ${tables:-0} tablas; el respaldo no sirve" >&2
  exit 1
fi
echo "RESTAURACIÓN OK: $TARGET con $tables tablas (código de salida de pg_restore: $rc)"

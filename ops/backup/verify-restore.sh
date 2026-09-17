#!/usr/bin/env bash
# Prueba que los respaldos SIRVEN, no que existen.
#
# Un respaldo que nadie restauró nunca es una suposición. Esto toma el volcado más
# reciente de una base, lo restaura en una base desechable, comprueba que trae todo lo que
# el volcado dice traer y que es una base de la plataforma con sus datos, y la borra.
#
# No supone filas de negocio: una empresa recién dada de alta no tiene ninguna y su
# respaldo vale lo mismo. Se comprueba:
#   - que cada esquema y cada tabla del índice del volcado existe en la base restaurada
#     (restore-tenant.sh muestra los errores de pg_restore, pero no se detiene en ellos);
#   - platform.event_outbox, que existe en las tres clases de base;
#   - registro y empresa: que el historial de migraciones (public.schema_migrations) trae
#     filas, que son datos y no esquema, y que existe el esquema que declara la cabecera de
#     cada migración registrada; en el registro, además, al menos un usuario (el superadmin
#     de ops/db/bootstrap-platform.sh);
#   - celda: sus migraciones se aplican a mano y no dejan historial, así que se exigen los
#     esquemas que declaran las migraciones de celda del árbol desplegado.
#
# Pensado para correr semanalmente y dejar rastro: si falla, el respaldo estaba roto y
# hay tiempo de arreglarlo antes de necesitarlo.
#
# Uso: ops/backup/verify-restore.sh [base]
#   Sin base, una de cada clase del ultimo respaldo: el registro, la celda con el volcado mas
#   grande y la empresa con el volcado mas grande. Una clase sin respaldo hace fallar la
#   verificacion salvo la de empresas, que puede no haber ninguna todavia.
set -uo pipefail
umask 077

APP_DIR="${APP_DIR:-/opt/core-force-mail/app}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source=/dev/null
. "$HERE/report-metric.sh"

# CF_VERIFICACION_ANIDADA: la llamada por base de la verificacion completa; la metrica la publica
# la llamada de fuera, con el total.
falla() {
  [[ -n "${CF_VERIFICACION_ANIDADA:-}" ]] || cf_publicar_metrica verificacion_respaldo 1 0
  echo "FALLA: $*" >&2
  exit 1
}

# Credenciales por el resolvedor comun: la contrasena sale del almacen de secretos, el host
# y el usuario del .env.
# shellcheck source=/dev/null
. "$HERE/../db/pg-credentials.sh" || falla "sin credenciales de Postgres"

BACKUP_DIR="${BACKUP_DIR:-$(cf_read_env BACKUP_DIR)}"
BACKUP_DIR="${BACKUP_DIR:-/opt/core-force-mail/backups}"

DB="${1:-}"
if [[ -z "$DB" ]]; then
  [[ -d "$BACKUP_DIR" ]] || falla "no existe $BACKUP_DIR"
  # Espera a un respaldo en curso: el volcado mas reciente podria estar a medias.
  exec 8>"$BACKUP_DIR/.respaldo.lock"
  flock -w 7200 8 || falla "un respaldo sigue en curso tras dos horas"
  # La corrida completa mas reciente es la ultima que respaldo el registro; celda y empresa salen
  # de esa misma corrida.
  corrida="$(ls -1dt "$BACKUP_DIR"/*/mail_registry.dump 2>/dev/null | head -1 | xargs -r dirname)"
  [[ -n "$corrida" ]] || falla "no hay respaldo del registro (mail_registry) en $BACKUP_DIR"
  mayor() { find "$corrida" -maxdepth 1 -type f -name "$1" -printf '%s %f\n' | sort -rn | head -1 | sed -E 's/^[0-9]+ //; s/\.dump$//'; }
  bases=(mail_registry)
  celda="$(mayor 'mail_cell_*.dump')"
  [[ -n "$celda" ]] || falla "la corrida $corrida no tiene respaldo de ninguna celda (mail_cell_*)"
  bases+=("$celda")
  empresa="$(mayor 'mail_tenant_*.dump')"
  if [[ -n "$empresa" ]]; then
    bases+=("$empresa")
  else
    echo "  aviso: no hay respaldo de ninguna empresa (mail_tenant_*); se verifican registro y celda"
  fi
  total=0
  for base in "${bases[@]}"; do
    salida="$(CF_VERIFICACION_ANIDADA=1 bash "$HERE/verify-restore.sh" "$base" 2>&1)"
    rc=$?
    printf '%s\n' "$salida"
    [[ $rc -eq 0 ]] || { cf_publicar_metrica verificacion_respaldo 1 "$total"; echo "FALLA: la verificacion de $base no paso" >&2; exit 1; }
    tablas_base="$(sed -n 's/^VERIFICACIÓN OK: .*(\([0-9]*\) tablas)$/\1/p' <<<"$salida")"
    total=$((total + ${tablas_base:-0}))
  done
  cf_publicar_metrica verificacion_respaldo 0 "$total"
  echo "VERIFICACIÓN COMPLETA OK: ${bases[*]} ($total tablas)"
  exit 0
fi

case "$DB" in
  mail_registry) plano=registro; migraciones="$APP_DIR/migrations/registry" ;;
  mail_cell_*)   plano=celda;    migraciones="$APP_DIR/migrations/cell/canonical" ;;
  mail_tenant_*) plano=empresa;  migraciones="$APP_DIR/migrations/tenant/canonical" ;;
  *) falla "$DB no es la base del registro, de una celda ni de una empresa" ;;
esac
[[ -d "$migraciones" ]] || falla "no existe $migraciones: sin las migraciones no se sabe qué esquemas esperar"

DUMP="$(ls -1dt "$BACKUP_DIR"/*/"$DB".dump 2>/dev/null | head -1)"
[[ -n "$DUMP" ]] || falla "no hay respaldo local de $DB en $BACKUP_DIR"
[[ "$DB" =~ ^[a-z0-9_]+$ ]] || falla "nombre de base inesperado: $(printf '%q' "$DB")"
cf_pg_montar "$(dirname "$DUMP")" ro || falla "no se pudo preparar $DUMP para las herramientas de Postgres"

SCRATCH="verify_restore_$(date -u +%Y%m%d%H%M%S)"
echo "Verificando $DUMP ($plano) restaurándolo en $SCRATCH"

log="$(mktemp)"
cleanup() {
  cf_psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d postgres -q \
    -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='$SCRATCH' AND pid<>pg_backend_pid()" >/dev/null 2>&1
  cf_psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d postgres -q \
    -c "DROP DATABASE IF EXISTS \"$SCRATCH\"" >/dev/null 2>&1
  rm -f "$log"
}
trap cleanup EXIT

if ! bash "$HERE/restore-tenant.sh" "$DB" "$DUMP" --into "$SCRATCH" > "$log" 2>&1; then
  tail -15 "$log" >&2
  falla "el respaldo de $DB no se pudo restaurar"
fi

q() { cf_psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$SCRATCH" -v ON_ERROR_STOP=1 -At -c "$1"; }

# Índice del volcado: "<id>; <oid> <oid> SCHEMA - <esquema> <dueño>" y
# "<id>; <oid> <oid> TABLE <esquema> <tabla> <dueño>"; TABLE DATA y TABLE ATTACH son otras
# entradas.
indice="$(cf_pg_restore --list "$DUMP" 2>/dev/null | awk '
  $4 == "SCHEMA" && $5 == "-" { print $6 }
  $4 == "TABLE" && $5 != "DATA" && $5 != "ATTACH" { print $5 "." $6 }' | LC_ALL=C sort -u)"
[[ -n "$indice" ]] || falla "el índice de $DUMP no lista esquemas ni tablas"
restaurado="$(q "SELECT nspname FROM pg_namespace
                 UNION ALL
                 SELECT n.nspname || '.' || c.relname FROM pg_class c
                   JOIN pg_namespace n ON n.oid = c.relnamespace
                  WHERE c.relkind IN ('r', 'p')" | LC_ALL=C sort -u)" \
  || falla "no se pudo leer el catálogo de $SCRATCH"
faltan="$(LC_ALL=C comm -23 <(printf '%s\n' "$indice") <(printf '%s\n' "$restaurado"))"
[[ -z "$faltan" ]] || falla "la base restaurada no tiene lo que el volcado trae: $(head -10 <<<"$faltan" | tr '\n' ' ')"
tablas="$(grep -c '\.' <<<"$indice")"

[[ "$(q "SELECT to_regclass('platform.event_outbox') IS NOT NULL")" == "t" ]] \
  || falla "falta platform.event_outbox, que existe en toda base de la plataforma"

# Esquema que declara la cabecera "-- Schema: <esquema> | Service: <servicio>".
esquema_de() { sed -n '1s/^-- Schema: \([A-Za-z0-9_]*\) |.*/\1/p' "$1"; }

ficheros=()
sin_fichero=0
if [[ "$plano" == celda ]]; then
  mapfile -t ficheros < <(find "$migraciones" -type f -name '*.sql' | LC_ALL=C sort)
else
  [[ "$(q "SELECT to_regclass('public.schema_migrations') IS NOT NULL")" == "t" ]] \
    || falla "falta public.schema_migrations: la base no pasó por el migrador de la plataforma"
  aplicadas="$(q "SELECT name FROM public.schema_migrations WHERE name <> '__baseline__' ORDER BY name")" \
    || falla "no se pudo leer public.schema_migrations"
  [[ -n "$aplicadas" ]] || falla "public.schema_migrations está vacía: el volcado no trae los datos"
  while IFS= read -r nombre; do
    # Ruta relativa al directorio de migraciones o, en historiales antiguos, solo el
    # fichero. Viene de la base restaurada: nunca se sale de ese directorio.
    [[ "$nombre" =~ ^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*$ && "$nombre" != *..* ]] \
      || falla "nombre de migración inesperado en el historial: $(printf '%q' "$nombre")"
    f="$migraciones/$nombre"
    [[ -f "$f" ]] || f="$(find "$migraciones" -type f -name "$nombre" | head -1)"
    if [[ -n "$f" && -f "$f" ]]; then
      ficheros+=("$f")
    else
      sin_fichero=$((sin_fichero + 1))
    fi
  done <<<"$aplicadas"
fi

esperados="$(for f in "${ficheros[@]}"; do esquema_de "$f"; done | LC_ALL=C sort -u)"
[[ -n "$esperados" ]] || falla "ninguna migración de $migraciones declara su esquema en la cabecera"
faltan="$(LC_ALL=C comm -23 <(printf '%s\n' "$esperados") <(q "SELECT nspname FROM pg_namespace" | LC_ALL=C sort -u))"
[[ -z "$faltan" ]] || falla "faltan esquemas que declaran sus migraciones: $(tr '\n' ' ' <<<"$faltan")"
[[ $sin_fichero -eq 0 ]] \
  || echo "  aviso: $sin_fichero migraciones del historial no están en $migraciones; su esquema no se comprueba"

if [[ "$plano" == registro ]]; then
  usuarios="$(q "SELECT count(*) FROM identity.users")" || falla "no se pudo leer identity.users"
  [[ "${usuarios:-0}" -ge 1 ]] || falla "el registro restaurado no tiene usuarios: sin el superadmin la plataforma no se opera"
fi

echo "  $tablas tablas del volcado presentes; esquemas propios: $(tr '\n' ' ' <<<"$esperados")"
[[ -n "${CF_VERIFICACION_ANIDADA:-}" ]] || cf_publicar_metrica verificacion_respaldo 0 "$tablas"
echo "VERIFICACIÓN OK: el respaldo de $DB restaura completo ($tablas tablas)"

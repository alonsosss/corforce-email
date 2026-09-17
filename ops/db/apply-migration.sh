#!/usr/bin/env bash
# Aplica un fichero de migracion a las bases de empresa, y comprueba que hizo algo.
#
#   ops/db/apply-migration.sh migrations/tenant/canonical/production-ops/193_orders_view.sql
#   ops/db/apply-migration.sh <fichero> mail_tenant_demo          # solo una base
#   VERIFY_SQL="SELECT to_regclass('production.v_orders') IS NOT NULL" ops/db/apply-migration.sh <fichero>
#
# Por que existe: aplicar una migracion a produccion era una cadena de scp, docker cp y psql
# escrita a mano cada vez. Esa cadena se equivoca en silencio -un nombre de restriccion mal
# escrito en un DROP ... IF EXISTS no falla, no hace nada- y ademas dependia de leer la
# contrasena del .env, que ya no la tiene.
#
# Se aplica con ON_ERROR_STOP: una migracion a medias es peor que una no aplicada, porque
# deja el esquema en un estado que nadie describio.
#
# VERIFY_SQL, si se da, debe devolver `t` en CADA base despues de aplicar. Es la unica forma
# de distinguir "se ejecuto" de "consiguio lo que decia": un fichero idempotente aplicado
# sobre un esquema distinto del esperado termina con exito sin haber cambiado nada.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
FILE="${1:-}"
shift || true

[[ -n "$FILE" && -f "$FILE" ]] || {
  echo "uso: ops/db/apply-migration.sh <fichero.sql> [base...]" >&2; exit 1; }

# shellcheck source=/dev/null
. "$ROOT/ops/db/pg-credentials.sh" || exit 1

if [[ $# -gt 0 ]]; then
  DBS=("$@")
else
  mapfile -t DBS < <(cf_tenant_databases)
fi

[[ ${#DBS[@]} -gt 0 ]] || { echo "apply-migration: no hay bases de empresa" >&2; exit 1; }

echo "migracion: $(basename "$FILE")"
echo "bases: ${DBS[*]}"

fallos=0
for db in "${DBS[@]}"; do
  printf '  %-40s ' "$db"
  # Por la entrada estandar y no con -f: asi el fichero no tiene que existir dentro del
  # contenedor efimero con el que el perfil autoalojado alcanza la base
  # (ops/db/pg-credentials.sh).
  salida="$(cf_psql -d "$db" -v ON_ERROR_STOP=1 -q <"$FILE" 2>&1)"
  if [[ $? -ne 0 ]]; then
    echo "FALLO"
    printf '%s\n' "$salida" | sed 's/^/      /' | head -5
    fallos=$((fallos + 1))
    continue
  fi

  if [[ -n "${VERIFY_SQL:-}" ]]; then
    ok="$(cf_psql -d "$db" -At -c "SELECT ($VERIFY_SQL)" 2>&1)"
    if [[ "$ok" != "t" ]]; then
      echo "APLICADA PERO NO SURTIO EFECTO"
      echo "      la comprobacion devolvio: $ok"
      fallos=$((fallos + 1))
      continue
    fi
  fi

  # Los avisos de idempotencia son esperados; cualquier otro se muestra sin hacer fallar.
  avisos="$(printf '%s\n' "$salida" | grep -i 'NOTICE' | grep -viE 'already exists|does not exist, skipping|, skipping$' || true)"
  echo "OK"
  [[ -n "$avisos" ]] && printf '%s\n' "$avisos" | sed 's/^/      /'
done

if [[ $fallos -gt 0 ]]; then
  echo "apply-migration: $fallos base(s) con problema" >&2
  exit 1
fi
echo "apply-migration: aplicada en ${#DBS[@]} base(s)"

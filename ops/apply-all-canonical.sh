#!/usr/bin/env bash
# Aplica TODAS las migraciones canonicas a cada base de empresa, por servicio y por numero.
#
#   ops/apply-all-canonical.sh                   # todas las bases
#   ops/apply-all-canonical.sh mail_tenant_demo   # una sola
#
# Es idempotente por construccion: las migraciones canonicas se escriben para poder
# reaplicarse. Los avisos de "ya existe" son el resultado esperado y no cuentan como error;
# cualquier otro si.
#
# El orden importa y NO es alfabetico: dentro de cada servicio las migraciones se aplican por
# su numero. Ordenar como texto pone la 190 antes que la 36, y una migracion se encontraria
# un esquema que todavia no existe.
#
# Las credenciales salen del resolvedor comun: la contrasena del almacen de secretos, el host
# y el usuario del .env. Este script apuntaba a un contenedor de Postgres local y a una ruta
# bajo $HOME que dejaron de existir al pasar a RDS, asi que llevaba tiempo sin poder correr.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CANONICAL="${CANONICAL_DIR:-$ROOT/migrations/tenant/canonical}"

[[ -d "$CANONICAL" ]] || { echo "no existe $CANONICAL" >&2; exit 1; }

# shellcheck source=/dev/null
. "$ROOT/ops/db/pg-credentials.sh" || exit 1

if [[ $# -gt 0 ]]; then
  DBS=("$@")
else
  mapfile -t DBS < <(cf_tenant_databases)
fi
[[ ${#DBS[@]} -gt 0 ]] || { echo "no hay bases de empresa" >&2; exit 1; }

# Orden de aplicacion: servicio, numero, nombre.
mapfile -t FILES < <(
  find "$CANONICAL" -name '*.sql' -printf '%h\t%f\n' |
    sed -E 's/\t([0-9]+)/\t\1\t/' |
    sort -t$'\t' -k1,1 -k2,2n -k3,3 |
    awk -F'\t' '{ print $1 "/" $2 $3 }'
)

echo "bases: ${DBS[*]}"
echo "migraciones canonicas: ${#FILES[@]}"

# Avisos que solo dicen "ya estaba": es lo que se espera al reaplicar.
INOFENSIVOS='already exists|does not exist, skipping|, skipping$|multiple primary keys|duplicate key value|cannot drop|is not a'

total_errores=0
for db in "${DBS[@]}"; do
  echo "===== $db"
  errores=0
  for f in "${FILES[@]}"; do
    # Por la entrada estandar y no con -f: el fichero no tiene que existir dentro del
    # contenedor efimero del perfil autoalojado (ops/db/pg-credentials.sh).
    salida="$(cf_psql_entrada -d "$db" -X -q -v ON_ERROR_STOP=0 <"$f" 2>&1)"
    reales="$(printf '%s\n' "$salida" | grep -iE 'ERROR' | grep -viE "$INOFENSIVOS")"
    if [[ -n "$reales" ]]; then
      echo "  [ERROR] ${f#"$CANONICAL"/}"
      printf '%s\n' "$reales" | sed 's/^/      /' | head -4
      errores=$((errores + 1))
    fi
  done
  echo "  -> $errores fichero(s) con error real"
  total_errores=$((total_errores + errores))
done

[[ $total_errores -gt 0 ]] && exit 1
echo "LISTO: sin errores reales"

#!/usr/bin/env bash
# Retira UNA clave OPCIONAL del almacen de secretos.
#
# Por que existe: add-secret.sh anade o reemplaza, pero no quita, y rechaza un valor vacio. Retirar
# un secreto forma parte de su ciclo de vida (el maestro compartido de la migracion de buzones,
# DOVECOT_MIGRATION_MASTER_*, se retira en cuanto la credencial por trabajo esta probada); sin esta
# via, el camino de hecho era descifrar y editar el JSON a mano, que es justo lo que el almacen
# viene a evitar.
#
# Solo admite claves marcadas como opcionales (`CLAVE?`) en secret-keys.txt o secret-keys-db.txt:
# retirar una obligatoria dejaria fetch-secrets.sh sin poder materializar (todo o nada) y el
# siguiente despliegue sin credenciales.
#
# Uso, en el servidor:
#   ops/security/secrets/remove-secret.sh MI_CLAVE            # simulacion
#   ops/security/secrets/remove-secret.sh MI_CLAVE --apply    # retira
#
# Sin --apply no escribe: dice que haria. Nunca imprime valores.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./store.sh
. "$SCRIPT_DIR/store.sh"

KEYS_FILE="${SECRET_KEYS_FILE:-$SCRIPT_DIR/secret-keys.txt}"
KEYS_DB_FILE="${SECRET_KEYS_DB_FILE:-$SCRIPT_DIR/secret-keys-db.txt}"

CLAVE="${1:-}"
shift || true
APLICAR=0
while [ $# -gt 0 ]; do
    case "$1" in
        --apply) APLICAR=1; shift ;;
        *) echo "opcion desconocida: $1" >&2; exit 1 ;;
    esac
done

[ -n "$CLAVE" ] || { echo "uso: remove-secret.sh CLAVE [--apply]" >&2; exit 1; }
store_requisitos || exit 1

if grep -qE "^${CLAVE}$" "$KEYS_FILE" "$KEYS_DB_FILE"; then
    echo "ERROR: $CLAVE es obligatoria en secret-keys.txt o secret-keys-db.txt; sin ella fetch-secrets.sh no materializa nada." >&2
    echo "Si de verdad deja de ser un secreto, primero retirala de esa lista (y de reparto.tsv) en el repositorio." >&2
    exit 1
fi
if ! grep -qE "^${CLAVE}\?$" "$KEYS_FILE" "$KEYS_DB_FILE"; then
    echo "ERROR: $CLAVE no esta declarada como opcional en secret-keys.txt ni en secret-keys-db.txt." >&2
    exit 1
fi

store_existe || { echo "ERROR: el almacen $(store_descripcion) no existe o no responde" >&2; exit 1; }
ACTUAL="$(store_leer_json)" || { echo "ERROR: no se pudo leer el almacen; no se escribe nada." >&2; exit 1; }

NUEVO="$(CLAVE="$CLAVE" ACTUAL="$ACTUAL" python3 - <<'PY'
import json, os
d = json.loads(os.environ["ACTUAL"] or "{}")
k = os.environ["CLAVE"]
existia = k in d
d.pop(k, None)
print(json.dumps({"json": json.dumps(d), "existia": existia, "total": len(d)}))
PY
)"
EXISTIA="$(echo "$NUEVO" | python3 -c 'import sys,json; print(json.load(sys.stdin)["existia"])')"
TOTAL="$(echo "$NUEVO" | python3 -c 'import sys,json; print(json.load(sys.stdin)["total"])')"

if [ "$EXISTIA" != "True" ]; then
    echo "$CLAVE no esta en el almacen; nada que retirar."
    exit 0
fi
echo "clave:   $CLAVE (se RETIRA del almacen)"
echo "almacen: $(store_descripcion) ($TOTAL claves tras el cambio)"

if [ "$APLICAR" -ne 1 ]; then
    echo
    echo "Nada escrito. Repite con --apply."
    exit 0
fi

echo "$NUEVO" | python3 -c 'import sys,json; sys.stdout.write(json.load(sys.stdin)["json"])' \
    | store_escribir_json
echo ">> retirada del almacen; recrea los contenedores que la recibian (reparto.tsv) para que dejen de verla"

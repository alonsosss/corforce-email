#!/usr/bin/env bash
# Migracion de una sola vez: sube al almacen de secretos los valores que hoy viven en el
# .env del servidor y deja ese .env sin credenciales.
#
# Por defecto NO escribe nada: muestra que haria (que claves subiria y cuales saldrian del
# .env, nunca sus valores). Con --apply ejecuta.
#
# Uso, en el servidor y desde el directorio del despliegue:
#   ops/security/secrets/push-secrets.sh /opt/core-force-mail/app/.env
#   ops/security/secrets/push-secrets.sh /opt/core-force-mail/app/.env --apply
#
# El .env sale reescrito SIN las claves de secret-keys.txt ni secret-keys-db.txt; el almacen
# queda como unica fuente de las dos. Antes de reescribir, el script vuelve a leer el almacen y
# compara valor por valor: si algo no coincide, aborta sin tocar el .env. Los valores que ya
# hubiera en el almacen (de una corrida anterior o de add-secret.sh) se conservan: esto MEZCLA,
# no reemplaza.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./store.sh
. "$SCRIPT_DIR/store.sh"

KEYS_FILE="${SECRET_KEYS_FILE:-$SCRIPT_DIR/secret-keys.txt}"
KEYS_DB_FILE="${SECRET_KEYS_DB_FILE:-$SCRIPT_DIR/secret-keys-db.txt}"

ENV_FILE="${1:-}"
APPLY="${2:-}"
[[ -n "$ENV_FILE" && -f "$ENV_FILE" ]] || { echo "uso: push-secrets.sh <ruta-al-.env> [--apply]" >&2; exit 1; }
command -v gpg >/dev/null || { echo "push-secrets: falta gpg (paquete gnupg)" >&2; exit 1; }

payload_file="$(mktemp)"
sanitized_file="$(mktemp)"
trap 'shred -u "$payload_file" "$sanitized_file" 2>/dev/null || rm -f "$payload_file" "$sanitized_file"' EXIT
chmod 600 "$payload_file" "$sanitized_file"

python3 - "$ENV_FILE" "$KEYS_FILE" "$KEYS_DB_FILE" "$payload_file" "$sanitized_file" <<'PY'
import json
import re
import sys

env_file, keys_file, keys_db_file, payload_out, sanitized_out = sys.argv[1:6]

def declaradas(ruta):
    return [
        line.strip() for line in open(ruta, encoding="utf-8")
        if line.strip() and not line.startswith("#")
    ]

# "?" = opcional (ver secret-keys.txt). Las dos listas se tratan igual: ambas son secretos que
# no pueden quedar en el .env, solo cambia el fichero de materializacion en fetch-secrets.sh.
declaradas_todas = declaradas(keys_file) + declaradas(keys_db_file)
nombres = [k[:-1] if k.endswith("?") else k for k in declaradas_todas]
obligatorias = [k for k in declaradas_todas if not k.endswith("?")]

values, kept = {}, []
for line in open(env_file, encoding="utf-8"):
    m = re.match(r"^([A-Z0-9_]+)=(.*)$", line.rstrip("\n"))
    if m and m.group(1) in nombres:
        # Una clave vacia no se sube: en el almacen equivale a no tenerla, y subirla
        # como cadena vacia hacia fallar la materializacion completa mas adelante.
        if m.group(2).strip():
            values[m.group(1)] = m.group(2)
        continue
    kept.append(line)

faltan = [k for k in obligatorias if k not in values]
if faltan:
    print("ERROR: estas claves son obligatorias y no tienen valor en el .env: "
          + ", ".join(faltan), file=sys.stderr)
    print("Sin ellas la materializacion fallaria y el despliegue quedaria sin credenciales.",
          file=sys.stderr)
    sys.exit(1)

opcionales_sin_valor = [k[:-1] for k in declaradas_todas
                        if k.endswith("?") and k[:-1] not in values]
if opcionales_sin_valor:
    print("Opcionales sin configurar (no se suben): " + ", ".join(opcionales_sin_valor),
          file=sys.stderr)

json.dump(values, open(payload_out, "w", encoding="utf-8"))
open(sanitized_out, "w", encoding="utf-8").writelines(kept)

print(f"claves a subir: {len(values)}")
for k in sorted(values):
    print(f"  {k}")
PY

if [[ "$APPLY" != "--apply" ]]; then
  echo
  echo "Simulacion. Nada se subio ni se modifico. Repetir con --apply para ejecutar."
  exit 0
fi

ACTUAL="$(store_leer_json --permitir-vacio)" || exit 1
NUEVO="$(ACTUAL="$ACTUAL" python3 -c '
import json, os
actual = json.loads(os.environ["ACTUAL"] or "{}")
nuevo = json.load(open("'"$payload_file"'", encoding="utf-8"))
actual.update(nuevo)
print(json.dumps(actual))
')"
printf '%s' "$NUEVO" | store_escribir_json
echo "push-secrets: almacen actualizado en $STORE_FILE"

# Verificacion antes de tocar el .env: lo que quedo guardado debe ser identico a lo enviado.
STORED_PAYLOAD="$(store_leer_json)" || { echo "push-secrets: no se pudo releer el almacen recien escrito; el .env NO se toca" >&2; exit 1; }
STORED_PAYLOAD="$STORED_PAYLOAD" python3 - "$payload_file" <<'PY'
import json, os, sys
sent = json.load(open(sys.argv[1], encoding="utf-8"))
stored = json.loads(os.environ["STORED_PAYLOAD"])
diff = [k for k, v in sent.items() if stored.get(k) != v]
if diff:
    print("push-secrets: el almacen no coincide con lo enviado: " + ", ".join(diff), file=sys.stderr)
    sys.exit(1)
print("push-secrets: verificacion correcta")
PY

backup="${ENV_FILE}.pre-secrets.$(date +%Y%m%d%H%M%S)"
cp -p "$ENV_FILE" "$backup"
chmod 600 "$backup"
cp -p "$sanitized_file" "$ENV_FILE"
chmod 600 "$ENV_FILE"
echo "push-secrets: .env reescrito sin credenciales (copia de rescate en $backup)"
echo "push-secrets: BORRAR esa copia en cuanto se verifique el despliegue: shred -u $backup"

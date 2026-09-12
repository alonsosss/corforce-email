#!/usr/bin/env bash
# Guardarrail: ninguna credencial de la lista canonica puede quedar con valor en un fichero
# versionado. Corre en CI y localmente.
#
# Se revisan asignaciones de la forma CLAVE=valor en ficheros versionados. Se admiten
# valores vacios y marcadores (CAMBIAR_ESTO, your-key-here, ...), que es lo que debe llevar
# .env.example.
#
# Queda FUERA del alcance a proposito el valor por defecto de desarrollo en la
# interpolacion de Compose (${POSTGRES_PASSWORD:-dev_password_123}): no es una credencial
# de produccion sino el arranque de una base local efimera, y su ausencia obligaria a cada
# desarrollador a inventarse una configuracion. Lo que este guardarrail persigue es que un
# secreto REAL entre al repositorio.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
KEYS_FILE="$ROOT/ops/security/secrets/secret-keys.txt"

cd "$ROOT"
LISTA="$(mktemp)"
trap 'rm -f "$LISTA"' EXIT
git ls-files -z >"$LISTA"

python3 - "$KEYS_FILE" "$LISTA" <<'PY'
import os
import re
import sys

# Se descarta el sufijo "?" (opcional): que una credencial no este contratada todavia no la
# vuelve publicable en el repositorio.
keys = {
    line.strip().rstrip("?") for line in open(sys.argv[1], encoding="utf-8")
    if line.strip() and not line.startswith("#")
}
pattern = re.compile(r"^\s*(?:export\s+)?(" + "|".join(sorted(keys)) + r")\s*=\s*(.+?)\s*$")
# Marcadores admitidos: son los que ya usan la documentacion y .env.example de este
# repositorio. Un valor real (una llave de 44 caracteres en base64, por ejemplo) no encaja
# en ninguno y hace fallar la comprobacion.
placeholder = re.compile(
    r"^(\"|')?(\s*|\*+|cambia\w*.*|cambiar.*|change.*|your.*|tu[_-].*|xxx+|placeholder.*|"
    r"<.*>|\.\.\.|dev[_-].*|\$\{.*\}|\$\(.*\)|test.*|.*prueba.*|dummy.*|fake.*|"
    r"ejemplo.*|example.*|minimo.*|admin|"
    r"admin[_-]\w*|secret_?key_?here.*)(\"|')?$",
    re.IGNORECASE,
)

# Un valor CORTO no es una credencial: las de esta plataforma son llaves y tokens de decenas de
# caracteres. Los fixtures de prueba ("gw") disparaban el guardarrail y lo dejaban en rojo
# de forma permanente, que es la manera de que nadie mire lo que si importa.
LARGO_MINIMO_CREDENCIAL = 8

hallazgos = []
for path in open(sys.argv[2], "rb").read().split(b"\0"):
    if not path:
        continue
    path = path.decode()
    if path.startswith("ops/security/secrets/"):
        continue
    try:
        with open(path, encoding="utf-8") as fh:
            for n, line in enumerate(fh, 1):
                m = pattern.match(line)
                if not m:
                    continue
                # En un lenguaje con varias asignaciones por linea (un monkeypatch de
                # Python, por ejemplo) el resto de la linea NO es el valor: se corta en la
                # comilla de cierre, o el guardarrail juzga un fixture por lo que venga
                # detras.
                valor = m.group(2)
                if valor[:1] in "\"'":
                    fin = valor.find(valor[0], 1)
                    if fin > 0:
                        valor = valor[: fin + 1]
                if placeholder.match(valor) or len(valor.strip("\"'")) < LARGO_MINIMO_CREDENCIAL:
                    continue
                hallazgos.append((path, n, m.group(1)))
    except (UnicodeDecodeError, IsADirectoryError, FileNotFoundError):
        continue

if hallazgos:
    print("Credenciales con valor en ficheros versionados:", file=sys.stderr)
    for path, n, key in hallazgos:
        # Nunca se imprime el valor, solo donde esta.
        print(f"  {path}:{n}  {key}", file=sys.stderr)
    print("\nLa fuente de estos valores es el almacen de secretos "
          "(ops/security/secrets/README.md).", file=sys.stderr)
    sys.exit(1)

print(f"  OK: ninguna de las {len(keys)} credenciales canonicas tiene valor en el repositorio.")
PY

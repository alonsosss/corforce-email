#!/usr/bin/env bash
# Anade o actualiza UNA clave en el almacen de secretos.
#
# Por que existe: push-secrets.sh es la migracion inicial y exige que el .env traiga
# TODAS las credenciales canonicas, cosa que deja de ser cierta en cuanto la migracion
# se hizo. A partir de ahi no habia forma soportada de introducir un secreto nuevo, y el
# camino de hecho era editar el .env —justo lo que el almacen viene a evitar—.
#
# El valor NUNCA viaja por la linea de comandos: se lee de una variable de entorno o de
# un fichero. Un argumento queda en el historial del shell y en la lista de procesos, que
# cualquiera del host puede mirar.
#
# Uso, en el servidor:
#   VALOR=... ops/security/secrets/add-secret.sh MI_CLAVE            # desde variable
#   ops/security/secrets/add-secret.sh MI_CLAVE --desde-env .env     # desde el .env actual
#   ops/security/secrets/add-secret.sh MI_CLAVE --desde-env .env --quitar-del-env
#
# Sin --apply no escribe: dice que haria. Nunca imprime valores, ni los nuevos ni los
# que ya estaban.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./store.sh
. "$SCRIPT_DIR/store.sh"

KEYS_FILE="${SECRET_KEYS_FILE:-$SCRIPT_DIR/secret-keys.txt}"
KEYS_DB_FILE="${SECRET_KEYS_DB_FILE:-$SCRIPT_DIR/secret-keys-db.txt}"

CLAVE="${1:-}"
shift || true
DESDE_ENV=""
QUITAR_DEL_ENV=0
APLICAR=0
while [ $# -gt 0 ]; do
    case "$1" in
        --desde-env)       DESDE_ENV="$2"; shift 2 ;;
        --quitar-del-env)  QUITAR_DEL_ENV=1; shift ;;
        --apply)           APLICAR=1; shift ;;
        *) echo "opcion desconocida: $1" >&2; exit 1 ;;
    esac
done

[ -n "$CLAVE" ] || { echo "uso: add-secret.sh CLAVE [--desde-env .env] [--quitar-del-env] [--apply]" >&2; exit 1; }
command -v gpg >/dev/null || { echo "add-secret: falta gpg (paquete gnupg)" >&2; exit 1; }

# La clave tiene que estar declarada como secreto (en cualquiera de los dos ficheros). Sin esto
# se podria subir cualquier variable al almacen y fetch-secrets.sh no la materializaria nunca:
# quedaria guardada donde nadie la lee, con la falsa sensacion de estar protegida.
if ! grep -qE "^${CLAVE}\??$" "$KEYS_FILE" && ! grep -qE "^${CLAVE}\??$" "$KEYS_DB_FILE"; then
    echo "ERROR: $CLAVE no esta en secret-keys.txt ni en secret-keys-db.txt." >&2
    echo "Declararla ahi es parte de introducir el secreto: si no, no se materializa." >&2
    exit 1
fi

if [ -n "$DESDE_ENV" ]; then
    [ -f "$DESDE_ENV" ] || { echo "no existe $DESDE_ENV" >&2; exit 1; }
    VALOR="$(grep -m1 "^${CLAVE}=" "$DESDE_ENV" | cut -d= -f2- || true)"
fi
[ -n "${VALOR:-}" ] || { echo "ERROR: sin valor para $CLAVE (usa la variable VALOR o --desde-env)" >&2; exit 1; }

# Un valor con salto de linea se guarda sin protestar y rompe el almacen ENTERO en el
# siguiente despliegue: fetch-secrets.sh aborta al encontrarlo y ningun servicio recibe
# ninguna credencial. Se rechaza aqui, en la entrada, donde hay un solo sitio que mirar y
# quien lo ejecuta sabe aun de donde salio el valor. El caso real es un JSON de cuenta de
# servicio (Firebase) copiado tal cual: hay que compactarlo con `jq -c`.
case "$VALOR" in
  *"
"*)
    echo "ERROR: el valor de $CLAVE contiene un salto de linea." >&2
    echo "El almacen solo admite valores de una linea. Si es un JSON:" >&2
    echo "  VALOR=\"\$(jq -c . cuenta-servicio.json)\" ops/security/secrets/add-secret.sh $CLAVE --apply" >&2
    exit 1 ;;
esac

# Un fallo de LECTURA no es un almacen vacio. Tratarlos igual —frase incorrecta, un fichero
# corrompido, ejecutarlo antes de init-store.sh— convertiria la escritura de mas abajo en un
# reemplazo del almacen entero por una sola clave. Solo se parte de "{}" cuando el almacen
# todavia no existe.
if [ -f "$STORE_FILE" ]; then
    ACTUAL="$(store_leer_json)" || { echo "ERROR: no se pudo leer el almacen; no se escribe nada." >&2; exit 1; }
else
    ACTUAL='{}'
    echo ">> el almacen $STORE_FILE no existe todavia: $CLAVE seria la primera clave"
fi

NUEVO="$(CLAVE="$CLAVE" VALOR="$VALOR" ACTUAL="$ACTUAL" python3 - <<'PY'
import json, os
d = json.loads(os.environ["ACTUAL"] or "{}")
k = os.environ["CLAVE"]
existia = k in d
d[k] = os.environ["VALOR"]
print(json.dumps({"json": json.dumps(d), "existia": existia, "total": len(d)}))
PY
)"
EXISTIA="$(echo "$NUEVO" | python3 -c 'import sys,json; print(json.load(sys.stdin)["existia"])')"
TOTAL="$(echo "$NUEVO" | python3 -c 'import sys,json; print(json.load(sys.stdin)["total"])')"

echo "clave:   $CLAVE ($([ "$EXISTIA" = "True" ] && echo 'se REEMPLAZA la que ya estaba' || echo 'nueva'))"
echo "almacen: $STORE_FILE ($TOTAL claves tras el cambio)"
[ "$QUITAR_DEL_ENV" -eq 1 ] && [ -n "$DESDE_ENV" ] && echo "y se quita de $DESDE_ENV"

if [ "$APLICAR" -ne 1 ]; then
    echo
    echo "Nada escrito. Repite con --apply."
    exit 0
fi

echo "$NUEVO" | python3 -c 'import sys,json; sys.stdout.write(json.load(sys.stdin)["json"])' \
    | store_escribir_json
echo ">> guardada en el almacen"

# Se comprueba releyendo: que la clave este y que su valor sea el que se quiso poner. La
# comparacion ocurre dentro del proceso; no se imprime ninguno de los dos.
VERIF="$(store_leer_json | CLAVE="$CLAVE" VALOR="$VALOR" python3 -c '
import json, os, sys
d = json.load(sys.stdin)
print("ok" if d.get(os.environ["CLAVE"]) == os.environ["VALOR"] else "no coincide")')"
[ "$VERIF" = "ok" ] || { echo "ERROR: el almacen no devuelve el valor esperado; el .env NO se toca" >&2; exit 1; }
echo ">> verificada releyendo el almacen"

if [ "$QUITAR_DEL_ENV" -eq 1 ] && [ -n "$DESDE_ENV" ]; then
    # Solo despues de verificar: quitarla antes dejaria el servicio sin llave si la
    # escritura hubiera fallado a medias.
    tmp="$(mktemp)"
    grep -v "^${CLAVE}=" "$DESDE_ENV" > "$tmp"
    cat "$tmp" > "$DESDE_ENV"
    rm -f "$tmp"
    echo ">> retirada de $DESDE_ENV (el almacen es ya su unica fuente)"
fi

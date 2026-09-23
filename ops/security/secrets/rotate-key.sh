#!/usr/bin/env bash
# Rota una llave de cifrado del almacen de secretos SIN dejar ilegible lo ya cifrado.
#
# Una llave de cifrado no se cambia como una contrasena: lo guardado con la anterior
# tiene que seguir abriendose hasta que el servicio lo re-cifre. Por eso la rotacion son
# dos claves: la activa (cifra y descifra) y la lista de retiradas (solo descifran). Este
# script mueve la activa actual a la lista y pone una nueva; el servicio, al arrancar con
# llaves retiradas, re-cifra todo lo que encuentra y dice en el log cuando la lista ya
# se puede vaciar. Procedimiento completo: docs/RUNBOOK-ROTACION-CLAVE-COURIER.md.
#
# Uso, en el servidor:
#   ops/security/secrets/rotate-key.sh CARRIER_ENCRYPTION_KEY CARRIER_ENCRYPTION_KEYS_OLD           # simulacion
#   ops/security/secrets/rotate-key.sh CARRIER_ENCRYPTION_KEY CARRIER_ENCRYPTION_KEYS_OLD --apply   # rota
#
# Nunca imprime valores: ni la llave nueva, ni la actual, ni las retiradas. La escritura
# va por add-secret.sh, que verifica releyendo el almacen antes de dar por buena cada clave.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./store.sh
. "$SCRIPT_DIR/store.sh"
KEYS_FILE="${SECRET_KEYS_FILE:-$SCRIPT_DIR/secret-keys.txt}"

CLAVE_ACTIVA="${1:-}"
CLAVE_VIEJAS="${2:-}"
shift 2 2>/dev/null || true
APLICAR=0
while [ $# -gt 0 ]; do
    case "$1" in
        --apply) APLICAR=1; shift ;;
        *) echo "opcion desconocida: $1" >&2; exit 1 ;;
    esac
done

[ -n "$CLAVE_ACTIVA" ] && [ -n "$CLAVE_VIEJAS" ] \
    || { echo "uso: rotate-key.sh CLAVE_ACTIVA CLAVE_VIEJAS [--apply]" >&2; exit 1; }
[ "$CLAVE_ACTIVA" != "$CLAVE_VIEJAS" ] || { echo "ERROR: la activa y la lista de retiradas no pueden ser la misma clave" >&2; exit 1; }
store_requisitos || exit 1
command -v openssl >/dev/null || { echo "ERROR: falta openssl" >&2; exit 1; }
[ -f "$KEYS_FILE" ] || { echo "ERROR: no existe $KEYS_FILE" >&2; exit 1; }

# Las dos claves tienen que estar declaradas: add-secret.sh tambien lo exige, pero
# comprobarlo antes evita quedarse a medias con la lista escrita y la activa sin rotar.
for clave in "$CLAVE_ACTIVA" "$CLAVE_VIEJAS"; do
    grep -qE "^${clave}\??$" "$KEYS_FILE" \
        || { echo "ERROR: $clave no esta en secret-keys.txt; declararla es parte de introducir el secreto" >&2; exit 1; }
done

# El JSON del almacen no sale de este proceso. Solo se extraen las dos claves y se
# devuelve el estado como conteos, nunca los valores.
ACTUAL="$(store_leer_json)" || exit 1

ESTADO="$(ACTUAL="$ACTUAL" CLAVE_ACTIVA="$CLAVE_ACTIVA" CLAVE_VIEJAS="$CLAVE_VIEJAS" python3 - <<'PY'
import json, os
d = json.loads(os.environ["ACTUAL"] or "{}")
activa = str(d.get(os.environ["CLAVE_ACTIVA"], "")).strip()
viejas = [k.strip() for k in str(d.get(os.environ["CLAVE_VIEJAS"], "")).split(",") if k.strip()]
# La activa actual pasa al frente de la lista; si ya estaba (rotacion repetida a medias),
# no se duplica. La lista nueva conserva las anteriores: retirarlas es decision de quien
# ve el resumen del servicio, no de este script.
lista = [activa] + [k for k in viejas if k != activa] if activa else viejas
print(json.dumps({
    "hay_activa": bool(activa),
    "activa_valida": len(activa) == 64,
    "viejas_antes": len(viejas),
    "viejas_despues": len(lista),
    "lista": ",".join(lista),
}))
PY
)"
campo() { echo "$ESTADO" | python3 -c "import sys,json; print(json.load(sys.stdin)[\"$1\"])"; }

[ "$(campo hay_activa)" = "True" ] \
    || { echo "ERROR: $CLAVE_ACTIVA no tiene valor en el almacen; sin llave activa no hay nada que rotar (usa add-secret.sh para cargar la primera)" >&2; exit 1; }
[ "$(campo activa_valida)" = "True" ] \
    || { echo "ERROR: el valor actual de $CLAVE_ACTIVA no son 64 caracteres hexadecimales; revisar antes de rotar" >&2; exit 1; }

NUEVA="$(openssl rand -hex 32)"
[ "${#NUEVA}" -eq 64 ] || { echo "ERROR: openssl no devolvio una llave de 32 bytes" >&2; exit 1; }

echo "almacen:            $(store_descripcion)"
echo "llave activa:       $CLAVE_ACTIVA -> se genera una nueva de 32 bytes"
echo "llaves retiradas:   $CLAVE_VIEJAS -> $(campo viejas_antes) antes, $(campo viejas_despues) despues (la activa actual pasa a la lista)"

if [ "$APLICAR" -ne 1 ]; then
    echo
    echo "Nada escrito. Repite con --apply."
    exit 0
fi

# Orden a proposito: primero la lista, despues la activa. Si la segunda escritura falla,
# la activa sigue siendo la de siempre y la lista solo lleva una copia redundante de
# ella, que no rompe nada. Al reves, un fallo dejaria una activa nueva sin la vieja en
# la lista: todo lo guardado quedaria ilegible hasta arreglarlo a mano.
echo
echo ">> escribiendo $CLAVE_VIEJAS"
VALOR="$(campo lista)" "$SCRIPT_DIR/add-secret.sh" "$CLAVE_VIEJAS" --apply
echo
echo ">> escribiendo $CLAVE_ACTIVA"
VALOR="$NUEVA" "$SCRIPT_DIR/add-secret.sh" "$CLAVE_ACTIVA" --apply
unset NUEVA

cat <<FIN

Rotacion cargada en el almacen. Lo que sigue:

  1. Desde el PC, volver a desplegar carrier-integration con scripts/deploy-ecr.sh
     (recrear el contenedor es lo que hace que lea las llaves nuevas; reiniciarlo no basta).
  2. Vigilar en el servidor la linea de resumen, aparece medio minuto despues de arrancar:
       docker logs -f app-carrier-integration-1 2>&1 | grep 'rotacion de credenciales del courier'
     Termina con "N re-cifradas, M ilegibles". Con M=0 y sin empresas con error, la
     llave vieja ya no abre nada que no este tambien bajo la nueva.
  3. Solo entonces, vaciar la lista de retiradas:
       VALOR=' ' ops/security/secrets/add-secret.sh $CLAVE_VIEJAS --apply
     y volver a desplegar carrier-integration. Con M>0 NO se vacia: esas credenciales
     siguen dependiendo de la lista. Detalle en docs/RUNBOOK-ROTACION-CLAVE-COURIER.md.
FIN

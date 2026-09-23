#!/usr/bin/env bash
# Pasa el almacen de secretos del fichero gpg a OpenBao, o de vuelta (docs/adr/0011).
#
#   ops/security/openbao/migrar.sh                    # simulacion: compara, no escribe
#   ops/security/openbao/migrar.sh --apply            # gpg -> OpenBao
#   ops/security/openbao/migrar.sh --volver-a-gpg [--apply]   # OpenBao -> gpg
#
# Ninguna direccion cambia de backend hasta demostrar que el destino materializa EXACTAMENTE los
# mismos ficheros que el origen: fetch-secrets.sh se ejecuta contra los dos, a ficheros
# temporales en memoria, y se comparan byte a byte. Solo entonces se escribe `backend` junto a la
# frase, que es lo que store.sh lee. Nunca imprime un valor.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SECRETS_SCRIPTS="$(cd "$DIR/../secrets" && pwd)"
unset SECRETS_BACKEND
# shellcheck source=../secrets/store.sh
. "$SECRETS_SCRIPTS/store.sh"

DESTINO=openbao
APLICAR=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --apply) APLICAR=1; shift ;;
    --volver-a-gpg) DESTINO=gpg; shift ;;
    *) echo "opcion desconocida: $1" >&2; exit 1 ;;
  esac
done
ORIGEN=$([[ $DESTINO == openbao ]] && echo gpg || echo openbao)
FICHERO_BACKEND="$STORE_DIR/backend"

die() { echo "migrar: $*" >&2; exit 1; }

actual="$STORE_BACKEND"
[[ "$actual" != "$DESTINO" ]] || { echo "migrar: el almacen ya es $DESTINO; nada que hacer"; exit 0; }
[[ "$actual" == "$ORIGEN" ]] || die "el almacen es '$actual', no $ORIGEN"

STORE_BACKEND=gpg store_requisitos
python3 "$STORE_OPENBAO" salud >/dev/null || die "OpenBao no esta operativo (ops/security/openbao/instalar.sh)"

STORE_BACKEND="$ORIGEN"
json_origen="$(store_leer_json)" || die "no se pudo leer el origen ($ORIGEN)"
STORE_BACKEND="$DESTINO"
json_destino="$(store_leer_json --permitir-vacio)" || die "no se pudo leer el destino ($DESTINO)"

resumen="$(ORIGEN_JSON="$json_origen" DESTINO_JSON="$json_destino" python3 -c '
import json, os
o = json.loads(os.environ["ORIGEN_JSON"]); d = json.loads(os.environ["DESTINO_JSON"] or "{}")
distintas = sorted(k for k in set(o) | set(d) if o.get(k) != d.get(k))
print(f"{len(o)} {len(d)} {len(distintas)}")
')"
read -r n_origen n_destino n_distintas <<<"$resumen"
echo "migrar: $ORIGEN tiene $n_origen claves; $DESTINO tiene $n_destino; difieren $n_distintas"

if [[ $APLICAR -ne 1 ]]; then
  echo "Simulacion: nada escrito. Repite con --apply."
  exit 0
fi

if [[ "$n_distintas" -ne 0 ]]; then
  STORE_BACKEND="$DESTINO"
  printf '%s' "$json_origen" | store_escribir_json || die "no se pudo escribir en $DESTINO"
  echo "migrar: $n_origen claves escritas en $DESTINO y releidas"
fi
unset json_origen json_destino

# La prueba que decide: los dos backends materializan los mismos ficheros.
tmp="$(mktemp -d /dev/shm/cf-migrar.XXXXXX)"
trap 'find "$tmp" -type f -exec shred -u {} + 2>/dev/null; rm -rf "$tmp"' EXIT
for b in "$ORIGEN" "$DESTINO"; do
  SECRETS_BACKEND="$b" SECRETS_ENV_FILE="$tmp/$b.env" SECRETS_DB_ENV_FILE="$tmp/$b-db.env" \
    "$SECRETS_SCRIPTS/fetch-secrets.sh" >/dev/null || die "fetch-secrets.sh fallo contra $b"
done
cmp -s "$tmp/$ORIGEN.env" "$tmp/$DESTINO.env" && cmp -s "$tmp/$ORIGEN-db.env" "$tmp/$DESTINO-db.env" ||
  die "$ORIGEN y $DESTINO materializan ficheros distintos; no se cambia de backend"
echo "migrar: $ORIGEN y $DESTINO materializan los mismos secretos"

printf '%s\n' "$DESTINO" >"$FICHERO_BACKEND.tmp"
chmod 600 "$FICHERO_BACKEND.tmp"
mv -f "$FICHERO_BACKEND.tmp" "$FICHERO_BACKEND"
echo "migrar: el almacen es ahora $DESTINO ($FICHERO_BACKEND)"

if [[ "$DESTINO" == openbao && -f "$STORE_FILE" ]]; then
  # El fichero gpg deja de ser fuente: si se quedara con su nombre, un SECRETS_BACKEND=gpg suelto
  # desplegaria secretos que ya no son los vigentes. El camino de vuelta lo regenera desde OpenBao.
  mv -f "$STORE_FILE" "$STORE_FILE.pre-openbao"
  echo "migrar: $STORE_FILE queda como $STORE_FILE.pre-openbao; borralo (shred -u) cuando OpenBao"
  echo "        lleve unos dias en marcha y su instantanea este en el respaldo"
fi

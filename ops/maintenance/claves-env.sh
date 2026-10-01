#!/usr/bin/env bash
# Avisa de las claves de configuracion que el .env de un servidor no tiene.
#
#   <nombres de claves, uno por linea> | ops/maintenance/claves-env.sh <fichero .env> \
#       [--excluir <fichero de nombres>]... [--nuevas "<CLAVE CLAVE ...>"]
#
# Por que existe: el .env de un servidor se monta una vez y .env.example sigue creciendo. Una
# clave nueva sin la que un servicio no arranca (SCHEDULER_URL para analytics, por ejemplo) falta
# en silencio hasta que se recrea ese servicio. Solo se comparan NOMBRES: no se lee ni se imprime
# ningun valor. No bloquea, porque un .env montado a mano puede omitir a proposito claves
# opcionales; si el servicio no arranca, lo para esperar-sanos.sh con la causa.
#
# --excluir quita las claves de un fichero de nombres (uno por linea, # comenta, un "?" final se
# ignora): los secretos de ops/security/secrets/secret-keys*.txt viven en el almacen, nunca en el
# .env, y contarlos como ausentes es ruido.
# --nuevas nombra las claves que aparecieron en .env.example desde lo que corre en el servidor.
# Con ella solo esas se listan, que son las que pueden faltar sin que nadie lo sepa; las demas
# ausentes llevan meses asi y usan su valor por defecto, y van en una linea de resumen. Con 186
# nombres en la lista (2026-10-01) una clave nueva y obligatoria no se veia.
set -euo pipefail

ENV_FILE="${1:?uso: $0 <fichero .env> [--excluir <fichero>]... [--nuevas \"<claves>\"] (nombres por la entrada estandar)}"
shift
[[ -f "$ENV_FILE" ]] || { echo "claves-env: no existe $ENV_FILE" >&2; exit 2; }

excluidas=""
nuevas=""
con_nuevas=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --excluir)
      [[ -f "${2:-}" ]] || { echo "claves-env: --excluir necesita un fichero que exista" >&2; exit 2; }
      excluidas+="$(sed -E 's/#.*//; s/[?[:space:]]+$//; s/^[[:space:]]+//' "$2" | grep -E '^[A-Z][A-Z0-9_]*$' || true)"$'\n'
      shift 2 ;;
    --nuevas)
      [[ $# -ge 2 ]] || { echo "claves-env: --nuevas necesita la lista de claves" >&2; exit 2; }
      nuevas="$(tr ' ' '\n' <<<"$2" | grep -E '^[A-Z][A-Z0-9_]*$' || true)"
      con_nuevas=1
      shift 2 ;;
    *) echo "claves-env: opcion desconocida '$1'" >&2; exit 2 ;;
  esac
done

ordenar() { grep -E '^[A-Z][A-Z0-9_]*$' | LC_ALL=C sort -u || true; }
presentes="$(sed -n -E 's/^[[:space:]]*([A-Z][A-Z0-9_]*)=.*/\1/p' "$ENV_FILE" | ordenar)"
faltan="$(ordenar | LC_ALL=C comm -23 - <(printf '%s\n' "$presentes") |
  LC_ALL=C comm -23 - <(printf '%s' "$excluidas" | ordenar))"

if [[ $con_nuevas -eq 1 ]]; then
  urgentes="$(LC_ALL=C comm -12 <(printf '%s\n' "$faltan" | ordenar) <(printf '%s\n' "$nuevas" | ordenar))"
  resto="$(LC_ALL=C comm -23 <(printf '%s\n' "$faltan" | ordenar) <(printf '%s\n' "$nuevas" | ordenar))"
else
  urgentes="$faltan"
  resto=""
fi

if [[ -n "$urgentes" ]]; then
  if [[ $con_nuevas -eq 1 ]]; then
    echo "AVISO: claves NUEVAS de .env.example que $ENV_FILE no tiene ($(wc -l <<<"$urgentes")):" >&2
  else
    echo "AVISO: claves de .env.example que $ENV_FILE no tiene ($(wc -l <<<"$urgentes")):" >&2
  fi
  sed 's/^/  /' <<<"$urgentes" >&2
  echo "  Un servicio que las necesite no arrancara: anadelas con su valor, o vacias si son opcionales." >&2
fi
if [[ -n "$resto" ]]; then
  echo "claves-env: otras $(wc -l <<<"$resto") claves de .env.example no estan en $ENV_FILE desde antes del ultimo despliegue (usan su valor por defecto)"
fi
[[ -z "$urgentes" && -z "$resto" ]] && echo "claves-env: $ENV_FILE tiene todas las claves esperadas"
exit 0

#!/usr/bin/env bash
# Avisa de las claves de configuracion que el .env de un servidor no tiene.
#
#   <nombres de claves, uno por linea> | ops/maintenance/claves-env.sh <fichero .env>
#
# Por que existe: el .env de un servidor se monta una vez y .env.example sigue creciendo. Una
# clave nueva sin la que un servicio no arranca (SCHEDULER_URL para analytics, por ejemplo) falta
# en silencio hasta que se recrea ese servicio. Solo se comparan NOMBRES: no se lee ni se imprime
# ningun valor. No bloquea, porque un .env montado a mano puede omitir a proposito claves
# opcionales; si el servicio no arranca, lo para esperar-sanos.sh con la causa.
set -euo pipefail

ENV_FILE="${1:?uso: $0 <fichero .env> (nombres de claves por la entrada estandar)}"
[[ -f "$ENV_FILE" ]] || { echo "claves-env: no existe $ENV_FILE" >&2; exit 2; }

presentes="$(sed -n -E 's/^[[:space:]]*([A-Z][A-Z0-9_]*)=.*/\1/p' "$ENV_FILE" | LC_ALL=C sort -u)"
faltan="$({ grep -E '^[A-Z][A-Z0-9_]*$' || true; } | LC_ALL=C sort -u | LC_ALL=C comm -23 - <(printf '%s\n' "$presentes"))"
if [[ -n "$faltan" ]]; then
  echo "AVISO: claves de .env.example que $ENV_FILE no tiene ($(wc -l <<<"$faltan")):" >&2
  sed 's/^/  /' <<<"$faltan" >&2
  echo "  Un servicio que las necesite no arrancara: anadelas con su valor, o vacias si son opcionales." >&2
else
  echo "claves-env: $ENV_FILE tiene todas las claves esperadas"
fi
exit 0

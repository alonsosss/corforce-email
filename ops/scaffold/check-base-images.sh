#!/usr/bin/env bash
# Ninguna imagen puede depender de una etiqueta flotante.
#
# `:latest` (o una imagen sin etiqueta) significa que el runtime puede cambiar por
# debajo entre dos despliegues sin que nadie lo haya revisado. En pgbouncer eso es el
# pooler de la base de datos; en los motores de correo, el propio Postfix. Un cambio
# ahi no se ve como cambio: se ve como una falla inexplicable.
#
# Se exige etiqueta concreta en los FROM y digest o etiqueta concreta en el compose.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
FAIL=0

echo "== Imagenes base sin fijar =="

while IFS= read -r line; do
  file="${line%%:*}"; rest="${line#*:}"; num="${rest%%:*}"; text="${rest#*:}"
  img="$(awk '{print $2}' <<<"$text")"
  # Referencias a etapas del propio Dockerfile (AS builder) no son imagenes externas.
  [[ "$img" == "scratch" ]] && continue
  grep -qE "AS[[:space:]]+$img\b" "$ROOT/$file" 2>/dev/null && continue
  if [[ "$img" == *:latest || "$img" != *:* && "$img" != *@sha256:* ]]; then
    echo "  FALLA: $file:$num usa '$img' (etiqueta flotante)"; FAIL=1
  fi
done < <(grep -rn "^FROM " --include=Dockerfile "$ROOT/services" "$ROOT/web" "$ROOT/ops" 2>/dev/null \
         | sed "s|^$ROOT/||")

while IFS= read -r line; do
  file="${line%%:*}"; rest="${line#*:}"; num="${rest%%:*}"; text="${rest#*:}"
  img="$(sed -E 's/^[[:space:]]*image:[[:space:]]*//; s/[[:space:]]*$//' <<<"$text")"
  # Imagenes construidas por el propio compose (build:) no llevan etiqueta externa.
  [[ "$img" == *'${'* ]] && continue
  if [[ "$img" == *:latest || ( "$img" != *:* && "$img" != *@sha256:* ) ]]; then
    echo "  FALLA: $file:$num usa '$img' (etiqueta flotante)"; FAIL=1
  fi
done < <(grep -rn "^[[:space:]]*image:[[:space:]]" "$ROOT"/docker-compose*.yml 2>/dev/null | sed "s|^$ROOT/||")

[[ $FAIL -eq 0 ]] && echo "  OK: todas las imagenes estan fijadas a una version concreta."
exit $FAIL

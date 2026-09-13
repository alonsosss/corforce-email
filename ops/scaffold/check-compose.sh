#!/usr/bin/env bash
# Valida la estructura de los ficheros de compose del repositorio con el propio parser de
# docker compose. Existe porque un bloque de servicio insertado en medio de otro dejo
# docker-compose.yml sin ser YAML valido y ningun otro check lo lee: el e2e arranca
# binarios, no compose.
#
# No arranca nada ni lee secretos: interpola contra un .env vacio en una carpeta temporal.
# Sin docker (CI sin daemon, maquina sin compose) avisa y no falla: make checks debe poder
# correr sin docker, y la validacion vuelve a correr donde si lo haya.
#
# Un override que no se sostiene solo declara su base con una linea
# `# compose-base: <fichero de la misma carpeta>` y se valida junto a ella, en ese orden.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

if ! docker compose version >/dev/null 2>&1; then
  echo "AVISO: docker compose no disponible; no se validan los ficheros de compose" >&2
  exit 0
fi

mapfile -t FICHEROS < <(git ls-files --cached --others --exclude-standard -- 'docker-compose*.yml' 'deploy/**/docker-compose*.yml')
fallos=0
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
for f in "${FICHEROS[@]}"; do
  dir="$tmp/$(dirname "$f")"
  mkdir -p "$dir"
  cp "$f" "$dir/"
  : > "$dir/.env"
  args=(-f "$(basename "$f")")
  base=$(sed -n 's/^# compose-base: *\([^ ]*\) *$/\1/p' "$f" | head -1)
  if [[ -n "$base" ]]; then
    if [[ ! -f "$(dirname "$f")/$base" ]]; then
      echo "  FALLA: $f: su base $base no existe en $(dirname "$f")" >&2
      fallos=$((fallos + 1))
      continue
    fi
    cp "$(dirname "$f")/$base" "$dir/"
    args=(-f "$base" -f "$(basename "$f")")
  fi
  # Las variables marcadas como obligatorias (${VAR:?...}) son configuracion o secretos de
  # produccion: se rellenan con un valor de relleno para validar la estructura, no su valor.
  ok=0
  for _ in $(seq 1 60); do
    if out=$(cd "$dir" && docker compose "${args[@]}" config -q 2>&1); then ok=1; break; fi
    var=$(printf '%s' "$out" | sed -n 's/.*required variable \([A-Za-z_][A-Za-z0-9_]*\) is missing a value.*/\1/p' | head -1)
    [[ -n "$var" ]] || break
    # Compose valida la forma de algunos valores: un puerto tiene que ser un numero y el
    # origen de un bind mount una ruta (sin barra lo lee como el nombre de un volumen).
    case "$var" in
      *PORT*) valor=1 ;;
      *_DIR | *_ROOT) valor=/validacion-estructural ;;
      *) valor=validacion-estructural ;;
    esac
    echo "$var=$valor" >> "$dir/.env"
  done
  if [[ $ok -eq 1 ]]; then
    echo "  OK: $f"
  else
    echo "  FALLA: $f: $out" >&2
    fallos=$((fallos + 1))
  fi
done

if [[ $fallos -gt 0 ]]; then
  echo "compose: $fallos fichero(s) no validos" >&2
  exit 1
fi
echo "compose: ${#FICHEROS[@]} fichero(s) validos"

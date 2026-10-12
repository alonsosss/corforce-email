#!/usr/bin/env bash
# Avisa de lo que esta en main y no en produccion, sin que nadie tenga que recordarlo.
#
# El estado de produccion solo se puede leer del servidor (scripts/estado-produccion.sh), y desde alli
# solo llega esta maquina: GitHub no tiene la clave de despliegue ni debe tenerla. Por eso esto corre
# aqui, por un temporizador de usuario (ops/maintenance/systemd), y deja el resultado donde si se ve:
# una incidencia `pendiente-despliegue`, asignada, que se actualiza mientras quede algo y se cierra
# sola cuando produccion coincide con main. Solo lee del servidor; no despliega nada.
#
# Se evalua origin/main en un worktree propio: el arbol de trabajo (con sus cambios a medias o los de
# otra sesion) no cuenta ni se toca.
#
#   scripts/aviso-pendiente.sh              # actualiza la incidencia
#   AVISO_SIN_INCIDENCIA=1 scripts/aviso-pendiente.sh   # solo imprime el estado
#
# Sale 0 si lo hizo (haya o no pendiente) y 1 si no pudo averiguar el estado (servidor inalcanzable):
# en ese caso no toca la incidencia, porque no sabe.
set -uo pipefail

ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"; cd "$ROOT"
ETIQUETA="pendiente-despliegue"
RESPONSABLE="${MANTENIMIENTO_RESPONSABLE:-$(gh repo view --json owner --jq .owner.login 2>/dev/null || true)}"

git fetch -q origin main || { echo "aviso-pendiente: no se pudo actualizar origin/main" >&2; exit 1; }
SHA="$(git rev-parse origin/main)"
TMP="$(mktemp -d)"; trap 'git worktree remove --force "$TMP/wt" >/dev/null 2>&1; rm -rf "$TMP"' EXIT
git worktree add -q --detach "$TMP/wt" "$SHA" || exit 1
[[ -f "$ROOT/destino.env.local" ]] && cp "$ROOT/destino.env.local" "$TMP/wt/"

SALIDA="$TMP/estado.txt"
( cd "$TMP/wt" && DEPLOY_PROFILE=selfhosted scripts/estado-produccion.sh ) >"$SALIDA" 2>&1
RC=$?
cat "$SALIDA"
[[ "${AVISO_SIN_INCIDENCIA:-0}" == 1 ]] && exit $(( RC == 1 ? 1 : 0 ))
if [[ $RC -eq 1 ]]; then
  echo "aviso-pendiente: no se pudo averiguar el estado de produccion; la incidencia queda como estaba" >&2
  exit 1
fi

numero="$(gh issue list --state open --label "$ETIQUETA" --json number --jq '.[0].number // empty')"
if [[ $RC -eq 0 ]]; then
  [[ -n "$numero" ]] && gh issue close "$numero" --comment "Produccion coincide con main (${SHA:0:8})."
  exit 0
fi

{
  printf 'Hay cambios en `main` (`%s`) que no estan en produccion. Orden: migraciones, `scripts/deploy-ecr.sh`, `scripts/deploy-mail.sh` (un motor a la vez), con la CI en verde.\n\n' "${SHA:0:8}"
  printf 'Lo genera `scripts/aviso-pendiente.sh` cada lunes y cuando se ejecuta a mano; no despliega nada.\n\n```\n'
  head -c 55000 "$SALIDA"
  printf '\n```\n'
} >"$TMP/cuerpo.md"

gh label create "$ETIQUETA" --color fbca04 --description "main tiene cambios que no estan en produccion" 2>/dev/null || true
if [[ -n "$numero" ]]; then
  gh issue edit "$numero" --body-file "$TMP/cuerpo.md" >/dev/null
else
  numero="$(gh issue create --title "Cambios de main pendientes de desplegar" --label "$ETIQUETA" --body-file "$TMP/cuerpo.md" | sed 's|.*/||')"
  [[ -n "$RESPONSABLE" ]] && { gh issue edit "$numero" --add-assignee "$RESPONSABLE" >/dev/null || echo "aviso-pendiente: no se pudo asignar la incidencia $numero a $RESPONSABLE" >&2; }
fi
echo "aviso-pendiente: incidencia #$numero actualizada"

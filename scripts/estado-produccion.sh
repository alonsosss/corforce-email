#!/usr/bin/env bash
# Que corre en produccion y que falta por desplegar, leido del propio servidor.
#
# El estado de produccion no puede depender de que alguien lo recuerde: los despliegues los lanzan
# personas y sesiones distintas, y cada aviso de "esto queda pendiente" se perdia con quien lo sabia.
# El servidor ya guarda la verdad (.deployed-tag de la plataforma, .deployed-tags de cada motor y el
# historial .deploy-log); esto la lee y la compara con el repositorio. Solo lee: no compila, no toma
# el candado y no escribe nada, ni aqui ni en el servidor.
#
# Uso:
#   scripts/estado-produccion.sh                # con el mismo destino que los despliegues
#   ESTADO_SIN_MOTORES=1 scripts/estado-produccion.sh   # sin docker local: omite el calculo de motores
#
# Destino: DEPLOY_HOST, DEPLOY_USER, DEPLOY_SSH_KEY, DEPLOY_PATH y MAIL_DEPLOY_PATH, como
# scripts/deploy-ecr.sh y scripts/deploy-mail.sh, cuya propia decision se reutiliza (DEPLOY_PLAN=1):
# lo que aqui sale como pendiente es exactamente lo que harian ellos.
#
# Compara HEAD con el servidor. Lancese desde main al dia (git switch main && git pull): si HEAD no es
# origin/main se avisa, porque entonces el plan describe otro commit que el que se desplegaria.
#
# Sale 0 si no queda nada por desplegar, 2 si queda algo y 1 si no se pudo averiguar.
set -uo pipefail

ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"; cd "$ROOT"
# shellcheck source-path=SCRIPTDIR source=lib/despliegue.sh
. "$ROOT/scripts/lib/despliegue.sh"
MAIL_DEPLOY_PATH="${MAIL_DEPLOY_PATH:-/opt/core-force-mail/mail-src}"

titulo() { printf '\n== %s\n' "$*"; }
pendiente=0

despliegue_comprobar_conexion || exit 1

git fetch -q origin 2>/dev/null || echo "aviso: no se pudo actualizar origin; se compara con lo que hay en local" >&2
HEAD_SHA="$(git rev-parse HEAD)"
MAIN_SHA="$(git rev-parse -q --verify origin/main || true)"
if [[ -n "$MAIN_SHA" && "$HEAD_SHA" != "$MAIN_SHA" ]]; then
  echo "aviso: HEAD (${HEAD_SHA:0:9}) no es origin/main (${MAIN_SHA:0:9}): el estado se calcula para HEAD" >&2
fi

titulo "Plataforma"
BASE="$("${SSH[@]}" "cat $DEPLOY_PATH/.deployed-tag 2>/dev/null" || true)"
if [[ -z "$BASE" ]] || ! es_commit "$BASE"; then
  echo "el servidor no tiene un .deployed-tag que sea un commit de este repositorio ('${BASE:-vacio}')" >&2
  echo "  sin el no se puede calcular que falta; el primer despliegue con scripts/deploy-ecr.sh lo escribe" >&2
  exit 1
fi
printf 'en produccion: %s (%s)   repositorio: %s (%s)\n' \
  "${BASE:0:9}" "$(git log -1 --format=%cs "$BASE")" "${HEAD_SHA:0:9}" "$(git log -1 --format=%cs HEAD)"

if ! git merge-base --is-ancestor "$BASE" HEAD; then
  echo "AVISO: lo que corre (${BASE:0:9}) no es un antecesor de HEAD: o se desplego desde otra rama, o HEAD esta atrasado" >&2
fi

titulo "Commits sin desplegar en la plataforma"
n="$(git rev-list --count "$BASE"..HEAD)"
if [[ "$n" -eq 0 ]]; then
  echo "ninguno"
else
  pendiente=1
  echo "$n commit(s):"
  git log --format='  %h %cs %s' "$BASE"..HEAD | head -40
  [[ "$n" -gt 40 ]] && echo "  ... y $((n - 40)) mas"
fi

# Las migraciones no se registran en ninguna tabla: son idempotentes y se aplican por capa con
# ops/db/apply-migration.sh y el barrido. Lo que falta es lo que cambio desde el commit que corre, y
# va ANTES que los servicios que lo usan: un servicio nuevo sobre un esquema viejo responde 500.
titulo "Migraciones por aplicar (desde ${BASE:0:9}), antes que los servicios"
for capa in registry cell tenant; do
  case "$capa" in
    registry) nombre="registro" ;; cell) nombre="celda" ;; tenant) nombre="empresa" ;;
  esac
  mapfile -t nuevas < <(git diff --name-only --diff-filter=AR "$BASE" HEAD -- "migrations/$capa" | grep '\.sql$')
  mapfile -t tocadas < <(git diff --name-only --diff-filter=M "$BASE" HEAD -- "migrations/$capa" | grep '\.sql$')
  if [[ ${#nuevas[@]} -eq 0 && ${#tocadas[@]} -eq 0 ]]; then
    printf '%-9s ninguna\n' "$nombre"
    continue
  fi
  pendiente=1
  printf '%-9s\n' "$nombre"
  for f in "${nuevas[@]}"; do printf '  nueva      %s\n' "$f"; done
  # Las migraciones son aditivas: una ya publicada no se edita. Si aparece, alguien tiene que mirarla.
  for f in "${tocadas[@]}"; do printf '  MODIFICADA %s  (una migracion publicada no se edita: revisarla)\n' "$f"; done
done

titulo "Servicios (lo que haria scripts/deploy-ecr.sh)"
if salida="$(DEPLOY_PLAN=1 "$ROOT/scripts/deploy-ecr.sh" 2>&1)"; then
  printf '%s\n' "$salida" | grep -E '^(servicios|ficheros del servidor):'
  grep -q '^servicios: ninguno$' <<<"$salida" || pendiente=1
  grep -q '^ficheros del servidor: con cambios' <<<"$salida" && pendiente=1
else
  printf '%s\n' "$salida" | tail -5 >&2
  echo "no se pudo calcular el plan de la plataforma" >&2
  exit 1
fi

titulo "Motores (lo que haria scripts/deploy-mail.sh, de uno en uno)"
if [[ "${ESTADO_SIN_MOTORES:-0}" == 1 ]]; then
  echo "omitido (ESTADO_SIN_MOTORES=1)"
elif ! docker info >/dev/null 2>&1; then
  echo "omitido: el calculo lee deploy/mail con docker compose y aqui no hay docker"
  echo "  (sin el, compare a mano $MAIL_DEPLOY_PATH/.deployed-tags del servidor con git log -- deploy/mail)"
elif salida="$(MAIL_DEPLOY_PATH="$MAIL_DEPLOY_PATH" DEPLOY_PLAN=1 "$ROOT/scripts/deploy-mail.sh" 2>&1)"; then
  printf '%s\n' "$salida" | grep -vE '^>>'
  grep -q '^motores: ninguno$' <<<"$salida" || pendiente=1
else
  printf '%s\n' "$salida" | tail -5 >&2
  echo "no se pudo calcular el plan de los motores" >&2
  exit 1
fi

titulo "Integracion continua de ${HEAD_SHA:0:9}"
if ! command -v gh >/dev/null 2>&1; then
  echo "sin gh: compruebe a mano que CI, Motores y Release estan en verde antes de desplegar"
elif ! runs="$(gh run list --commit "$HEAD_SHA" --json workflowName,status,conclusion 2>/dev/null)"; then
  echo "gh no pudo consultar las ejecuciones (gh auth login)"
else
  RUNS="$runs" python3 - <<'PY'
import json, os
runs = json.loads(os.environ["RUNS"] or "[]")
if not runs:
    print("ninguna ejecucion para este commit")
for r in sorted(runs, key=lambda r: r["workflowName"]):
    estado = r["conclusion"] or r["status"]
    print("  %-40s %s" % (r["workflowName"], estado))
PY
fi

titulo "Ultimos despliegues (.deploy-log del servidor)"
historial="$("${SSH[@]}" "cat $DEPLOY_PATH/.deploy-log $MAIL_DEPLOY_PATH/.deploy-log 2>/dev/null" || true)"
if [[ -z "$historial" ]]; then
  echo "sin historial: el servidor aun no registro ningun despliegue con esta version de los guiones"
else
  sort <<<"$historial" | tail -10 | awk -F'\t' '{ printf "  %s  %-10s %-9s %s  (%s)\n", $1, $2, $3, $4, $5 }'
fi

titulo "Veredicto"
if [[ "$pendiente" -eq 0 ]]; then
  echo "produccion esta al dia con ${HEAD_SHA:0:9}"
  exit 0
fi
echo "queda trabajo por desplegar. Orden: migraciones (registro, celda, empresa), despues"
echo "scripts/deploy-ecr.sh y despues scripts/deploy-mail.sh (un motor a la vez), con la CI en verde"
echo "y, si hay motores, make e2e-mail (docs/Operacion_Despliegue.md, Mantenimiento)."
exit 2

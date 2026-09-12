#!/usr/bin/env bash
# Retira del SERVIDOR las copias locales antiguas de las imagenes de core-force.
#
# Por que existe: la politica de ciclo de vida de ECR (ops/ecr/lifecycle-policy.json) limpia
# el registro en AWS, pero no sabe nada del disco de la maquina. Cada despliegue hace un
# pull y esa copia se queda en /var/lib/docker para siempre. Con ciento y pico servicios y
# varios despliegues al dia, el disco se llena; y cuando se llena no falla el despliegue:
# falla Postgres, que es mucho peor.
#
# Mismo criterio que ya se decidio para ECR -conservar las ultimas, borrar el resto- pero
# aplicado donde ECR no llega.
#
# Uso:
#   ops/ecr/prune-local-images.sh              # en seco: dice que borraria
#   ops/ecr/prune-local-images.sh --apply      # borra
#   CONSERVAR=5 ops/ecr/prune-local-images.sh --apply
set -euo pipefail

CONSERVAR="${CONSERVAR:-3}"
APLICAR=0
[ "${1:-}" = "--apply" ] && APLICAR=1

# El registro se toma del entorno si esta, para no fijar aqui la cuenta de AWS.
REGISTRO="${ECR_REGISTRY:-}"
PATRON="core-force-mail/"
[ -n "$REGISTRO" ] && PATRON="$REGISTRO/core-force-mail/"

# Imagenes que un contenedor esta usando AHORA. Docker ya se niega a borrarlas, pero
# saltarselas explicitamente evita un muro de errores en la salida que esconde lo util.
mapfile -t EN_USO < <(docker ps -a --format '{{.Image}}' | sort -u)
esta_en_uso() {
  local img="$1"
  for u in "${EN_USO[@]}"; do [ "$u" = "$img" ] && return 0; done
  return 1
}

# UNA sola llamada a docker y una pasada.
#
# La version anterior preguntaba repositorio por repositorio: con ciento y pico servicios
# eran ciento y pico invocaciones sobre una maquina de dos nucleos, y tardaba minutos. Aqui
# se pide todo de golpe y se agrupa en awk, que ademas deja el orden por fecha ya resuelto.
#
# El orden importa: se conservan las mas NUEVAS, que son las candidatas a un retroceso con
# DEPLOY_TAG=<sha-anterior>.
mapfile -t CANDIDATAS < <(
  docker images --format '{{.CreatedAt}}\t{{.Repository}}\t{{.Repository}}:{{.Tag}}' \
    | grep -F "$PATRON" \
    | grep -v ':latest' \
    | grep -v '<none>' \
    | sort -r \
    | awk -v n="$CONSERVAR" -F'\t' '{ vistas[$2]++; if (vistas[$2] > n) print $3 }'
)

total_borrables=0
declare -A REPOS_TOCADOS=()

for img in "${CANDIDATAS[@]}"; do
  if esta_en_uso "$img"; then continue; fi
  REPOS_TOCADOS["${img%%:*}"]=1
  total_borrables=$((total_borrables + 1))
  if [ "$APLICAR" = "1" ]; then
    docker rmi "$img" >/dev/null 2>&1 || true
  else
    echo "  borraria  $img"
  fi
done

total_repos=${#REPOS_TOCADOS[@]}

if [ "$APLICAR" = "1" ]; then
  echo ">> retiradas $total_borrables copias locales de $total_repos repositorios (se conservan las $CONSERVAR ultimas de cada uno)"
  df -h /var/lib/docker 2>/dev/null | tail -1 | awk '{print ">> disco: "$3" usados de "$2" ("$5")"}'
else
  echo ">> en seco: $total_borrables copias de $total_repos repositorios (se conservarian las $CONSERVAR ultimas)"
  echo ">> repite con --apply para borrarlas"
fi

#!/usr/bin/env bash
# Imagen propia del almacen S3 del perfil, el servicio minio (selfhosted/minio/imagen/Dockerfile: Silo,
# la bifurcacion mantenida de MinIO, compilada desde su codigo fuente; docs/adr/0016).
#
#   scripts/imagen-minio.sh --referencia   # core-force-mail/minio:silo-<release>-<hash>, sin docker
#   scripts/imagen-minio.sh --construir    # la construye en esta maquina si no la tiene ya
#
# La etiqueta es la version de Silo que fija el Dockerfile mas los 12 primeros hex del sha256 del
# propio Dockerfile: cualquier cambio de la receta (version, commit, base, Go) es otra etiqueta, asi que
# un servidor que ya tiene una etiqueta tiene exactamente esa imagen y el despliegue sabe cuando enviar
# otra. ops/scaffold/check-selfhosted-profile.sh exige que los compose referencien esta etiqueta.
#
# Se construye con el Dockerfile por la entrada estandar, sin contexto: la imagen no depende de ningun
# otro fichero del repositorio. Solo en el puesto de trabajo o en la CI: el servidor nunca compila
# (scripts/deploy-ecr.sh la envia con docker save | ssh docker load, y los compose la piden con
# pull_policy: never).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DOCKERFILE="$ROOT/selfhosted/minio/imagen/Dockerfile"
REPOSITORIO=core-force-mail/minio

falla() { echo "imagen-minio: $*" >&2; exit 1; }

referencia() {
  [[ -f "$DOCKERFILE" ]] || falla "no existe $DOCKERFILE"
  local release hash
  release="$(sed -n -E 's/^ARG SILO_RELEASE=(RELEASE\.[0-9TZ-]+)$/\1/p' "$DOCKERFILE")"
  [[ -n "$release" ]] || falla "el Dockerfile no fija ARG SILO_RELEASE=RELEASE.<fecha>"
  hash="$(sha256sum "$DOCKERFILE" | cut -c1-12)"
  echo "$REPOSITORIO:silo-$release-$hash"
}

construir() {
  local ref
  ref="$(referencia)"
  if docker image inspect "$ref" >/dev/null 2>&1; then
    echo "imagen-minio: $ref ya esta en esta maquina" >&2
    return 0
  fi
  echo "imagen-minio: construyendo $ref desde el codigo fuente (minutos la primera vez)" >&2
  DOCKER_BUILDKIT=1 docker build -q -t "$ref" - <"$DOCKERFILE" >/dev/null ||
    falla "no se pudo construir $ref (repite sin -q para ver el detalle: docker build - < $DOCKERFILE)"
}

case "${1:-}" in
  --referencia) referencia ;;
  --construir) construir ;;
  *) echo "uso: $0 --referencia|--construir" >&2; exit 2 ;;
esac

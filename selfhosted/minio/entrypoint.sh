#!/bin/sh
# Arranque de MinIO en la produccion autoalojada (docker-compose.selfhosted.yml).
#
# Sin MINIO_ROOT_USER y MINIO_ROOT_PASSWORD, MinIO arranca con la cuenta raiz de fabrica
# (minioadmin/minioadmin) y solo lo avisa en el registro: cualquiera de la red interna tendria el
# almacen entero. Aqui se niega a arrancar sin las dos del almacen de secretos y con una
# contrasena corta. El resto de argumentos (server /data ...) los fija el compose.
set -eu

falla() {
  echo "minio: $*" >&2
  exit 1
}

[ -n "${MINIO_ROOT_USER:-}" ] || falla "falta MINIO_ROOT_USER (almacen de secretos); sin ella MinIO usaria la cuenta de fabrica"
[ -n "${MINIO_ROOT_PASSWORD:-}" ] || falla "falta MINIO_ROOT_PASSWORD (almacen de secretos); sin ella MinIO usaria la cuenta de fabrica"
[ ${#MINIO_ROOT_PASSWORD} -ge 32 ] || falla "MINIO_ROOT_PASSWORD debe tener al menos 32 caracteres (openssl rand -hex 20)"
[ "$MINIO_ROOT_USER" != minioadmin ] && [ "$MINIO_ROOT_PASSWORD" != minioadmin ] || falla "la cuenta raiz no puede ser la de fabrica"

exec minio "$@"

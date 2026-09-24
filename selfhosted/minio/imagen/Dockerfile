# Imagen propia del almacen S3 del perfil autoalojado (servicio minio), construida desde el codigo
# fuente (docs/adr/0016).
#
# MinIO dejo de publicar imagenes (quay.io/minio/minio y docker.io/minio/minio piden autenticacion) y
# su repositorio publico esta archivado en RELEASE.2025-10-15T17-29-55Z, que sigue expuesto a fallos
# altos publicados despues y corregidos solo en su producto de pago (CVE-2026-41145 y CVE-2026-40344:
# escritura de objetos sin firma valida; CVE-2026-34204, CVE-2026-39414). Por eso el servidor y el cliente
# salen de Silo (github.com/pgsty/silo y github.com/pgsty/mc), la bifurcacion mantenida de MinIO que
# corrige esos fallos y conserva el formato en disco (.minio.sys), las variables MINIO_*, las rutas
# /minio/health/* y las API S3 y de administracion que usan selfhosted/minio/*.sh y pkg/objectstore.
#
# El contexto de build es SOLO este fichero (scripts/imagen-minio.sh lo construye con `docker build -`)
# y la etiqueta de la imagen lleva su hash: cualquier cambio aqui es otra imagen, y
# ops/scaffold/check-selfhosted-profile.sh falla si los compose no la referencian. Las fuentes se bajan
# por commit y se comprueba que HEAD es ese commit; las dependencias Go se verifican contra el go.sum de
# cada proyecto y la base de sumas publica de Go. Binarios estaticos (CGO off), sin rutas locales
# (-trimpath) y con las marcas de version de sus Makefile: dos builds de este fichero dan los mismos
# binarios.
#
# Licencia: Silo y su cliente son AGPL-3.0-or-later, como MinIO. Se usan sin modificar; la licencia, los
# CREDITS y NOTICE de cada proyecto y el aviso de donde esta el codigo fuente van en /licenses.

ARG SILO_RELEASE=RELEASE.2026-09-16T00-00-00Z
ARG SILO_COMMIT=2a4d51406b7ed87af5fe6fe0f801f3290f96eb3c
# El cliente es el que fija el go.mod de esa version de Silo (replace github.com/minio/mc).
ARG MC_RELEASE=RELEASE.2026-09-16T00-00-00Z
ARG MC_COMMIT=e952aa78f10a2b77dd525a2b7e3143bcda0cd377

FROM golang:1.27.1-alpine3.24@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build

ARG SILO_RELEASE
ARG SILO_COMMIT
ARG MC_RELEASE
ARG MC_COMMIT

RUN apk add --no-cache git

# fuente <repositorio> <commit> <directorio>: solo ese commit, y se comprueba que es el que se pidio.
RUN cat >/usr/local/bin/fuente <<'EOF'
#!/bin/sh
set -eu
git init -q "$3"
git -C "$3" fetch -q --depth 1 "$1" "$2"
git -C "$3" -c advice.detachedHead=false checkout -q FETCH_HEAD
[ "$(git -C "$3" rev-parse HEAD)" = "$2" ] || { echo "fuente: $1 no esta en $2" >&2; exit 1; }
EOF
# marcas <modulo> <RELEASE.AAAA-MM-DDTHH-MM-SSZ> <commit>: las -X de version que ponen los Makefile.
RUN cat >/usr/local/bin/marcas <<'EOF'
#!/bin/sh
set -eu
fecha="${2#RELEASE.}"
version="${fecha%%T*}T$(echo "${fecha#*T}" | tr - :)"
printf -- '-s -w -X %s/cmd.Version=%s -X %s/cmd.CopyrightYear=%s -X %s/cmd.ReleaseTag=%s -X %s/cmd.CommitID=%s -X %s/cmd.ShortCommitID=%s' \
  "$1" "$version" "$1" "${version%%-*}" "$1" "$2" "$1" "$3" "$1" "$(echo "$3" | cut -c1-12)"
EOF
RUN chmod 0755 /usr/local/bin/fuente /usr/local/bin/marcas

ENV CGO_ENABLED=0 GOOS=linux GOTOOLCHAIN=local GOFLAGS=-mod=readonly

RUN fuente https://github.com/pgsty/silo.git "$SILO_COMMIT" /src/silo
WORKDIR /src/silo
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -tags kqueue -trimpath -buildvcs=false -o /out/silo \
      -ldflags "$(marcas github.com/minio/minio "$SILO_RELEASE" "$SILO_COMMIT")" .

RUN fuente https://github.com/pgsty/mc.git "$MC_COMMIT" /src/mc
WORKDIR /src/mc
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -tags kqueue -trimpath -buildvcs=false -o /out/mc \
      -ldflags "$(marcas github.com/minio/mc "$MC_RELEASE" "$MC_COMMIT")" .

RUN mkdir -p /out/licenses/silo /out/licenses/mc && \
    cp /src/silo/LICENSE /src/silo/CREDITS /src/silo/NOTICE /out/licenses/silo/ && \
    cp /src/mc/LICENSE /src/mc/CREDITS /src/mc/NOTICE /out/licenses/mc/ && \
    printf '%s\n' \
      "Esta imagen contiene Silo (bifurcacion mantenida del servidor MinIO) y su cliente mc, sin modificar," \
      "bajo la GNU Affero General Public License v3 o posterior (LICENSE de cada directorio)." \
      "Codigo fuente correspondiente:" \
      "  silo $SILO_RELEASE  https://github.com/pgsty/silo/tree/$SILO_COMMIT" \
      "  mc   $MC_RELEASE  https://github.com/pgsty/mc/tree/$MC_COMMIT" \
      "Receta de construccion: selfhosted/minio/imagen/Dockerfile del repositorio de Core Force Mail." \
      >/out/licenses/FUENTE.txt

# La base lleva el sh y las utilidades de busybox que usan el arranque (selfhosted/minio/entrypoint.sh),
# la inicializacion (init.sh: cat, tr) y el trabajo que da el volumen al usuario (stat, chown), y el wget
# del chequeo de salud. Sin gestor de paquetes ni curl: nada se instala despues del build.
FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6

ARG SILO_RELEASE
ARG SILO_COMMIT
ARG MC_RELEASE
ARG MC_COMMIT

# /lib/apk/db se conserva: es lo que lee un escaner de vulnerabilidades para saber que paquetes quedan.
RUN apk --no-network --purge del apk-tools alpine-keys && \
    addgroup -S -g 10001 minio && \
    adduser -S -D -H -u 10001 -G minio -h /data -s /sbin/nologin minio && \
    mkdir -p /data && chown 10001:10001 /data && chmod 0700 /data

COPY --from=build /out/silo /out/mc /usr/bin/
# El arranque del perfil y los guiones de operacion llaman al servidor minio.
RUN ln -s silo /usr/bin/minio
COPY --from=build /out/licenses /licenses

LABEL org.opencontainers.image.title="Almacen S3 de Core Force Mail (Silo)" \
      org.opencontainers.image.description="Servidor Silo y cliente mc compilados desde su codigo fuente publico" \
      org.opencontainers.image.version="${SILO_RELEASE}" \
      org.opencontainers.image.revision="${SILO_COMMIT}" \
      org.opencontainers.image.source="https://github.com/pgsty/silo/tree/${SILO_COMMIT}" \
      org.opencontainers.image.licenses="AGPL-3.0-or-later" \
      core-force-mail.silo.release="${SILO_RELEASE}" \
      core-force-mail.silo.commit="${SILO_COMMIT}" \
      core-force-mail.mc.release="${MC_RELEASE}" \
      core-force-mail.mc.commit="${MC_COMMIT}" \
      core-force-mail.mc.source="https://github.com/pgsty/mc/tree/${MC_COMMIT}"

ENV MINIO_UPDATE=off
USER 10001:10001
WORKDIR /data
EXPOSE 9000
ENTRYPOINT ["/usr/bin/silo"]
CMD ["server", "/data"]

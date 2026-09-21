FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o /out/migration-runner .

FROM alpine:3.23

LABEL maintainer="Core Force Mail"

# imapsync no esta en los repositorios de Debian ni de Ubuntu: Alpine lo publica en community. Se fija
# la serie 2.314 (un parche del paquete, -rN, entra al reconstruir; otra version de imapsync no) y el
# build falla si resulta otra. Nada mas se descarga de Internet durante el build.
RUN apk add --no-cache 'imapsync~=2.314' ca-certificates \
  && test "$(imapsync --version)" = "2.314" \
  && addgroup -S -g 10001 migration \
  && adduser -S -D -H -u 10001 -G migration -s /sbin/nologin migration \
  && install -d -m 0700 -o migration -g migration /run/migration

COPY --from=builder /out/migration-runner /usr/local/bin/migration-runner

ENV MIGRATION_WORK_DIR=/run/migration
USER 10001:10001

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 CMD ["/usr/local/bin/migration-runner", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/migration-runner"]

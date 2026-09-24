FROM golang:1.26-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/mail-files ./services/mail-files
# Directorio de las subidas en espera de ClamAV (MAIL_FILES_SPOOL_DIR), del usuario sin privilegios del
# perfil autoalojado: el volumen que se monta encima nace con este dueno y estos permisos.
RUN mkdir -p /spool && chown 65532:65532 /spool && chmod 0700 /spool

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /bin/mail-files /mail-files
COPY --from=builder --chown=65532:65532 /spool /var/spool/mail-files
EXPOSE 8061
# El binario sabe sondearse a si mismo: la imagen es scratch y no tiene shell ni curl
# (ver pkg/observability/healthcheck.go).
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD ["/mail-files", "--healthcheck", "8061"]
ENTRYPOINT ["/mail-files"]

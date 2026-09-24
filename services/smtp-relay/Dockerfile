FROM golang:1.26-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/smtp-relay ./services/smtp-relay

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /bin/smtp-relay /smtp-relay
# 8060: operativa (salud y metricas). 2525: SMTP con STARTTLS obligatorio antes de AUTH. 2465: SMTP con TLS
# implicito. Por encima de 1024: el proceso no necesita ninguna capacidad para abrirlos.
EXPOSE 8060 2525 2465
# El binario sabe sondearse a si mismo: la imagen es scratch y no tiene shell ni curl
# (ver pkg/observability/healthcheck.go).
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD ["/smtp-relay", "--healthcheck", "8060"]
ENTRYPOINT ["/smtp-relay"]

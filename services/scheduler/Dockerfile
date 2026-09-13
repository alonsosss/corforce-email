FROM golang:1.26-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/scheduler ./services/scheduler

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /bin/scheduler /scheduler
# scratch no trae base de zonas horarias: el binario lleva la suya (time/tzdata). Si faltara,
# la imagen no se construye, en vez de fallar al evaluar el primer cron fuera de UTC.
RUN ["/scheduler", "--tzcheck"]
EXPOSE 8033
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD ["/scheduler", "--healthcheck", "8033"]
ENTRYPOINT ["/scheduler"]

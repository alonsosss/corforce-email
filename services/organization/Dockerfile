FROM golang:1.25-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/organization ./services/organization

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /bin/organization /organization
COPY --from=builder /app/migrations/tenant /app/migrations/tenant
COPY --from=builder /app/migrations/registry /app/migrations/registry
EXPOSE 8003
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD ["/organization", "--healthcheck", "8003"]
ENTRYPOINT ["/organization"]

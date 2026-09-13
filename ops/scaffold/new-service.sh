#!/usr/bin/env bash
# Generador de microservicio backend. Crea el esqueleto hexagonal (compila), Dockerfile y
# migracion, y EMITE los snippets exactos para gateway/compose/.env/RBAC (no auto-edita
# archivos fragiles como YAML o el Go del gateway: los imprime para pegar, de modo que no
# se olvide ningun touchpoint). Ver docs/arquitectura/ARQUITECTURA-Y-ESCALABILIDAD.md.
#
# Uso:
#   ops/scaffold/new-service.sh <nombre-kebab> <puerto> [modulo-rbac|--ungated]
# Ej:
#   ops/scaffold/new-service.sh service-orders 8092 service_orders
#   ops/scaffold/new-service.sh reporting-lite 8093 --ungated
set -euo pipefail

NAME="${1:-}"
PORT="${2:-}"
MODULE_ARG="${3:-}"

if [[ -z "$NAME" || -z "$PORT" ]]; then
  echo "uso: $0 <nombre-kebab> <puerto> [modulo-rbac|--ungated]" >&2
  exit 1
fi
if [[ ! "$NAME" =~ ^[a-z][a-z0-9-]*$ ]]; then
  echo "error: el nombre debe ser kebab-case (a-z,0-9,-)" >&2; exit 1
fi
if [[ ! "$PORT" =~ ^[0-9]+$ ]]; then
  echo "error: el puerto debe ser numerico" >&2; exit 1
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SVC_DIR="$ROOT/services/$NAME"
if [[ -e "$SVC_DIR" ]]; then
  echo "error: $SVC_DIR ya existe" >&2; exit 1
fi

NAME_UPPER="$(echo "$NAME" | tr 'a-z-' 'A-Z_')"        # service-orders -> SERVICE_ORDERS
BIN="$NAME"
IMPORT="github.com/alonsosss/corforce-email/services/$NAME"
GATED=false
MODULE=""
if [[ -n "$MODULE_ARG" && "$MODULE_ARG" != "--ungated" ]]; then
  GATED=true; MODULE="$MODULE_ARG"
fi

# Validacion de colision de puerto contra .env.example.
if grep -qE "=${PORT}\$" "$ROOT/.env.example" 2>/dev/null; then
  echo "ADVERTENCIA: el puerto ${PORT} ya aparece en .env.example (posible colision)" >&2
fi

echo ">> creando $SVC_DIR"
mkdir -p "$SVC_DIR/internal/"{domain,ports,app,adapters/http,adapters/postgres,adapters/nats}

# ── main.go ───────────────────────────────────────────────────────────────────
cat > "$SVC_DIR/main.go" <<EOF
package main

import (
	"context"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/server"
	handler "$IMPORT/internal/adapters/http"
	natsadapter "$IMPORT/internal/adapters/nats"
	"$IMPORT/internal/adapters/postgres"
	"$IMPORT/internal/app"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx := context.Background()
	registryPool, err := db.NewPool(ctx, cfg.Postgres.DSN(), logger)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer registryPool.Close()

	tenantDB, mgr := db.NewTenantRouting(registryPool.Pool, cfg.Postgres, logger)
	defer mgr.CloseAll()
	ctxPool := &db.ContextPool{}

	var publisher *natsadapter.Publisher
	if bus, err := events.NewBus(cfg.NATS.URL, logger); err != nil {
		logger.Warn("$NAME: NATS no disponible, sin eventos", zap.Error(err))
		publisher = natsadapter.NewPublisher(nil)
	} else {
		defer bus.Close()
		publisher = natsadapter.NewPublisher(bus)
	}

	uc := app.New(app.Deps{
		Repo:   postgres.NewRepository(ctxPool),
		Events: publisher,
		Logger: logger,
	})

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.InjectFromGateway)
	r.Use(db.TenantPoolMiddleware(tenantDB))
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))
	r.Use(middleware.NewRateLimiter(120, time.Minute).Limit)
	r.Mount("/", handler.NewHandler(uc).Routes())

	port := $PORT
	if p := os.Getenv("${NAME_UPPER}_PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			port = v
		}
	}

	srv := server.New(port, r, logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}
EOF

# ── domain ────────────────────────────────────────────────────────────────────
cat > "$SVC_DIR/internal/domain/entities.go" <<'EOF'
package domain

import "time"

// Item es una entidad de ejemplo; reemplazar por el modelo real del dominio.
type Item struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}
EOF

cat > "$SVC_DIR/internal/domain/errors.go" <<'EOF'
package domain

import "errors"

var ErrNotFound = errors.New("not found")
EOF

# ── ports ─────────────────────────────────────────────────────────────────────
cat > "$SVC_DIR/internal/ports/repositories.go" <<EOF
package ports

import (
	"context"

	"$IMPORT/internal/domain"
	"github.com/google/uuid"
)

type Repository interface {
	ListItems(ctx context.Context, tenantID uuid.UUID) ([]domain.Item, error)
}

// EventPublisher desacopla la emision de eventos del bus concreto.
type EventPublisher interface {
	Publish(subject string, payload any)
}
EOF

# ── app ───────────────────────────────────────────────────────────────────────
cat > "$SVC_DIR/internal/app/usecase.go" <<EOF
package app

import (
	"context"

	"$IMPORT/internal/domain"
	"$IMPORT/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type Deps struct {
	Repo   ports.Repository
	Events ports.EventPublisher
	Logger *zap.Logger
}

type UseCase struct {
	repo   ports.Repository
	events ports.EventPublisher
	logger *zap.Logger
}

func New(d Deps) *UseCase {
	return &UseCase{repo: d.Repo, events: d.Events, logger: d.Logger}
}

func (uc *UseCase) ListItems(ctx context.Context, tenantID uuid.UUID) ([]domain.Item, error) {
	return uc.repo.ListItems(ctx, tenantID)
}
EOF

# ── adapters/postgres ─────────────────────────────────────────────────────────
cat > "$SVC_DIR/internal/adapters/postgres/repository.go" <<EOF
package postgres

import (
	"context"

	"github.com/alonsosss/corforce-email/pkg/db"
	"$IMPORT/internal/domain"
	"github.com/google/uuid"
)

type Repository struct {
	pool *db.ContextPool
}

func NewRepository(pool *db.ContextPool) *Repository {
	return &Repository{pool: pool}
}

// ListItems es un stub de ejemplo (no consulta ninguna tabla todavia). Reemplazar por la
// consulta real cuando exista la migracion del dominio. El pool resuelve el tenant desde
// el contexto (TenantPoolMiddleware).
func (r *Repository) ListItems(ctx context.Context, tenantID uuid.UUID) ([]domain.Item, error) {
	_ = r.pool
	_ = tenantID
	return []domain.Item{}, nil
}
EOF

# ── adapters/nats ─────────────────────────────────────────────────────────────
cat > "$SVC_DIR/internal/adapters/nats/publisher.go" <<EOF
package nats

import "github.com/alonsosss/corforce-email/pkg/events"

// Publisher implementa ports.EventPublisher sobre el bus NATS. Best-effort: si el bus no
// esta disponible, los eventos no se emiten (no bloquea la operacion).
type Publisher struct {
	bus *events.Bus
}

func NewPublisher(bus *events.Bus) *Publisher { return &Publisher{bus: bus} }

func (p *Publisher) Publish(subject string, payload any) {
	if p == nil || p.bus == nil {
		return
	}
	evt := events.Event{Type: subject, Source: "$NAME-service", Data: payload}
	if m, ok := payload.(map[string]any); ok {
		switch v := m["tenant_id"].(type) {
		case string:
			evt.TenantID = v
		case interface{ String() string }:
			evt.TenantID = v.String()
		}
	}
	_ = p.bus.Publish(subject, evt)
}
EOF

# ── adapters/http ─────────────────────────────────────────────────────────────
cat > "$SVC_DIR/internal/adapters/http/handler.go" <<EOF
package http

import (
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"$IMPORT/internal/app"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type Handler struct {
	uc *app.UseCase
}

func NewHandler(uc *app.UseCase) *Handler { return &Handler{uc: uc} }

func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Route("/api/v1/$NAME", func(r chi.Router) {
		r.Get("/health", h.Health)
		r.Get("/items", h.ListItems)
	})
	return r
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) ListItems(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	items, err := h.uc.ListItems(r.Context(), tenantID)
	if err != nil {
		response.ErrInternal(w)
		return
	}
	response.JSON(w, http.StatusOK, items)
}
EOF

# ── Dockerfile ────────────────────────────────────────────────────────────────
cat > "$SVC_DIR/Dockerfile" <<EOF
FROM golang:1.26-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \\
    --mount=type=cache,target=/root/.cache/go-build \\
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/$BIN ./services/$NAME

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /bin/$BIN /$BIN
EXPOSE $PORT
# El binario sabe sondearse a si mismo: la imagen es scratch y no tiene shell ni curl
# (ver pkg/observability/healthcheck.go).
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \\
    CMD ["/$BIN", "--healthcheck", "$PORT"]
ENTRYPOINT ["/$BIN"]
EOF

# ── migracion de dominio ──────────────────────────────────────────────────────
MIG_DIR="$ROOT/migrations/tenant/canonical/$NAME"
mkdir -p "$MIG_DIR"
cat > "$MIG_DIR/01_${NAME//-/_}.sql" <<EOF
-- Esquema del dominio $NAME. Idempotente (IF NOT EXISTS). Aditivo: nunca destructivo.
CREATE SCHEMA IF NOT EXISTS ${NAME//-/_};

-- Tabla de ejemplo; reemplazar por el modelo real.
CREATE TABLE IF NOT EXISTS ${NAME//-/_}.items (
    id         UUID PRIMARY KEY,
    tenant_id  UUID NOT NULL,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
EOF

# ── .env.example: agregar el puerto si no existe (append seguro a KEY=VALUE) ───
if ! grep -qE "^${NAME_UPPER}_PORT=" "$ROOT/.env.example" 2>/dev/null; then
  echo "${NAME_UPPER}_PORT=$PORT" >> "$ROOT/.env.example"
  echo ">> ${NAME_UPPER}_PORT=$PORT agregado a .env.example"
fi

echo ""
echo "=================================================================="
echo " Servicio '$NAME' generado. Faltan estos pasos MANUALES (pegar):"
echo "=================================================================="
echo ""
echo "1) docker-compose.yml  (nuevo servicio):"
cat <<EOF
  $NAME:
    build:
      context: .
      dockerfile: ./services/$NAME/Dockerfile
    ports:
      - "127.0.0.1:\${${NAME_UPPER}_PORT:-$PORT}:$PORT"
    env_file:
      - .env
      - path: /dev/shm/core-force-mail/secrets.env
        required: false
    environment:
      JWT_SIGNING_KEY: ""
    depends_on:
      pgbouncer: { condition: service_healthy }
      nats: { condition: service_healthy }
    restart: unless-stopped
    security_opt: ["no-new-privileges:true"]
    networks:
      - mail-internal
EOF
echo ""
echo "2) services/gateway/routes.json  (el gateway no se recompila: lee la tabla al arrancar):"
echo "     en \"services\": \"$NAME\": {\"host_env\": \"${NAME_UPPER}_HOST\", \"default_host\": \"$NAME\", \"default_port\": \"$PORT\"}"
if $GATED; then
  echo "     en \"routes\":   {\"prefix\": \"$NAME\", \"service\": \"$NAME\", \"module\": \"$MODULE\"}"
  echo ""
  echo "3) migrations/registry/NNN_${NAME}_permissions.sql  (si el modulo '$MODULE' es nuevo):"
  echo "     INSERT INTO access_control.permissions (module, resource, action, description) ... ON CONFLICT DO NOTHING"
  echo ""
  echo "4) organization.module_catalog.permission_modules  (si '$MODULE' se contrata por empresa):"
  echo "     anadir '$MODULE' a la lista del modulo de catalogo que lo agrupa (migracion de organization)."
else
  echo "     en \"routes\":   {\"prefix\": \"$NAME\", \"service\": \"$NAME\", \"module\": \"\"}"
  echo ""
  echo "3) RBAC: servicio marcado --ungated (no se gatea por modulo). Confirmar que es correcto:"
  echo "   solo las consultas de acceso que el propio servicio resuelve con el JWT deberian quedar sin gatear."
fi
echo ""
echo "Valida con: make validate-scaffold && make gen-events && make gen-observability-targets"
echo "=================================================================="

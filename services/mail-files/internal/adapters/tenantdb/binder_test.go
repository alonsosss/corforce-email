package tenantdb

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/mail-files/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fakePools struct {
	err    error
	tenant string
}

func (f *fakePools) ResolveForTenant(_ context.Context, tenantID string) (*pgxpool.Pool, error) {
	f.tenant = tenantID
	return &pgxpool.Pool{}, f.err
}

func TestBindFijaLaIdentidadDeLaSesionDeBase(t *testing.T) {
	pools := &fakePools{}
	tenant, mailbox := uuid.New(), uuid.New()
	ctx, err := NewBinder(pools).Bind(context.Background(), tenant, mailbox)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := db.PoolFromCtx(ctx); !ok || pools.tenant != tenant.String() {
		t.Fatal("sin la base de la empresa")
	}
	if middleware.GetTenantID(ctx) != tenant.String() || middleware.GetUserID(ctx) != mailbox.String() {
		t.Fatalf("identidad: %s %s", middleware.GetTenantID(ctx), middleware.GetUserID(ctx))
	}
	public, err := NewBinder(pools).Bind(context.Background(), tenant, uuid.Nil)
	if err != nil || middleware.GetTenantID(public) != tenant.String() || middleware.GetUserID(public) != "" {
		t.Fatalf("sin buzon: %v", err)
	}
}

func TestBindDistingueLaEmpresaDesconocida(t *testing.T) {
	unknown := NewBinder(&fakePools{err: fmt.Errorf("resolve tenant db: %w", pgx.ErrNoRows)})
	if _, err := unknown.Bind(context.Background(), uuid.New(), uuid.Nil); !errors.Is(err, domain.ErrTenantUnknown) {
		t.Fatalf("desconocida: %v", err)
	}
	down := NewBinder(&fakePools{err: errors.New("pgbouncer caido")})
	if _, err := down.Bind(context.Background(), uuid.New(), uuid.Nil); !errors.Is(err, domain.ErrUnavailable) || errors.Is(err, domain.ErrTenantUnknown) {
		t.Fatalf("caida: %v", err)
	}
}

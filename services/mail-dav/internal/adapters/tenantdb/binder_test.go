package tenantdb

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
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
	return nil, f.err
}

var principal = domain.Principal{TenantID: uuid.New(), MailboxID: uuid.New(), Username: "ana@acme.test"}

func TestBindResuelveLaBaseDeLaEmpresaDelBuzon(t *testing.T) {
	pools := &fakePools{}
	if _, err := NewBinder(pools).Bind(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	if pools.tenant != principal.TenantID.String() {
		t.Fatalf("empresa resuelta: %s", pools.tenant)
	}
}

// Una empresa que no figura en el registro es un fallo definitivo y se distingue de una base que no
// responde; las dos siguen siendo domain.ErrUnavailable para el protocolo, que responde 503.
func TestBindDistingueLaEmpresaDesconocidaDeUnaBaseQueNoResponde(t *testing.T) {
	unknown := NewBinder(&fakePools{err: fmt.Errorf("resolve tenant db: %w", pgx.ErrNoRows)})
	if _, err := unknown.Bind(context.Background(), principal); !errors.Is(err, domain.ErrTenantUnknown) || !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("empresa desconocida: %v", err)
	}
	down := NewBinder(&fakePools{err: errors.New("connection refused")})
	if _, err := down.Bind(context.Background(), principal); errors.Is(err, domain.ErrTenantUnknown) || !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("base caida: %v", err)
	}
}

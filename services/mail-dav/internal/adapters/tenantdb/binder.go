// Package tenantdb elige la base de la empresa del buzon autenticado.
package tenantdb

import (
	"context"
	"fmt"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolResolver es lo que se necesita de pkg/db.TenantDB.
type PoolResolver interface {
	ResolveForTenant(ctx context.Context, tenantID string) (*pgxpool.Pool, error)
}

// Binder implementa ports.TenantBinder. La empresa sale siempre de la identidad que devolvio mail-auth,
// nunca de la peticion.
type Binder struct {
	pools PoolResolver
}

func NewBinder(pools PoolResolver) *Binder { return &Binder{pools: pools} }

func (b *Binder) Bind(ctx context.Context, p domain.Principal) (context.Context, error) {
	pool, err := b.pools.ResolveForTenant(ctx, p.TenantID.String())
	if err != nil {
		return ctx, fmt.Errorf("%w: base de la empresa: %v", domain.ErrUnavailable, err)
	}
	ctx = middleware.WithIdentity(ctx, p.MailboxID.String(), p.TenantID.String())
	return db.WithPool(ctx, pool), nil
}

// Package tenantdb liga cada operacion a la base de la empresa del fichero y recorre las empresas
// activas para el barrido.
package tenantdb

import (
	"context"
	"fmt"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/mail-files/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolResolver es lo que se necesita de pkg/db.TenantDB.
type PoolResolver interface {
	ResolveForTenant(ctx context.Context, tenantID string) (*pgxpool.Pool, error)
}

// Binder implementa ports.TenantBinder. La empresa llega de la sesion del webmail (verificada por
// mail-auth) o de un enlace cuya firma ya se comprobo; nunca de un dato sin verificar.
type Binder struct {
	pools PoolResolver
}

func NewBinder(pools PoolResolver) *Binder { return &Binder{pools: pools} }

func (b *Binder) Bind(ctx context.Context, tenantID, mailboxID uuid.UUID) (context.Context, error) {
	pool, err := b.pools.ResolveForTenant(ctx, tenantID.String())
	if err != nil {
		if db.IsUnknownTenant(err) {
			return ctx, fmt.Errorf("%w: %v", domain.ErrTenantUnknown, err)
		}
		return ctx, fmt.Errorf("%w: base de la empresa: %v", domain.ErrUnavailable, err)
	}
	if mailboxID == uuid.Nil {
		ctx = middleware.WithTenantID(ctx, tenantID.String())
	} else {
		ctx = middleware.WithIdentity(ctx, mailboxID.String(), tenantID.String())
	}
	return db.WithPool(ctx, pool), nil
}

// Tenants implementa ports.Tenants sobre el recorrido de empresas activas de pkg/db, que deja el
// contexto con la base y la empresa.
type Tenants struct {
	db *db.TenantDB
}

func NewTenants(tdb *db.TenantDB) *Tenants { return &Tenants{db: tdb} }

func (t *Tenants) ForEach(ctx context.Context, fn func(ctx context.Context, tenantID uuid.UUID)) error {
	return t.db.ForEachActiveTenant(ctx, func(tctx context.Context, tenantID string) {
		id, err := uuid.Parse(tenantID)
		if err != nil {
			return
		}
		fn(tctx, id)
	})
}

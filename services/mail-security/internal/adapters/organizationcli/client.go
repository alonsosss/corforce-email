// Package organizationcli pregunta a organization por las empresas con el resolvedor de
// pkg/tenantcell: la misma ruta interna, la misma cache y el mismo margen con organization caido
// que la segunda barrera de la celda.
package organizationcli

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/google/uuid"
)

// Client implementa ports.TenantRegistry.
type Client struct {
	resolver *tenantcell.Resolver
}

func New(resolver *tenantcell.Resolver) *Client { return &Client{resolver: resolver} }

// TenantGone solo da por retirada una empresa con la negativa definitiva de organization (404
// TENANT_NOT_FOUND). Una empresa de otra celda existe: no es una baja.
func (c *Client) TenantGone(ctx context.Context, tenantID uuid.UUID) (bool, error) {
	_, err := c.resolver.CellOf(ctx, tenantID.String())
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, tenantcell.ErrUnknownTenant):
		return true, nil
	default:
		return false, err
	}
}

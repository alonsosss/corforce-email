package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Conexiones con proveedores DNS (domains.dns_providers) y modo de publicacion de cada dominio. El
// token solo se escribe y se lee cifrado; ninguna consulta lo compara ni lo devuelve en un error.

const dnsProviderColumns = `id, tenant_id, provider, api_token_enc, api_token_hint, zones, zones_visible,
 connected_by, connected_at, last_validated_at, updated_at`

func (r *Repository) GetDNSProvider(ctx context.Context, tenantID uuid.UUID, provider domain.DNSProvider) (*domain.DNSProviderConnection, error) {
	c := &domain.DNSProviderConnection{}
	var connectedBy *uuid.UUID
	err := r.pool.QueryRow(ctx,
		`SELECT `+dnsProviderColumns+` FROM domains.dns_providers WHERE tenant_id = $1 AND provider = $2`,
		tenantID, provider,
	).Scan(&c.ID, &c.TenantID, &c.Provider, &c.TokenEnc, &c.TokenHint, &c.Zones, &c.ZonesVisible,
		&connectedBy, &c.ConnectedAt, &c.LastValidatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrDNSProviderNotConnected
	}
	if err != nil {
		return nil, err
	}
	if connectedBy != nil {
		c.ConnectedBy = *connectedBy
	}
	return c, nil
}

// SaveDNSProvider crea la conexion o, si la empresa ya tenia una con el proveedor, la reemplaza
// entera conservando su id.
func (r *Repository) SaveDNSProvider(ctx context.Context, c *domain.DNSProviderConnection) error {
	var connectedBy *uuid.UUID
	if c.ConnectedBy != uuid.Nil {
		connectedBy = &c.ConnectedBy
	}
	zones := c.Zones
	if zones == nil {
		zones = []string{}
	}
	return r.pool.QueryRow(ctx,
		`INSERT INTO domains.dns_providers (id, tenant_id, provider, api_token_enc, api_token_hint, zones, zones_visible,
 connected_by, connected_at, last_validated_at)
 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
 ON CONFLICT (tenant_id, provider) DO UPDATE SET api_token_enc = EXCLUDED.api_token_enc,
 api_token_hint = EXCLUDED.api_token_hint, zones = EXCLUDED.zones, zones_visible = EXCLUDED.zones_visible,
 connected_by = EXCLUDED.connected_by, connected_at = EXCLUDED.connected_at, last_validated_at = EXCLUDED.last_validated_at
 RETURNING id, updated_at`,
		c.ID, c.TenantID, c.Provider, c.TokenEnc, c.TokenHint, zones, c.ZonesVisible,
		connectedBy, c.ConnectedAt, c.LastValidatedAt,
	).Scan(&c.ID, &c.UpdatedAt)
}

// UpdateDNSProviderZones solo toca la conexion que se valido: si entretanto se reconecto con otro
// token (otra fila de token cifrado), no le pone las zonas de la anterior.
func (r *Repository) UpdateDNSProviderZones(ctx context.Context, c *domain.DNSProviderConnection) error {
	zones := c.Zones
	if zones == nil {
		zones = []string{}
	}
	_, err := r.pool.Exec(ctx,
		`UPDATE domains.dns_providers SET zones = $4, zones_visible = $5, last_validated_at = $6
 WHERE tenant_id = $1 AND provider = $2 AND api_token_enc = $3`,
		c.TenantID, c.Provider, c.TokenEnc, zones, c.ZonesVisible, c.LastValidatedAt)
	return err
}

func (r *Repository) DeleteDNSProvider(ctx context.Context, tenantID uuid.UUID, provider domain.DNSProvider) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM domains.dns_providers WHERE tenant_id = $1 AND provider = $2`, tenantID, provider)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (r *Repository) ResetDNSMode(ctx context.Context, tenantID uuid.UUID, mode domain.DNSMode) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE domains.domains SET dns_mode = $2 WHERE tenant_id = $1 AND dns_mode = $3`,
		tenantID, domain.DNSModeManual, mode)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (r *Repository) SetDNSMode(ctx context.Context, tenantID, id uuid.UUID, mode domain.DNSMode) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE domains.domains SET dns_mode = $3 WHERE tenant_id = $1 AND id = $2`, tenantID, id, mode)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrDomainNotFound
	}
	return nil
}

func (r *Repository) MarkDNSPublished(ctx context.Context, tenantID, id uuid.UUID, at time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE domains.domains SET dns_published_at = $3 WHERE tenant_id = $1 AND id = $2`, tenantID, id, at)
	return err
}

func (r *Repository) Transact(ctx context.Context, fn func(ctx context.Context) error) error {
	return r.inTx(ctx, fn)
}

package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/alonsosss/corforce-email/services/templates/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	assetSHAConstraint = "assets_tenant_sha256_key"

	brandKitColumns = `tenant_id, logo_asset_id, colors, fonts, footer_company, footer_address, footer_website,
		footer_support_email, updated_by, updated_at`
	assetColumns = `id, tenant_id, sha256, object_key, content_type, size_bytes, width, height, name, created_by, created_at`
)

func (r *Repository) GetBrandKit(ctx context.Context, tenantID uuid.UUID) (*domain.BrandKit, error) {
	var (
		k         domain.BrandKit
		updatedAt time.Time
	)
	err := r.pool.QueryRow(ctx,
		`SELECT `+brandKitColumns+` FROM templates.brand_kits WHERE tenant_id = $1`, tenantID,
	).Scan(&k.TenantID, &k.LogoAssetID, &k.Colors, &k.Fonts, &k.Footer.Company, &k.Footer.Address,
		&k.Footer.Website, &k.Footer.SupportEmail, &k.UpdatedBy, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	k.UpdatedAt = &updatedAt
	return &k, nil
}

func (r *Repository) UpsertBrandKit(ctx context.Context, k *domain.BrandKit) error {
	var updatedAt time.Time
	err := r.pool.QueryRow(ctx,
		`INSERT INTO templates.brand_kits (tenant_id, logo_asset_id, colors, fonts, footer_company, footer_address,
		        footer_website, footer_support_email, updated_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 ON CONFLICT (tenant_id) DO UPDATE SET
		        logo_asset_id = EXCLUDED.logo_asset_id, colors = EXCLUDED.colors, fonts = EXCLUDED.fonts,
		        footer_company = EXCLUDED.footer_company, footer_address = EXCLUDED.footer_address,
		        footer_website = EXCLUDED.footer_website, footer_support_email = EXCLUDED.footer_support_email,
		        updated_by = EXCLUDED.updated_by
		 RETURNING updated_at`,
		k.TenantID, k.LogoAssetID, k.Colors, k.Fonts, k.Footer.Company, k.Footer.Address,
		k.Footer.Website, k.Footer.SupportEmail, k.UpdatedBy,
	).Scan(&updatedAt)
	if err != nil {
		return err
	}
	k.UpdatedAt = &updatedAt
	return nil
}

func (r *Repository) CreateAsset(ctx context.Context, a *domain.Asset) error {
	err := r.pool.QueryRow(ctx,
		`INSERT INTO templates.assets (id, tenant_id, sha256, object_key, content_type, size_bytes, width, height, name, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING created_at`,
		a.ID, a.TenantID, a.SHA256, a.ObjectKey, a.ContentType, a.SizeBytes, a.Width, a.Height, a.Name, a.CreatedBy,
	).Scan(&a.CreatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == assetSHAConstraint {
		return domain.ErrAssetExists
	}
	return err
}

func (r *Repository) GetAsset(ctx context.Context, tenantID, id uuid.UUID) (*domain.Asset, error) {
	return scanAssetRow(r.pool.QueryRow(ctx,
		`SELECT `+assetColumns+` FROM templates.assets WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`,
		tenantID, id))
}

func (r *Repository) GetAssetBySHA(ctx context.Context, tenantID uuid.UUID, sha256Hex string) (*domain.Asset, bool, error) {
	var (
		a       domain.Asset
		deleted bool
	)
	err := r.pool.QueryRow(ctx,
		`SELECT `+assetColumns+`, deleted_at IS NOT NULL FROM templates.assets WHERE tenant_id = $1 AND sha256 = $2`,
		tenantID, sha256Hex,
	).Scan(&a.ID, &a.TenantID, &a.SHA256, &a.ObjectKey, &a.ContentType, &a.SizeBytes, &a.Width, &a.Height,
		&a.Name, &a.CreatedBy, &a.CreatedAt, &deleted)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, domain.ErrAssetNotFound
	}
	if err != nil {
		return nil, false, err
	}
	return &a, deleted, nil
}

func (r *Repository) RestoreAsset(ctx context.Context, tenantID, id uuid.UUID, name string) (*domain.Asset, error) {
	return scanAssetRow(r.pool.QueryRow(ctx,
		`UPDATE templates.assets SET deleted_at = NULL, name = $3
		  WHERE tenant_id = $1 AND id = $2
		  RETURNING `+assetColumns,
		tenantID, id, name))
}

func (r *Repository) ListAssets(ctx context.Context, tenantID uuid.UUID, after *ports.AssetCursor, limit int) ([]*domain.Asset, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if after == nil {
		rows, err = r.pool.Query(ctx,
			`SELECT `+assetColumns+` FROM templates.assets
			  WHERE tenant_id = $1 AND deleted_at IS NULL
			  ORDER BY created_at DESC, id DESC LIMIT $2`,
			tenantID, limit)
	} else {
		rows, err = r.pool.Query(ctx,
			`SELECT `+assetColumns+` FROM templates.assets
			  WHERE tenant_id = $1 AND deleted_at IS NULL AND (created_at, id) < ($2, $3)
			  ORDER BY created_at DESC, id DESC LIMIT $4`,
			tenantID, after.CreatedAt, after.ID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]*domain.Asset, 0)
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, a)
	}
	return items, rows.Err()
}

func (r *Repository) DeleteAsset(ctx context.Context, tenantID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE templates.assets SET deleted_at = now() WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`,
		tenantID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrAssetNotFound
	}
	return nil
}

func scanAssetRow(row pgx.Row) (*domain.Asset, error) {
	a, err := scanAsset(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrAssetNotFound
	}
	return a, err
}

func scanAsset(row pgx.Row) (*domain.Asset, error) {
	var a domain.Asset
	if err := row.Scan(&a.ID, &a.TenantID, &a.SHA256, &a.ObjectKey, &a.ContentType, &a.SizeBytes, &a.Width, &a.Height,
		&a.Name, &a.CreatedBy, &a.CreatedAt); err != nil {
		return nil, err
	}
	return &a, nil
}

package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/alonsosss/corforce-email/services/templates/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// BrandKitView es el kit con la URL del logo resuelta (vacia sin logo o sin almacen).
type BrandKitView struct {
	Kit     *domain.BrandKit
	LogoURL string
}

// AssetView es una imagen con su URL publica.
type AssetView struct {
	Asset *domain.Asset
	URL   string
}

func (uc *UseCase) brandKit(ctx context.Context, tenantID uuid.UUID) (*domain.BrandKit, error) {
	if uc.brandKits == nil {
		return domain.EmptyBrandKit(tenantID), nil
	}
	kit, err := uc.brandKits.GetBrandKit(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if kit == nil {
		return domain.EmptyBrandKit(tenantID), nil
	}
	return kit, nil
}

func (uc *UseCase) GetBrandKit(ctx context.Context, tenantID uuid.UUID) (*BrandKitView, error) {
	kit, err := uc.brandKit(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return uc.brandKitView(ctx, kit)
}

// UpdateBrandKit reemplaza el kit entero. El logo tiene que ser una imagen vigente de la
// empresa.
func (uc *UseCase) UpdateBrandKit(ctx context.Context, tenantID, userID uuid.UUID, in domain.BrandKit) (*BrandKitView, error) {
	if userID == uuid.Nil {
		return nil, domain.ErrMissingCreator
	}
	kit, err := domain.NormalizeBrandKit(in)
	if err != nil {
		return nil, err
	}
	if kit.LogoAssetID != nil {
		if _, err := uc.assets.GetAsset(ctx, tenantID, *kit.LogoAssetID); err != nil {
			if errors.Is(err, domain.ErrAssetNotFound) {
				return nil, fmt.Errorf("%w: logo_asset_id no es una imagen de la empresa", domain.ErrInvalidBrandKit)
			}
			return nil, err
		}
	}
	kit.TenantID, kit.UpdatedBy = tenantID, userID
	if err := uc.brandKits.UpsertBrandKit(ctx, &kit); err != nil {
		return nil, err
	}
	return uc.brandKitView(ctx, &kit)
}

// brandKitView resuelve la URL del logo. Un logo retirado del listado sigue sirviendose (su
// objeto no se borra), pero ya no se ofrece.
func (uc *UseCase) brandKitView(ctx context.Context, kit *domain.BrandKit) (*BrandKitView, error) {
	view := &BrandKitView{Kit: kit}
	if kit.LogoAssetID == nil || uc.assets == nil || uc.store == nil {
		return view, nil
	}
	asset, err := uc.assets.GetAsset(ctx, kit.TenantID, *kit.LogoAssetID)
	if errors.Is(err, domain.ErrAssetNotFound) {
		return view, nil
	}
	if err != nil {
		return nil, err
	}
	view.LogoURL = uc.store.PublicURL(asset.ObjectKey)
	return view, nil
}

// AssetUploadsAvailable dice si el servicio puede admitir subidas: sin almacen o sin ClamAV
// no las admite. El handler lo consulta antes de leer el cuerpo.
func (uc *UseCase) AssetUploadsAvailable() error {
	if uc.store == nil {
		return domain.ErrStorageUnavailable
	}
	if uc.scanner == nil {
		return domain.ErrScannerUnavailable
	}
	return nil
}

// UploadAsset comprueba la imagen, la analiza con ClamAV y la guarda por contenido. created
// es falso si la empresa ya tenia esa misma imagen vigente: se devuelve la existente.
func (uc *UseCase) UploadAsset(ctx context.Context, tenantID, userID uuid.UUID, name string, data []byte) (*AssetView, bool, error) {
	if userID == uuid.Nil {
		return nil, false, domain.ErrMissingCreator
	}
	if err := uc.AssetUploadsAvailable(); err != nil {
		return nil, false, err
	}
	info, err := domain.InspectImage(data)
	if err != nil {
		return nil, false, err
	}
	if err := uc.scanner.Scan(ctx, data); err != nil {
		if errors.Is(err, domain.ErrAssetRejected) {
			uc.logger.Warn("imagen rechazada por ClamAV", zap.String("tenant_id", tenantID.String()), zap.Error(err))
		}
		return nil, false, err
	}
	name = domain.NormalizeAssetName(name, info.Ext)
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])

	existing, deleted, err := uc.assets.GetAssetBySHA(ctx, tenantID, digest)
	switch {
	case err == nil && deleted:
		restored, err := uc.assets.RestoreAsset(ctx, tenantID, existing.ID, name)
		if err != nil {
			return nil, false, err
		}
		return uc.assetView(restored), true, nil
	case err == nil:
		return uc.assetView(existing), false, nil
	case !errors.Is(err, domain.ErrAssetNotFound):
		return nil, false, err
	}

	key := domain.AssetObjectKey(tenantID, digest, info.Ext)
	if err := uc.store.Put(ctx, key, data, info.ContentType); err != nil {
		return nil, false, err
	}
	asset := &domain.Asset{
		ID: uuid.New(), TenantID: tenantID, SHA256: digest, ObjectKey: key,
		ContentType: info.ContentType, SizeBytes: len(data), Width: info.Width, Height: info.Height,
		Name: name, CreatedBy: userID,
	}
	if err := uc.assets.CreateAsset(ctx, asset); err != nil {
		if !errors.Is(err, domain.ErrAssetExists) {
			return nil, false, err
		}
		// Otra subida concurrente de la misma imagen gano la insercion.
		if existing, _, err = uc.assets.GetAssetBySHA(ctx, tenantID, digest); err != nil {
			return nil, false, err
		}
		return uc.assetView(existing), false, nil
	}
	return uc.assetView(asset), true, nil
}

// ListAssets devuelve una pagina de imagenes vigentes y el cursor de la siguiente ("" si no
// hay mas).
func (uc *UseCase) ListAssets(ctx context.Context, tenantID uuid.UUID, limit int, cursor string) ([]AssetView, string, error) {
	if limit < 1 {
		limit = domain.DefaultAssetPage
	}
	limit = min(limit, domain.MaxAssetPage)
	after, err := decodeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	items, err := uc.assets.ListAssets(ctx, tenantID, after, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		last := items[limit-1]
		next = encodeCursor(ports.AssetCursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	out := make([]AssetView, 0, len(items))
	for _, a := range items {
		out = append(out, *uc.assetView(a))
	}
	return out, next, nil
}

// DeleteAsset retira la imagen del listado. El objeto se conserva: los correos enviados la
// siguen mostrando.
func (uc *UseCase) DeleteAsset(ctx context.Context, tenantID, id uuid.UUID) error {
	return uc.assets.DeleteAsset(ctx, tenantID, id)
}

func (uc *UseCase) assetView(a *domain.Asset) *AssetView {
	view := &AssetView{Asset: a}
	if uc.store != nil {
		view.URL = uc.store.PublicURL(a.ObjectKey)
	}
	return view
}

func encodeCursor(c ports.AssetCursor) string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID.String()))
}

func decodeCursor(s string) (*ports.AssetCursor, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, domain.ErrInvalidCursor
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return nil, domain.ErrInvalidCursor
	}
	createdAt, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return nil, domain.ErrInvalidCursor
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return nil, domain.ErrInvalidCursor
	}
	return &ports.AssetCursor{CreatedAt: createdAt, ID: parsed}, nil
}

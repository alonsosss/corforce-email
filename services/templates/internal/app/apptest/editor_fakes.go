package apptest

import (
	"context"
	"sort"
	"time"

	"github.com/alonsosss/corforce-email/services/templates/internal/deliverability"
	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/alonsosss/corforce-email/services/templates/internal/ports"
	"github.com/google/uuid"
)

// BrandKits guarda un kit por empresa en memoria.
type BrandKits struct{ Kits map[uuid.UUID]domain.BrandKit }

func NewBrandKits() *BrandKits { return &BrandKits{Kits: map[uuid.UUID]domain.BrandKit{}} }

func (f *BrandKits) GetBrandKit(_ context.Context, tenantID uuid.UUID) (*domain.BrandKit, error) {
	k, ok := f.Kits[tenantID]
	if !ok {
		return nil, nil
	}
	return &k, nil
}

func (f *BrandKits) UpsertBrandKit(_ context.Context, k *domain.BrandKit) error {
	now := time.Now()
	k.UpdatedAt = &now
	f.Kits[k.TenantID] = *k
	return nil
}

type storedAsset struct {
	asset   domain.Asset
	deleted bool
}

// Assets guarda imagenes en memoria con sha256 unico por empresa y borrado logico.
type Assets struct{ items []*storedAsset }

func NewAssets() *Assets { return &Assets{} }

func (f *Assets) CreateAsset(_ context.Context, a *domain.Asset) error {
	for _, s := range f.items {
		if s.asset.TenantID == a.TenantID && s.asset.SHA256 == a.SHA256 {
			return domain.ErrAssetExists
		}
	}
	a.CreatedAt = time.Now().Add(time.Duration(len(f.items)) * time.Millisecond)
	f.items = append(f.items, &storedAsset{asset: *a})
	return nil
}

func (f *Assets) find(tenantID, id uuid.UUID) *storedAsset {
	for _, s := range f.items {
		if s.asset.TenantID == tenantID && s.asset.ID == id {
			return s
		}
	}
	return nil
}

func (f *Assets) GetAsset(_ context.Context, tenantID, id uuid.UUID) (*domain.Asset, error) {
	s := f.find(tenantID, id)
	if s == nil || s.deleted {
		return nil, domain.ErrAssetNotFound
	}
	a := s.asset
	return &a, nil
}

func (f *Assets) GetAssetBySHA(_ context.Context, tenantID uuid.UUID, sha256Hex string) (*domain.Asset, bool, error) {
	for _, s := range f.items {
		if s.asset.TenantID == tenantID && s.asset.SHA256 == sha256Hex {
			a := s.asset
			return &a, s.deleted, nil
		}
	}
	return nil, false, domain.ErrAssetNotFound
}

func (f *Assets) RestoreAsset(_ context.Context, tenantID, id uuid.UUID, name string) (*domain.Asset, error) {
	s := f.find(tenantID, id)
	if s == nil {
		return nil, domain.ErrAssetNotFound
	}
	s.deleted, s.asset.Name = false, name
	a := s.asset
	return &a, nil
}

func (f *Assets) ListAssets(_ context.Context, tenantID uuid.UUID, after *ports.AssetCursor, limit int) ([]*domain.Asset, error) {
	var out []*domain.Asset
	for _, s := range f.items {
		if s.asset.TenantID == tenantID && !s.deleted {
			a := s.asset
			out = append(out, &a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if after != nil {
		kept := out[:0]
		for _, a := range out {
			if a.CreatedAt.Before(after.CreatedAt) {
				kept = append(kept, a)
			}
		}
		out = kept
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *Assets) DeleteAsset(_ context.Context, tenantID, id uuid.UUID) error {
	s := f.find(tenantID, id)
	if s == nil || s.deleted {
		return domain.ErrAssetNotFound
	}
	s.deleted = true
	return nil
}

// Store guarda los objetos en memoria.
type Store struct{ Objects map[string][]byte }

func NewStore() *Store { return &Store{Objects: map[string][]byte{}} }

func (f *Store) Put(_ context.Context, key string, data []byte, _ string) error {
	f.Objects[key] = append([]byte(nil), data...)
	return nil
}

func (f *Store) PublicURL(key string) string { return "https://app.test/media/" + key }

// Scanner devuelve Err en cada analisis y cuenta las llamadas.
type Scanner struct {
	Err   error
	Calls int
}

func (f *Scanner) Scan(context.Context, []byte) error {
	f.Calls++
	return f.Err
}

// Spam devuelve Result o Err y guarda la ultima muestra.
type Spam struct {
	Result deliverability.Spam
	Err    error
	Last   ports.SpamSample
}

func (f *Spam) Check(_ context.Context, s ports.SpamSample) (deliverability.Spam, error) {
	f.Last = s
	return f.Result, f.Err
}

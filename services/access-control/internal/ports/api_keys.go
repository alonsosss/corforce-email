package ports

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/access-control/internal/domain"
	"github.com/google/uuid"
)

// APIKeyRepository guarda las claves de API. Crear y revocar escriben tambien el evento de
// auditoria en la outbox del registro, en la misma transaccion: una clave existe (o deja de
// valer) si y solo si queda su rastro.
type APIKeyRepository interface {
	Create(ctx context.Context, key *domain.APIKey, event domain.APIKeyEvent) error
	List(ctx context.Context, tenantID uuid.UUID) ([]*domain.APIKey, error)
	Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.APIKey, error)
	// GetByPrefix busca en todas las empresas: el prefijo es unico en la plataforma.
	GetByPrefix(ctx context.Context, prefix string) (*domain.APIKey, error)
	// CountActive cuenta las claves no revocadas y no caducadas de la empresa.
	CountActive(ctx context.Context, tenantID uuid.UUID, now time.Time) (int, error)
	// Revoke marca la clave como revocada. domain.ErrAPIKeyNotFound si no es de la empresa y
	// domain.ErrAPIKeyRevoked si ya lo estaba.
	Revoke(ctx context.Context, tenantID, id, actor uuid.UUID, at time.Time, event func(*domain.APIKey) domain.APIKeyEvent) (*domain.APIKey, error)
	// TouchUsage anota el ultimo uso si el anterior es mas viejo que minInterval.
	TouchUsage(ctx context.Context, id uuid.UUID, at time.Time, ip string, minInterval time.Duration) error
	// Rehash sustituye el hash guardado por uno con la llave activa.
	Rehash(ctx context.Context, id uuid.UUID, hash []byte, keyID string) error
	// GrantablePermissions es el catalogo de lo que una clave puede llevar.
	GrantablePermissions(ctx context.Context) ([]*domain.Permission, error)
}

// APIKeyHasher calcula y comprueba el hash del secreto con la llave del almacen.
type APIKeyHasher interface {
	Hash(input []byte) (hash []byte, keyID string, err error)
	// Verify compara en tiempo constante; current dice si el hash es de la llave activa.
	Verify(input, hash []byte, keyID string) (ok, current bool, err error)
}

// APIKeyRevocations anuncia a quien cachea claves resueltas (gateway, smtp-relay) que una dejo
// de valer, sin esperar a que caduque su cache. Best-effort: sin el aviso la revocacion llega
// igual al vencer la cache.
type APIKeyRevocations interface {
	Revoked(ctx context.Context, keyID uuid.UUID)
}

// APIKeyMetrics cuenta las resoluciones por resultado.
type APIKeyMetrics interface {
	Resolved(result string)
}

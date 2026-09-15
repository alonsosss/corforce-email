package ports

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/google/uuid"
)

// Repository persiste dominios y comprobaciones DNS en la base de la empresa. El pool
// se resuelve desde el contexto (TenantPoolMiddleware o ForEachActiveTenant).
type Repository interface {
	Create(ctx context.Context, d *domain.Domain) error
	GetByID(ctx context.Context, tenantID, id uuid.UUID) (*domain.Domain, error)
	GetByName(ctx context.Context, tenantID uuid.UUID, name string) (*domain.Domain, error)
	List(ctx context.Context, tenantID uuid.UUID, offset, limit int) ([]*domain.Domain, int64, error)
	Update(ctx context.Context, d *domain.Domain) error
	Delete(ctx context.Context, tenantID, id uuid.UUID) error

	// ListForRecheck devuelve los dominios que el barrido debe reverificar: los
	// verificados y los pendientes creados despues de pendingSince.
	ListForRecheck(ctx context.Context, tenantID uuid.UUID, pendingSince time.Time) ([]*domain.Domain, error)
	// ListWithExpiredPreviousDKIM devuelve los dominios cuya clave DKIM anterior lleva
	// en gracia mas de la ventana indicada.
	ListWithExpiredPreviousDKIM(ctx context.Context, tenantID uuid.UUID, rotatedBefore time.Time) ([]*domain.Domain, error)
	// ListPendingDeactivation devuelve los dominios cuya desactivacion en el directorio de la
	// celda sigue sin confirmarse.
	ListPendingDeactivation(ctx context.Context, tenantID uuid.UUID) ([]*domain.Domain, error)

	SaveChecks(ctx context.Context, checks []domain.DNSCheck) error
	// LatestChecks devuelve la ultima comprobacion de cada registro del dominio.
	LatestChecks(ctx context.Context, tenantID, domainID uuid.UUID) ([]domain.DNSCheck, error)
	PruneChecks(ctx context.Context, tenantID uuid.UUID, before time.Time) (int64, error)
}

// DNSResolver consulta la zona publica del cliente.
type DNSResolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
	LookupMX(ctx context.Context, name string) ([]domain.MXRecord, error)
}

// Cipher cifra y descifra la clave privada DKIM en reposo. Lo implementa
// pkg/crypto.KeyRing con rotacion de llaves.
type Cipher interface {
	Encrypt(plaintext []byte) ([]byte, error)
	Decrypt(data []byte) ([]byte, error)
}

// MailDirectoryClient activa o desactiva el dominio en el directorio de la celda
// (mail-directory), que es lo que leen Postfix y Dovecot.
type MailDirectoryClient interface {
	// SetActivation devuelve domain.ErrDomainHasMailboxes si mail-directory se niega a
	// desactivar porque quedan buzones.
	SetActivation(ctx context.Context, tenantID uuid.UUID, name string, active bool) error
}

// DomainIndex es el indice global de los dominios de correo activos que sirve organization
// (dominio -> empresa; la celda es la de la empresa). Un dominio se reclama antes de activarlo en
// el directorio de la celda y se suelta despues de desactivarlo: es lo que lleva el webmail de
// cada buzon a su celda y lo que impide activar el mismo dominio en dos celdas. Las dos llamadas
// son idempotentes.
type DomainIndex interface {
	// Claim devuelve domain.ErrDomainClaimedElsewhere si otra empresa tiene el dominio activo.
	Claim(ctx context.Context, tenantID uuid.UUID, name string) error
	// Release no falla si la empresa no tiene el dominio reclamado.
	Release(ctx context.Context, tenantID uuid.UUID, name string) error
}

// DKIMKey es una clave privada lista para publicar. Solo existe en memoria.
type DKIMKey struct {
	Selector      string
	PrivateKeyPEM string
}

// MailSecurityClient publica y retira claves DKIM en mail-security, que las escribe en el
// Redis de los motores (DKIM_PRIV_KEYS, DKIM_SELECTORS).
type MailSecurityClient interface {
	// PublishDKIM publica el juego completo de claves del dominio en orden: la ultima es la que
	// Rspamd usa para firmar y mail-security retira los demas selectores del dominio.
	PublishDKIM(ctx context.Context, tenantID uuid.UUID, name string, keys []DKIMKey) error
	// RetireDKIM retira un selector concreto del dominio.
	RetireDKIM(ctx context.Context, tenantID uuid.UUID, name, selector string) error
	// DeleteDKIM retira todas las claves del dominio.
	DeleteDKIM(ctx context.Context, tenantID uuid.UUID, name string) error
}

// EventPublisher emite los hechos del dominio en el stream DOMAINS. Nunca lleva claves.
type EventPublisher interface {
	DomainCreated(ctx context.Context, d *domain.Domain) error
	DomainVerified(ctx context.Context, d *domain.Domain) error
	DomainFailed(ctx context.Context, d *domain.Domain) error
	DomainDeleted(ctx context.Context, d *domain.Domain) error
	DKIMRotated(ctx context.Context, d *domain.Domain) error
}

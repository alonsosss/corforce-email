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
	// Update guarda el estado del dominio (uso, estado, verificacion, politica DMARC y
	// desactivacion pendiente). Nunca escribe las claves DKIM: una verificacion o un barrido
	// que leyo el dominio antes de una rotacion no puede devolverle las claves que la rotacion
	// retiro. Las claves solo cambian con los metodos DKIM de abajo, cada uno condicionado al
	// selector que el llamante vio.
	Update(ctx context.Context, d *domain.Domain) error
	Delete(ctx context.Context, tenantID, id uuid.UUID) error

	// WithDKIMLock serializa por dominio todo lo que decide sobre sus claves y las entrega a la
	// celda: fn corre en una transaccion con el cerrojo del dominio tomado y recibe la fila
	// leida dentro de ella. Lo que fn escribe se confirma si devuelve nil.
	WithDKIMLock(ctx context.Context, tenantID, id uuid.UUID, fn func(ctx context.Context, d *domain.Domain) error) error
	// SaveDKIMKeys guarda el juego de claves nuevo del dominio y su entrada del historial en una
	// transaccion, solo si la clave actual sigue siendo expectedSelector; si no,
	// domain.ErrDKIMKeysChanged.
	SaveDKIMKeys(ctx context.Context, d *domain.Domain, expectedSelector string, rotation *domain.DKIMRotation) error
	// ClearPreviousDKIM olvida la clave anterior si sigue siendo selector.
	ClearPreviousDKIM(ctx context.Context, tenantID, id uuid.UUID, selector string) error
	// MarkPreviousDKIMSigning adelanta a at la ultima firma de la clave anterior si sigue siendo
	// selector.
	MarkPreviousDKIMSigning(ctx context.Context, tenantID, id uuid.UUID, selector string, at time.Time) error
	// ConfirmDKIM anota la primera vez que se vio publicado el TXT de la clave actual si sigue
	// siendo selector.
	ConfirmDKIM(ctx context.Context, tenantID, id uuid.UUID, selector string, at time.Time) error
	// CompleteDKIMRevocation quita la marca de revocacion pendiente si la clave actual sigue
	// siendo selector.
	CompleteDKIMRevocation(ctx context.Context, tenantID, id uuid.UUID, selector string) error
	// ListDKIMRotations devuelve el historial del dominio, la mas reciente primero.
	ListDKIMRotations(ctx context.Context, tenantID, domainID uuid.UUID, limit int) ([]domain.DKIMRotation, error)
	// UsedDKIMSelectors devuelve todos los selectores que el dominio uso alguna vez.
	UsedDKIMSelectors(ctx context.Context, tenantID, domainID uuid.UUID) ([]string, error)
	// ListPendingDKIMRevocation devuelve los dominios con una revocacion sin confirmar en la celda.
	ListPendingDKIMRevocation(ctx context.Context, tenantID uuid.UUID) ([]*domain.Domain, error)

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

// MTASTSPolicyReader lee de mail-directory la politica MTA-STS de un dominio, que vive en la base de
// la celda y no aqui.
type MTASTSPolicyReader interface {
	// PolicyID devuelve la version vigente de la politica, la que lleva el TXT _mta-sts, o vacio si el
	// dominio no la publica (modo none, sin politica o dominio que la celda no tiene todavia).
	PolicyID(ctx context.Context, tenantID uuid.UUID, name string) (string, error)
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
}

// KeyEvents encola los hechos de las claves DKIM en la outbox de la base de la empresa, dentro
// de la transaccion que las cambia (pkg/outbox): una rotacion o una revocacion existe en el
// historial si y solo si existe su evento. Nunca llevan claves privadas.
type KeyEvents interface {
	DKIMRotated(ctx context.Context, d *domain.Domain, rotation *domain.DKIMRotation) error
	DKIMRevoked(ctx context.Context, d *domain.Domain, rotation *domain.DKIMRotation) error
}

// DNSProviderRepository guarda la conexion de la empresa con su proveedor DNS y el modo de
// publicacion de cada dominio, en la base de la empresa.
type DNSProviderRepository interface {
	// GetDNSProvider devuelve domain.ErrDNSProviderNotConnected si la empresa no lo tiene.
	GetDNSProvider(ctx context.Context, tenantID uuid.UUID, provider domain.DNSProvider) (*domain.DNSProviderConnection, error)
	// SaveDNSProvider crea la conexion o reemplaza la que habia (token, zonas, quien y cuando).
	SaveDNSProvider(ctx context.Context, c *domain.DNSProviderConnection) error
	// UpdateDNSProviderZones anota lo que el token veia en una validacion posterior al alta.
	UpdateDNSProviderZones(ctx context.Context, c *domain.DNSProviderConnection) error
	// DeleteDNSProvider borra la conexion con su token; false si no habia.
	DeleteDNSProvider(ctx context.Context, tenantID uuid.UUID, provider domain.DNSProvider) (bool, error)
	// ResetDNSMode devuelve a manual los dominios de la empresa en ese modo y cuenta cuantos.
	ResetDNSMode(ctx context.Context, tenantID uuid.UUID, mode domain.DNSMode) (int64, error)
	SetDNSMode(ctx context.Context, tenantID, id uuid.UUID, mode domain.DNSMode) error
	MarkDNSPublished(ctx context.Context, tenantID, id uuid.UUID, at time.Time) error
	// Transact corre fn en una transaccion de la base de la empresa (o en la del contexto).
	Transact(ctx context.Context, fn func(ctx context.Context) error) error
}

// DNSProviderAPI es la API de un proveedor DNS. Cada llamada lleva el token de la empresa; ningun
// error lo contiene ni repite el mensaje del proveedor: se traducen a los errores de domain
// (ErrDNSProviderTokenInvalid, ErrDNSProviderPermissionDenied, ErrDNSZoneNotFound,
// ErrDNSProviderRateLimited, ErrDNSProviderUnavailable, ErrDNSProviderRejected, ErrDNSRecordExists).
type DNSProviderAPI interface {
	// VerifyToken comprueba que el token existe y esta activo.
	VerifyToken(ctx context.Context, token domain.APIToken) error
	// ListZones devuelve todas las zonas que el token ve.
	ListZones(ctx context.Context, token domain.APIToken) ([]domain.DNSZone, error)
	// ListRecords devuelve los registros de la zona con ese tipo y nombre exactos.
	ListRecords(ctx context.Context, token domain.APIToken, zone domain.DNSZone, recordType, name string) ([]domain.ProviderRecord, error)
	CreateRecord(ctx context.Context, token domain.APIToken, zone domain.DNSZone, rec domain.ProviderRecord) error
	// UpdateRecord sobrescribe el registro rec.ID con rec.
	UpdateRecord(ctx context.Context, token domain.APIToken, zone domain.DNSZone, rec domain.ProviderRecord) error
	// DeleteRecord no falla si el registro ya no existe.
	DeleteRecord(ctx context.Context, token domain.APIToken, zone domain.DNSZone, recordID string) error
}

// DNSEvents encola los hechos de la publicacion automatica en la outbox de la base de la empresa,
// en la transaccion del cambio. Nunca llevan el token.
type DNSEvents interface {
	DNSProviderConnected(ctx context.Context, c *domain.DNSProviderConnection) error
	DNSProviderDisconnected(ctx context.Context, tenantID uuid.UUID, provider domain.DNSProvider, actorID uuid.UUID, domainsReset int64, at time.Time) error
	DNSPublished(ctx context.Context, d *domain.Domain, p *domain.DNSPublication, actorID uuid.UUID) error
}

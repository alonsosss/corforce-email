package ports

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

// Transactor abre la transaccion sujeta a RLS en la que corre cada caso de uso. Toda
// lectura y escritura del directorio pasa por aqui: es lo que activa el rol mail_app.
type Transactor interface {
	InTx(ctx context.Context, fn func(ctx context.Context) error) error
}

type Page struct {
	Offset int
	Limit  int
}

// DomainFilter acota el listado de dominios. Search vacio no filtra; llega ya validado y
// se busca sin distinguir mayusculas como subcadena del nombre.
type DomainFilter struct {
	Search string
}

// MailboxFilter acota el listado de buzones: Search sobre username y nombre visible (sin
// distinguir mayusculas) y Domain exacto, ya normalizado. Vacios no filtran.
type MailboxFilter struct {
	Search string
	Domain string
}

// TransportScope acota el acceso a rutas de plataforma (tenant_id NULL): solo quien
// opera la plataforma las administra; todas las empresas las ven.
type TransportScope struct {
	TenantID uuid.UUID
	Platform bool
}

type DomainRepository interface {
	List(ctx context.Context, tenantID uuid.UUID, filter DomainFilter, page Page) ([]domain.Domain, int64, error)
	Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Domain, error)
	GetByName(ctx context.Context, tenantID uuid.UUID, name string) (*domain.Domain, error)
	Create(ctx context.Context, d *domain.Domain) error
	Update(ctx context.Context, d *domain.Domain) error
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
	// NameInUse responde si el nombre ya es dominio o dominio alias de CUALQUIER empresa
	// de la celda, sin revelar de cual.
	NameInUse(ctx context.Context, name string) (bool, error)
	// Usage cuenta buzones, aliases y dominios alias que cuelgan del dominio.
	Usage(ctx context.Context, tenantID uuid.UUID, name string) (mailboxes, aliases, aliasDomains int64, err error)
}

type AliasDomainRepository interface {
	List(ctx context.Context, tenantID uuid.UUID, page Page) ([]domain.AliasDomain, int64, error)
	Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.AliasDomain, error)
	Create(ctx context.Context, a *domain.AliasDomain) error
	Update(ctx context.Context, a *domain.AliasDomain) error
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
	ExistsByName(ctx context.Context, tenantID uuid.UUID, name string) (bool, error)
}

type MailboxRepository interface {
	List(ctx context.Context, tenantID uuid.UUID, filter MailboxFilter, page Page) ([]domain.Mailbox, int64, error)
	Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Mailbox, error)
	GetByUsername(ctx context.Context, tenantID uuid.UUID, username string) (*domain.Mailbox, error)
	Create(ctx context.Context, m *domain.Mailbox) error
	Update(ctx context.Context, m *domain.Mailbox) error
	UpdatePassword(ctx context.Context, tenantID, id uuid.UUID, hash string) error
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
	CountByDomain(ctx context.Context, tenantID uuid.UUID, name string) (int64, error)
	// QuotaSumByDomain suma las cuotas de los buzones del dominio salvo el excluido.
	QuotaSumByDomain(ctx context.Context, tenantID uuid.UUID, name string, exclude uuid.UUID) (int64, error)
	Quota(ctx context.Context, tenantID, id uuid.UUID) (*domain.QuotaUsage, error)
	DeleteQuotaUsage(ctx context.Context, tenantID uuid.UUID, username string) error
	Logins(ctx context.Context, tenantID uuid.UUID, username string, limit int) ([]domain.SASLLogin, error)
	// AddressInUse responde si la direccion ya es buzon, alias o alias temporal de la empresa.
	AddressInUse(ctx context.Context, tenantID uuid.UUID, address string) (bool, error)
}

type AppPasswordRepository interface {
	List(ctx context.Context, tenantID, mailboxID uuid.UUID) ([]domain.AppPassword, error)
	Get(ctx context.Context, tenantID, mailboxID, id uuid.UUID) (*domain.AppPassword, error)
	Create(ctx context.Context, p *domain.AppPassword) error
	Update(ctx context.Context, p *domain.AppPassword) error
	Delete(ctx context.Context, tenantID, mailboxID, id uuid.UUID) error
	DeleteByMailbox(ctx context.Context, tenantID, mailboxID uuid.UUID) error
	DeactivateByMailbox(ctx context.Context, tenantID, mailboxID uuid.UUID) error
}

type SieveRepository interface {
	ByUsername(ctx context.Context, tenantID uuid.UUID, username string) ([]domain.SieveFilter, error)
	// Replace deja como unico filtro de ese tipo el indicado; con nil lo elimina.
	Replace(ctx context.Context, tenantID uuid.UUID, username, filterType string, f *domain.SieveFilter) error
	DeleteByUsername(ctx context.Context, tenantID uuid.UUID, username string) error
}

type AliasRepository interface {
	List(ctx context.Context, tenantID uuid.UUID, page Page) ([]domain.Alias, int64, error)
	Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Alias, error)
	Create(ctx context.Context, a *domain.Alias) error
	Update(ctx context.Context, a *domain.Alias) error
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
	CountByDomain(ctx context.Context, tenantID uuid.UUID, name string) (int64, error)
}

type SpamAliasRepository interface {
	List(ctx context.Context, tenantID uuid.UUID, page Page) ([]domain.SpamAlias, int64, error)
	Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.SpamAlias, error)
	Create(ctx context.Context, a *domain.SpamAlias) error
	Update(ctx context.Context, a *domain.SpamAlias) error
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
	DeleteByGoto(ctx context.Context, tenantID uuid.UUID, username string) error
}

type SenderACLRepository interface {
	List(ctx context.Context, tenantID uuid.UUID, page Page) ([]domain.SenderACL, int64, error)
	Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.SenderACL, error)
	Create(ctx context.Context, a *domain.SenderACL) error
	Update(ctx context.Context, a *domain.SenderACL) error
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
	DeleteByLoggedInAs(ctx context.Context, tenantID uuid.UUID, username string) error
}

// SenderIdentityRepository resuelve con que direcciones concretas puede enviar un buzon
// segun la misma regla que aplica Postfix (mail.sender_identities). Lee toda la celda: el
// buzon es unico en ella y quien pregunta (el webmail) no conoce la empresa.
type SenderIdentityRepository interface {
	ForLogin(ctx context.Context, username string, limit int) ([]string, error)
}

// Las contrasenas de relayhost y transporte viajan aparte de la entidad: entran en la
// escritura y no se leen nunca. Un puntero nil en Update las deja como estan.
type RelayhostRepository interface {
	List(ctx context.Context, tenantID uuid.UUID, page Page) ([]domain.Relayhost, int64, error)
	Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Relayhost, error)
	Create(ctx context.Context, r *domain.Relayhost, password string) error
	Update(ctx context.Context, r *domain.Relayhost, password *string) error
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
}

type TransportRepository interface {
	List(ctx context.Context, tenantID uuid.UUID, page Page) ([]domain.Transport, int64, error)
	Get(ctx context.Context, scope TransportScope, id uuid.UUID) (*domain.Transport, error)
	Create(ctx context.Context, t *domain.Transport, password string) error
	Update(ctx context.Context, scope TransportScope, t *domain.Transport, password *string) error
	Delete(ctx context.Context, scope TransportScope, id uuid.UUID) error
}

type TLSPolicyRepository interface {
	List(ctx context.Context, tenantID uuid.UUID, page Page) ([]domain.TLSPolicy, int64, error)
	Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.TLSPolicy, error)
	Create(ctx context.Context, p *domain.TLSPolicy) error
	Update(ctx context.Context, p *domain.TLSPolicy) error
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
}

type RecipientMapRepository interface {
	List(ctx context.Context, tenantID uuid.UUID, page Page) ([]domain.RecipientMap, int64, error)
	Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.RecipientMap, error)
	Create(ctx context.Context, m *domain.RecipientMap) error
	Update(ctx context.Context, m *domain.RecipientMap) error
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
}

type BCCMapRepository interface {
	List(ctx context.Context, tenantID uuid.UUID, page Page) ([]domain.BCCMap, int64, error)
	Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.BCCMap, error)
	Create(ctx context.Context, m *domain.BCCMap) error
	Update(ctx context.Context, m *domain.BCCMap) error
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
}

// RetirementRepository guarda las bajas de empresa en la celda y apaga en bloque el directorio de
// una empresa. Todo corre en la transaccion del caso de uso; los cerrojos son de esa transaccion.
type RetirementRepository interface {
	// HoldShared toma el cerrojo compartido de la empresa y dice si esta dada de baja. Lo toma
	// cada escritura en su directorio: no se cruza con una baja en curso.
	HoldShared(ctx context.Context, tenantID uuid.UUID) (retired bool, err error)
	// HoldExclusive toma el cerrojo exclusivo de la empresa: espera a las escrituras en curso, y
	// las que llegan despues esperan a la baja.
	HoldExclusive(ctx context.Context, tenantID uuid.UUID) error
	// Mark registra la baja si no estaba y devuelve cuando se registro.
	Mark(ctx context.Context, tenantID uuid.UUID) (time.Time, error)
	// Los Deactivate* apagan lo que sigue encendido de la empresa y devuelven las filas que
	// cambiaron, para anunciar cada una.
	DeactivateDomains(ctx context.Context, tenantID uuid.UUID) ([]domain.Domain, error)
	DeactivateAliasDomains(ctx context.Context, tenantID uuid.UUID) ([]domain.AliasDomain, error)
	DeactivateMailboxes(ctx context.Context, tenantID uuid.UUID) ([]domain.Mailbox, error)
	DeactivateAliases(ctx context.Context, tenantID uuid.UUID) ([]domain.Alias, error)
	// DeactivateSettings apaga las contrasenas de aplicacion, los relayhosts, los transportes de la
	// empresa, las politicas TLS y los mapas de destinatario y de copia, borra las contrasenas SASL
	// que Postfix guarda en claro y cuenta las filas que cambio. Ninguna de esas tablas tiene
	// evento: los motores y mail-auth las leen en cada consulta.
	DeactivateSettings(ctx context.Context, tenantID uuid.UUID) (domain.RetirementCounts, error)
}

// Secrets aisla el hash de contrasenas y la generacion de contrasenas de aplicacion.
type Secrets interface {
	HashPassword(plain string) (string, error)
	GenerateAppPassword() (string, error)
}

// EventPublisher emite los hechos del directorio que otros servicios materializan
// (mail-security alimenta DOMAIN_MAP y las etiquetas, billing cuenta, el webmail revoca
// sesiones). Cada metodo ENCOLA en la outbox por la transaccion del contexto: se llama
// dentro de Transactor.InTx y su error revierte la escritura. Nunca lleva contrasenas ni
// hashes.
type EventPublisher interface {
	DomainCreated(ctx context.Context, d *domain.Domain) error
	DomainUpdated(ctx context.Context, d *domain.Domain) error
	DomainDeleted(ctx context.Context, d *domain.Domain) error
	DomainActivated(ctx context.Context, d *domain.Domain) error
	AliasDomainCreated(ctx context.Context, a *domain.AliasDomain) error
	AliasDomainUpdated(ctx context.Context, a *domain.AliasDomain) error
	AliasDomainDeleted(ctx context.Context, a *domain.AliasDomain) error
	MailboxCreated(ctx context.Context, m *domain.Mailbox) error
	MailboxUpdated(ctx context.Context, m *domain.Mailbox) error
	MailboxDeleted(ctx context.Context, m *domain.Mailbox) error
	MailboxCredentialsChanged(ctx context.Context, m *domain.Mailbox) error
	AliasCreated(ctx context.Context, a *domain.Alias) error
	AliasUpdated(ctx context.Context, a *domain.Alias) error
	AliasDeleted(ctx context.Context, a *domain.Alias) error
}

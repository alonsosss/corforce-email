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
	// ActiveOnly deja fuera los buzones que no estan activos del todo (active distinto de 1).
	ActiveOnly bool
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
	// ExistingIDs devuelve cuales de los ids son buzones de la empresa, en cualquier estado.
	ExistingIDs(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error)
	GetByUsername(ctx context.Context, tenantID uuid.UUID, username string) (*domain.Mailbox, error)
	Create(ctx context.Context, m *domain.Mailbox) error
	Update(ctx context.Context, m *domain.Mailbox) error
	UpdatePassword(ctx context.Context, tenantID, id uuid.UUID, hash string) error
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
	CountByDomain(ctx context.Context, tenantID uuid.UUID, name string) (int64, error)
	// QuotaSumByDomain suma las cuotas de los buzones del dominio salvo el excluido.
	QuotaSumByDomain(ctx context.Context, tenantID uuid.UUID, name string, exclude uuid.UUID) (int64, error)
	// CountByTenant cuenta los buzones de la empresa en la celda, para el limite de su plan.
	CountByTenant(ctx context.Context, tenantID uuid.UUID) (int64, error)
	// QuotaSumByTenant suma las cuotas ASIGNADAS a los buzones de la empresa salvo el
	// excluido. Es espacio comprometido, no ocupado: es lo que limita el plan.
	QuotaSumByTenant(ctx context.Context, tenantID uuid.UUID, exclude uuid.UUID) (int64, error)
	Quota(ctx context.Context, tenantID, id uuid.UUID) (*domain.QuotaUsage, error)
	DeleteQuotaUsage(ctx context.Context, tenantID uuid.UUID, username string) error
	Logins(ctx context.Context, tenantID uuid.UUID, username string, limit int) ([]domain.SASLLogin, error)
	// AddressInUse responde si la direccion ya es buzon, alias o alias temporal de la empresa.
	AddressInUse(ctx context.Context, tenantID uuid.UUID, address string) (bool, error)
	// RecordDeletion deja la marca de baja del buzon (mail.mailbox_deletions) que el barrido de maildir
	// de Dovecot consume al retirar su directorio del disco. Va en la transaccion del borrado.
	RecordDeletion(ctx context.Context, m *domain.Mailbox) error
	// DeletionPending dice si la direccion tiene, de cualquier empresa de la celda, una marca de baja mas
	// joven que hold que el barrido aun no consumio: su maildir sigue en el disco.
	DeletionPending(ctx context.Context, username string, hold time.Duration) (bool, error)
}

type AppPasswordRepository interface {
	List(ctx context.Context, tenantID, mailboxID uuid.UUID) ([]domain.AppPassword, error)
	Get(ctx context.Context, tenantID, mailboxID, id uuid.UUID) (*domain.AppPassword, error)
	Create(ctx context.Context, p *domain.AppPassword) error
	Update(ctx context.Context, p *domain.AppPassword) error
	Delete(ctx context.Context, tenantID, mailboxID, id uuid.UUID) error
	DeleteByMailbox(ctx context.Context, tenantID, mailboxID uuid.UUID) error
	// DeactivateByMailbox apaga las activas del buzon y cuenta las que apago.
	DeactivateByMailbox(ctx context.Context, tenantID, mailboxID uuid.UUID) (int64, error)
}

type SieveRepository interface {
	ByUsername(ctx context.Context, tenantID uuid.UUID, username string) ([]domain.SieveFilter, error)
	// Replace deja como unico filtro de ese tipo el indicado; con nil lo elimina.
	Replace(ctx context.Context, tenantID uuid.UUID, username, filterType string, f *domain.SieveFilter) error
	DeleteByUsername(ctx context.Context, tenantID uuid.UUID, username string) error
}

// VacationRepository guarda la respuesta automatica de un buzon (mail.vacation_replies). Una fila
// por buzon: Upsert la crea o la reemplaza entera.
type VacationRepository interface {
	// ByUsername devuelve domain.ErrNotFound si el buzon nunca la configuro.
	ByUsername(ctx context.Context, tenantID uuid.UUID, username string) (*domain.VacationReply, error)
	Upsert(ctx context.Context, v *domain.VacationReply) error
	DeleteByUsername(ctx context.Context, tenantID uuid.UUID, username string) error
}

// SignatureRepository guarda la firma de un buzon (mail.mailbox_signatures). Una fila por buzon.
type SignatureRepository interface {
	// ByUsername devuelve domain.ErrNotFound si el buzon nunca la guardo.
	ByUsername(ctx context.Context, tenantID uuid.UUID, username string) (*domain.MailboxSignature, error)
	Upsert(ctx context.Context, s *domain.MailboxSignature) error
	DeleteByUsername(ctx context.Context, tenantID uuid.UUID, username string) error
}

// FilterRepository guarda las reglas y el reenvio de un buzon (mail.mailbox_filters) con su script
// generado. Una fila por buzon.
type FilterRepository interface {
	// ByUsername devuelve domain.ErrNotFound si el buzon nunca guardo reglas.
	ByUsername(ctx context.Context, tenantID uuid.UUID, username string) (*domain.MailboxFilters, error)
	Upsert(ctx context.Context, f *domain.MailboxFilters) error
	DeleteByUsername(ctx context.Context, tenantID uuid.UUID, username string) error
}

// ScheduledSendRepository guarda los envios programados (mail.scheduled_sends). Las operaciones de un
// buzon van acotadas por empresa y nombre; las del trabajador (Claim, ForUpdate, Close) recorren toda
// la celda con el rol de servicio, como MailboxLocator.
type ScheduledSendRepository interface {
	Create(ctx context.Context, s *domain.ScheduledSend) error
	// ListByUsername devuelve las pendientes, en curso y fallidas del buzon, por hora de envio.
	ListByUsername(ctx context.Context, tenantID uuid.UUID, username string, limit int) ([]domain.ScheduledSend, error)
	// CountPending cuenta las pendientes y en curso del buzon.
	CountPending(ctx context.Context, tenantID uuid.UUID, username string) (int, error)
	// GetForUpdate bloquea la fila del buzon; domain.ErrNotFound si no es suya.
	GetForUpdate(ctx context.Context, tenantID uuid.UUID, username string, id uuid.UUID) (*domain.ScheduledSend, error)
	Reschedule(ctx context.Context, tenantID, id uuid.UUID, sendAt time.Time) (*domain.ScheduledSend, error)
	Cancel(ctx context.Context, tenantID, id uuid.UUID) error
	DeleteByUsername(ctx context.Context, tenantID uuid.UUID, username string) error
	// Claim cierra como failed las filas cuyo arriendo vencio sin intentos restantes, purga las terminadas mas
	// viejas que retention y reclama hasta p.Limit vencidas (pending con send_at pasado, o sending con
	// el arriendo vencido) con FOR UPDATE SKIP LOCKED: pasan a sending con arriendo y un intento mas.
	Claim(ctx context.Context, p domain.ClaimParams, maxAttempts int, retention time.Duration) ([]domain.ScheduledSend, error)
	// ClaimedForUpdate bloquea una fila de cualquier buzon de la celda.
	ClaimedForUpdate(ctx context.Context, id uuid.UUID) (*domain.ScheduledSend, error)
	// Close aplica la transicion con la hora de la base (send_at = now() + RetryAfter al reintentar).
	Close(ctx context.Context, id uuid.UUID, t domain.ScheduledTransition) (*domain.ScheduledSend, error)
}

// ReminderRepository guarda los recordatorios del webmail (mail.mailbox_reminders). Como
// ScheduledSendRepository: lo de un buzon va acotado por empresa y nombre; Claim, ClaimedForUpdate y
// Close recorren toda la celda con el rol de servicio.
type ReminderRepository interface {
	Create(ctx context.Context, r *domain.Reminder) error
	// ListByUsername devuelve los pendientes, en curso y fallidos del buzon de ese tipo, por hora.
	ListByUsername(ctx context.Context, tenantID uuid.UUID, username, kind string, limit int) ([]domain.Reminder, error)
	// CountActive cuenta los pendientes y en curso del buzon, de todos los tipos.
	CountActive(ctx context.Context, tenantID uuid.UUID, username string) (int, error)
	// GetForUpdate bloquea la fila del buzon; domain.ErrNotFound si no es suya.
	GetForUpdate(ctx context.Context, tenantID uuid.UUID, username string, id uuid.UUID) (*domain.Reminder, error)
	Reschedule(ctx context.Context, tenantID, id uuid.UUID, due time.Time) (*domain.Reminder, error)
	Cancel(ctx context.Context, tenantID, id uuid.UUID) error
	DeleteByUsername(ctx context.Context, tenantID uuid.UUID, username string) error
	// Claim hace lo mismo que ScheduledSendRepository.Claim sobre los recordatorios: cierra como failed
	// los arriendos vencidos sin intentos, purga los terminados mas viejos que retention y reclama hasta
	// p.Limit vencidos con FOR UPDATE SKIP LOCKED, que pasan a running con arriendo y un intento mas.
	Claim(ctx context.Context, p domain.ClaimParams, maxAttempts int, retention time.Duration) ([]domain.Reminder, error)
	// ClaimedForUpdate bloquea una fila de cualquier buzon de la celda.
	ClaimedForUpdate(ctx context.Context, id uuid.UUID) (*domain.Reminder, error)
	// Close aplica la transicion con la hora de la base (due_at = now() + RetryAfter al reintentar).
	Close(ctx context.Context, id uuid.UUID, t domain.ReminderTransition) (*domain.Reminder, error)
}

// QuickReplyRepository guarda las respuestas rapidas de un buzon (mail.mailbox_quick_replies). Un
// nombre repetido en el buzon es domain.ErrAlreadyExists.
type QuickReplyRepository interface {
	ListByUsername(ctx context.Context, tenantID uuid.UUID, username string) ([]domain.QuickReply, error)
	Count(ctx context.Context, tenantID uuid.UUID, username string) (int, error)
	Create(ctx context.Context, q *domain.QuickReply) error
	// Update reemplaza nombre y contenido; domain.ErrNotFound si la respuesta no es del buzon.
	Update(ctx context.Context, q *domain.QuickReply) error
	Delete(ctx context.Context, tenantID uuid.UUID, username string, id uuid.UUID) error
	DeleteByUsername(ctx context.Context, tenantID uuid.UUID, username string) error
}

// MTASTSRepository guarda la politica MTA-STS de los dominios de una empresa
// (mail.mta_sts_policies). Un dominio sin fila no publica politica.
type MTASTSRepository interface {
	// ByDomain devuelve domain.ErrNotFound si el dominio no tiene politica.
	ByDomain(ctx context.Context, tenantID uuid.UUID, name string) (*domain.MTASTSPolicy, error)
	// States lista los dominios de la empresa con su modo (none los que no tienen politica).
	States(ctx context.Context, tenantID uuid.UUID, page Page) ([]domain.MTASTSState, int64, error)
	// Upsert crea la politica del dominio o la reemplaza (modo, max_age y version).
	Upsert(ctx context.Context, p *domain.MTASTSPolicy) error
	DeleteByDomain(ctx context.Context, tenantID uuid.UUID, name string) error
}

// MTASTSPublisher lee la politica que se sirve a los remitentes, sin empresa en la peticion (es
// publica). Corre fuera de RLS con el rol de servicio.
type MTASTSPublisher interface {
	// Published devuelve domain.ErrNotFound si el dominio no esta activo o su modo es none.
	Published(ctx context.Context, name string) (*domain.MTASTSPolicy, error)
}

// MXResolver consulta los MX publicados de un dominio. Un dominio sin MX devuelve lista vacia sin
// error; solo un fallo de la consulta devuelve error.
type MXResolver interface {
	LookupMX(ctx context.Context, name string) ([]string, error)
}

// MailboxLocator resuelve un buzon por su nombre en toda la celda, sin empresa: lo pide el webmail,
// que se autentico como ese buzon y no conoce su empresa. Corre fuera de RLS, con el rol de servicio.
type MailboxLocator interface {
	// Locate devuelve domain.ErrNotFound si no hay un buzon activo con ese nombre.
	Locate(ctx context.Context, username string) (tenantID, mailboxID uuid.UUID, err error)
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
	// MailboxUpdated anuncia el cambio con los atributos que cambio (domain.MailboxChanges):
	// quien guarda sesiones del buzon decide con esa lista si tiene que cerrarlas.
	MailboxUpdated(ctx context.Context, m *domain.Mailbox, changed []domain.MailboxAttr) error
	MailboxDeleted(ctx context.Context, m *domain.Mailbox) error
	// MailboxCredentialsChanged anuncia que una credencial del buzon dejo de valer o perdio
	// protocolos: la contrasena principal cambio, o una de aplicacion perdio un inicio de sesion.
	// changed dice cual de las dos cosas fue: AttrPassword o AttrAppPassword cuando la credencial
	// misma cambio, y los atributos del buzon cuando lo que perdio fueron protocolos.
	MailboxCredentialsChanged(ctx context.Context, m *domain.Mailbox, credential domain.Credential, changed []domain.MailboxAttr) error
	AliasCreated(ctx context.Context, a *domain.Alias) error
	AliasUpdated(ctx context.Context, a *domain.Alias) error
	AliasDeleted(ctx context.Context, a *domain.Alias) error
}

// PlanAllowance es lo que el plan de la empresa incluye de un recurso: Limit es la cantidad
// incluida (-1 sin limite) y HardLimit si no admite excederse. Unknown queda a true cuando
// no se pudo consultar a billing, la empresa no tiene plan o el plan no fija ese recurso:
// quien la recibe no restringe.
type PlanAllowance struct {
	Limit     int64
	HardLimit bool
	Unknown   bool
	// SubscriptionInactive: la empresa tiene plan pero su suscripcion no esta vigente (dada
	// de baja o suspendida). No crece, sea cual sea el limite del plan.
	SubscriptionInactive bool
}

// PlanLimits consulta a billing lo que el plan de la empresa incluye. Una consulta que
// falla NO bloquea el alta: devuelve Unknown y quien la llama lo registra y sigue, igual
// que reputation con su derecho mensual. El consumo lo pone este servicio, que es el dueno
// del directorio de su celda.
type PlanLimits interface {
	Limit(ctx context.Context, tenantID uuid.UUID, resource string) (PlanAllowance, error)
}

// Motivos por los que un alta se resuelve sin aplicar el limite del plan. Conjunto cerrado:
// son etiquetas de metrica.
const (
	// PlanSkipUnreachable: billing no respondio. Es el caso que hay que vigilar.
	PlanSkipUnreachable = "unreachable"
	// PlanSkipNoPlan: la empresa no tiene plan o su plan no fija ese recurso. Es normal
	// mientras no haya planes creados.
	PlanSkipNoPlan = "sin_plan"
	// PlanSkipInactiva: la empresa esta dada de baja. No es un fallo: se deniega el
	// crecimiento a proposito.
	PlanSkipInactiva = "suscripcion_inactiva"
)

// Metrics son las metricas propias del directorio. Opcional: sin ella no se mide nada y
// todo lo demas funciona igual.
type Metrics interface {
	// PlanLimitsConfigured publica si hay a quien preguntar los limites del plan.
	PlanLimitsConfigured(ok bool)
	// PlanLimitSkipped cuenta una decision tomada sin el limite del plan.
	PlanLimitSkipped(motivo string)
}

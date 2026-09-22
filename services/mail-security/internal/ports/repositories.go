package ports

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// PolicyRepository son las tablas propias vistas desde el API de administracion: cada
// metodo va acotado por empresa y corre dentro de TransactRLS.
type PolicyRepository interface {
	ListSpamScores(ctx context.Context, tenantID uuid.UUID) ([]domain.SpamScore, error)
	GetSpamScore(ctx context.Context, tenantID uuid.UUID, object string) (*domain.SpamScore, error)
	UpsertSpamScore(ctx context.Context, s *domain.SpamScore) error
	DeleteSpamScore(ctx context.Context, tenantID uuid.UUID, object string) error

	ListAddressLists(ctx context.Context, tenantID uuid.UUID, object string, kind domain.ListKind) ([]domain.AddressListEntry, error)
	CreateAddressList(ctx context.Context, e *domain.AddressListEntry) error
	DeleteAddressList(ctx context.Context, tenantID, id uuid.UUID) error

	ListSettingsMaps(ctx context.Context, tenantID uuid.UUID) ([]domain.SettingsMap, error)
	GetSettingsMap(ctx context.Context, tenantID, id uuid.UUID) (*domain.SettingsMap, error)
	CreateSettingsMap(ctx context.Context, m *domain.SettingsMap) error
	UpdateSettingsMap(ctx context.Context, m *domain.SettingsMap) error
	DeleteSettingsMap(ctx context.Context, tenantID, id uuid.UUID) error

	ListFooters(ctx context.Context, tenantID uuid.UUID) ([]domain.DomainFooter, error)
	GetFooter(ctx context.Context, tenantID uuid.UUID, domainName string) (*domain.DomainFooter, error)
	UpsertFooter(ctx context.Context, f *domain.DomainFooter) error
	DeleteFooter(ctx context.Context, tenantID uuid.UUID, domainName string) error

	ListForwardingHosts(ctx context.Context, tenantID uuid.UUID) ([]domain.ForwardingHost, error)
	GetForwardingHost(ctx context.Context, tenantID, id uuid.UUID) (*domain.ForwardingHost, error)
	CreateForwardingHost(ctx context.Context, h *domain.ForwardingHost) error
	DeleteForwardingHost(ctx context.Context, tenantID, id uuid.UUID) error

	ListRateLimits(ctx context.Context, tenantID uuid.UUID) ([]domain.RateLimit, error)
	GetRateLimit(ctx context.Context, tenantID uuid.UUID, object string) (*domain.RateLimit, error)
	UpsertRateLimit(ctx context.Context, r *domain.RateLimit) error
	DeleteRateLimit(ctx context.Context, tenantID uuid.UUID, object string) error

	ListMailboxTags(ctx context.Context, tenantID uuid.UUID) ([]domain.MailboxTags, error)
	GetMailboxTags(ctx context.Context, tenantID uuid.UUID, username string) (*domain.MailboxTags, error)
	UpsertMailboxTags(ctx context.Context, t *domain.MailboxTags) error

	ListSMTPAccess(ctx context.Context, tenantID uuid.UUID) ([]domain.SMTPAccess, error)
	// GetSMTPAccess devuelve ErrNotFound si el buzon no tiene redes.
	GetSMTPAccess(ctx context.Context, tenantID uuid.UUID, username string) (*domain.SMTPAccess, error)
	// ReplaceSMTPAccess deja como unicas redes del buzon las dadas.
	ReplaceSMTPAccess(ctx context.Context, tenantID uuid.UUID, username string, networks []netip.Prefix) error
	// DeleteSMTPAccess devuelve ErrNotFound si el buzon no tenia redes.
	DeleteSMTPAccess(ctx context.Context, tenantID uuid.UUID, username string) error

	GetQuarantineSettings(ctx context.Context, tenantID uuid.UUID) (*domain.QuarantineSettings, error)
	UpsertQuarantineSettings(ctx context.Context, s *domain.QuarantineSettings) error
}

// PolicyReader son las mismas tablas leidas para los motores y la reconciliacion de
// Redis: sin empresa en la peticion, como duena del pool y a lo ancho de la celda.
type PolicyReader interface {
	AllSpamScores(ctx context.Context) ([]domain.SpamScore, error)
	AllAddressLists(ctx context.Context) ([]domain.AddressListEntry, error)
	AllActiveSettingsMaps(ctx context.Context) ([]domain.SettingsMap, error)
	// PolicyUpdatedAt es el ultimo updated_at de las tablas que alimentan /settings.
	PolicyUpdatedAt(ctx context.Context) (time.Time, error)
	FooterByDomain(ctx context.Context, domainName string) (*domain.DomainFooter, error)
	AllForwardingHosts(ctx context.Context) ([]domain.ForwardingHost, error)
	AllRateLimits(ctx context.Context) ([]domain.RateLimit, error)
	AllMailboxTags(ctx context.Context) ([]domain.MailboxTags, error)
	AllSMTPAccess(ctx context.Context) ([]domain.SMTPAccess, error)
	AllQuarantineSettings(ctx context.Context) ([]domain.QuarantineSettings, error)
	QuarantineSettingsFor(ctx context.Context, tenantID uuid.UUID) (*domain.QuarantineSettings, error)
	AllFirewallNetworks(ctx context.Context) ([]domain.FirewallNetwork, error)
	// FirewallOptions devuelve ErrNotFound si la plataforma no las ha fijado.
	FirewallOptions(ctx context.Context) (*domain.FirewallOptions, error)
	DeleteMailboxTagsByUsername(ctx context.Context, username string) error
	DeleteSMTPAccessByUsername(ctx context.Context, username string) error
}

// FirewallRepository escribe las tablas del cortafuegos de la celda. Son de plataforma,
// sin empresa: corre como duena del pool (OwnerTransactor) tras exigir al operador.
type FirewallRepository interface {
	CreateFirewallNetwork(ctx context.Context, n *domain.FirewallNetwork) error
	// DeleteFirewallNetwork devuelve la red borrada o ErrNotFound.
	DeleteFirewallNetwork(ctx context.Context, id uuid.UUID) (*domain.FirewallNetwork, error)
	UpsertFirewallOptions(ctx context.Context, o *domain.FirewallOptions) error
}

// QuarantineRepository es la cuarentena. Insert y las podas las usa el exportador (sin
// empresa en el contexto); el resto, el API de administracion bajo RLS.
type QuarantineRepository interface {
	Insert(ctx context.Context, item *domain.QuarantineItem) error
	PruneRcpt(ctx context.Context, tenantID uuid.UUID, rcpt string, keep int) (int64, error)
	PruneAged(ctx context.Context, tenantID uuid.UUID, maxAgeDays int) (int64, error)
	List(ctx context.Context, tenantID uuid.UUID, f domain.QuarantineFilter) ([]domain.QuarantineItem, int64, error)
	Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.QuarantineItem, error)
	GetMessage(ctx context.Context, tenantID, id uuid.UUID) ([]byte, error)
	// LockForRelease devuelve la fila con su mensaje y la bloquea hasta el fin de la
	// transaccion: dos liberaciones simultaneas no entregan el mensaje dos veces.
	LockForRelease(ctx context.Context, tenantID, id uuid.UUID) (*domain.QuarantineItem, error)
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
}

// QuarantineNoticeRepository es lo que el aviso de cuarentena y sus enlaces sin sesion
// leen y escriben. Los metodos del barrido corren sin empresa en la peticion (a lo ancho
// de la celda) y filtran por tenant_id en el SQL.
type QuarantineNoticeRepository interface {
	// PendingNotices devuelve, de una empresa, los mensajes sin avisar con puntuacion hasta
	// maxScore, como mucho perMailbox por buzon (los mas recientes), sin el mensaje crudo.
	PendingNotices(ctx context.Context, tenantID uuid.UUID, maxScore decimal.Decimal, perMailbox int) ([]domain.QuarantineItem, error)
	// MarkNotified marca los mensajes como avisados. Va en la transaccion de InsertNotice.
	MarkNotified(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) error
	// InsertNotice registra el aviso; una clave de idempotencia ya registrada no se repite.
	InsertNotice(ctx context.Context, n *domain.QuarantineNotice) error
	// FindByQHash devuelve la fila de un enlace (sin el mensaje crudo) o ErrNotFound.
	FindByQHash(ctx context.Context, tenantID uuid.UUID, qhash string) (*domain.QuarantineItem, error)
	// LockForLink bloquea la fila hasta el fin de la transaccion, sin el mensaje crudo.
	LockForLink(ctx context.Context, tenantID, id uuid.UUID) (*domain.QuarantineItem, error)
	// InsertLinkUse registra el uso del enlace de un mensaje; ErrLinkUsed si ya lo tenia.
	InsertLinkUse(ctx context.Context, u *domain.QuarantineLinkUse) error
	// PruneHistory borra avisos y usos de enlace mas antiguos que el max_age_days de su
	// empresa, o que defaultMaxAgeDays si la empresa no tiene ajustes.
	PruneHistory(ctx context.Context, defaultMaxAgeDays int) (int64, error)
}

// NoticeReceipt es lo que transactional devuelve al aceptar un aviso.
type NoticeReceipt struct {
	MessageID  *uuid.UUID
	Status     string
	Suppressed bool
}

// ErrNoticeUnavailable: transactional no respondio, respondio 5xx, 429 o rechazo la
// credencial interna. El aviso se reintenta en el siguiente barrido con la misma clave.
var ErrNoticeUnavailable = errors.New("transactional no disponible")

// NoticeRejectedError es un 4xx de negocio de transactional (remitente sin verificar,
// envio restringido, validacion): reintentar daria lo mismo.
type NoticeRejectedError struct {
	Status  int
	Code    string
	Message string
}

func (e *NoticeRejectedError) Error() string {
	return fmt.Sprintf("transactional rechazo el aviso (%d %s): %s", e.Status, e.Code, e.Message)
}

// NoticeSender es transactional visto desde este servicio: el envio interno con
// purpose=quarantine_notice.
type NoticeSender interface {
	SendQuarantineNotice(ctx context.Context, tenantID uuid.UUID, m domain.NoticeMail) (*NoticeReceipt, error)
}

// DocumentStamps guarda la marca de modificacion de los documentos que sondean los
// motores, compartida por todas las replicas. Corre en una OwnerTransactor.
type DocumentStamps interface {
	// LockDocument lee la marca bloqueando su fila; nil si aun no hay ninguna.
	LockDocument(ctx context.Context, document string) (*domain.DocumentStamp, error)
	// SaveDocument guarda la marca y devuelve la que queda en la base, que puede ser la de
	// otra replica si ambas daban de alta el documento a la vez.
	SaveDocument(ctx context.Context, s domain.DocumentStamp) (domain.DocumentStamp, error)
}

// DirectoryReader es lo que este servicio lee del directorio de correo: solo las vistas
// publicadas mail.v_routing_* (columnas enumeradas por mail-directory).
type DirectoryReader interface {
	// AliasGoto devuelve el destino (lista separada por comas) de un alias que recibe.
	AliasGoto(ctx context.Context, address string) (string, bool, error)
	// AliasDomainTarget devuelve el dominio destino de un dominio alias activo.
	AliasDomainTarget(ctx context.Context, domainName string) (string, bool, error)
	// MailboxByUsername devuelve el buzon (cualquier estado) o ErrNotFound.
	MailboxByUsername(ctx context.Context, username string) (*domain.Mailbox, error)
	// ActiveDomains lista dominios activos y dominios alias activos de la celda.
	ActiveDomains(ctx context.Context) ([]string, error)
	// DomainActive dice si un dominio (o dominio alias) concreto esta activo.
	DomainActive(ctx context.Context, domainName string) (bool, error)
	// ObjectOwnedBy comprueba que un buzon o dominio pertenece a la empresa.
	ObjectOwnedBy(ctx context.Context, tenantID uuid.UUID, object string) (bool, error)
	// DomainStates devuelve empresa y estado de los dominios propios del directorio (no los
	// dominios alias) de entre los dados; el que no figura no tiene entrada.
	DomainStates(ctx context.Context, names []string) (map[string]domain.DirectoryDomain, error)
	// AliasDomainsOf lista los dominios alias que apuntan a un dominio.
	AliasDomainsOf(ctx context.Context, targetDomain string) ([]string, error)
	// AliasesTargeting lista las direcciones de alias cuyo destino incluye el buzon.
	AliasesTargeting(ctx context.Context, username string) ([]string, error)
	// BCCDestination consulta mail.v_routing_bcc_maps (kind 'rcpt' o 'sender').
	BCCDestination(ctx context.Context, kind, localDest string) (string, bool, error)
	// InternalAliases lista los aliases activos marcados como internos de la celda.
	InternalAliases(ctx context.Context) ([]domain.InternalAlias, error)
}

// EngineStore es Redis visto desde este servicio: el unico escritor de las claves que
// los motores leen.
type EngineStore interface {
	Ping(ctx context.Context) error
	Get(ctx context.Context, key string) (string, bool, error)
	HSet(ctx context.Context, key, field, value string) error
	HDel(ctx context.Context, key string, fields ...string) error
	HGet(ctx context.Context, key, field string) (string, bool, error)
	HGetAll(ctx context.Context, key string) (map[string]string, error)
	// HKeys lista los campos de un hash que casan con un patron glob (HSCAN MATCH).
	HKeys(ctx context.Context, key, pattern string) ([]string, error)
	// Keys lista las claves que casan con un patron glob (SCAN MATCH, sin bloquear Redis).
	Keys(ctx context.Context, pattern string) ([]string, error)
	Del(ctx context.Context, keys ...string) error
	Set(ctx context.Context, key, value string) error
	LPushTrim(ctx context.Context, key, value string, maxLen int64) error
}

// DKIMDomainLock serializa por dominio, entre todas las replicas de la celda, la decision de
// escribir o retirar sus claves DKIM: fn corre con el cerrojo tomado y lee el directorio en la
// misma transaccion, de modo que una publicacion y una retirada del mismo dominio no se cruzan.
type DKIMDomainLock interface {
	WithDomainLock(ctx context.Context, domainName string, fn func(ctx context.Context) error) error
}

// TenantRegistry es organization visto desde este servicio.
type TenantRegistry interface {
	// TenantGone es true solo si organization responde que la empresa no existe (baja
	// terminada). Sin respuesta aplicable no se sabe y devuelve error.
	TenantGone(ctx context.Context, tenantID uuid.UUID) (bool, error)
}

// DKIMReconcileMetrics cuenta el repaso de las claves DKIM de los motores.
type DKIMReconcileMetrics interface {
	// DKIMKeysRemoved suma los dominios a los que el repaso retiro las claves, por motivo.
	DKIMKeysRemoved(reason domain.DKIMRemovalReason, domains int)
	// DKIMUnresolved suma los dominios cuyas claves se conservaron sin respuesta de organization.
	DKIMUnresolved(domains int)
	// DKIMReconciled anota el instante de una pasada completa.
	DKIMReconciled(at time.Time)
}

// EngineSessions es Dovecot visto desde este servicio: su API de administracion de la celda.
type EngineSessions interface {
	// ForgetCredentials vacia la cache de autenticacion del buzon y, con kick, cierra despues sus
	// sesiones abiertas. Idempotente: un buzon sin entradas ni sesiones no es un error. Los errores
	// envuelven domain.ErrEngineUnreachable, ErrEngineRejected o ErrEngineCommand.
	ForgetCredentials(ctx context.Context, username string, kick bool) error
}

// EngineQueue es la cola de Postfix de la celda vista desde este servicio: el agente que corre dentro
// del contenedor de Postfix. Los errores envuelven domain.ErrEngineUnreachable, ErrEngineRejected o
// ErrEngineCommand; un mensaje que ya no esta en la cola es domain.ErrNotFound.
type EngineQueue interface {
	List(ctx context.Context, limit int) (domain.QueueListing, error)
	Apply(ctx context.Context, action domain.QueueAction, id string) error
	Flush(ctx context.Context) error
}

// QueueMetrics anota lo que el monitor ve en la cola de Postfix.
type QueueMetrics interface {
	// QueueObserved anota el conteo por cola y el instante del mensaje mas antiguo (cero con la cola
	// vacia) de una consulta que salio bien.
	QueueObserved(counts map[string]int, oldestArrival time.Time)
	// QueuePollFailed anota una consulta que fallo.
	QueuePollFailed()
}

// QuarantineMetrics cuenta lo que pasa con el correo en cuarentena de la celda. Sirve para medir falsos
// positivos del antispam: cuanto de lo retenido acaba liberado por su dueno (docs/Plan_Estrategico_Mejoras_Correo.md,
// A4). Son totales de la celda, sin etiqueta de empresa ni de buzon: una etiqueta asi no acota su cardinalidad.
type QuarantineMetrics interface {
	QuarantineStored()
	QuarantineReleased()
	QuarantineDiscarded()
	QuarantineLearnedSpam()
	QuarantineLearnedHam()
}

// SessionRevocationMetrics cuenta la revocacion de credenciales en Dovecot.
type SessionRevocationMetrics interface {
	SessionsRevoked(action domain.SessionAction)
	SessionRevocationFailed(reason domain.SessionRevocationFailure)
}

// EventPublisher encola los eventos del servicio en la outbox de la celda por la
// transaccion del contexto: se llama DENTRO de ella y su error la revierte, de modo que
// el evento existe si y solo si existe el cambio que lo origina.
type EventPublisher interface {
	QuarantineStored(ctx context.Context, item *domain.QuarantineItem) error
	QuarantineReleased(ctx context.Context, item *domain.QuarantineItem, userID string) error
}

// Reinjector devuelve un mensaje de cuarentena al flujo de entrega (SMTP interno).
type Reinjector interface {
	Reinject(ctx context.Context, sender, rcpt string, msg []byte) error
}

// SpamLearner entrena el clasificador de Rspamd con un mensaje.
type SpamLearner interface {
	LearnSpam(ctx context.Context, msg []byte) error
	LearnHam(ctx context.Context, msg []byte) error
}

// AntispamInspector lee del controller de Rspamd sus contadores (GET /stat) y su historial reciente
// (GET /history): solo lectura, nunca configuracion, pesos ni entrenamiento. Sin contrasena devuelve
// domain.ErrNotConfigured; un controller que no responde, domain.ErrEngineUnreachable.
type AntispamInspector interface {
	Stats(ctx context.Context) (domain.RspamdStats, error)
	History(ctx context.Context) ([]domain.RspamdHistoryRow, error)
}

// Transactor abre la transaccion bajo la que corre el API de administracion: cambia al
// rol sujeto a RLS y fija la empresa de la peticion (pkg/db.ContextPool.TransactRLS).
type Transactor interface {
	TransactRLS(ctx context.Context, fn func(ctx context.Context) error) error
}

// OwnerTransactor abre una transaccion como duena del pool, sin rol sujeto a RLS
// (pkg/db.ContextPool.Transact). La usan el camino de los motores, que no tiene empresa
// en la peticion, y la administracion del cortafuegos, cuyas tablas son de plataforma.
type OwnerTransactor interface {
	Transact(ctx context.Context, fn func(ctx context.Context) error) error
}

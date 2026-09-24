package app

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	defaultPageSize = 50
	maxPageSize     = 200
	defaultLogins   = 50
	maxLogins       = 500
)

// Deps agrupa los puertos por nombre: con tantos repositorios del mismo esquema, un
// constructor posicional invita a cruzar dos del mismo tipo.
type Deps struct {
	Tx           ports.Transactor
	Domains      ports.DomainRepository
	AliasDomains ports.AliasDomainRepository
	Mailboxes    ports.MailboxRepository
	AppPasswords ports.AppPasswordRepository
	Sieve        ports.SieveRepository
	Vacation     ports.VacationRepository
	Signatures   ports.SignatureRepository
	Filters      ports.FilterRepository
	Scheduled    ports.ScheduledSendRepository
	Locator      ports.MailboxLocator
	Aliases      ports.AliasRepository
	SpamAliases  ports.SpamAliasRepository
	SenderACL    ports.SenderACLRepository
	Relayhosts   ports.RelayhostRepository
	Transports   ports.TransportRepository
	TLSPolicies  ports.TLSPolicyRepository
	RecipientMap ports.RecipientMapRepository
	BCCMaps      ports.BCCMapRepository
	Senders      ports.SenderIdentityRepository
	Retirements  ports.RetirementRepository
	MTASTS       ports.MTASTSRepository
	MTASTSPublic ports.MTASTSPublisher
	MX           ports.MXResolver
	// PlatformMX es MAIL_MX_HOSTNAME: el unico mx de toda politica MTA-STS y el MX que enforce
	// exige en el DNS del dominio.
	PlatformMX string
	// DAVServerURL es MAIL_DAV_PUBLIC_URL, ya validada; vacia si mail-dav no se publica.
	DAVServerURL string
	// MailboxRecreateHold es MAIL_DIRECTORY_MAILBOX_RECREATE_HOLD: mientras la marca de baja de una
	// direccion sea mas joven que esto y el barrido de Dovecot no la haya consumido, no se crea un buzon
	// con ese nombre (su maildir anterior sigue en el disco). Cero desactiva la retencion.
	MailboxRecreateHold time.Duration
	Secrets             ports.Secrets
	Events              ports.EventPublisher
	// Plan consulta a billing lo que incluye el plan de la empresa. Opcional: sin el, el
	// directorio aplica solo los limites del dominio, como antes de que hubiera planes.
	Plan ports.PlanLimits
	// Metrics es opcional: sin ella no se mide nada y todo lo demas funciona igual.
	Metrics ports.Metrics
	// Clock es opcional (time.Now): las pruebas fijan la hora con la que se validan los envios programados.
	Clock  func() time.Time
	Logger *zap.Logger
}

type UseCase struct {
	tx              ports.Transactor
	domains         ports.DomainRepository
	aliasDomains    ports.AliasDomainRepository
	mailboxes       ports.MailboxRepository
	appPasswords    ports.AppPasswordRepository
	sieve           ports.SieveRepository
	vacation        ports.VacationRepository
	signatures      ports.SignatureRepository
	filters         ports.FilterRepository
	scheduled       ports.ScheduledSendRepository
	locator         ports.MailboxLocator
	aliases         ports.AliasRepository
	spamAliases     ports.SpamAliasRepository
	senderACL       ports.SenderACLRepository
	relayhosts      ports.RelayhostRepository
	transports      ports.TransportRepository
	tlsPolicies     ports.TLSPolicyRepository
	recipientMap    ports.RecipientMapRepository
	bccMaps         ports.BCCMapRepository
	senders         ports.SenderIdentityRepository
	retirements     ports.RetirementRepository
	mtaSTS          ports.MTASTSRepository
	mtaSTSPublisher ports.MTASTSPublisher
	mx              ports.MXResolver
	platformMX      string
	davServerURL    string
	recreateHold    time.Duration
	secrets         ports.Secrets
	events          ports.EventPublisher
	plan            ports.PlanLimits
	metrics         ports.Metrics
	now             func() time.Time
	logger          *zap.Logger
}

func New(d Deps) *UseCase {
	logger := d.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	now := d.Clock
	if now == nil {
		now = time.Now
	}
	return &UseCase{
		tx: d.Tx, domains: d.Domains, aliasDomains: d.AliasDomains, mailboxes: d.Mailboxes,
		appPasswords: d.AppPasswords, sieve: d.Sieve, vacation: d.Vacation,
		signatures: d.Signatures, filters: d.Filters, scheduled: d.Scheduled, locator: d.Locator, aliases: d.Aliases, spamAliases: d.SpamAliases,
		senderACL: d.SenderACL, relayhosts: d.Relayhosts, transports: d.Transports,
		tlsPolicies: d.TLSPolicies, recipientMap: d.RecipientMap, bccMaps: d.BCCMaps,
		senders: d.Senders, retirements: d.Retirements, mtaSTS: d.MTASTS, mtaSTSPublisher: d.MTASTSPublic, mx: d.MX,
		platformMX: d.PlatformMX, davServerURL: d.DAVServerURL, recreateHold: d.MailboxRecreateHold,
		secrets: d.Secrets, events: d.Events, plan: d.Plan, metrics: d.Metrics, now: now, logger: logger,
	}
}

// writeTx abre la transaccion de una escritura en el directorio de la empresa. Una empresa dada de
// baja en la celda (RetireTenant) no escribe nada: ErrTenantRetired. El cerrojo compartido de la
// empresa ordena la escritura respecto de la baja, que toma el exclusivo: la que llega segunda
// espera a la primera, asi que nada de lo que la baja apaga vuelve a encenderse despues de ella.
func (uc *UseCase) writeTx(ctx context.Context, tenantID uuid.UUID, fn func(ctx context.Context) error) error {
	return uc.tx.InTx(ctx, func(ctx context.Context) error {
		retired, err := uc.retirements.HoldShared(ctx, tenantID)
		if err != nil {
			return err
		}
		if retired {
			return domain.ErrTenantRetired
		}
		return fn(ctx)
	})
}

// Scope es quien actua: la empresa de la peticion y si ademas opera la plataforma (lo
// unico que habilita administrar rutas sin empresa).
type Scope struct {
	TenantID uuid.UUID
	Platform bool
}

func (s Scope) transportScope() ports.TransportScope {
	return ports.TransportScope{TenantID: s.TenantID, Platform: s.Platform}
}

// NormalizePage acota pagina y tamano y devuelve el desplazamiento correspondiente.
func NormalizePage(page, perPage int) (int, int, ports.Page) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = defaultPageSize
	}
	if perPage > maxPageSize {
		perPage = maxPageSize
	}
	return page, perPage, ports.Page{Offset: (page - 1) * perPage, Limit: perPage}
}

// normalizeSearch recorta el texto de busqueda de un listado y rechaza el que supera el
// tope.
func normalizeSearch(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if utf8.RuneCountInString(s) > domain.MaxSearchLength {
		return "", domain.ErrSearchTooLong
	}
	return s, nil
}

// ownDomain resuelve un dominio propio de la empresa por nombre.
func (uc *UseCase) ownDomain(ctx context.Context, tenantID uuid.UUID, name string) (*domain.Domain, error) {
	d, err := uc.domains.GetByName(ctx, tenantID, name)
	if err == domain.ErrNotFound {
		return nil, domain.ErrDomainNotOwned
	}
	return d, err
}

// ownsDomainOrAlias indica si la empresa sirve ese dominio, propio o alias.
func (uc *UseCase) ownsDomainOrAlias(ctx context.Context, tenantID uuid.UUID, name string) error {
	if _, err := uc.domains.GetByName(ctx, tenantID, name); err == nil {
		return nil
	} else if err != domain.ErrNotFound {
		return err
	}
	ok, err := uc.aliasDomains.ExistsByName(ctx, tenantID, name)
	if err != nil {
		return err
	}
	if !ok {
		return domain.ErrDomainNotOwned
	}
	return nil
}

// ownMailbox exige que la direccion sea un buzon de la empresa.
func (uc *UseCase) ownMailbox(ctx context.Context, tenantID uuid.UUID, username string) (*domain.Mailbox, error) {
	m, err := uc.mailboxes.GetByUsername(ctx, tenantID, username)
	if err == domain.ErrNotFound {
		return nil, domain.ErrMailboxNotOwned
	}
	return m, err
}

func (uc *UseCase) ownRelayhost(ctx context.Context, tenantID uuid.UUID, id *uuid.UUID) error {
	if id == nil {
		return nil
	}
	if _, err := uc.relayhosts.Get(ctx, tenantID, *id); err != nil {
		if err == domain.ErrNotFound {
			return domain.ErrRelayhostNotOwned
		}
		return err
	}
	return nil
}

// addressFree exige que la direccion no sea ya buzon, alias ni alias temporal.
func (uc *UseCase) addressFree(ctx context.Context, tenantID uuid.UUID, address string) error {
	taken, err := uc.mailboxes.AddressInUse(ctx, tenantID, address)
	if err != nil {
		return err
	}
	if taken {
		return domain.ErrAddressTaken
	}
	return nil
}

// planLimit consulta a billing lo que el plan de la empresa incluye del recurso. Nunca
// falla hacia arriba: si no hay a quien preguntar o billing no responde, devuelve un limite
// desconocido, que no restringe, y lo deja en el registro. Un billing caido no puede dejar
// a una empresa sin poder crear buzones; el limite del dominio se aplica igual.
func (uc *UseCase) planLimit(ctx context.Context, tenantID uuid.UUID, resource string) domain.PlanLimit {
	if uc.plan == nil {
		uc.planSkipped(ports.PlanSkipNoPlan)
		return domain.PlanLimit{Unknown: true}
	}
	allowance, err := uc.plan.Limit(ctx, tenantID, resource)
	if err != nil {
		uc.logger.Warn("mail-directory: billing no respondio; se sigue sin el limite del plan",
			zap.String("tenant_id", tenantID.String()), zap.String("resource", resource), zap.Error(err))
		uc.planSkipped(ports.PlanSkipUnreachable)
		return domain.PlanLimit{Unknown: true}
	}
	if allowance.SubscriptionInactive {
		uc.planSkipped(ports.PlanSkipInactiva)
	} else if allowance.Unknown {
		uc.planSkipped(ports.PlanSkipNoPlan)
	}
	return domain.PlanLimit{
		Included: allowance.Limit, HardLimit: allowance.HardLimit, Unknown: allowance.Unknown,
		SubscriptionInactive: allowance.SubscriptionInactive,
	}
}

// Recursos de billing que limitan el directorio. Los nombres son el contrato de
// billing (services/billing/internal/domain/resource.go).
const (
	planResourceMailboxes = "mailboxes"
	planResourceStorage   = "storage_bytes"
)

// planSkipped anota que se decidio sin el limite del plan. La metrica es opcional.
func (uc *UseCase) planSkipped(motivo string) {
	if uc.metrics != nil {
		uc.metrics.PlanLimitSkipped(motivo)
	}
}

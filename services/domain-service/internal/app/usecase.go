package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Valores por defecto de los plazos que si admiten un defecto razonable en codigo.
const (
	// DefaultDKIMRotationGrace: la clave anterior de una rotacion programada se conserva, desde la
	// ultima vez que pudo firmar, mas de lo que un mensaje firmado con ella puede seguir en la cola
	// de Postfix (maximal_queue_lifetime, deploy/mail/postfix/conf/main.cf) mas un TTL habitual de
	// un TXT: un receptor que lo recibe al final de la cola tiene que poder leer su TXT. El minimo
	// de main.go es esa suma; esto deja un dia mas para el receptor que comprueba DKIM despues de
	// aceptar el mensaje. ops/scaffold/check-dkim-grace.sh lo comprueba contra main.cf.
	DefaultDKIMRotationGrace = 168 * time.Hour
	// DefaultPendingRecheckWindow: un dominio pendiente se reverifica solo mientras es
	// reciente; despues es el cliente quien lanza la verificacion.
	DefaultPendingRecheckWindow = 7 * 24 * time.Hour
	DefaultCheckRetention       = 30 * 24 * time.Hour
	defaultPageSize             = 50
	maxPageSize                 = 200
)

// Deps agrupa los puertos y la configuracion del caso de uso.
type Deps struct {
	Repo          ports.Repository
	DNS           ports.DNSResolver
	Cipher        ports.Cipher
	MailDirectory ports.MailDirectoryClient
	MailSecurity  ports.MailSecurityClient
	// DomainIndex es el indice global de dominios activos de organization.
	DomainIndex ports.DomainIndex
	Events      ports.EventPublisher
	// KeyEvents encola los eventos de las claves DKIM en la transaccion que las cambia.
	KeyEvents ports.KeyEvents
	// Platform son los valores que aparecen en los registros del cliente.
	Platform domain.PlatformDNS
	// PlatformHostname es MAIL_HOSTNAME: ni el ni sus subdominios se dan de alta.
	PlatformHostname     string
	DKIMRotationGrace    time.Duration
	PendingRecheckWindow time.Duration
	CheckRetention       time.Duration
	Logger               *zap.Logger
	// Now permite fijar el reloj en las pruebas. Nil = time.Now.
	Now func() time.Time
}

type UseCase struct {
	repo          ports.Repository
	dns           ports.DNSResolver
	cipher        ports.Cipher
	mailDirectory ports.MailDirectoryClient
	mailSecurity  ports.MailSecurityClient
	index         ports.DomainIndex
	events        ports.EventPublisher
	keyEvents     ports.KeyEvents
	platform      domain.PlatformDNS
	platformHost  string
	rotationGrace time.Duration
	pendingWindow time.Duration
	retention     time.Duration
	logger        *zap.Logger
	now           func() time.Time
}

func New(d Deps) *UseCase {
	uc := &UseCase{
		repo: d.Repo, dns: d.DNS, cipher: d.Cipher,
		mailDirectory: d.MailDirectory, mailSecurity: d.MailSecurity, index: d.DomainIndex, events: d.Events, keyEvents: d.KeyEvents,
		platform: d.Platform, platformHost: d.PlatformHostname,
		rotationGrace: d.DKIMRotationGrace, pendingWindow: d.PendingRecheckWindow,
		retention: d.CheckRetention, logger: d.Logger, now: d.Now,
	}
	if uc.logger == nil {
		uc.logger = zap.NewNop()
	}
	if uc.now == nil {
		uc.now = time.Now
	}
	if uc.rotationGrace <= 0 {
		uc.rotationGrace = DefaultDKIMRotationGrace
	}
	if uc.pendingWindow <= 0 {
		uc.pendingWindow = DefaultPendingRecheckWindow
	}
	if uc.retention <= 0 {
		uc.retention = DefaultCheckRetention
	}
	return uc
}

// ExpectedRecords devuelve los registros que el cliente debe publicar para el dominio.
func (uc *UseCase) ExpectedRecords(d *domain.Domain) []domain.DNSRecord {
	return domain.ExpectedRecords(d, uc.platform)
}

// NormalizePage acota la paginacion de los listados.
func NormalizePage(page, perPage int) (int, int) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = defaultPageSize
	}
	if perPage > maxPageSize {
		perPage = maxPageSize
	}
	return page, perPage
}

type CreateRequest struct {
	Domain      string
	Purpose     string
	DMARCPolicy string
}

// Create da de alta el dominio con su token de propiedad y su primer par DKIM. Queda en
// pending hasta que Verify confirme los registros.
func (uc *UseCase) Create(ctx context.Context, tenantID uuid.UUID, req CreateRequest) (*domain.Domain, error) {
	name := domain.NormalizeDomainName(req.Domain)
	if err := domain.ValidateDomainName(name, uc.platformHost); err != nil {
		return nil, err
	}
	purpose := domain.Purpose(req.Purpose)
	if !purpose.Valid() {
		return nil, domain.ErrInvalidPurpose
	}
	policy := domain.DMARCQuarantine
	if req.DMARCPolicy != "" {
		policy = domain.DMARCPolicy(req.DMARCPolicy)
		if !policy.Valid() {
			return nil, domain.ErrInvalidDMARCPolicy
		}
	}
	if existing, err := uc.repo.GetByName(ctx, tenantID, name); err == nil && existing != nil {
		return nil, domain.ErrDomainAlreadyExists
	} else if err != nil && !errors.Is(err, domain.ErrDomainNotFound) {
		return nil, fmt.Errorf("comprobar dominio existente: %w", err)
	}

	token, err := newVerificationToken()
	if err != nil {
		return nil, err
	}
	now := uc.now()
	d := &domain.Domain{
		ID: uuid.New(), TenantID: tenantID, Domain: name,
		Purpose: purpose, Status: domain.StatusPending,
		VerificationToken: token, DMARCPolicy: policy,
	}
	if err := uc.assignDKIMKey(d, dkimSelector(now, "", "")); err != nil {
		return nil, err
	}
	if err := uc.repo.Create(ctx, d); err != nil {
		return nil, err
	}
	uc.publish("domains.domain.created", d, func() error { return uc.events.DomainCreated(ctx, d) })
	return d, nil
}

func (uc *UseCase) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Domain, error) {
	return uc.repo.GetByID(ctx, tenantID, id)
}

func (uc *UseCase) LatestChecks(ctx context.Context, tenantID, id uuid.UUID) ([]domain.DNSCheck, error) {
	return uc.repo.LatestChecks(ctx, tenantID, id)
}

func (uc *UseCase) List(ctx context.Context, tenantID uuid.UUID, page, perPage int) ([]*domain.Domain, int64, error) {
	return uc.repo.List(ctx, tenantID, (page-1)*perPage, perPage)
}

type UpdateRequest struct {
	Purpose     *string
	DMARCPolicy *string
}

// Update cambia el uso o la politica DMARC. Anadir el uso corporativo exige un MX que
// todavia no se ha comprobado, asi que un dominio verificado vuelve a pending; quitarlo
// lo desactiva en el directorio de la celda (mail-directory se niega si quedan buzones) y lo
// suelta del indice global de dominios.
func (uc *UseCase) Update(ctx context.Context, tenantID, id uuid.UUID, req UpdateRequest) (*domain.Domain, error) {
	if req.Purpose == nil && req.DMARCPolicy == nil {
		return nil, domain.ErrNothingToUpdate
	}
	d, err := uc.repo.GetByID(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if req.DMARCPolicy != nil {
		policy := domain.DMARCPolicy(*req.DMARCPolicy)
		if !policy.Valid() {
			return nil, domain.ErrInvalidDMARCPolicy
		}
		d.DMARCPolicy = policy
	}
	if req.Purpose != nil {
		purpose := domain.Purpose(*req.Purpose)
		if !purpose.Valid() {
			return nil, domain.ErrInvalidPurpose
		}
		wasCorporate, isCorporate := d.Purpose.IncludesCorporate(), purpose.IncludesCorporate()
		d.Purpose = purpose
		switch {
		case d.Status == domain.StatusVerified && !wasCorporate && isCorporate:
			d.Status = domain.StatusPending
			d.VerifiedAt = nil
		case d.Status == domain.StatusVerified && wasCorporate && !isCorporate:
			if err := uc.retireFromDirectory(ctx, d); err != nil {
				return nil, integrationError("retirar del directorio de la celda", err)
			}
		}
	}
	if err := uc.repo.Update(ctx, d); err != nil {
		return nil, err
	}
	return d, nil
}

// Delete retira el dominio: primero lo desactiva en el directorio (409 si hay buzones), tambien
// si ya no es corporativo pero su desactivacion seguia pendiente, y lo suelta del indice global
// de dominios; despues borra sus claves del Redis de los motores y solo entonces borra la fila.
// Si un servicio no responde, la fila se queda: una clave de firma huerfana en Redis o un dominio
// que sigue reclamado por una empresa que ya no lo tiene son peores que un dominio que tarda en
// borrarse.
func (uc *UseCase) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	d, err := uc.repo.GetByID(ctx, tenantID, id)
	if err != nil {
		return err
	}
	if d.Purpose.IncludesCorporate() || d.DirectoryDeactivationPending {
		if err := uc.retireFromDirectory(ctx, d); err != nil {
			return integrationError("retirar del directorio de la celda", err)
		}
	}
	if err := uc.mailSecurity.DeleteDKIM(ctx, tenantID, d.Domain); err != nil {
		return integrationError("retirar claves DKIM en mail-security", err)
	}
	if err := uc.repo.Delete(ctx, tenantID, id); err != nil {
		return err
	}
	d.Status = domain.StatusDisabled
	uc.publish("domains.domain.deleted", d, func() error { return uc.events.DomainDeleted(ctx, d) })
	return nil
}

// integrationError conserva el 409 de buzones y convierte cualquier otro fallo de un
// servicio vecino en ErrIntegrationUnavailable, dejando el detalle envuelto para el log.
func integrationError(op string, err error) error {
	if errors.Is(err, domain.ErrDomainHasMailboxes) {
		return err
	}
	return fmt.Errorf("%s: %w: %w", op, domain.ErrIntegrationUnavailable, err)
}

// publish emite un evento sin que su fallo deshaga lo que ya quedo escrito; queda
// constancia para reintentarlo (pendiente: outbox en pkg/events).
func (uc *UseCase) publish(subject string, d *domain.Domain, fn func() error) {
	if uc.events == nil {
		return
	}
	if err := fn(); err != nil {
		uc.logger.Warn("no se pudo publicar el evento",
			zap.String("subject", subject), zap.String("tenant_id", d.TenantID.String()),
			zap.String("domain", d.Domain), zap.Error(err))
	}
}

// newVerificationToken devuelve 16 bytes aleatorios en hexadecimal (32 caracteres).
func newVerificationToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generar token de propiedad: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

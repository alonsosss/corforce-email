package app

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/alonsosss/corforce-email/services/transactional/internal/ports"
	"go.uber.org/zap"
)

// RateLimiter acota la tasa de envio al proveedor de un carril (SES_MAX_SEND_RATE o
// SES_MAX_SEND_RATE_MARKETING por segundo).
type RateLimiter interface {
	Wait(ctx context.Context) error
}

// Lane es el carril de salida de una clase: su emisor (con su configuration set) y su
// limitador de tasa. Las clases nunca comparten carril.
type Lane struct {
	Sender  ports.Sender
	Limiter RateLimiter
}

// Config son los parametros de negocio que llegan del entorno.
type Config struct {
	// PlatformFromEmail y PlatformFromName firman los correos de la propia plataforma
	// (POST /internal/send-email).
	PlatformFromEmail string
	PlatformFromName  string
	// AllowUnverifiedPlatformFrom permite enviar desde el remitente de plataforma aunque
	// su dominio no figure en la proyeccion. Solo para arrancar una plataforma nueva;
	// cada envio asi queda avisado en el log.
	AllowUnverifiedPlatformFrom bool
	// Source identifica a este servicio ante suppression.
	Source string
}

type Deps struct {
	Repo        ports.Repository
	Events      ports.EventPublisher
	Suppression ports.SuppressionClient
	Templates   ports.TemplateRenderer
	Reputation  ports.ReputationClient
	// Sender y Limiter son el carril transaccional.
	Sender  ports.Sender
	Limiter RateLimiter
	// Marketing es el carril de marketing; sin el, los mensajes de marketing esperan en
	// la cola en vez de salir por el transaccional.
	Marketing Lane
	Links     *domain.LinkSigner
	// UTM anade los parametros de campana a los enlaces del marketing; nil no los anade.
	UTM    *domain.LinkTagger
	Config Config
	Logger *zap.Logger
	// Metrics es opcional; nil no cuenta nada.
	Metrics ports.Metrics
	// Now permite fijar el reloj en pruebas; nil usa time.Now.
	Now func() time.Time
}

type UseCase struct {
	repo        ports.Repository
	events      ports.EventPublisher
	suppression ports.SuppressionClient
	templates   ports.TemplateRenderer
	reputation  ports.ReputationClient
	lanes       map[string]Lane
	metrics     ports.Metrics
	links       *domain.LinkSigner
	utm         *domain.LinkTagger
	cfg         Config
	logger      *zap.Logger
	now         func() time.Time
}

func New(d Deps) *UseCase {
	now := d.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if d.Config.Source == "" {
		d.Config.Source = "transactional"
	}
	metrics := d.Metrics
	if metrics == nil {
		metrics = noopMetrics{}
	}
	return &UseCase{
		repo:        d.Repo,
		events:      d.Events,
		suppression: d.Suppression,
		templates:   d.Templates,
		reputation:  d.Reputation,
		lanes: map[string]Lane{
			domain.ClassTransactional: {Sender: d.Sender, Limiter: d.Limiter},
			domain.ClassMarketing:     d.Marketing,
		},
		metrics: metrics,
		links:   d.Links,
		utm:     d.UTM,
		cfg:     d.Config,
		logger:  d.Logger,
		now:     now,
	}
}

type noopMetrics struct{}

func (noopMetrics) SendAttempt(string, string)         {}
func (noopMetrics) SESEvent(string)                    {}
func (noopMetrics) SESEventRejected(string)            {}
func (noopMetrics) SESAccount(domain.SESAccountStatus) {}
func (noopMetrics) SESAccountCheckFailed()             {}

// laneFor devuelve el carril de la clase del mensaje. Una clase sin carril completo es un
// error de configuracion: el mensaje no sale por el carril de otra clase.
func (uc *UseCase) laneFor(class string) (Lane, error) {
	lane, ok := uc.lanes[domain.ClassOrDefault(class)]
	if !ok || lane.Sender == nil || lane.Limiter == nil {
		return Lane{}, domain.ErrLaneNotConfigured
	}
	return lane, nil
}

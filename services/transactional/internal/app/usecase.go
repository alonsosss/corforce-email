package app

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/alonsosss/corforce-email/services/transactional/internal/ports"
	"go.uber.org/zap"
)

// RateLimiter acota la tasa de envio al proveedor (SES_MAX_SEND_RATE por segundo).
type RateLimiter interface {
	Wait(ctx context.Context) error
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
	Sender      ports.Sender
	Limiter     RateLimiter
	Links       *domain.LinkSigner
	Config      Config
	Logger      *zap.Logger
	// Now permite fijar el reloj en pruebas; nil usa time.Now.
	Now func() time.Time
}

type UseCase struct {
	repo        ports.Repository
	events      ports.EventPublisher
	suppression ports.SuppressionClient
	templates   ports.TemplateRenderer
	sender      ports.Sender
	limiter     RateLimiter
	links       *domain.LinkSigner
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
	return &UseCase{
		repo:        d.Repo,
		events:      d.Events,
		suppression: d.Suppression,
		templates:   d.Templates,
		sender:      d.Sender,
		limiter:     d.Limiter,
		links:       d.Links,
		cfg:         d.Config,
		logger:      d.Logger,
		now:         now,
	}
}

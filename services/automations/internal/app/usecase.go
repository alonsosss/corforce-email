// Package app es el caso de uso de automations: el correo del doble opt-in, el ciclo de
// vida de los flujos, la entrada de contactos por eventos y el ejecutor de sus pasos.
package app

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/alonsosss/corforce-email/services/automations/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	// recordTimeout acota el registro del resultado de una llamada ya hecha. Corre sobre
	// un contexto que no se cancela con el apagado: un envio que transactional acepto debe
	// quedar escrito aunque el proceso este saliendo.
	recordTimeout = 15 * time.Second
	// claimBatch y maxClaimRounds acotan cuanto trabajo toma un tick por empresa.
	claimBatch     = 20
	maxClaimRounds = 50
	// blockedWindow es la ventana en que se cuentan los bloqueos de un flujo para pausarlo.
	blockedWindow = time.Hour
	// processedRetention: cuanto se recuerda un evento de disparo ya procesado, muy por
	// encima de la retencion del stream.
	processedRetention = 30 * 24 * time.Hour
)

type Config struct {
	// DOILimits es el tope de correos de confirmacion por contacto.
	DOILimits domain.DOILimits
	// PauseAfterFailures: ejecuciones de un mismo flujo bloqueadas por transactional
	// (SENDING_RESTRICTED, PLAN_LIMIT_REACHED) en una hora que lo pausan.
	PauseAfterFailures int
	// PublicBaseURL es la URL publica de la plataforma, sin barra final. De ella cuelga el
	// enlace de prueba con que se comprueba la plantilla del doble opt-in.
	PublicBaseURL string
}

type Deps struct {
	Settings   ports.SettingsRepository
	Deliveries ports.DeliveryRepository
	Workflows  ports.WorkflowRepository
	Runs       ports.RunRepository
	Processed  ports.ProcessedRepository
	Tx         ports.Transactor
	Events     ports.EventPublisher
	Sender     ports.Sender
	Contacts   ports.Contacts
	Templates  ports.Templates
	Config     Config
	Logger     *zap.Logger
	// Now permite fijar el reloj en las pruebas; nil = time.Now en UTC.
	Now func() time.Time
}

type UseCase struct {
	settings   ports.SettingsRepository
	deliveries ports.DeliveryRepository
	workflows  ports.WorkflowRepository
	runs       ports.RunRepository
	processed  ports.ProcessedRepository
	tx         ports.Transactor
	events     ports.EventPublisher
	sender     ports.Sender
	contacts   ports.Contacts
	templates  ports.Templates
	cfg        Config
	logger     *zap.Logger
	now        func() time.Time
}

func New(d Deps) *UseCase {
	now := d.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	logger := d.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	cfg := d.Config
	if cfg.DOILimits.Validate() != nil {
		cfg.DOILimits = domain.DOILimits{PerDay: 1, Per30Days: 3}
	}
	if cfg.PauseAfterFailures < 1 {
		cfg.PauseAfterFailures = 20
	}
	return &UseCase{
		settings: d.Settings, deliveries: d.Deliveries, workflows: d.Workflows, runs: d.Runs,
		processed: d.Processed, tx: d.Tx, events: d.Events, sender: d.Sender, contacts: d.Contacts,
		templates: d.Templates, cfg: cfg, logger: logger, now: now,
	}
}

// PruneProcessedEvents olvida los eventos de disparo ya procesados fuera de retencion.
func (uc *UseCase) PruneProcessedEvents(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	return uc.processed.PruneProcessed(ctx, tenantID, uc.now().Add(-processedRetention))
}

func recordContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
}

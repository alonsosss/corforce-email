package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/billing/internal/domain"
	"github.com/alonsosss/corforce-email/services/billing/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Config es la configuracion que decide negocio.
type Config struct {
	// DefaultPlanCode es el plan que recibe una empresa nueva; vacio = ninguno.
	DefaultPlanCode string
	// TrialDays > 0 abre la suscripcion de una empresa nueva en prueba esos dias.
	TrialDays int
	// Enforce deniega los derechos de una empresa sin suscripcion. Es un interruptor de
	// despliegue: apagado mientras las empresas existentes reciben su plan.
	Enforce bool
}

type Deps struct {
	Plans         ports.PlanRepository
	Subscriptions ports.SubscriptionRepository
	Usage         ports.UsageRepository
	Ledger        ports.EventLedger
	Tx            ports.Transactor
	Events        ports.EventPublisher
	Logger        *zap.Logger
	Config        Config
	// Now fija el reloj en las pruebas; nil = time.Now en UTC.
	Now func() time.Time
}

type UseCase struct {
	plans  ports.PlanRepository
	subs   ports.SubscriptionRepository
	usage  ports.UsageRepository
	ledger ports.EventLedger
	tx     ports.Transactor
	events ports.EventPublisher
	logger *zap.Logger
	cfg    Config
	now    func() time.Time
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
	return &UseCase{
		plans: d.Plans, subs: d.Subscriptions, usage: d.Usage, ledger: d.Ledger,
		tx: d.Tx, events: d.Events, logger: logger, cfg: d.Config, now: now,
	}
}

// IsPermanent distingue el error que no cambia al reintentar (un evento mal formado) del
// transitorio (la base no respondio).
func IsPermanent(err error) bool { return errors.Is(err, domain.ErrInvalidEvent) }

// subscriptionWithPlan devuelve la suscripcion de la empresa y su plan, o nil y nil si no
// tiene suscripcion.
func (uc *UseCase) subscriptionWithPlan(ctx context.Context, tenantID uuid.UUID) (*domain.Subscription, *domain.Plan, error) {
	sub, err := uc.subs.GetByTenant(ctx, tenantID)
	if errors.Is(err, domain.ErrSubscriptionNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	plan, err := uc.plans.Get(ctx, sub.PlanID)
	if err != nil {
		return nil, nil, fmt.Errorf("plan %s de la suscripcion de %s: %w", sub.PlanID, tenantID, err)
	}
	return sub, plan, nil
}

// validateEventRef rechaza el evento que no se puede deduplicar ni atribuir.
func validateEventRef(eventID string, tenantID uuid.UUID) error {
	if strings.TrimSpace(eventID) == "" || len(eventID) > domain.MaxEventIDLength {
		return fmt.Errorf("%w: id de evento vacio o demasiado largo", domain.ErrInvalidEvent)
	}
	if tenantID == uuid.Nil {
		return fmt.Errorf("%w: sin tenant_id", domain.ErrInvalidEvent)
	}
	return nil
}

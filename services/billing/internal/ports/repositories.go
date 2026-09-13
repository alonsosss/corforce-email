package ports

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/billing/internal/domain"
	"github.com/google/uuid"
)

// Todos los repositorios leen del registro y usan la transaccion del contexto cuando la
// hay, asi que cualquier combinacion de ellos es valida dentro de Transactor.Transact.

type PlanRepository interface {
	// Create guarda el plan y sus limites; domain.ErrPlanCodeTaken si el codigo existe.
	Create(ctx context.Context, p *domain.Plan) error
	Get(ctx context.Context, id uuid.UUID) (*domain.Plan, error)
	// GetForUpdate bloquea el plan: mientras dura, ninguna suscripcion nueva lo referencia.
	GetForUpdate(ctx context.Context, id uuid.UUID) (*domain.Plan, error)
	GetByCode(ctx context.Context, code string) (*domain.Plan, error)
	// List devuelve los planes; status vacio = todos.
	List(ctx context.Context, status domain.PlanStatus) ([]domain.Plan, error)
	Update(ctx context.Context, p *domain.Plan, replaceLimits bool) error
	HasSubscriptions(ctx context.Context, planID uuid.UUID) (bool, error)
}

// SubscriptionFilter acota el listado de la plataforma; Status vacio = todas.
type SubscriptionFilter struct {
	Status  domain.SubscriptionStatus
	Page    int
	PerPage int
}

type SubscriptionRepository interface {
	// Create inserta la suscripcion; false si la empresa ya tenia una.
	Create(ctx context.Context, s *domain.Subscription) (bool, error)
	GetByTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Subscription, error)
	GetByTenantForUpdate(ctx context.Context, tenantID uuid.UUID) (*domain.Subscription, error)
	Update(ctx context.Context, s *domain.Subscription) error
	List(ctx context.Context, f SubscriptionFilter) ([]domain.Subscription, int64, error)
	// ListDue devuelve las empresas con periodo vencido, prueba terminada o cancelacion
	// programada que ya llego, salvo las excluidas.
	ListDue(ctx context.Context, now time.Time, exclude []uuid.UUID, limit int) ([]uuid.UUID, error)
}

type UsageRepository interface {
	// LockCounter devuelve el contador (lo crea en cero si no existe) bloqueado hasta el
	// fin de la transaccion: dos eventos del mismo recurso y empresa no se pisan.
	LockCounter(ctx context.Context, tenantID uuid.UUID, resource domain.Resource, periodStart time.Time) (*domain.Counter, error)
	SaveCounter(ctx context.Context, c *domain.Counter) error
	Quantity(ctx context.Context, tenantID uuid.UUID, resource domain.Resource, periodStart time.Time) (int64, error)
	// Quantities devuelve lo usado de cada recurso: los de stock su contador vivo y los de
	// flujo el del periodo que empieza en flowPeriodStart. Lo que no tiene fila no aparece.
	Quantities(ctx context.Context, tenantID uuid.UUID, flowPeriodStart time.Time) (map[domain.Resource]int64, error)
	// AddStockItem registra que una fuente informa del objeto; true si es la primera.
	AddStockItem(ctx context.Context, tenantID uuid.UUID, resource domain.Resource, key, source string) (bool, error)
	// RemoveStockItem retira la fuente del objeto; true si era la ultima.
	RemoveStockItem(ctx context.Context, tenantID uuid.UUID, resource domain.Resource, key, source string) (bool, error)
}

// EventLedger deduplica los eventos consumidos.
type EventLedger interface {
	// MarkProcessed registra el evento; false si ya estaba (reentrega).
	MarkProcessed(ctx context.Context, eventID, subject string) (bool, error)
	PruneProcessed(ctx context.Context, before time.Time) (int64, error)
}

// Transactor abre la transaccion de negocio.
type Transactor interface {
	Transact(ctx context.Context, fn func(ctx context.Context) error) error
}

// EventPublisher encola los eventos propios DENTRO de la transaccion (outbox): el evento
// existe si y solo si el cambio existe.
type EventPublisher interface {
	SubscriptionCreated(ctx context.Context, s *domain.Subscription) error
	SubscriptionChanged(ctx context.Context, s *domain.Subscription, previousStatus domain.SubscriptionStatus, previousPlanCode string) error
	SubscriptionSuspended(ctx context.Context, s *domain.Subscription, previousStatus domain.SubscriptionStatus) error
	PeriodClosed(ctx context.Context, s *domain.Subscription, start, end time.Time, usage map[domain.Resource]int64) error
	LimitReached(ctx context.Context, tenantID uuid.UUID, limit domain.PlanLimit, periodStart time.Time, used int64) error
}

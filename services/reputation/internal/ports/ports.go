// Package ports declara lo que la aplicacion necesita de fuera: la base de la empresa, la
// outbox, Redis, billing, el directorio de empresas y las metricas.
package ports

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/google/uuid"
)

// HistoryFilter acota el historial de estados. Class vacia = todas las clases.
type HistoryFilter struct {
	Class   domain.Class
	Page    int
	PerPage int
}

// StatsRepository guarda los contadores diarios y los eventos ya contados. El pool o la
// transaccion salen del contexto (db.ContextPool).
type StatsRepository interface {
	// MarkProcessed anota el evento; false si ya estaba (reentrega de JetStream).
	MarkProcessed(ctx context.Context, tenantID uuid.UUID, eventID string) (bool, error)
	// AddDaily suma delta a los contadores del dia (UTC) y la clase.
	AddDaily(ctx context.Context, tenantID uuid.UUID, class domain.Class, day time.Time, delta domain.Counts) error
	// WindowCounts suma los contadores de la clase desde el dia from (incluido).
	WindowCounts(ctx context.Context, tenantID uuid.UUID, class domain.Class, from time.Time) (domain.Counts, error)
	WindowCountsByClass(ctx context.Context, tenantID uuid.UUID, from time.Time) (map[domain.Class]domain.Counts, error)
	// Prune borra los eventos anotados antes de processedBefore y los dias anteriores a
	// statsBefore.
	Prune(ctx context.Context, processedBefore, statsBefore time.Time) (events int64, days int64, err error)
}

// StateRepository guarda el estado vigente de cada clase y su historial.
type StateRepository interface {
	// Ensure crea la fila en ok si no existe, para que siempre haya algo que bloquear.
	Ensure(ctx context.Context, tenantID uuid.UUID, class domain.Class, now time.Time) error
	// GetForUpdate bloquea la fila hasta el final de la transaccion.
	GetForUpdate(ctx context.Context, tenantID uuid.UUID, class domain.Class) (domain.Record, error)
	// Get devuelve el estado vigente o domain.DefaultRecord si la clase aun no tiene fila.
	Get(ctx context.Context, tenantID uuid.UUID, class domain.Class) (domain.Record, error)
	List(ctx context.Context, tenantID uuid.UUID) ([]domain.Record, error)
	Save(ctx context.Context, r domain.Record) error
	// AppendHistory inserta la transicion y completa su ID.
	AppendHistory(ctx context.Context, c *domain.Change) error
	ListHistory(ctx context.Context, tenantID uuid.UUID, f HistoryFilter) ([]domain.Change, int64, error)
}

// LimitRepository guarda los limites de tasa fijados por el superadmin.
type LimitRepository interface {
	// Get devuelve nil si la clase no tiene limites propios.
	Get(ctx context.Context, tenantID uuid.UUID, class domain.Class) (*domain.LimitOverride, error)
	List(ctx context.Context, tenantID uuid.UUID) ([]domain.LimitOverride, error)
	// Upsert crea o reemplaza los limites y completa UpdatedAt.
	Upsert(ctx context.Context, o *domain.LimitOverride) error
	Delete(ctx context.Context, tenantID uuid.UUID, class domain.Class) error
}

// Transactor abre la transaccion de negocio; db.ContextPool lo cumple.
type Transactor interface {
	Transact(ctx context.Context, fn func(ctx context.Context) error) error
}

// EventPublisher encola los eventos del dominio. Se llama DENTRO de la transaccion: el
// evento existe si y solo si el cambio existe (outbox).
type EventPublisher interface {
	StateChanged(ctx context.Context, c domain.Change) error
}

// RateLimiter lleva el uso de las ventanas de hora y dia.
type RateLimiter interface {
	// Reserve comprueba las dos ventanas y, solo si count cabe en ambas, lo suma a las dos
	// en una sola operacion atomica.
	Reserve(ctx context.Context, tenantID uuid.UUID, class domain.Class, now time.Time, count int64, limits domain.Limits) (domain.RateOutcome, error)
	// Usage lee el uso actual de la hora y el dia sin modificarlo.
	Usage(ctx context.Context, tenantID uuid.UUID, class domain.Class, now time.Time) (hour int64, day int64, err error)
}

// Entitlements consulta el derecho mensual del plan de la empresa en billing.
type Entitlements interface {
	Check(ctx context.Context, tenantID uuid.UUID, class domain.Class, quantity int64) (domain.Entitlement, error)
}

// TenantDirectory da acceso a la base de cualquier empresa, para los trabajos de fondo y
// las operaciones de plataforma que actuan sobre una empresa distinta de la del token.
type TenantDirectory interface {
	// Scope devuelve un contexto apuntado a la base de la empresa;
	// domain.ErrTenantNotFound si no figura en el registro.
	Scope(ctx context.Context, tenantID uuid.UUID) (context.Context, error)
	// ForEachActive recorre las empresas activas en paralelo, con perTenant como tope de
	// cada una; fn recibe un contexto ya apuntado a la base de esa empresa.
	ForEachActive(ctx context.Context, perTenant time.Duration, fn func(ctx context.Context, tenantID uuid.UUID)) error
}

// Metrics cuenta lo que hace el servicio.
type Metrics interface {
	Authorize(class domain.Class, result string)
	StateChanged(class domain.Class, to domain.State)
	// Degraded cuenta una autorizacion concedida sin poder consultar la dependencia.
	Degraded(dependency string)
}

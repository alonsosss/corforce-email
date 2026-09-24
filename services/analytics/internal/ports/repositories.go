package ports

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/services/analytics/internal/domain"
	"github.com/google/uuid"
)

var (
	// ErrSchedulerUnavailable: el scheduler no respondio. El cierre se vuelve a intentar.
	ErrSchedulerUnavailable = errors.New("el scheduler no está disponible")
	// ErrReportRejected: el scheduler rechazo el cierre de forma definitiva (la ejecucion ya
	// vencio, se cancelo o no es de esta empresa). Repetirlo daria siempre lo mismo.
	ErrReportRejected = errors.New("el scheduler rechazó el cierre de la ejecución")
)

// SchedulerReporter cierra en el scheduler la ejecucion que despacho un trabajo. Es el
// contrato de POST /internal/scheduler/executions/{id}/complete y /fail.
type SchedulerReporter interface {
	Complete(ctx context.Context, tenantID, executionID uuid.UUID, result any) error
	Fail(ctx context.Context, tenantID, executionID uuid.UUID, message string, retryable bool) error
}

// Transactor abre la transaccion de la ingesta de un evento; db.ContextPool lo cumple.
// Todos los repositorios resuelven el pool o la transaccion desde el contexto.
type Transactor interface {
	Transact(ctx context.Context, fn func(ctx context.Context) error) error
}

// EventLedger es la deduplicacion de la ingesta por id de evento.
type EventLedger interface {
	// MarkProcessed registra el evento; false si ya estaba (reentrega).
	MarkProcessed(ctx context.Context, tenantID, eventID uuid.UUID) (bool, error)
	PruneProcessed(ctx context.Context, tenantID uuid.UUID, before time.Time) (int64, error)
}

// FactRepository guarda una fila por mensaje.
type FactRepository interface {
	// LockOrCreate devuelve la fila del mensaje bloqueada hasta el fin de la
	// transaccion; si no existe, la crea con las dimensiones de seed. Dos eventos del
	// mismo mensaje quedan asi en serie.
	LockOrCreate(ctx context.Context, seed domain.MessageFact) (*domain.MessageFact, error)
	Save(ctx context.Context, f *domain.MessageFact) error
	// PruneInactive borra los mensajes sin actividad desde before. No toca agregados.
	PruneInactive(ctx context.Context, tenantID uuid.UUID, before time.Time) (int64, error)
}

// StatsRepository mantiene los agregados diarios.
type StatsRepository interface {
	// Apply suma los contadores de un dia en el agregado por clase y, si el mensaje
	// tiene campana o dominio, en los de campana y dominio.
	Apply(ctx context.Context, tenantID uuid.UUID, dims domain.Dimensions, day domain.DayCounters) error
}

// CampaignRepository guarda lo que se sabe de cada campana por sus eventos.
type CampaignRepository interface {
	// CreateIfAbsent inserta la campana; false si ya existia.
	CreateIfAbsent(ctx context.Context, c domain.CampaignSeen) (bool, error)
	// Lock devuelve la campana bloqueada hasta el fin de la transaccion.
	Lock(ctx context.Context, tenantID, campaignID uuid.UUID) (*domain.CampaignSeen, error)
	Save(ctx context.Context, c *domain.CampaignSeen) error
}

// LinkRepository agrega los clics por enlace de cada campana.
type LinkRepository interface {
	// RecordClick suma el clic a su URL y, si es el primero de ese mensaje en esa URL, a
	// sus unicos. Corre dentro de la transaccion de la ingesta, con la fila del mensaje
	// ya bloqueada.
	RecordClick(ctx context.Context, c domain.LinkClick) error
	// PruneClicks olvida que mensaje pulso que URL antes de before. Los agregados no se tocan.
	PruneClicks(ctx context.Context, tenantID uuid.UUID, before time.Time) (int64, error)
}

// ReportRepository responde las consultas del panel. Las series devuelven un punto por
// cada dia del rango, con ceros donde no hubo actividad.
type ReportRepository interface {
	Totals(ctx context.Context, tenantID uuid.UUID, q domain.ClassQuery) (domain.Counters, error)
	Series(ctx context.Context, tenantID uuid.UUID, q domain.ClassQuery) ([]domain.DayCounters, error)
	CountCampaigns(ctx context.Context, tenantID uuid.UUID) (int64, error)
	// ListCampaigns ordena por inicio descendente; las campanas sin inicio conocido, al final.
	ListCampaigns(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.CampaignSummary, error)
	// GetCampaign devuelve domain.ErrCampaignNotFound si no hay eventos ni envios de la campana.
	GetCampaign(ctx context.Context, tenantID, campaignID uuid.UUID) (*domain.CampaignSummary, error)
	CampaignSeries(ctx context.Context, tenantID, campaignID uuid.UUID, r domain.Range) ([]domain.DayCounters, error)
	// TopDomains ordena por enviados descendente.
	TopDomains(ctx context.Context, tenantID uuid.UUID, q domain.ClassQuery, limit int) ([]domain.DomainStats, error)
	// CampaignLinks ordena por clics descendente; sin clics devuelve la lista vacia.
	CampaignLinks(ctx context.Context, tenantID, campaignID uuid.UUID, limit int) (*domain.CampaignLinks, error)
}

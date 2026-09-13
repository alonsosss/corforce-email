package ports

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/analytics/internal/domain"
	"github.com/google/uuid"
)

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
}

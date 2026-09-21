package ports

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/google/uuid"
)

type SecurityFilters struct {
	EventType    *string
	RiskLevel    *string
	DateFrom     *time.Time
	DateTo       *time.Time
	Acknowledged *bool
}

type AuditLogRepository interface {
	Create(ctx context.Context, log *domain.AuditLog) error
	VerifyChain(ctx context.Context, tenantID uuid.UUID) (*domain.ChainIntegrity, error)
	RecentLoginOtherIP(ctx context.Context, tenantID, userID uuid.UUID, currentIP string, since time.Time, excludeID uuid.UUID) (string, error)
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*domain.AuditLog, error)
	List(ctx context.Context, query domain.AuditQuery, page, pageSize int) ([]*domain.AuditLog, error)
	Count(ctx context.Context, query domain.AuditQuery) (int64, error)
	BulkCreate(ctx context.Context, logs []*domain.AuditLog) error

	// Consultas del detector de seguridad sobre el historial de la propia bitacora.
	// excludeID descarta el registro recien insertado: el detector corre despues de
	// persistir y sin excluirlo el evento actual se contaria como "historial".
	HasUserActionFromIP(ctx context.Context, tenantID, userID uuid.UUID, action, ip string, excludeID uuid.UUID) (bool, error)
	ListUserActionAgents(ctx context.Context, tenantID, userID uuid.UUID, action string, excludeID uuid.UUID, limit int) ([]string, error)
	CountRecentByActionIP(ctx context.Context, tenantID uuid.UUID, action, ip string, since time.Time) (int64, error)
}

type SecurityEventRepository interface {
	Create(ctx context.Context, evt *domain.SecurityEvent) error
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*domain.SecurityEvent, error)
	List(ctx context.Context, tenantID uuid.UUID, filters SecurityFilters, page, pageSize int) ([]*domain.SecurityEvent, int64, error)
	Acknowledge(ctx context.Context, id, acknowledgedBy uuid.UUID) error
	GetUnacknowledged(ctx context.Context, tenantID uuid.UUID) ([]*domain.SecurityEvent, error)
	// HasRecentEvent evita duplicar la misma alerta (p.ej. una rafaga de fuerza
	// bruta genera UN evento por ventana, no uno por intento).
	HasRecentEvent(ctx context.Context, tenantID uuid.UUID, eventType, ip string, since time.Time) (bool, error)
	// VerifyChain recorre la cadena de hash de los eventos. Sin clave configurada verifica
	// lo que haya y falla si encuentra filas firmadas.
	VerifyChain(ctx context.Context, tenantID uuid.UUID) (*domain.ChainIntegrity, error)
}

type DataChangeRepository interface {
	CreateBatch(ctx context.Context, records []*domain.DataChangeRecord) error
	GetByLogID(ctx context.Context, tenantID, logID uuid.UUID) ([]*domain.DataChangeRecord, error)
}

type AuditSummaryRepository interface {
	GetByModule(ctx context.Context, tenantID uuid.UUID, dateFrom, dateTo time.Time) ([]*domain.AuditSummary, error)
	GetUserActivity(ctx context.Context, tenantID, userID uuid.UUID, dateFrom, dateTo time.Time) (int64, error)
}

type EventPublisher interface {
	// El riesgo, la IP y el usuario viajan en la alerta para que el servicio de
	// notificaciones decida a quien avisar sin releer la base del tenant.
	PublishSecurityAlert(tenantID, eventType, detail, riskLevel, ip, userID string) error
}

// ChainAnchorRepository guarda y contrasta las anclas de la cabeza de cada cadena. Solo se
// anade: no hay operacion que borre o modifique un ancla.
type ChainAnchorRepository interface {
	// Head es la ultima fila firmada de la cadena, nil si esta vacia.
	Head(ctx context.Context, chain domain.ChainName) (*domain.ChainHead, error)
	// Findings contrasta las anclas guardadas con las filas actuales de la cadena.
	Findings(ctx context.Context, chain domain.ChainName) (domain.AnchorFindings, error)
	// Save registra el ancla y completa su id y su fecha. inserted=false si esa posicion ya
	// estaba anclada: la primera que se escribio es la que vale.
	Save(ctx context.Context, anchor *domain.ChainAnchor) (inserted bool, err error)
}

// ChainAnchorPublisher anuncia un ancla nueva. Se llama dentro de la transaccion que la
// guarda, de modo que el evento existe si y solo si el ancla existe.
type ChainAnchorPublisher interface {
	ChainAnchored(ctx context.Context, anchor *domain.ChainAnchor) error
}

// Transactor abre la transaccion en la que se guarda el ancla y se encola su evento.
type Transactor interface {
	Transact(ctx context.Context, fn func(ctx context.Context) error) error
}

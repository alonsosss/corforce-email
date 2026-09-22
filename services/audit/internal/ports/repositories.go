package ports

import (
	"context"
	"errors"
	"fmt"
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

// Total es el total de un listado paginado. Con Capped el valor es el tope hasta el que se
// cuenta con exactitud y el total real es mayor.
type Total struct {
	Value  int64
	Capped bool
}

type AuditLogRepository interface {
	Create(ctx context.Context, log *domain.AuditLog) error
	// VerifyChain recorre la cadena; con opts.From continua desde un punto ya verificado (relee la
	// fila del punto y comprueba que sigue siendo la misma) y con opts.OnBatch informa de cada lote.
	VerifyChain(ctx context.Context, tenantID uuid.UUID, opts domain.VerifyOptions) (*domain.ChainIntegrity, error)
	RecentLoginOtherIP(ctx context.Context, tenantID, userID uuid.UUID, currentIP string, since time.Time, excludeID uuid.UUID) (string, error)
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*domain.AuditLog, error)
	List(ctx context.Context, query domain.AuditQuery, page, pageSize int) ([]*domain.AuditLog, error)
	Count(ctx context.Context, query domain.AuditQuery) (Total, error)
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
	VerifyChain(ctx context.Context, tenantID uuid.UUID, opts domain.VerifyOptions) (*domain.ChainIntegrity, error)
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

// IntegrityRunRepository guarda las verificaciones de cadena en segundo plano de la empresa. La
// base garantiza que solo hay una en curso por empresa: es el cerrojo entre procesos.
type IntegrityRunRepository interface {
	// Open registra la verificacion como en curso. Si la empresa ya tiene una en curso no escribe
	// nada y la devuelve junto a domain.ErrRunActive.
	Open(ctx context.Context, run *domain.IntegrityRun) (*domain.IntegrityRun, error)
	Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.IntegrityRun, error)
	// Active es la verificacion en curso de la empresa, nil si no hay.
	Active(ctx context.Context, tenantID uuid.UUID) (*domain.IntegrityRun, error)
	// LastCompleted es la ultima verificacion terminada de la empresa, nil si no hay. Con onlyOK
	// solo cuenta las que dieron la cadena por buena; con un modo, solo las de ese modo.
	LastCompleted(ctx context.Context, tenantID uuid.UUID, onlyOK bool, mode domain.RunMode) (*domain.IntegrityRun, error)
	List(ctx context.Context, tenantID uuid.UUID, limit int) ([]*domain.IntegrityRun, error)
	// Claim toma una verificacion en curso cuyo dueno dejo de latir hace mas de staleAfter (o que ya
	// es de owner) y la devuelve; ok es false si otro proceso la tiene viva.
	Claim(ctx context.Context, tenantID, id uuid.UUID, owner string, staleAfter time.Duration) (run *domain.IntegrityRun, ok bool, err error)
	// Progress anota el avance y el latido. Devuelve domain.ErrRunLost si owner ya no es el dueno y
	// domain.ErrRunCancelled si se pidio cancelarla.
	Progress(ctx context.Context, tenantID, id uuid.UUID, owner string, p domain.RunProgress) error
	// Finish cierra la verificacion; solo su dueno puede.
	Finish(ctx context.Context, tenantID, id uuid.UUID, owner string, f domain.RunFinish) error
	// RequestCancel marca la verificacion en curso para que su dueno la detenga; false si no esta en curso.
	RequestCancel(ctx context.Context, tenantID, id uuid.UUID) (bool, error)
}

// IntegrityMetrics cuenta lo que hacen las verificaciones. Sus etiquetas toman valores de
// conjuntos cerrados (origen y desenlace): nada que llegue de fuera las abre, y ninguna lleva la
// empresa.
type IntegrityMetrics interface {
	RunFinished(origin domain.RunTrigger, outcome string)
	// SweepBroken es cuantas empresas dejo con la cadena rota la ultima pasada del barrido.
	SweepBroken(tenants int)
}

// TenantDirectory lista las empresas activas del registro con su slug, para nombrarlas en el
// informe de anclas.
type TenantDirectory interface {
	ActiveTenants(ctx context.Context) ([]domain.TenantRef, error)
}

// ReportSigner firma el bloque del informe de anclas con la llave activa de la cadena, para que
// quien lo recibe pueda comprobar que no se fabrico.
type ReportSigner interface {
	KeyID() string
	Sign(data []byte) []byte
}

// ErrReportUnavailable: el envio no llego a transactional o este no pudo atenderlo; repetirlo mas
// tarde puede salir bien.
var ErrReportUnavailable = errors.New("el informe de anclas no se pudo entregar a transactional")

// ReportRejectedError: transactional rechazo el informe con un error que repetir no arregla
// (remitente de plataforma sin configurar, destinatario invalido).
type ReportRejectedError struct {
	Status  int
	Code    string
	Message string
}

func (e *ReportRejectedError) Error() string {
	return fmt.Sprintf("transactional rechazo el informe de anclas: %d %s %s", e.Status, e.Code, e.Message)
}

// AnchorReportSender entrega un informe en texto plano a una direccion, como correo de la propia
// plataforma. suppressed es verdadero si transactional lo dejo sin enviar porque la direccion
// esta suprimida (rebote, queja): el informe no salio del servidor.
type AnchorReportSender interface {
	SendAnchorReport(ctx context.Context, to, subject, text string) (suppressed bool, err error)
}

// AnchorReportMetrics cuenta los envios del informe de anclas. result toma los valores
// domain.ReportResult*, cerrados.
type AnchorReportMetrics interface {
	ReportResult(result string)
	ReportSucceeded(at time.Time)
	// ReportSchedule publica el intervalo configurado: la alerta lo lee para saber cuanto silencio
	// es demasiado, y cero significa que el informe esta desactivado.
	ReportSchedule(interval time.Duration)
}

// ChainBreakNotifier recibe cada cadena que una verificacion o el anclaje dan por rota, para
// avisar fuera del servidor sin esperar al informe periodico. No bloquea a quien lo llama.
type ChainBreakNotifier interface {
	ChainBroken(ctx context.Context, tenantID uuid.UUID, chain domain.ChainName, reason string)
}

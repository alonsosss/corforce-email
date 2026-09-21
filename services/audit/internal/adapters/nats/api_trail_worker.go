package nats

import (
	"context"
	"encoding/json"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/audit/internal/app"
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/google/uuid"
	natsgo "github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

const (
	trailStream  = "AUDIT_API"
	trailDurable = "audit-api-trail"
)

// APITrailWorker persiste el rastro de escrituras del API que publica el gateway
// (audit.api.write, JetStream durable) en audit.audit_logs, que es append-only (el repo
// no expone UPDATE/DELETE): la bitacora es inmutable por construccion. Complementa la
// auditoria de eventos de dominio que este servicio ya consume.
type APITrailWorker struct {
	bus      *events.Bus
	uc       *app.AuditUseCase
	tenantDB *db.TenantDB
	logger   *zap.Logger
	sub      *natsgo.Subscription
}

func NewAPITrailWorker(bus *events.Bus, uc *app.AuditUseCase, tenantDB *db.TenantDB, logger *zap.Logger) *APITrailWorker {
	return &APITrailWorker{bus: bus, uc: uc, tenantDB: tenantDB, logger: logger}
}

func (w *APITrailWorker) Start() error {
	if err := w.bus.EnsureStream(trailStream, []string{"audit.api.>"}); err != nil {
		return err
	}
	sub, err := w.bus.DurableQueueSubscribe("audit.api.write", trailDurable, w.handle)
	if err != nil {
		return err
	}
	w.sub = sub
	return nil
}

func (w *APITrailWorker) Stop() {
	if w.sub != nil {
		_ = w.sub.Drain()
	}
}

func (w *APITrailWorker) handle(evt events.Event, ack func()) {
	data, _ := evt.Data.(map[string]interface{})
	tenantID, err := uuid.Parse(trailStr(data["tenant_id"]))
	if err != nil {
		w.logger.Error("audit.api.write malformado; se descarta", zap.Any("data", data))
		ack()
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := w.tenantDB.ResolveForTenant(ctx, tenantID.String())
	if err != nil {
		if db.IsUnknownTenant(err) {
			// El tenant no existe en el registro: reintentar no lo va a hacer aparecer y
			// el evento quedaria girando en JetStream para siempre. Se descarta.
			w.logger.Error("audit.api.write de un tenant inexistente; se descarta",
				zap.String("tenant_id", tenantID.String()))
			ack()
			return
		}
		w.logger.Warn("audit.api.write: tenant sin pool; se reintentara", zap.Error(err))
		return // sin ack: JetStream reentrega
	}
	ctx = db.WithPool(ctx, pool)

	l := trailLog(evt.ID, tenantID, data)

	if err := w.uc.LogAction(ctx, l); err != nil {
		w.logger.Warn("persistir rastro API fallo; se reintentara", zap.Error(err))
		return // sin ack
	}
	ack()
}

// trailLog arma el apunte de una escritura del API. Toma el id del evento: JetStream entrega
// al menos una vez, y con el id del evento una reentrega (el apunte se guardo pero el ack se
// perdio) choca con el ya guardado en vez de duplicarlo.
func trailLog(eventID string, tenantID uuid.UUID, data map[string]interface{}) *domain.AuditLog {
	userID, _ := uuid.Parse(trailStr(data["user_id"]))
	ip := trailStr(data["ip"])
	if ip == "" {
		ip = "0.0.0.0"
	}
	severity := "info"
	if s, ok := data["status"].(float64); ok && s >= 400 {
		severity = "warning"
	}
	l := &domain.AuditLog{
		TenantID:  tenantID,
		UserID:    userID,
		Action:    truncateRunes(trailStr(data["method"]), maxShortColumn),
		Module:    truncateRunes(trailStr(data["module"]), maxShortColumn),
		Resource:  trailStr(data["path"]),
		IPAddress: ip,
		Severity:  severity,
	}
	if id, err := uuid.Parse(eventID); err == nil {
		l.ID = id
	}
	if ua := trailStr(data["user_agent"]); ua != "" {
		l.UserAgent = &ua
	}
	if rid := trailStr(data["request_id"]); rid != "" {
		rid = truncateRunes(rid, maxShortColumn)
		l.RequestID = &rid
	}
	l.Changes = trailChanges(data)
	return l
}

// trailChanges es el detalle del apunte: roles y resultado y, si la peticion eligio celda
// destino (operador de la plataforma), la celda pedida.
func trailChanges(data map[string]interface{}) *string {
	detail := map[string]interface{}{"roles": trailStr(data["roles"]), "status": data["status"]}
	if cell := trailStr(data["target_cell"]); cell != "" {
		detail["target_cell"] = cell
	}
	b, err := json.Marshal(detail)
	if err != nil {
		return nil
	}
	d := string(b)
	return &d
}

// maxShortColumn es el largo de action, module y request_id en audit.audit_logs (varchar(100)). Un
// valor mas largo haria fallar el INSERT en cada reentrega y la escritura quedaria sin apunte.
const maxShortColumn = 100

func truncateRunes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

func trailStr(v interface{}) string {
	s, _ := v.(string)
	return s
}

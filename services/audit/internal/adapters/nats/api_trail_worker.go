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
		Action:    trailStr(data["method"]),
		Module:    trailStr(data["module"]),
		Resource:  trailStr(data["path"]),
		IPAddress: ip,
		Severity:  severity,
	}
	if ua := trailStr(data["user_agent"]); ua != "" {
		l.UserAgent = &ua
	}
	if rid := trailStr(data["request_id"]); rid != "" {
		l.RequestID = &rid
	}
	if detail, err := json.Marshal(map[string]interface{}{
		"roles": trailStr(data["roles"]), "status": data["status"],
	}); err == nil {
		d := string(detail)
		l.Changes = &d
	}

	if err := w.uc.LogAction(ctx, l); err != nil {
		w.logger.Warn("persistir rastro API fallo; se reintentara", zap.Error(err))
		return // sin ack
	}
	ack()
}

func trailStr(v interface{}) string {
	s, _ := v.(string)
	return s
}

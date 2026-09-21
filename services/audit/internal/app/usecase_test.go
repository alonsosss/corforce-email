package app

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/alonsosss/corforce-email/services/audit/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

var errBoom = errors.New("boom")

// recordedLogs registra lo que el caso de uso pide a la bitacora. Embebe fakeLogs para el
// resto del puerto, que estas pruebas no tocan.
type recordedLogs struct {
	fakeLogs
	created    []*domain.AuditLog
	bulk       []*domain.AuditLog
	bulkCalls  int
	createErr  error
	bulkErr    error
	byID       *domain.AuditLog
	byIDErr    error
	listErr    error
	countErr   error
	listed     []*domain.AuditLog
	total      int64
	verified   *domain.ChainIntegrity
	verifyErr  error
	lastQuery  domain.AuditQuery
	lastPage   int
	lastPgSize int
}

func (r *recordedLogs) Create(_ context.Context, l *domain.AuditLog) error {
	if r.createErr != nil {
		return r.createErr
	}
	r.created = append(r.created, l)
	return nil
}

func (r *recordedLogs) BulkCreate(_ context.Context, logs []*domain.AuditLog) error {
	r.bulkCalls++
	if r.bulkErr != nil {
		return r.bulkErr
	}
	r.bulk = append(r.bulk, logs...)
	return nil
}

func (r *recordedLogs) GetByID(context.Context, uuid.UUID, uuid.UUID) (*domain.AuditLog, error) {
	return r.byID, r.byIDErr
}

func (r *recordedLogs) List(_ context.Context, q domain.AuditQuery, page, size int) ([]*domain.AuditLog, error) {
	r.lastQuery, r.lastPage, r.lastPgSize = q, page, size
	return r.listed, r.listErr
}

func (r *recordedLogs) Count(context.Context, domain.AuditQuery) (int64, error) {
	return r.total, r.countErr
}

func (r *recordedLogs) VerifyChain(context.Context, uuid.UUID) (*domain.ChainIntegrity, error) {
	return r.verified, r.verifyErr
}

type recordedSecurity struct {
	fakeSecurity
	createErr error
	byID      *domain.SecurityEvent
	byIDErr   error
	acked     []uuid.UUID
	ackedBy   uuid.UUID
	ackErr    error
}

func (r *recordedSecurity) Create(_ context.Context, e *domain.SecurityEvent) error {
	if r.createErr != nil {
		return r.createErr
	}
	r.created = append(r.created, e)
	return nil
}

func (r *recordedSecurity) GetByID(context.Context, uuid.UUID, uuid.UUID) (*domain.SecurityEvent, error) {
	return r.byID, r.byIDErr
}

func (r *recordedSecurity) Acknowledge(_ context.Context, id, by uuid.UUID) error {
	r.acked, r.ackedBy = append(r.acked, id), by
	return r.ackErr
}

type alert struct{ tenant, eventType, detail, risk, ip, user string }

type recordedPublisher struct {
	alerts []alert
	err    error
}

func (p *recordedPublisher) PublishSecurityAlert(tenant, eventType, detail, risk, ip, user string) error {
	p.alerts = append(p.alerts, alert{tenant, eventType, detail, risk, ip, user})
	return p.err
}

type recordedChanges struct {
	batches [][]*domain.DataChangeRecord
	err     error
}

func (c *recordedChanges) CreateBatch(_ context.Context, r []*domain.DataChangeRecord) error {
	c.batches = append(c.batches, r)
	return c.err
}

func (c *recordedChanges) GetByLogID(context.Context, uuid.UUID, uuid.UUID) ([]*domain.DataChangeRecord, error) {
	return nil, nil
}

type rig struct {
	uc  *AuditUseCase
	log *recordedLogs
	sec *recordedSecurity
	pub *recordedPublisher
	chg *recordedChanges
}

func newRig() *rig {
	r := &rig{log: &recordedLogs{}, sec: &recordedSecurity{}, pub: &recordedPublisher{}, chg: &recordedChanges{}}
	r.uc = NewAuditUseCase(AuditDeps{Logs: r.log, Security: r.sec, Changes: r.chg, Events: r.pub, Logger: zap.NewNop()})
	return r
}

func entry(action, severity string) *domain.AuditLog {
	return &domain.AuditLog{
		TenantID: uuid.New(), UserID: uuid.New(), Action: action, Module: "identity",
		Resource: "users", IPAddress: "203.0.113.7", Severity: severity,
	}
}

func TestLogActionRechazaSeveridadInvalidaSinEscribir(t *testing.T) {
	r := newRig()
	for _, sev := range []string{"", "INFO", "fatal", " info"} {
		if err := r.uc.LogAction(context.Background(), entry("user.created", sev)); !errors.Is(err, domain.ErrInvalidSeverity) {
			t.Fatalf("severidad %q: %v, se esperaba ErrInvalidSeverity", sev, err)
		}
	}
	if len(r.log.created) != 0 {
		t.Fatalf("una entrada invalida llego a la bitacora: %d", len(r.log.created))
	}
}

func TestLogActionAsignaIdYHoraUTCDelServidor(t *testing.T) {
	r := newRig()
	l := entry("user.created", "info")
	l.CreatedAt = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	before := time.Now().UTC().Add(-time.Second)
	if err := r.uc.LogAction(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	if l.ID == uuid.Nil {
		t.Fatal("sin id")
	}
	if l.CreatedAt.Before(before) || l.CreatedAt.Location() != time.UTC {
		t.Fatalf("la hora la pone el servidor en UTC, no el llamante: %v", l.CreatedAt)
	}
}

func TestLogActionRespetaElIdDelEvento(t *testing.T) {
	r := newRig()
	l := entry("user.created", "info")
	l.ID = uuid.New()
	want := l.ID
	if err := r.uc.LogAction(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	if r.log.created[0].ID != want {
		t.Fatal("el id dado se sustituyo: la reentrega de un evento no se reconoceria")
	}
}

func TestUnaReentregaYaGuardadaNoRepiteEfectos(t *testing.T) {
	r := newRig()
	r.log.createErr = domain.ErrLogAlreadyRecorded
	if err := r.uc.LogAction(context.Background(), entry("login_failed", "warning")); err != nil {
		t.Fatalf("una reentrega ya guardada no es un fallo: %v", err)
	}
	if len(r.sec.created) != 0 || len(r.pub.alerts) != 0 {
		t.Fatal("la reentrega duplico el evento de seguridad o la alerta")
	}
}

func TestLogActionFalloDeLaBitacoraNoLevantaAlertas(t *testing.T) {
	r := newRig()
	r.log.createErr = errBoom
	err := r.uc.LogAction(context.Background(), entry("login_failed", "warning"))
	if !errors.Is(err, errBoom) {
		t.Fatalf("el error debe llegar al llamante para que el bus reintente: %v", err)
	}
	if len(r.sec.created) != 0 || len(r.pub.alerts) != 0 {
		t.Fatal("se alerto de algo que no quedo registrado")
	}
}

func TestLasAccionesSensiblesLevantanEventoDeSeguridad(t *testing.T) {
	casos := []struct {
		action, eventType, severity, risk string
	}{
		{"login_failed", "failed_login", "info", "low"},
		{"unauthorized_access", "unauthorized_access", "warning", "medium"},
		{"permission_changed", "permission_change", "critical", "critical"},
		{"data_exported", "data_export", "info", "low"},
		{"config_changed", "config_change", "warning", "medium"},
	}
	for _, c := range casos {
		t.Run(c.action, func(t *testing.T) {
			r := newRig()
			l := entry(c.action, c.severity)
			if err := r.uc.LogAction(context.Background(), l); err != nil {
				t.Fatal(err)
			}
			if len(r.sec.created) != 1 || len(r.pub.alerts) != 1 {
				t.Fatalf("eventos %d, alertas %d", len(r.sec.created), len(r.pub.alerts))
			}
			evt, a := r.sec.created[0], r.pub.alerts[0]
			if evt.EventType != c.eventType || evt.RiskLevel != c.risk || evt.TenantID != l.TenantID || *evt.UserID != l.UserID || evt.IPAddress != l.IPAddress {
				t.Fatalf("evento: %+v", evt)
			}
			if a.tenant != l.TenantID.String() || a.eventType != c.eventType || a.risk != c.risk || a.ip != l.IPAddress || a.user != l.UserID.String() {
				t.Fatalf("alerta: %+v", a)
			}
		})
	}
}

func TestUnaAccionComunNoLevantaEventoDeSeguridad(t *testing.T) {
	r := newRig()
	if err := r.uc.LogAction(context.Background(), entry("user.updated", "critical")); err != nil {
		t.Fatal(err)
	}
	if len(r.sec.created) != 0 || len(r.pub.alerts) != 0 {
		t.Fatal("una accion no sensible levanto una alerta")
	}
}

func TestElFalloDeLaAlertaNoDeshaceElApunte(t *testing.T) {
	r := newRig()
	r.sec.createErr = errBoom
	r.pub.err = errBoom
	if err := r.uc.LogAction(context.Background(), entry("login_failed", "info")); err != nil {
		t.Fatalf("el apunte ya esta guardado; un fallo del aviso no puede devolver error: %v", err)
	}
	if len(r.log.created) != 1 {
		t.Fatal("el apunte no se guardo")
	}
	if len(r.pub.alerts) != 1 {
		t.Fatal("aunque falle guardar el evento, la alerta se intenta publicar")
	}
}

func TestBulkRechazaElLoteEnteroSiUnaSeveridadEsInvalida(t *testing.T) {
	r := newRig()
	logs := []*domain.AuditLog{entry("a", "info"), entry("b", "bogus"), entry("c", "info")}
	if err := r.uc.BulkLogActions(context.Background(), logs); !errors.Is(err, domain.ErrInvalidSeverity) {
		t.Fatalf("%v", err)
	}
	if r.log.bulkCalls != 0 {
		t.Fatal("un lote con una entrada invalida llego a la bitacora")
	}
}

func TestBulkAsignaIdsDistintosYUnaSolaHora(t *testing.T) {
	r := newRig()
	given := uuid.New()
	a, b := entry("a", "info"), entry("b", "warning")
	a.ID = given
	if err := r.uc.BulkLogActions(context.Background(), []*domain.AuditLog{a, b}); err != nil {
		t.Fatal(err)
	}
	if len(r.log.bulk) != 2 || a.ID != given || b.ID == uuid.Nil || a.ID == b.ID {
		t.Fatalf("ids: %v %v", a.ID, b.ID)
	}
	if !a.CreatedAt.Equal(b.CreatedAt) || a.CreatedAt.IsZero() {
		t.Fatal("el lote comparte hora")
	}
}

func TestBulkPropagaElErrorDeLaBitacora(t *testing.T) {
	r := newRig()
	r.log.bulkErr = errBoom
	if err := r.uc.BulkLogActions(context.Background(), []*domain.AuditLog{entry("a", "info")}); !errors.Is(err, errBoom) {
		t.Fatalf("%v", err)
	}
}

func TestSearchDevuelveLaPaginaYElTotal(t *testing.T) {
	r := newRig()
	r.log.listed = []*domain.AuditLog{entry("a", "info")}
	r.log.total = 41
	q := domain.AuditQuery{TenantID: uuid.New()}
	logs, total, err := r.uc.SearchAuditLogs(context.Background(), q, 3, 20)
	if err != nil || len(logs) != 1 || total != 41 {
		t.Fatalf("%v %d %v", logs, total, err)
	}
	if r.log.lastQuery.TenantID != q.TenantID || r.log.lastPage != 3 || r.log.lastPgSize != 20 {
		t.Fatal("la consulta no llego intacta a la bitacora")
	}
}

func TestSearchPropagaCadaFalloSinResultadoParcial(t *testing.T) {
	r := newRig()
	r.log.listErr = errBoom
	if logs, total, err := r.uc.SearchAuditLogs(context.Background(), domain.AuditQuery{}, 1, 20); !errors.Is(err, errBoom) || logs != nil || total != 0 {
		t.Fatalf("fallo de List: %v %v %d", err, logs, total)
	}
	r.log.listErr, r.log.countErr = nil, errBoom
	if logs, total, err := r.uc.SearchAuditLogs(context.Background(), domain.AuditQuery{}, 1, 20); !errors.Is(err, errBoom) || logs != nil || total != 0 {
		t.Fatalf("fallo de Count: %v %v %d", err, logs, total)
	}
}

func TestGetAuditLogDistingueAusenteDeFallo(t *testing.T) {
	r := newRig()
	if _, err := r.uc.GetAuditLog(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, domain.ErrLogNotFound) {
		t.Fatalf("ausente: %v", err)
	}
	r.log.byIDErr = errBoom
	if _, err := r.uc.GetAuditLog(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, errBoom) {
		t.Fatalf("fallo: %v", err)
	}
	r.log.byIDErr, r.log.byID = nil, entry("a", "info")
	if l, err := r.uc.GetAuditLog(context.Background(), uuid.New(), uuid.New()); err != nil || l == nil {
		t.Fatalf("presente: %v", err)
	}
}

func TestVerifyChainIntegrityDevuelveElVeredictoTalCual(t *testing.T) {
	r := newRig()
	broken := uuid.New()
	r.log.verified = &domain.ChainIntegrity{OK: false, Checked: 7, BrokenID: &broken}
	res, err := r.uc.VerifyChainIntegrity(context.Background(), uuid.New())
	if err != nil || res.OK || res.Checked != 7 || *res.BrokenID != broken {
		t.Fatalf("%+v %v", res, err)
	}
	r.log.verifyErr = errBoom
	if _, err := r.uc.VerifyChainIntegrity(context.Background(), uuid.New()); !errors.Is(err, errBoom) {
		t.Fatalf("un fallo de lectura no puede convertirse en un veredicto: %v", err)
	}
}

func TestAcknowledgeSoloReconoceUnEventoDeLaEmpresa(t *testing.T) {
	r := newRig()
	evt, by := uuid.New(), uuid.New()

	if err := r.uc.AcknowledgeSecurityEvent(context.Background(), evt, uuid.New(), by); !errors.Is(err, domain.ErrSecurityEventNotFound) {
		t.Fatalf("ausente: %v", err)
	}
	if len(r.sec.acked) != 0 {
		t.Fatal("se reconocio un evento que no existe para esa empresa")
	}

	r.sec.byID = &domain.SecurityEvent{ID: evt}
	if err := r.uc.AcknowledgeSecurityEvent(context.Background(), evt, uuid.New(), by); err != nil {
		t.Fatal(err)
	}
	if len(r.sec.acked) != 1 || r.sec.acked[0] != evt || r.sec.ackedBy != by {
		t.Fatalf("reconocimiento: %v por %v", r.sec.acked, r.sec.ackedBy)
	}

	r.sec.byIDErr = errBoom
	if err := r.uc.AcknowledgeSecurityEvent(context.Background(), evt, uuid.New(), by); !errors.Is(err, errBoom) {
		t.Fatalf("%v", err)
	}
}

func TestGetSecurityEventDistingueAusenteDeFallo(t *testing.T) {
	r := newRig()
	if _, err := r.uc.GetSecurityEvent(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, domain.ErrSecurityEventNotFound) {
		t.Fatalf("%v", err)
	}
	r.sec.byIDErr = errBoom
	if _, err := r.uc.GetSecurityEvent(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, errBoom) {
		t.Fatalf("%v", err)
	}
}

func changedFields(batch []*domain.DataChangeRecord) map[string][2]string {
	out := map[string][2]string{}
	for _, rec := range batch {
		var oldV, newV string
		if rec.OldValue != nil {
			oldV = *rec.OldValue
		}
		if rec.NewValue != nil {
			newV = *rec.NewValue
		}
		out[rec.FieldName] = [2]string{oldV, newV}
	}
	return out
}

func TestCompareChangesRegistraSoloLoQueCambio(t *testing.T) {
	r := newRig()
	tenant, logID := uuid.New(), uuid.New()
	before := `{"name":"Ana","plan":"basic","quota":10,"tags":["a"],"same":"x","gone":true}`
	after := `{"name":"Ana Maria","plan":"basic","quota":10.5,"tags":["a","b"],"same":"x","added":{"k":1}}`
	if err := r.uc.CompareChanges(context.Background(), tenant, logID, before, after); err != nil {
		t.Fatal(err)
	}
	if len(r.chg.batches) != 1 {
		t.Fatalf("lotes: %d", len(r.chg.batches))
	}
	got := changedFields(r.chg.batches[0])
	want := map[string][2]string{
		"name":  {`"Ana"`, `"Ana Maria"`},
		"quota": {`10`, `10.5`},
		"tags":  {`["a"]`, `["a","b"]`},
		"gone":  {`true`, ``},
		"added": {``, `{"k":1}`},
	}
	if len(got) != len(want) {
		t.Fatalf("campos: %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %v, se esperaba %v", k, got[k], v)
		}
	}
	for _, rec := range r.chg.batches[0] {
		if rec.TenantID != tenant || rec.AuditLogID != logID || rec.ID == uuid.Nil {
			t.Fatalf("registro mal referenciado: %+v", rec)
		}
	}
}

func TestCompareChangesSinDiferenciasNoEscribe(t *testing.T) {
	r := newRig()
	doc := `{"a":1,"b":[1,2]}`
	if err := r.uc.CompareChanges(context.Background(), uuid.New(), uuid.New(), doc, `{"b":[1,2],"a":1}`); err != nil {
		t.Fatal(err)
	}
	if len(r.chg.batches) != 0 {
		t.Fatal("el orden de las claves no es un cambio")
	}
}

func TestCompareChangesRechazaJSONIlegible(t *testing.T) {
	r := newRig()
	for _, c := range [][2]string{{`{`, `{}`}, {`{}`, `nope`}, {`[1]`, `{}`}, {``, `{}`}} {
		if err := r.uc.CompareChanges(context.Background(), uuid.New(), uuid.New(), c[0], c[1]); err == nil {
			t.Errorf("%q -> %q: se esperaba error", c[0], c[1])
		}
	}
	if len(r.chg.batches) != 0 {
		t.Fatal("un documento ilegible escribio cambios")
	}
}

func TestCompareChangesPropagaElFalloDelAlmacen(t *testing.T) {
	r := newRig()
	r.chg.err = errBoom
	if err := r.uc.CompareChanges(context.Background(), uuid.New(), uuid.New(), `{"a":1}`, `{"a":2}`); !errors.Is(err, errBoom) {
		t.Fatalf("%v", err)
	}
}

func TestCompareChangesEmiteCadaCampoUnaVez(t *testing.T) {
	r := newRig()
	if err := r.uc.CompareChanges(context.Background(), uuid.New(), uuid.New(), `{"a":1,"b":1,"c":1}`, `{"a":2,"b":2,"c":2}`); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, rec := range r.chg.batches[0] {
		names = append(names, rec.FieldName)
	}
	sort.Strings(names)
	if len(names) != 3 || names[0] != "a" || names[2] != "c" {
		t.Fatalf("campos: %v", names)
	}
}

func TestSeverityToRiskCubreTodaSeveridadValida(t *testing.T) {
	for _, sev := range domain.ValidSeverities {
		if got := severityToRisk(sev); !contains(domain.ValidRiskLevels, got) {
			t.Errorf("%s -> %q no es un nivel de riesgo valido", sev, got)
		}
	}
	for action, eventType := range securityActions {
		if !contains(domain.ValidEventTypes, eventType) {
			t.Errorf("%s -> %q no es un tipo de evento valido", action, eventType)
		}
	}
}

var _ ports.AuditLogRepository = (*recordedLogs)(nil)
var _ ports.SecurityEventRepository = (*recordedSecurity)(nil)
var _ ports.DataChangeRepository = (*recordedChanges)(nil)

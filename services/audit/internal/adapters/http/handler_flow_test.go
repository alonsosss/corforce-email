package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/audit/internal/app"
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/alonsosss/corforce-email/services/audit/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

var errStore = errors.New("store down")

// stubLogs hace de bitacora: guarda lo que se le escribe y deja fijar lo que devuelve.
type stubLogs struct {
	created   []*domain.AuditLog
	bulk      []*domain.AuditLog
	byID      *domain.AuditLog
	listed    []*domain.AuditLog
	total     int64
	capped    bool
	verdict   *domain.ChainIntegrity
	err       error
	query     domain.AuditQuery
	page, per int
	// gate, si no es nil, retiene la verificacion de la cadena hasta que se cierre.
	gate chan struct{}
}

func (s *stubLogs) Create(_ context.Context, l *domain.AuditLog) error {
	if s.err != nil {
		return s.err
	}
	s.created = append(s.created, l)
	return nil
}
func (s *stubLogs) BulkCreate(_ context.Context, l []*domain.AuditLog) error {
	if s.err != nil {
		return s.err
	}
	s.bulk = append(s.bulk, l...)
	return nil
}
func (s *stubLogs) VerifyChain(ctx context.Context, _ uuid.UUID, _ domain.VerifyOptions) (*domain.ChainIntegrity, error) {
	if s.gate != nil {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.verdict == nil {
		return nil, s.err
	}
	v := *s.verdict
	return &v, s.err
}
func (s *stubLogs) GetByID(context.Context, uuid.UUID, uuid.UUID) (*domain.AuditLog, error) {
	return s.byID, s.err
}
func (s *stubLogs) List(_ context.Context, q domain.AuditQuery, page, per int) ([]*domain.AuditLog, error) {
	s.query, s.page, s.per = q, page, per
	return s.listed, s.err
}
func (s *stubLogs) Count(context.Context, domain.AuditQuery) (ports.Total, error) {
	return ports.Total{Value: s.total, Capped: s.capped}, s.err
}
func (s *stubLogs) RecentLoginOtherIP(context.Context, uuid.UUID, uuid.UUID, string, time.Time, uuid.UUID) (string, error) {
	return "", nil
}
func (s *stubLogs) HasUserActionFromIP(context.Context, uuid.UUID, uuid.UUID, string, string, uuid.UUID) (bool, error) {
	return false, nil
}
func (s *stubLogs) ListUserActionAgents(context.Context, uuid.UUID, uuid.UUID, string, uuid.UUID, int) ([]string, error) {
	return nil, nil
}
func (s *stubLogs) CountRecentByActionIP(context.Context, uuid.UUID, string, string, time.Time) (int64, error) {
	return 0, nil
}

type stubSecurity struct {
	byID    *domain.SecurityEvent
	listed  []*domain.SecurityEvent
	total   int64
	filters ports.SecurityFilters
	acked   []uuid.UUID
	ackedBy uuid.UUID
	err     error
}

func (s *stubSecurity) Create(context.Context, *domain.SecurityEvent) error { return nil }
func (s *stubSecurity) VerifyChain(context.Context, uuid.UUID, domain.VerifyOptions) (*domain.ChainIntegrity, error) {
	return &domain.ChainIntegrity{OK: true, Chain: domain.ChainSecurityEvents}, s.err
}
func (s *stubSecurity) GetByID(context.Context, uuid.UUID, uuid.UUID) (*domain.SecurityEvent, error) {
	return s.byID, s.err
}
func (s *stubSecurity) List(_ context.Context, _ uuid.UUID, f ports.SecurityFilters, _, _ int) ([]*domain.SecurityEvent, int64, error) {
	s.filters = f
	return s.listed, s.total, s.err
}
func (s *stubSecurity) Acknowledge(_ context.Context, id, by uuid.UUID) error {
	s.acked, s.ackedBy = append(s.acked, id), by
	return s.err
}
func (s *stubSecurity) GetUnacknowledged(context.Context, uuid.UUID) ([]*domain.SecurityEvent, error) {
	return s.listed, s.err
}
func (s *stubSecurity) HasRecentEvent(context.Context, uuid.UUID, string, string, time.Time) (bool, error) {
	return false, nil
}

type stubChanges struct {
	batches [][]*domain.DataChangeRecord
	listed  []*domain.DataChangeRecord
	err     error
}

func (s *stubChanges) CreateBatch(_ context.Context, r []*domain.DataChangeRecord) error {
	s.batches = append(s.batches, r)
	return s.err
}
func (s *stubChanges) GetByLogID(context.Context, uuid.UUID, uuid.UUID) ([]*domain.DataChangeRecord, error) {
	return s.listed, s.err
}

type stubSummary struct {
	rows     []*domain.AuditSummary
	count    int64
	from, to time.Time
	err      error
}

func (s *stubSummary) GetByModule(_ context.Context, _ uuid.UUID, from, to time.Time) ([]*domain.AuditSummary, error) {
	s.from, s.to = from, to
	return s.rows, s.err
}
func (s *stubSummary) GetUserActivity(_ context.Context, _, _ uuid.UUID, from, to time.Time) (int64, error) {
	s.from, s.to = from, to
	return s.count, s.err
}

type stubPublisher struct{}

// stubAnchors hace de tabla de anclas vacia: la cadena no tiene ninguna, asi que el veredicto
// lo dan solo las filas.
type stubAnchors struct{ headSeq int64 }

func (a stubAnchors) Head(context.Context, domain.ChainName) (*domain.ChainHead, error) {
	if a.headSeq > 0 {
		return &domain.ChainHead{Seq: a.headSeq}, nil
	}
	return nil, nil
}
func (stubAnchors) Findings(context.Context, domain.ChainName) (domain.AnchorFindings, error) {
	return domain.AnchorFindings{}, nil
}
func (stubAnchors) Save(context.Context, *domain.ChainAnchor) (bool, error) { return false, nil }

func (stubPublisher) PublishSecurityAlert(_, _, _, _, _, _ string) error { return nil }

type flow struct {
	srv      http.Handler
	logs     *stubLogs
	security *stubSecurity
	changes  *stubChanges
	summary  *stubSummary
	tenant   string
	user     string
}

// newFlow monta las rutas reales sobre el caso de uso real con las bitacoras de arriba. Los
// roles del sistema pasan la autorizacion sin consultar a access-control: aqui se prueba lo
// que hace cada ruta, no el permiso (lo cubre handler_test.go).
func newFlow() *flow {
	f := &flow{logs: &stubLogs{}, security: &stubSecurity{}, changes: &stubChanges{}, summary: &stubSummary{},
		tenant: uuid.NewString(), user: uuid.NewString()}
	uc := app.NewAuditUseCase(app.AuditDeps{Logs: f.logs, Security: f.security, Changes: f.changes,
		Summary: f.summary, Events: stubPublisher{}, Logger: zap.NewNop(), Anchors: stubAnchors{}})
	r := chi.NewRouter()
	r.Use(middleware.InjectFromGateway)
	r.Mount(base, NewHandler(uc, authz.NewChecker(unreachable, "")).Routes())
	f.srv = r
	return f
}

func (f *flow) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", f.user)
	req.Header.Set("X-Tenant-ID", f.tenant)
	req.Header.Set("X-User-Roles", middleware.RoleTenantAdmin)
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

type envelope struct {
	Data  json.RawMessage `json:"data"`
	Meta  map[string]any  `json:"meta"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) envelope {
	t.Helper()
	var e envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("cuerpo ilegible (%d): %q", rec.Code, rec.Body.String())
	}
	return e
}

func validLog(extra string) string {
	body := `{"action":"invoice.created","module":"billing","resource":"invoices","severity":"info","ip_address":"203.0.113.9"`
	if extra != "" {
		body += "," + extra
	}
	return body + "}"
}

func TestSearchTraduceLosFiltrosYAcotaLaPaginacion(t *testing.T) {
	f := newFlow()
	uid := uuid.NewString()
	url := base + "/logs?user_id=" + uid + "&module=billing&resource=invoices&action=invoice.created&severity=warning" +
		"&ip_address=203.0.113.9&date_from=2026-01-01T00:00:00Z&date_to=2026-01-31T23:59:59Z&page=3&per_page=50"
	f.logs.total = 120
	f.logs.listed = []*domain.AuditLog{{ID: uuid.New()}}
	rec := f.do(http.MethodGet, url, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	q := f.logs.query
	if q.TenantID.String() != f.tenant {
		t.Fatalf("la consulta se acota a la empresa de la sesion, no a la del cliente: %v", q.TenantID)
	}
	if q.UserID == nil || q.UserID.String() != uid || *q.Module != "billing" || *q.Resource != "invoices" ||
		*q.Action != "invoice.created" || *q.Severity != "warning" || *q.IPAddress != "203.0.113.9" {
		t.Fatalf("filtros: %+v", q)
	}
	if q.DateFrom == nil || !q.DateFrom.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) || q.DateTo == nil {
		t.Fatalf("fechas: %v %v", q.DateFrom, q.DateTo)
	}
	if f.logs.page != 3 || f.logs.per != 50 {
		t.Fatalf("pagina %d, tamano %d", f.logs.page, f.logs.per)
	}
	e := decode(t, rec)
	if e.Meta["total"] != float64(120) || e.Meta["page"] != float64(3) {
		t.Fatalf("meta: %v", e.Meta)
	}
	if _, ok := e.Meta["total_capped"]; ok {
		t.Fatalf("un total exacto no lleva total_capped: %v", e.Meta)
	}
}

func TestSearchDeclaraQueElTotalEsUnTope(t *testing.T) {
	f := newFlow()
	f.logs.total, f.logs.capped = 10000, true
	f.logs.listed = []*domain.AuditLog{{ID: uuid.New()}}
	rec := f.do(http.MethodGet, base+"/logs?page=2&per_page=50", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	e := decode(t, rec)
	if e.Meta["total"] != float64(10000) || e.Meta["total_capped"] != true || e.Meta["total_pages"] != float64(200) {
		t.Fatalf("meta: %v", e.Meta)
	}
}

func TestSearchIgnoraPaginacionFueraDeRango(t *testing.T) {
	f := newFlow()
	for _, q := range []string{"page=0&per_page=101", "page=-4&per_page=0", "page=abc&per_page=xyz", "per_page=1000000"} {
		if rec := f.do(http.MethodGet, base+"/logs?"+q, ""); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", q, rec.Code)
		}
		if f.logs.page != 1 || f.logs.per != 20 {
			t.Fatalf("%s: pagina %d, tamano %d; se esperaba la de por defecto 1/20", q, f.logs.page, f.logs.per)
		}
	}
}

func TestSearchRechazaFiltrosMalFormados(t *testing.T) {
	f := newFlow()
	for _, q := range []string{"user_id=nope", "date_from=ayer", "date_to=2026-13-40", "date_from=2026-01-01"} {
		rec := f.do(http.MethodGet, base+"/logs?"+q, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, se esperaba 400", q, rec.Code)
		}
	}
}

func TestSearchSinResultadosDevuelveListaVacia(t *testing.T) {
	f := newFlow()
	rec := f.do(http.MethodGet, base+"/logs", "")
	if rec.Code != http.StatusOK || strings.TrimSpace(string(decode(t, rec).Data)) != "[]" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestUnFalloDelAlmacenNoFiltraSuMensaje(t *testing.T) {
	f := newFlow()
	f.logs.err, f.security.err, f.summary.err, f.changes.err = errStore, errStore, errStore, errStore
	for _, p := range []struct{ method, path, body string }{
		{http.MethodGet, base + "/logs", ""},
		{http.MethodPost, base + "/logs", validLog(`"user_id":"` + f.user + `"`)},
		{http.MethodPost, base + "/logs/bulk", `{"logs":[` + validLog(`"user_id":"`+f.user+`"`) + `]}`},
		{http.MethodGet, base + "/logs/" + uuid.NewString(), ""},
		{http.MethodGet, base + "/integrity", ""},
		{http.MethodGet, base + "/security-events", ""},
		{http.MethodGet, base + "/security-events/unacknowledged", ""},
		{http.MethodGet, base + "/security-events/" + uuid.NewString(), ""},
		{http.MethodPost, base + "/security-events/" + uuid.NewString() + "/acknowledge", ""},
		{http.MethodGet, base + "/summary", ""},
		{http.MethodGet, base + "/user-activity/" + uuid.NewString(), ""},
		{http.MethodGet, base + "/changes/" + uuid.NewString(), ""},
	} {
		rec := f.do(p.method, p.path, p.body)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s %s: %d, se esperaba 500", p.method, p.path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), errStore.Error()) {
			t.Errorf("%s %s filtra el error interno: %s", p.method, p.path, rec.Body)
		}
	}
}

func TestCrearApunteFijaLaEmpresaYElUsuarioDeLaSesion(t *testing.T) {
	f := newFlow()
	otraEmpresa := uuid.NewString()
	body := validLog(`"user_id":"` + f.user + `","tenant_id":"` + otraEmpresa + `"`)
	if rec := f.do(http.MethodPost, base+"/logs", body); rec.Code != http.StatusBadRequest {
		t.Fatalf("un campo tenant_id en el cuerpo debe rechazarse: %d", rec.Code)
	}
	if len(f.logs.created) != 0 {
		t.Fatal("se escribio con un cuerpo rechazado")
	}

	rec := f.do(http.MethodPost, base+"/logs", validLog(""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	got := f.logs.created[0]
	if got.TenantID.String() != f.tenant || got.UserID.String() != f.user {
		t.Fatalf("empresa %s usuario %s; se esperaba la sesion", got.TenantID, got.UserID)
	}
	var out domain.AuditLog
	if err := json.Unmarshal(decode(t, rec).Data, &out); err != nil || out.ID == uuid.Nil || out.CreatedAt.IsZero() {
		t.Fatalf("respuesta: %v %+v", err, out)
	}
}

func TestCrearApunteRegistraElDiffDeAntesYDespues(t *testing.T) {
	f := newFlow()
	body := validLog(`"before":"{\"plan\":\"basic\"}","after":"{\"plan\":\"pro\"}","session_id":"` + uuid.NewString() + `"`)
	if rec := f.do(http.MethodPost, base+"/logs", body); rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if f.logs.created[0].SessionID == nil {
		t.Fatal("la sesion del apunte se descarto")
	}
	if len(f.changes.batches) != 1 || len(f.changes.batches[0]) != 1 || f.changes.batches[0][0].FieldName != "plan" {
		t.Fatalf("diff: %+v", f.changes.batches)
	}
}

func TestCrearApunteValidaLaEntrada(t *testing.T) {
	f := newFlow()
	larga := strings.Repeat("x", 101)
	ip := strings.Repeat("1", 46)
	casos := map[string]string{
		"sin accion":          `{"module":"m","resource":"r","severity":"info"}`,
		"sin modulo":          `{"action":"a","resource":"r","severity":"info"}`,
		"sin recurso":         `{"action":"a","module":"m","severity":"info"}`,
		"sin severidad":       `{"action":"a","module":"m","resource":"r"}`,
		"severidad invalida":  `{"action":"a","module":"m","resource":"r","severity":"fatal"}`,
		"accion demasiado":    `{"action":"` + larga + `","module":"m","resource":"r","severity":"info"}`,
		"modulo demasiado":    `{"action":"a","module":"` + larga + `","resource":"r","severity":"info"}`,
		"ip demasiado larga":  `{"action":"a","module":"m","resource":"r","severity":"info","ip_address":"` + ip + `"}`,
		"request_id largo":    `{"action":"a","module":"m","resource":"r","severity":"info","request_id":"` + larga + `"}`,
		"sesion no uuid":      `{"action":"a","module":"m","resource":"r","severity":"info","session_id":"abc"}`,
		"before no es json":   `{"action":"a","module":"m","resource":"r","severity":"info","before":"{no"}`,
		"after no es json":    `{"action":"a","module":"m","resource":"r","severity":"info","after":"nope"}`,
		"solo espacios":       `{"action":"   ","module":"m","resource":"r","severity":"info"}`,
		"tipo incorrecto":     `{"action":5,"module":"m","resource":"r","severity":"info"}`,
		"cuerpo con basura":   `{"action":"a","module":"m","resource":"r","severity":"info"} {}`,
		"cuerpo no es objeto": `[]`,
	}
	for name, body := range casos {
		t.Run(name, func(t *testing.T) {
			rec := f.do(http.MethodPost, base+"/logs", body)
			if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
				t.Fatalf("%d, se esperaba 400 o 422: %s", rec.Code, rec.Body)
			}
		})
	}
	if len(f.logs.created) != 0 {
		t.Fatalf("se guardaron %d entradas invalidas", len(f.logs.created))
	}
}

func TestBulkRegistraElLoteConLaSesion(t *testing.T) {
	f := newFlow()
	body := `{"logs":[` + validLog(`"session_id":"`+uuid.NewString()+`"`) + `,` + validLog("") + `]}`
	rec := f.do(http.MethodPost, base+"/logs/bulk", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var out map[string]int
	if err := json.Unmarshal(decode(t, rec).Data, &out); err != nil || out["count"] != 2 {
		t.Fatalf("%v %v", out, err)
	}
	for _, l := range f.logs.bulk {
		if l.TenantID.String() != f.tenant || l.UserID.String() != f.user {
			t.Fatalf("empresa %s usuario %s", l.TenantID, l.UserID)
		}
	}
	if f.logs.bulk[0].SessionID == nil {
		t.Fatal("la sesion del apunte se descarto")
	}
}

func TestBulkRechazaElLoteSiUnaEntradaEsInvalida(t *testing.T) {
	f := newFlow()
	bad := `{"action":"a","module":"m","resource":"r","severity":"fatal"}`
	rec := f.do(http.MethodPost, base+"/logs/bulk", `{"logs":[`+validLog("")+`,`+bad+`]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("%d, una severidad invalida es un 422 y no un 500: %s", rec.Code, rec.Body)
	}
	if e := decode(t, rec); e.Error == nil || !strings.Contains(e.Error.Message, "logs[1]") {
		t.Fatalf("el mensaje debe decir que entrada falla: %s", rec.Body)
	}
	if len(f.logs.bulk) != 0 {
		t.Fatal("un lote con una entrada invalida se escribio parcialmente")
	}
}

func TestBulkAcotaElTamanoDelLote(t *testing.T) {
	f := newFlow()
	if rec := f.do(http.MethodPost, base+"/logs/bulk", `{"logs":[]}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("lote vacio: %d", rec.Code)
	}
	if rec := f.do(http.MethodPost, base+"/logs/bulk", `{}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("sin logs: %d", rec.Code)
	}
	items := make([]string, maxBulkLogs+1)
	for i := range items {
		items[i] = validLog("")
	}
	if rec := f.do(http.MethodPost, base+"/logs/bulk", `{"logs":[`+strings.Join(items, ",")+`]}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("lote de %d: %d", len(items), rec.Code)
	}
	if rec := f.do(http.MethodPost, base+"/logs/bulk", `{"logs":[`+strings.Join(items[:maxBulkLogs], ",")+`]}`); rec.Code != http.StatusCreated {
		t.Fatalf("lote de %d: %d", maxBulkLogs, rec.Code)
	}
}

func TestGetLogDistingueInvalidoAusenteYPresente(t *testing.T) {
	f := newFlow()
	if rec := f.do(http.MethodGet, base+"/logs/no-es-uuid", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("id malo: %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, base+"/logs/"+uuid.NewString(), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("ausente: %d", rec.Code)
	}
	f.logs.byID = &domain.AuditLog{ID: uuid.New(), Action: "x"}
	if rec := f.do(http.MethodGet, base+"/logs/"+uuid.NewString(), ""); rec.Code != http.StatusOK {
		t.Fatalf("presente: %d", rec.Code)
	}
}

func TestIntegridadDevuelveElVeredictoDeLaCadena(t *testing.T) {
	f := newFlow()
	broken := uuid.New()
	f.logs.verdict = &domain.ChainIntegrity{OK: false, Checked: 4, BrokenID: &broken}
	rec := f.do(http.MethodGet, base+"/integrity", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d", rec.Code)
	}
	var got domain.ChainIntegrity
	if err := json.Unmarshal(decode(t, rec).Data, &got); err != nil || got.OK || got.Checked != 4 || got.BrokenID == nil || *got.BrokenID != broken {
		t.Fatalf("%+v %v", got, err)
	}

	f.logs.verdict = &domain.ChainIntegrity{OK: true, Checked: 9}
	rec = f.do(http.MethodGet, base+"/integrity", "")
	if strings.Contains(rec.Body.String(), "broken_id") || strings.Contains(rec.Body.String(), "reason") {
		t.Fatalf("una cadena intacta no senala ninguna fila ni causa: %s", rec.Body)
	}
}

func TestIntegridadOcupadaEsUn429ConReintento(t *testing.T) {
	f := newFlow()
	f.logs.err = domain.ErrVerificationBusy
	rec := f.do(http.MethodGet, base+"/integrity", "")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("%d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if env := decode(t, rec); env.Error == nil || env.Error.Code != "VERIFICATION_BUSY" {
		t.Fatalf("%s", rec.Body)
	}
}

func TestIntegridadDiceLaCausaLaCadenaYLaVersionDeLaFilaRota(t *testing.T) {
	f := newFlow()
	seq, version := int64(41), 2
	f.logs.verdict = &domain.ChainIntegrity{
		OK: false, Checked: 40, Chain: domain.ChainAuditLogs, Reason: domain.ReasonHashKeyMissing,
		BrokenSeq: &seq, BrokenVersion: &version, Versions: map[string]int{"1": 10, "2": 30},
	}
	rec := f.do(http.MethodGet, base+"/integrity", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(decode(t, rec).Data, &got); err != nil {
		t.Fatal(err)
	}
	if got["reason"] != "hash_key_missing" || got["chain"] != "audit_logs" || got["broken_seq"] != float64(41) || got["broken_hash_version"] != float64(2) {
		t.Fatalf("%v", got)
	}
	if v, _ := got["versions"].(map[string]any); v["1"] != float64(10) || v["2"] != float64(30) {
		t.Fatalf("no reporta las filas por version: %v", got["versions"])
	}
}

func TestEventosDeSeguridadFiltranYReconocen(t *testing.T) {
	f := newFlow()
	f.security.total = 3
	rec := f.do(http.MethodGet, base+"/security-events?event_type=failed_login&risk_level=high&acknowledged=false&date_from=2026-01-01T00:00:00Z&date_to=2026-02-01T00:00:00Z", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	fl := f.security.filters
	if *fl.EventType != "failed_login" || *fl.RiskLevel != "high" || fl.Acknowledged == nil || *fl.Acknowledged || fl.DateFrom == nil || fl.DateTo == nil {
		t.Fatalf("filtros: %+v", fl)
	}
	for _, q := range []string{"acknowledged=quiza", "date_from=x", "date_to=y"} {
		if rec := f.do(http.MethodGet, base+"/security-events?"+q, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}

	id := uuid.New()
	if rec := f.do(http.MethodPost, base+"/security-events/"+id.String()+"/acknowledge", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("ausente: %d", rec.Code)
	}
	if len(f.security.acked) != 0 {
		t.Fatal("se reconocio un evento inexistente")
	}
	f.security.byID = &domain.SecurityEvent{ID: id}
	if rec := f.do(http.MethodPost, base+"/security-events/"+id.String()+"/acknowledge", ""); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(f.security.acked) != 1 || f.security.ackedBy.String() != f.user {
		t.Fatalf("el reconocimiento se atribuye al usuario de la sesion: %v", f.security.ackedBy)
	}
	if rec := f.do(http.MethodPost, base+"/security-events/no-uuid/acknowledge", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("id malo: %d", rec.Code)
	}
}

func TestReconocerExigeUsuarioEnLaSesion(t *testing.T) {
	f := newFlow()
	req := httptest.NewRequest(http.MethodPost, base+"/security-events/"+uuid.NewString()+"/acknowledge", nil)
	req.Header.Set("X-Tenant-ID", f.tenant)
	req.Header.Set("X-User-Roles", middleware.RoleSuperadmin)
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || len(f.security.acked) != 0 {
		t.Fatalf("%d, reconocimientos: %d", rec.Code, len(f.security.acked))
	}
}

func TestGetSecurityEventDistingueAusenteYPresente(t *testing.T) {
	f := newFlow()
	if rec := f.do(http.MethodGet, base+"/security-events/"+uuid.NewString(), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("ausente: %d", rec.Code)
	}
	f.security.byID = &domain.SecurityEvent{ID: uuid.New()}
	if rec := f.do(http.MethodGet, base+"/security-events/"+uuid.NewString(), ""); rec.Code != http.StatusOK {
		t.Fatalf("presente: %d", rec.Code)
	}
}

func TestResumenYActividadUsanElRangoPedidoOElUltimoMes(t *testing.T) {
	f := newFlow()
	uid := uuid.NewString()
	from, to := "2026-03-01T00:00:00Z", "2026-03-31T00:00:00Z"
	for _, path := range []string{base + "/summary", base + "/user-activity/" + uid} {
		if rec := f.do(http.MethodGet, path+"?from="+from+"&to="+to, ""); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, rec.Code)
		}
		if f.summary.from.Format(time.RFC3339) != from || f.summary.to.Format(time.RFC3339) != to {
			t.Fatalf("%s: rango %v - %v", path, f.summary.from, f.summary.to)
		}
		if rec := f.do(http.MethodGet, path, ""); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, rec.Code)
		}
		if span := f.summary.to.Sub(f.summary.from); span < 27*24*time.Hour || span > 32*24*time.Hour {
			t.Fatalf("%s: sin rango el periodo por defecto es un mes, no %v", path, span)
		}
		for _, bad := range []string{"?from=x&to=" + to, "?from=" + from + "&to=y"} {
			if rec := f.do(http.MethodGet, path+bad, ""); rec.Code != http.StatusBadRequest {
				t.Fatalf("%s%s: %d", path, bad, rec.Code)
			}
		}
	}
	if rec := f.do(http.MethodGet, base+"/user-activity/no-uuid", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("usuario malo: %d", rec.Code)
	}
	f.summary.count = 17
	rec := f.do(http.MethodGet, base+"/user-activity/"+uid, "")
	var out map[string]any
	if err := json.Unmarshal(decode(t, rec).Data, &out); err != nil || out["total_actions"] != float64(17) || out["user_id"] != uid {
		t.Fatalf("%v %v", out, err)
	}
}

func TestChangesDevuelveLosCamposDelApunte(t *testing.T) {
	f := newFlow()
	old, next := "basic", "pro"
	f.changes.listed = []*domain.DataChangeRecord{{ID: uuid.New(), FieldName: "plan", OldValue: &old, NewValue: &next}}
	rec := f.do(http.MethodGet, base+"/changes/"+uuid.NewString(), "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"field_name":"plan"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestSinEmpresaValidaNingunaRutaLlegaALaBitacora(t *testing.T) {
	f := newFlow()
	f.tenant = "no-es-uuid"
	for _, rt := range rutas {
		if rec := f.do(rt.method, rt.path, rt.body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s: %d", rt.method, rt.path, rec.Code)
		}
	}
	if len(f.logs.created)+len(f.logs.bulk) != 0 {
		t.Fatal("se escribio sin empresa")
	}
}

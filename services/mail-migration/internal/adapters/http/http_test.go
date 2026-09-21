package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	testRunnerKey = "clave-del-ejecutor-de-prueba-0123456789"
	sourcePass    = "clave-de-origen-123"
)

type env struct {
	uc        *app.UseCase
	repo      *apptest.Repo
	mailbox   *apptest.Mailboxes
	tenant    uuid.UUID
	user      uuid.UUID
	admin     http.Handler
	runner    http.Handler
	policy    *[]map[string]string
	policySrv *httptest.Server
	checker   *authz.Checker
}

func newEnv(t *testing.T, configured bool) *env {
	t.Helper()
	e := &env{repo: apptest.NewRepo(), tenant: uuid.New(), user: uuid.New()}
	e.mailbox = &apptest.Mailboxes{Ref: ports.MailboxRef{Username: "ana@acme.test", Active: true}}
	tenants := &apptest.Tenants{IDs: []uuid.UUID{e.tenant}}
	perms := []map[string]string{}
	e.policy = &perms
	e.policySrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"permissions": *e.policy}})
	}))
	t.Cleanup(e.policySrv.Close)
	e.uc = app.New(app.Deps{
		Repo: e.repo, Tx: apptest.Tx{}, Mailboxes: e.mailbox,
		Resolver: &apptest.Resolver{Addrs: []netip.Addr{netip.MustParseAddr("93.184.216.34")}},
		Cipher:   apptest.Cipher{}, Tenants: tenants, Events: &apptest.Events{},
		Config: app.Config{
			RunnerConfigured: configured, MaxActivePerTenant: 2, Lease: 90 * time.Second, MaxAttempts: 3, SweepInterval: time.Second,
			Source: domain.SourcePolicy{Ports: []int{143, 993}},
		},
	})
	e.checker = authz.NewChecker(e.policySrv.URL, "token")
	e.admin = middleware.InjectFromGateway(NewHandler(e.uc, e.checker).Routes())
	e.runner = NewRunnerHandler(e.uc, testRunnerKey, zap.NewNop()).Routes()
	return e
}

func (e *env) allow(actions ...string) {
	e.checker.Invalidate(e.user.String(), e.tenant.String())
	*e.policy = nil
	for _, a := range actions {
		*e.policy = append(*e.policy, map[string]string{"module": "migration", "resource": "jobs", "action": a})
	}
}

func (e *env) do(h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func (e *env) asUser(method, path, body string) *httptest.ResponseRecorder {
	return e.do(e.admin, method, path, body, map[string]string{"X-User-ID": e.user.String(), "X-Tenant-ID": e.tenant.String()})
}

func (e *env) asRunner(method, path, body string) *httptest.ResponseRecorder {
	return e.do(e.runner, method, path, body, map[string]string{"Authorization": "Bearer " + testRunnerKey})
}

func createBody(mailbox uuid.UUID) string {
	b, _ := json.Marshal(map[string]any{
		"mailbox_id": mailbox.String(), "source_host": "imap.origen.example", "source_port": 993, "source_tls": "ssl",
		"source_username": "ana@origen.example", "source_password": sourcePass,
	})
	return string(b)
}

type errEnvelope struct {
	Error *struct {
		Code    string            `json:"code"`
		Details map[string]string `json:"details"`
	} `json:"error"`
	Data json.RawMessage `json:"data"`
}

func decodeEnv(t *testing.T, rec *httptest.ResponseRecorder) errEnvelope {
	t.Helper()
	var env errEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("cuerpo ilegible %q: %v", rec.Body.String(), err)
	}
	return env
}

func TestCrearDevuelveElTrabajoSinNingunaCredencial(t *testing.T) {
	e := newEnv(t, true)
	e.allow("create")
	rec := e.asUser(http.MethodPost, "/api/v1/mail-migration/jobs", createBody(uuid.New()))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, secret := range []string{sourcePass, "password", "enc:", "lease"} {
		if strings.Contains(body, secret) {
			t.Fatalf("la respuesta contiene %q: %s", secret, body)
		}
	}
	var out struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data["status"] != "pending" || out.Data["mailbox_username"] != "ana@acme.test" || out.Data["source_username"] != "ana@origen.example" {
		t.Fatalf("job: %v", out.Data)
	}
	if progress, _ := out.Data["progress"].(map[string]any); progress["folders"] == nil {
		t.Fatalf("folders debe ser [] y no null: %v", progress)
	}
}

func TestCadaRutaExigeSuPermiso(t *testing.T) {
	e := newEnv(t, true)
	e.allow("read")
	if rec := e.asUser(http.MethodPost, "/api/v1/mail-migration/jobs", createBody(uuid.New())); rec.Code != http.StatusForbidden {
		t.Errorf("crear con solo read: %d", rec.Code)
	}
	if rec := e.asUser(http.MethodPost, "/api/v1/mail-migration/jobs/"+uuid.NewString()+"/cancel", ""); rec.Code != http.StatusForbidden {
		t.Errorf("cancelar con solo read: %d", rec.Code)
	}
	for _, path := range []string{"/api/v1/mail-migration/meta", "/api/v1/mail-migration/jobs"} {
		if rec := e.asUser(http.MethodGet, path, ""); rec.Code != http.StatusOK {
			t.Errorf("GET %s con read: %d %s", path, rec.Code, rec.Body)
		}
	}
	e.allow("create", "cancel")
	if rec := e.asUser(http.MethodGet, "/api/v1/mail-migration/jobs", ""); rec.Code != http.StatusForbidden {
		t.Errorf("listar sin read: %d", rec.Code)
	}
	if rec := e.asUser(http.MethodGet, "/api/v1/mail-migration/jobs/"+uuid.NewString(), ""); rec.Code != http.StatusForbidden {
		t.Errorf("ver sin read: %d", rec.Code)
	}
}

func TestCrearTraduceLosErrores(t *testing.T) {
	e := newEnv(t, true)
	e.allow("create")
	post := func(body string) (int, errEnvelope) {
		rec := e.asUser(http.MethodPost, "/api/v1/mail-migration/jobs", body)
		return rec.Code, decodeEnv(t, rec)
	}
	mailbox := uuid.New()
	mut := func(key string, value any) string {
		var m map[string]any
		_ = json.Unmarshal([]byte(createBody(mailbox)), &m)
		m[key] = value
		b, _ := json.Marshal(m)
		return string(b)
	}

	if code, env := post(mut("source_host", "169.254.169.254")); code != 422 || env.Error.Code != "SOURCE_HOST_NOT_ALLOWED" || env.Error.Details["field"] != "source_host" {
		t.Errorf("host interno: %d %+v", code, env.Error)
	}
	if code, env := post(mut("source_port", 25)); code != 422 || env.Error.Code != "VALIDATION_ERROR" || env.Error.Details["field"] != "source_port" {
		t.Errorf("puerto: %d %+v", code, env.Error)
	}
	if code, env := post(mut("mailbox_id", "no-es-uuid")); code != 422 || env.Error.Details["field"] != "mailbox_id" {
		t.Errorf("mailbox_id: %d %+v", code, env.Error)
	}
	if code, _ := post(`{"mailbox_id":"x","otro":1}`); code != 400 {
		t.Errorf("campo desconocido: %d", code)
	}
	if code, _ := post(strings.Repeat("a", 20<<10)); code != 400 {
		t.Errorf("cuerpo enorme: %d", code)
	}
	e.mailbox.Err = domain.ErrMailboxNotFound
	if code, env := post(mut("source_host", "imap.origen.example")); code != 404 || env.Error.Code != "MAILBOX_NOT_FOUND" {
		t.Errorf("buzon ajeno: %d %+v", code, env.Error)
	}
	e.mailbox.Err = nil
	e.mailbox.Ref.Active = false
	if code, env := post(createBody(mailbox)); code != 409 || env.Error.Code != "MAILBOX_INACTIVE" {
		t.Errorf("buzon inactivo: %d %+v", code, env.Error)
	}
	e.mailbox.Ref.Active = true
	if code, _ := post(createBody(mailbox)); code != 201 {
		t.Fatalf("alta: %d", code)
	}
	if code, env := post(createBody(mailbox)); code != 409 || env.Error.Code != "JOB_ALREADY_ACTIVE" {
		t.Errorf("segundo trabajo del buzon: %d %+v", code, env.Error)
	}
	if code, _ := post(createBody(uuid.New())); code != 201 {
		t.Fatalf("segundo buzon: %d", code)
	}
	if code, env := post(createBody(uuid.New())); code != 429 || env.Error.Code != "TENANT_LIMIT_REACHED" {
		t.Errorf("limite: %d %+v", code, env.Error)
	}
}

func TestSinClaveDelEjecutorCrearResponde503(t *testing.T) {
	e := newEnv(t, false)
	e.allow("create", "read")
	rec := e.asUser(http.MethodPost, "/api/v1/mail-migration/jobs", createBody(uuid.New()))
	if env := decodeEnv(t, rec); rec.Code != http.StatusServiceUnavailable || env.Error.Code != "NOT_CONFIGURED" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	meta := e.asUser(http.MethodGet, "/api/v1/mail-migration/meta", "")
	var out struct {
		Data struct {
			Configured        bool              `json:"configured"`
			SourcePorts       []int             `json:"source_ports"`
			SourceTLSModes    []string          `json:"source_tls_modes"`
			DefaultTLSForPort map[string]string `json:"default_tls_for_port"`
			MaxActiveJobs     int               `json:"max_active_jobs"`
			Phases            []string          `json:"phases"`
		} `json:"data"`
	}
	if err := json.Unmarshal(meta.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	d := out.Data
	if d.Configured || len(d.SourcePorts) != 2 || len(d.SourceTLSModes) != 2 || d.DefaultTLSForPort["993"] != "ssl" ||
		d.DefaultTLSForPort["143"] != "starttls" || d.MaxActiveJobs != 2 || len(d.Phases) != 2 {
		t.Fatalf("meta: %+v", d)
	}
}

func TestListarVerYCancelar(t *testing.T) {
	e := newEnv(t, true)
	e.allow("read", "create", "cancel")
	mailbox := uuid.New()
	rec := e.asUser(http.MethodPost, "/api/v1/mail-migration/jobs", createBody(mailbox))
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	list := e.asUser(http.MethodGet, "/api/v1/mail-migration/jobs?mailbox_id="+mailbox.String()+"&status=pending&per_page=5", "")
	var page struct {
		Data []map[string]any `json:"data"`
		Meta struct {
			Total      int `json:"total"`
			TotalPages int `json:"total_pages"`
			PerPage    int `json:"per_page"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil || len(page.Data) != 1 || page.Meta.TotalPages != 1 {
		t.Fatalf("listar: %v %s", err, list.Body)
	}
	if rec := e.asUser(http.MethodGet, "/api/v1/mail-migration/jobs?status=inventado", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("estado invalido: %d", rec.Code)
	}
	if rec := e.asUser(http.MethodGet, "/api/v1/mail-migration/jobs?mailbox_id=x", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("mailbox_id invalido: %d", rec.Code)
	}
	if rec := e.asUser(http.MethodGet, "/api/v1/mail-migration/jobs/"+created.Data.ID, ""); rec.Code != http.StatusOK {
		t.Errorf("ver: %d", rec.Code)
	}
	if rec := e.asUser(http.MethodGet, "/api/v1/mail-migration/jobs/"+uuid.NewString(), ""); rec.Code != http.StatusNotFound {
		t.Errorf("ver inexistente: %d", rec.Code)
	}
	if rec := e.asUser(http.MethodGet, "/api/v1/mail-migration/jobs/no-es-uuid", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("ver con id malo: %d", rec.Code)
	}
	cancel := e.asUser(http.MethodPost, "/api/v1/mail-migration/jobs/"+created.Data.ID+"/cancel", "")
	if cancel.Code != http.StatusOK || !strings.Contains(cancel.Body.String(), `"status":"cancelled"`) {
		t.Fatalf("cancelar: %d %s", cancel.Code, cancel.Body)
	}
	again := e.asUser(http.MethodPost, "/api/v1/mail-migration/jobs/"+created.Data.ID+"/cancel", "")
	if env := decodeEnv(t, again); again.Code != http.StatusConflict || env.Error.Code != "JOB_NOT_CANCELLABLE" {
		t.Fatalf("segunda cancelacion: %d %s", again.Code, again.Body)
	}
}

func TestSinEmpresaOSinUsuarioNoSeAtiende(t *testing.T) {
	e := newEnv(t, true)
	e.allow("read", "create")
	req := e.do(e.admin, http.MethodGet, "/api/v1/mail-migration/jobs", "", map[string]string{"X-User-ID": e.user.String()})
	if req.Code != http.StatusForbidden && req.Code != http.StatusUnauthorized {
		t.Errorf("sin empresa: %d", req.Code)
	}
}

func TestElEjecutorExigeSuClave(t *testing.T) {
	e := newEnv(t, true)
	for name, headers := range map[string]map[string]string{
		"sin cabecera": {},
		"clave mala":   {"Authorization": "Bearer otra"},
		"sin Bearer":   {"Authorization": testRunnerKey},
		"vacia":        {"Authorization": "Bearer "},
		"gateway":      {"X-Gateway-Token": "token", "X-User-ID": uuid.NewString()},
	} {
		rec := e.do(e.runner, http.MethodPost, "/v1/claim", `{"runner_id":"r"}`, headers)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
}

func TestElReclamoEntregaLaCredencialUnaVezYElCierreLaBorra(t *testing.T) {
	e := newEnv(t, true)
	e.allow("create", "read")
	if rec := e.asRunner(http.MethodPost, "/v1/claim", `{"runner_id":"r1"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("sin trabajo: %d %s", rec.Code, rec.Body)
	}
	if rec := e.asUser(http.MethodPost, "/api/v1/mail-migration/jobs", createBody(uuid.New())); rec.Code != http.StatusCreated {
		t.Fatalf("alta: %d", rec.Code)
	}
	claim := e.asRunner(http.MethodPost, "/v1/claim", `{"runner_id":"r1"}`)
	if claim.Code != http.StatusOK || claim.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("claim: %d %v", claim.Code, claim.Header())
	}
	var got struct {
		Data struct {
			JobID    string `json:"job_id"`
			TenantID string `json:"tenant_id"`
			LeaseID  string `json:"lease_id"`
			Source   struct {
				Host, Username, Password string
				Port                     int
				TLS                      string
			} `json:"source"`
			Destination struct{ Username string } `json:"destination"`
		} `json:"data"`
	}
	if err := json.Unmarshal(claim.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Data.Source.Password != sourcePass || got.Data.Source.Host != "imap.origen.example" || got.Data.Source.Port != 993 ||
		got.Data.Source.TLS != "ssl" || got.Data.Destination.Username != "ana@acme.test" || got.Data.TenantID != e.tenant.String() {
		t.Fatalf("claim: %+v", got.Data)
	}
	if strings.Contains(e.asUser(http.MethodGet, "/api/v1/mail-migration/jobs", "").Body.String(), sourcePass) {
		t.Fatal("la API de administracion muestra la contrasena de origen")
	}

	base := "/v1/tenants/" + got.Data.TenantID + "/jobs/" + got.Data.JobID
	hb := `{"lease_id":"` + got.Data.LeaseID + `","phase":"initial","progress":{"folders_total":3,"messages_copied":7,"folders":[{"name":"INBOX","messages_copied":7,"messages_skipped":0,"messages_failed":0}]}}`
	if rec := e.asRunner(http.MethodPost, base+"/heartbeat", hb); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"cancel":false`) {
		t.Fatalf("latido: %d %s", rec.Code, rec.Body)
	}
	if rec := e.asRunner(http.MethodPost, base+"/heartbeat", strings.Replace(hb, got.Data.LeaseID, uuid.NewString(), 1)); rec.Code != http.StatusConflict || decodeEnv(t, rec).Error.Code != "LEASE_LOST" {
		t.Fatalf("lease ajeno: %d %s", rec.Code, rec.Body)
	}
	if rec := e.asRunner(http.MethodPost, base+"/heartbeat", strings.Replace(hb, `"initial"`, `"otra"`, 1)); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("fase invalida: %d", rec.Code)
	}
	if rec := e.asRunner(http.MethodPost, base+"/heartbeat", `{"lease_id":"x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("lease mal formado: %d", rec.Code)
	}
	done := `{"lease_id":"` + got.Data.LeaseID + `","outcome":"failed","progress":{},"error":{"code":"source_auth_failed","message":"login fallido con ` + sourcePass + `"}}`
	rec := e.asRunner(http.MethodPost, base+"/complete", done)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), sourcePass) || !strings.Contains(rec.Body.String(), `"source_auth_failed"`) {
		t.Fatalf("cierre: %d %s", rec.Code, rec.Body)
	}
	for _, j := range e.repo.Jobs {
		if j.SourcePasswordEnc != nil {
			t.Fatal("la credencial sobrevive al cierre")
		}
	}
	if rec := e.asRunner(http.MethodPost, base+"/complete", done); rec.Code != http.StatusConflict {
		t.Fatalf("cerrar dos veces: %d", rec.Code)
	}
	if rec := e.asRunner(http.MethodPost, base+"/complete", strings.Replace(done, `"failed"`, `"paused"`, 1)); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("resultado invalido: %d", rec.Code)
	}
}

func TestElEjecutorRechazaIdentificadoresYEmpresasMalas(t *testing.T) {
	e := newEnv(t, true)
	if rec := e.asRunner(http.MethodPost, "/v1/tenants/no-uuid/jobs/"+uuid.NewString()+"/heartbeat", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("tenant malo: %d", rec.Code)
	}
	if rec := e.asRunner(http.MethodPost, "/v1/tenants/"+uuid.NewString()+"/jobs/x/complete", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("job malo: %d", rec.Code)
	}
	if rec := e.asRunner(http.MethodPost, "/v1/claim", `{"runner_id":""}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("ejecutor sin nombre: %d", rec.Code)
	}
	if rec := e.asRunner(http.MethodPost, "/v1/claim", `{"runner_id":"r","x":1}`); rec.Code != http.StatusBadRequest {
		t.Errorf("campo desconocido: %d", rec.Code)
	}
	e2 := newEnv(t, false)
	if rec := e2.asRunner(http.MethodPost, "/v1/claim", `{"runner_id":"r"}`); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("sin clave configurada en el servicio: %d", rec.Code)
	}
}

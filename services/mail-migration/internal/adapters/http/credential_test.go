package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-migration/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type credentialEnv struct {
	uc       *app.UseCase
	tenant   uuid.UUID
	runner   http.Handler
	verifier http.Handler
	repo     *apptest.Repo
}

func newCredentialEnv(t *testing.T, jobCredentials bool) *credentialEnv {
	t.Helper()
	e := &credentialEnv{repo: apptest.NewRepo(), tenant: uuid.New()}
	e.uc = app.New(app.Deps{
		Repo: e.repo, Tx: apptest.Tx{},
		Mailboxes: &apptest.Mailboxes{Ref: ports.MailboxRef{Username: "ana@acme.test", Active: true}},
		Resolver:  &apptest.Resolver{Addrs: []netip.Addr{netip.MustParseAddr("93.184.216.34")}},
		Cipher:    apptest.Cipher{}, Tenants: &apptest.Tenants{IDs: []uuid.UUID{e.tenant}}, Events: &apptest.Events{},
		Config: app.Config{
			RunnerConfigured: true, MaxActivePerTenant: 2, Lease: 90 * time.Second, MaxAttempts: 3, SweepInterval: time.Second,
			Source: domain.SourcePolicy{Ports: []int{143, 993}}, JobCredentials: jobCredentials,
		},
	})
	e.runner = NewRunnerHandler(e.uc, testRunnerKey, zap.NewNop()).Routes()
	e.verifier = NewCredentialHandler(e.uc).Routes()
	return e
}

func (e *credentialEnv) claim(t *testing.T) (token string, jobID, leaseID string) {
	t.Helper()
	if _, err := e.uc.Create(t.Context(), e.tenant, uuid.New(), app.CreateInput{MailboxID: uuid.New(), Source: domain.Source{
		Host: "imap.origen.example", Port: 993, TLS: domain.TLSImplicit, Username: "ana@origen.example", Password: sourcePass,
	}}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/claim", strings.NewReader(`{"runner_id":"r1"}`))
	req.Header.Set("Authorization", "Bearer "+testRunnerKey)
	rec := httptest.NewRecorder()
	e.runner.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("claim: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Data struct {
			JobID       string `json:"job_id"`
			LeaseID     string `json:"lease_id"`
			Destination struct {
				Username string `json:"username"`
				Password string `json:"password"`
			} `json:"destination"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Data.Destination.Password, out.Data.JobID, out.Data.LeaseID
}

func (e *credentialEnv) verify(body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/verify", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.verifier.ServeHTTP(rec, req)
	return rec
}

func verifyBody(token, username string) string {
	b, _ := json.Marshal(map[string]string{"token": token, "username": username})
	return string(b)
}

func TestElReclamoLlevaLaCredencialDeDestinoYMailAuthPuedeVerificarla(t *testing.T) {
	e := newCredentialEnv(t, true)
	token, jobID, leaseID := e.claim(t)
	if !strings.HasPrefix(token, domain.DestinationTokenPrefix+".") {
		t.Fatalf("el reclamo no lleva credencial de destino: %q", token)
	}

	rec := e.verify(verifyBody(token, "ana@acme.test"))
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("verify: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	var out struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data["tenant_id"] != e.tenant.String() || out.Data["job_id"] != jobID || out.Data["username"] != "ana@acme.test" || out.Data["mailbox_id"] == "" {
		t.Fatalf("respuesta: %v", out.Data)
	}
	if strings.Contains(rec.Body.String(), token) {
		t.Fatal("la verificacion repite la credencial")
	}

	if rec := e.verify(verifyBody(token, "bea@acme.test")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("otro buzon: %d", rec.Code)
	}

	complete := httptest.NewRequest(http.MethodPost, "/v1/tenants/"+e.tenant.String()+"/jobs/"+jobID+"/complete",
		strings.NewReader(`{"lease_id":"`+leaseID+`","outcome":"succeeded"}`))
	complete.Header.Set("Authorization", "Bearer "+testRunnerKey)
	crec := httptest.NewRecorder()
	e.runner.ServeHTTP(crec, complete)
	if crec.Code != http.StatusOK {
		t.Fatalf("complete: %d %s", crec.Code, crec.Body)
	}
	if rec := e.verify(verifyBody(token, "ana@acme.test")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("cerrado el trabajo, la credencial abre: %d", rec.Code)
	}
}

func TestSinCredencialesActivasElReclamoNoLlevaCredencialDeDestino(t *testing.T) {
	e := newCredentialEnv(t, false)
	token, _, _ := e.claim(t)
	if token != "" {
		t.Fatalf("emitio credencial con la funcion apagada: %q", token)
	}
	if rec := e.verify(verifyBody(domain.DestinationTokenPrefix+".x.y.z", "ana@acme.test")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("verify con la funcion apagada: %d", rec.Code)
	}
}

func TestVerificarRechazaCuerposMalosSinDecirMas(t *testing.T) {
	e := newCredentialEnv(t, true)
	for name, body := range map[string]string{
		"vacio":         `{}`,
		"sin token":     `{"username":"ana@acme.test"}`,
		"sin usuario":   `{"token":"cfmj1.x"}`,
		"campo extra":   `{"token":"a","username":"b","extra":1}`,
		"no es json":    `token=a`,
		"token de mas":  `{"token":"` + strings.Repeat("a", 5000) + `","username":"b"}`,
		"dos objetos":   `{"token":"a","username":"b"}{}`,
		"token invalid": verifyBody("cfmj1.no-es-un-token", "ana@acme.test"),
	} {
		rec := e.verify(body)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d", name, rec.Code)
		}
		if rec.Code == http.StatusOK {
			t.Errorf("%s: abrio", name)
		}
	}
}

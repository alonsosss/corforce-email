package http

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

type engineServer struct {
	srv    *httptest.Server
	dir    *apptest.Directory
	policy *apptest.PolicyReader
}

func newEngineServer(t *testing.T) *engineServer {
	t.Helper()
	dir, policy, store := apptest.NewDirectory(), apptest.NewPolicyReader(), apptest.NewStore()
	logger := zap.NewNop()
	uc := app.NewEngineUseCase(app.EngineDeps{
		Directory: dir, Policy: policy, Quarantine: &apptest.Quarantine{},
		Tx: &apptest.Tx{}, Documents: apptest.NewDocuments(),
		Sync: app.NewRedisSync(store, dir, policy, logger), Store: store, Events: &apptest.Publisher{}, Logger: logger, LogLines: 10,
	})
	srv := httptest.NewServer(NewEngineHandler(uc, logger).Routes(""))
	t.Cleanup(srv.Close)
	return &engineServer{srv: srv, dir: dir, policy: policy}
}

func call(t *testing.T, method, url string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestAliasExpDevuelveElBuzonFinalOVacio(t *testing.T) {
	s := newEngineServer(t)
	s.dir.Mailboxes["ana@acme.com"] = domain.Mailbox{TenantID: uuid.New(), Username: "ana@acme.com", Domain: "acme.com", Active: 1}
	s.dir.Mailboxes["luis@acme.com"] = domain.Mailbox{TenantID: uuid.New(), Username: "luis@acme.com", Domain: "acme.com", Active: 1}
	s.dir.Aliases["ventas@acme.com"] = "ana@acme.com"
	s.dir.Aliases["todos@acme.com"] = "ana@acme.com,luis@acme.com"

	resp, body := call(t, http.MethodPost, s.srv.URL+"/aliasexp", map[string]string{"Rcpt": "ventas+promo@acme.com"})
	if resp.StatusCode != http.StatusOK || body != "ana@acme.com" {
		t.Fatalf("un solo buzon: %d %q", resp.StatusCode, body)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("content-type: %s", resp.Header.Get("Content-Type"))
	}
	resp, body = call(t, http.MethodPost, s.srv.URL+"/aliasexp", map[string]string{"Rcpt": "todos@acme.com"})
	if resp.StatusCode != http.StatusOK || body != "" {
		t.Fatalf("varios buzones: %d %q", resp.StatusCode, body)
	}
	resp, body = call(t, http.MethodPost, s.srv.URL+"/aliasexp", map[string]string{"Rcpt": "postmaster@acme.com"})
	if resp.StatusCode != http.StatusOK || body != "" {
		t.Fatalf("postmaster no se expande: %d %q", resp.StatusCode, body)
	}
	resp, body = call(t, http.MethodPost, s.srv.URL+"/aliasexp", map[string]string{"Rcpt": "x@ajeno.com"})
	if resp.StatusCode != http.StatusOK || body != "" {
		t.Fatalf("dominio ajeno: %d %q", resp.StatusCode, body)
	}
}

func TestForwardingHostsPermitDunnoYMapa(t *testing.T) {
	s := newEngineServer(t)
	s.policy.FwdHosts = []domain.ForwardingHost{{Host: "10.1.0.0/16", Source: "relay"}, {Host: "192.0.2.7/32"}}

	resp, body := call(t, http.MethodGet, s.srv.URL+"/forwardinghosts?host=10.1.2.3", nil)
	if resp.StatusCode != http.StatusOK || body != "PERMIT" {
		t.Fatalf("dentro del cidr: %d %q", resp.StatusCode, body)
	}
	resp, body = call(t, http.MethodGet, s.srv.URL+"/forwardinghosts?host=203.0.113.9", nil)
	if resp.StatusCode != http.StatusOK || body != "DUNNO" {
		t.Fatalf("fuera del cidr: %d %q", resp.StatusCode, body)
	}
	resp, body = call(t, http.MethodGet, s.srv.URL+"/forwardinghosts?host=no-ip", nil)
	if resp.StatusCode != http.StatusOK || body != "DUNNO" {
		t.Fatalf("host invalido sigue el protocolo tcp_table: %d %q", resp.StatusCode, body)
	}
	resp, body = call(t, http.MethodGet, s.srv.URL+"/forwardinghosts", nil)
	if resp.StatusCode != http.StatusOK || body != "240.240.240.240\n10.1.0.0/16\n192.0.2.7/32\n" {
		t.Fatalf("mapa: %d %q", resp.StatusCode, body)
	}
}

func TestSettingsSirveUCLYRespondeNotModified(t *testing.T) {
	s := newEngineServer(t)
	// La marca inicial solo toma updated_at si ya paso segun el reloj del caso de uso, que
	// desde este paquete es el real: una fecha fija haria depender el test del dia.
	s.policy.UpdatedAt = time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	s.policy.Scores = []domain.SpamScore{{Object: "acme.com", HighScore: decimal.NewFromInt(15), LowScore: decimal.NewFromInt(8)}}
	s.policy.Lists = []domain.AddressListEntry{{Object: "acme.com", Kind: domain.ListAllow, Pattern: "@partner.com"}}

	resp, body := call(t, http.MethodGet, s.srv.URL+"/settings", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("primera carga: %d", resp.StatusCode)
	}
	lastModified := resp.Header.Get("Last-Modified")
	if lastModified != s.policy.UpdatedAt.Format(http.TimeFormat) {
		t.Fatalf("Last-Modified tras arrancar debe ser el ultimo updated_at: %q", lastModified)
	}
	for _, want := range []string{"settings {", "watchdog {", "reject = 9999.0;", `"/@acme[.]com$/i"`, "MAILCOW_WHITE"} {
		if !strings.Contains(body, want) {
			t.Fatalf("falta %q en:\n%s", want, body)
		}
	}

	resp, body = call(t, http.MethodGet, s.srv.URL+"/settings", map[string]string{"If-Modified-Since": lastModified})
	if resp.StatusCode != http.StatusNotModified || body != "" {
		t.Fatalf("sin cambios: %d %q", resp.StatusCode, body)
	}
	if resp.Header.Get("Last-Modified") != lastModified {
		t.Fatalf("el 304 debe llevar Last-Modified: %q", resp.Header.Get("Last-Modified"))
	}

	// Rspamd 4 pregunta con HEAD antes de cada GET de un mapa HTTP: sin respuesta al HEAD no
	// carga el mapa.
	resp, body = call(t, http.MethodHead, s.srv.URL+"/settings", nil)
	if resp.StatusCode != http.StatusOK || body != "" || resp.Header.Get("Last-Modified") != lastModified {
		t.Fatalf("HEAD de settings: %d %q %q", resp.StatusCode, body, resp.Header.Get("Last-Modified"))
	}
	resp, _ = call(t, http.MethodHead, s.srv.URL+"/settings", map[string]string{"If-Modified-Since": lastModified})
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("HEAD condicional de settings: %d", resp.StatusCode)
	}
	resp, body = call(t, http.MethodHead, s.srv.URL+"/forwardinghosts", nil)
	if resp.StatusCode != http.StatusOK || body != "" {
		t.Fatalf("HEAD de forwardinghosts: %d %q", resp.StatusCode, body)
	}

	s.policy.Scores = nil
	resp, body = call(t, http.MethodGet, s.srv.URL+"/settings", map[string]string{"If-Modified-Since": lastModified})
	if resp.StatusCode != http.StatusOK || strings.Contains(body, "score_0") {
		t.Fatalf("tras borrar el umbral debe servir el documento nuevo: %d\n%s", resp.StatusCode, body)
	}
	if resp.Header.Get("Last-Modified") == lastModified {
		t.Fatal("la marca debe avanzar tras el cambio")
	}
}

func TestFooterYBCC(t *testing.T) {
	s := newEngineServer(t)
	s.policy.Footers["acme.com"] = domain.DomainFooter{Domain: "acme.com", HTML: "<p>x</p>", Plain: "x"}
	s.dir.BCC["rcpt|@acme.com"] = "archivo@acme.com"

	resp, body := call(t, http.MethodPost, s.srv.URL+"/footer", map[string]string{"Domain": "acme.com", "Username": "ana@acme.com", "From": "ana@acme.com"})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"html":"<p>x</p>"`) || !strings.Contains(body, `"vars":"{`) {
		t.Fatalf("footer: %d %s", resp.StatusCode, body)
	}
	resp, body = call(t, http.MethodPost, s.srv.URL+"/bcc", map[string]string{"Rcpt": "@acme.com"})
	if resp.StatusCode != http.StatusCreated || body != "archivo@acme.com" {
		t.Fatalf("bcc con fila: %d %q", resp.StatusCode, body)
	}
	resp, body = call(t, http.MethodPost, s.srv.URL+"/bcc", map[string]string{"From": "x@acme.com"})
	if resp.StatusCode != http.StatusOK || body != "" {
		t.Fatalf("bcc sin fila: %d %q", resp.StatusCode, body)
	}
}

func TestEngineNetworkGuardRechazaIPsPublicas(t *testing.T) {
	handler := EngineNetworkGuard("10.0.0.0/8")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	for addr, want := range map[string]int{"10.2.3.4:1234": http.StatusOK, "203.0.113.5:1234": http.StatusForbidden, "garbage": http.StatusForbidden} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/settings", nil)
		req.RemoteAddr = addr
		handler.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("%s: %d, quiero %d", addr, rec.Code, want)
		}
	}
}

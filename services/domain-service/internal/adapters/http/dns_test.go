package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/crypto"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/app"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/ports"
	"github.com/google/uuid"
)

// Contrato HTTP de la publicacion automatica del DNS: el permiso concreto de cada ruta, el step-up
// solo al guardar la credencial y que el token no sale en ninguna respuesta.

const (
	tokenSecreto = "cf_Secreto_de_contrato_0123456789abcd"
	// tokenQueFalla lo rechaza el proveedor de prueba repitiendolo en su error, como un mensaje de
	// validacion que cita la peticion.
	tokenQueFalla = "cf_Rechazado_de_contrato_987654321zyx"
)

// autorizador concede solo los permisos de granted y anota los que se pidieron.
type autorizador struct {
	mu      sync.Mutex
	granted map[string]bool
	asked   []string
}

func (a *autorizador) RequirePermission(module, resource, action string) func(http.Handler) http.Handler {
	perm := module + "/" + resource + "/" + action
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			a.mu.Lock()
			a.asked = append(a.asked, perm)
			ok := a.granted[perm]
			a.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"code":"FORBIDDEN","message":"sin permiso"}}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// stepUpExigido responde como middleware.RequireStepUp en enforce sin cabecera valida.
func stepUpExigido(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Step-Up") != "valido" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"STEP_UP_REQUIRED","message":"reconfirma"}}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

type repoStub struct {
	ports.Repository
	mu     sync.Mutex
	domain *domain.Domain
}

func (r *repoStub) GetByID(_ context.Context, tenantID, id uuid.UUID) (*domain.Domain, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.domain == nil || r.domain.ID != id || r.domain.TenantID != tenantID {
		return nil, domain.ErrDomainNotFound
	}
	c := *r.domain
	return &c, nil
}

func (r *repoStub) WithDKIMLock(ctx context.Context, tenantID, id uuid.UUID, fn func(context.Context, *domain.Domain) error) error {
	d, err := r.GetByID(ctx, tenantID, id)
	if err != nil {
		return err
	}
	return fn(ctx, d)
}

func (r *repoStub) SaveChecks(context.Context, []domain.DNSCheck) error { return nil }

func (r *repoStub) Update(_ context.Context, d *domain.Domain) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.domain.Status, r.domain.LastCheckedAt = d.Status, d.LastCheckedAt
	return nil
}

func (r *repoStub) LatestChecks(context.Context, uuid.UUID, uuid.UUID) ([]domain.DNSCheck, error) {
	return nil, nil
}

func (r *repoStub) ListDKIMRotations(context.Context, uuid.UUID, uuid.UUID, int) ([]domain.DKIMRotation, error) {
	return nil, nil
}

type dnsRepoStub struct {
	mu   sync.Mutex
	repo *repoStub
	conn *domain.DNSProviderConnection
}

func (s *dnsRepoStub) GetDNSProvider(_ context.Context, tenantID uuid.UUID, _ domain.DNSProvider) (*domain.DNSProviderConnection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil || s.conn.TenantID != tenantID {
		return nil, domain.ErrDNSProviderNotConnected
	}
	c := *s.conn
	return &c, nil
}

func (s *dnsRepoStub) SaveDNSProvider(_ context.Context, c *domain.DNSProviderConnection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *c
	s.conn = &cp
	return nil
}

func (s *dnsRepoStub) UpdateDNSProviderZones(context.Context, *domain.DNSProviderConnection) error {
	return nil
}

func (s *dnsRepoStub) DeleteDNSProvider(context.Context, uuid.UUID, domain.DNSProvider) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	had := s.conn != nil
	s.conn = nil
	return had, nil
}

func (s *dnsRepoStub) ResetDNSMode(context.Context, uuid.UUID, domain.DNSMode) (int64, error) {
	return 0, nil
}

func (s *dnsRepoStub) SetDNSMode(_ context.Context, _, _ uuid.UUID, mode domain.DNSMode) error {
	s.repo.mu.Lock()
	defer s.repo.mu.Unlock()
	s.repo.domain.DNSMode = mode
	return nil
}

func (s *dnsRepoStub) MarkDNSPublished(context.Context, uuid.UUID, uuid.UUID, time.Time) error {
	return nil
}

func (s *dnsRepoStub) Transact(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

type apiStub struct {
	mu      sync.Mutex
	records []domain.ProviderRecord
}

func (a *apiStub) VerifyToken(_ context.Context, tok domain.APIToken) error {
	if tok.Reveal() == tokenQueFalla {
		return &errorQueCita{tok.Reveal()}
	}
	return nil
}

// errorQueCita es un fallo del proveedor cuyo texto repite el token, envuelto sobre el de domain.
type errorQueCita struct{ token string }

func (e *errorQueCita) Error() string { return "cloudflare rechazo " + e.token }
func (e *errorQueCita) Unwrap() error { return domain.ErrDNSProviderTokenInvalid }

func (a *apiStub) ListZones(context.Context, domain.APIToken) ([]domain.DNSZone, error) {
	return []domain.DNSZone{{ID: "zacme", Name: "acme.com"}}, nil
}

func (a *apiStub) ListRecords(_ context.Context, _ domain.APIToken, _ domain.DNSZone, recordType, name string) ([]domain.ProviderRecord, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []domain.ProviderRecord
	for _, r := range a.records {
		if r.Type == recordType && domain.SameHost(r.Name, name) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (a *apiStub) CreateRecord(_ context.Context, _ domain.APIToken, _ domain.DNSZone, rec domain.ProviderRecord) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	rec.ID = uuid.NewString()
	a.records = append(a.records, rec)
	return nil
}

func (a *apiStub) UpdateRecord(context.Context, domain.APIToken, domain.DNSZone, domain.ProviderRecord) error {
	return nil
}

func (a *apiStub) DeleteRecord(context.Context, domain.APIToken, domain.DNSZone, string) error {
	return nil
}

type eventsStub struct{}

func (eventsStub) DNSProviderConnected(context.Context, *domain.DNSProviderConnection) error {
	return nil
}
func (eventsStub) DNSProviderDisconnected(context.Context, uuid.UUID, domain.DNSProvider, uuid.UUID, int64, time.Time) error {
	return nil
}
func (eventsStub) DNSPublished(context.Context, *domain.Domain, *domain.DNSPublication, uuid.UUID) error {
	return nil
}

type resolverStub struct{}

func (resolverStub) LookupTXT(context.Context, string) ([]string, error)         { return nil, nil }
func (resolverStub) LookupMX(context.Context, string) ([]domain.MXRecord, error) { return nil, nil }

type contrato struct {
	t       *testing.T
	authz   *autorizador
	h       http.Handler
	tenant  uuid.UUID
	user    uuid.UUID
	repo    *repoStub
	dnsRepo *dnsRepoStub
}

func nuevoContrato(t *testing.T, granted ...string) *contrato {
	t.Helper()
	t.Setenv("CONTRATO_DNS_KEY", strings.Repeat("ab", 32))
	kr, err := crypto.LoadKeyRing("CONTRATO_DNS_KEY", "CONTRATO_DNS_KEY_OLD")
	if err != nil {
		t.Fatal(err)
	}
	c := &contrato{t: t, authz: &autorizador{granted: map[string]bool{}}, tenant: uuid.New(), user: uuid.New()}
	for _, g := range granted {
		c.authz.granted[g] = true
	}
	c.repo = &repoStub{domain: &domain.Domain{
		ID: uuid.New(), TenantID: c.tenant, Domain: "acme.com", Purpose: domain.PurposeSending, Status: domain.StatusPending,
		VerificationToken: "0123456789abcdef0123456789abcdef", DKIMSelector: "cfm202609", DKIMPublicKey: "PUB",
		DMARCPolicy: domain.DMARCQuarantine, DNSMode: domain.DNSModeManual,
	}}
	c.dnsRepo = &dnsRepoStub{repo: c.repo}
	uc := app.New(app.Deps{
		Repo: c.repo, DNS: resolverStub{}, Cipher: kr,
		DNSProviders: c.dnsRepo, DNSAPIs: map[domain.DNSProvider]ports.DNSProviderAPI{domain.DNSProviderCloudflare: &apiStub{}},
		DNSEvents: eventsStub{},
		Platform:  domain.PlatformDNS{MXHostname: "mx.plataforma.example", SPFInclude: "include:spf.plataforma.example", DMARCRUA: "dmarc@plataforma.example"},
	})
	c.h = middleware.InjectFromGateway(NewHandler(uc, c.authz, stepUpExigido).Routes())
	return c
}

func (c *contrato) do(method, path, body string, headers ...string) *httptest.ResponseRecorder {
	c.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Tenant-ID", c.tenant.String())
	req.Header.Set("X-User-ID", c.user.String())
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	for _, secreto := range []string{tokenSecreto, tokenQueFalla, "Secreto_de_contrato", "Rechazado_de_contrato"} {
		if strings.Contains(rec.Body.String(), secreto) {
			c.t.Errorf("%s %s: la respuesta lleva el token: %s", method, path, rec.Body.String())
		}
	}
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return env.Error.Code
}

func data(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var env struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("cuerpo %s: %v", rec.Body.String(), err)
	}
	return env.Data
}

const (
	permRead       = "domains/dns_providers/read"
	permConnect    = "domains/dns_providers/connect"
	permDisconnect = "domains/dns_providers/disconnect"
	permPublish    = "domains/domains/publish_dns"
)

func TestCadaRutaExigeSuPermiso(t *testing.T) {
	c := nuevoContrato(t)
	id := c.repo.domain.ID.String()
	for _, r := range []struct {
		method, path, body, perm string
	}{
		{http.MethodGet, "/api/v1/domains/dns-providers/cloudflare", "", permRead},
		{http.MethodPost, "/api/v1/domains/dns-providers/cloudflare/connect", `{"api_token":"` + tokenSecreto + `"}`, permConnect},
		{http.MethodPost, "/api/v1/domains/dns-providers/cloudflare/disconnect", "", permDisconnect},
		{http.MethodPost, "/api/v1/domains/" + id + "/dns-mode", `{"mode":"cloudflare"}`, permPublish},
		{http.MethodPost, "/api/v1/domains/" + id + "/publish-dns", `{}`, permPublish},
	} {
		c.authz.asked = nil
		rec := c.do(r.method, r.path, r.body, "X-Step-Up", "valido")
		if rec.Code != http.StatusForbidden || errorCode(t, rec) != "FORBIDDEN" {
			t.Errorf("%s %s sin permiso: %d %s", r.method, r.path, rec.Code, rec.Body.String())
		}
		if len(c.authz.asked) != 1 || c.authz.asked[0] != r.perm {
			t.Errorf("%s %s pide %v; want %s", r.method, r.path, c.authz.asked, r.perm)
		}
	}
	if c.dnsRepo.conn != nil {
		t.Error("sin permiso se guardo la conexion")
	}
}

// Conectar guarda una credencial de terceros: exige step-up, y solo a quien tiene el permiso.
func TestConectarExigeStepUpDespuesDelPermiso(t *testing.T) {
	c := nuevoContrato(t, permConnect, permRead, permDisconnect)
	body := `{"api_token":"` + tokenSecreto + `"}`
	rec := c.do(http.MethodPost, "/api/v1/domains/dns-providers/cloudflare/connect", body)
	if rec.Code != http.StatusForbidden || errorCode(t, rec) != "STEP_UP_REQUIRED" || c.dnsRepo.conn != nil {
		t.Fatalf("sin step-up: %d %s", rec.Code, rec.Body.String())
	}
	sinPermiso := nuevoContrato(t)
	if rec := sinPermiso.do(http.MethodPost, "/api/v1/domains/dns-providers/cloudflare/connect", body); errorCode(t, rec) != "FORBIDDEN" {
		t.Errorf("sin permiso no se pide step-up: %s", rec.Body.String())
	}
	if rec := c.do(http.MethodGet, "/api/v1/domains/dns-providers/cloudflare", ""); rec.Code != http.StatusOK {
		t.Errorf("leer el estado no pide step-up: %d", rec.Code)
	}
	if rec := c.do(http.MethodPost, "/api/v1/domains/dns-providers/cloudflare/disconnect", ""); rec.Code != http.StatusOK {
		t.Errorf("desconectar no pide step-up: %d", rec.Code)
	}
}

func TestElTokenNoSaleEnNingunaRespuesta(t *testing.T) {
	c := nuevoContrato(t, permConnect, permRead, permDisconnect, permPublish, "domains/domains/read")
	path := "/api/v1/domains/dns-providers/cloudflare"
	step := []string{"X-Step-Up", "valido"}

	if d := data(t, c.do(http.MethodGet, path, "")); d["connected"] != false || d["provider"] != "cloudflare" {
		t.Errorf("sin conectar: %v", d)
	}
	rec := c.do(http.MethodPost, path+"/connect", `{"api_token":"`+tokenSecreto+`"}`, step...)
	if rec.Code != http.StatusOK {
		t.Fatalf("conectar: %d %s", rec.Code, rec.Body.String())
	}
	d := data(t, rec)
	if d["connected"] != true || d["token_hint"] != "abcd" || d["zones_visible"] != float64(1) || d["connected_by"] != c.user.String() {
		t.Errorf("conexion %v", d)
	}
	for _, campo := range []string{"api_token", "token", "api_token_enc", "token_enc"} {
		if _, ok := d[campo]; ok {
			t.Errorf("la respuesta lleva %s", campo)
		}
	}
	if d := data(t, c.do(http.MethodGet, path, "")); d["connected"] != true || d["token_hint"] != "abcd" {
		t.Errorf("estado %v", d)
	}

	for nombre, caso := range map[string]struct {
		body   string
		status int
		code   string
	}{
		"el proveedor lo rechaza citandolo": {`{"api_token":"` + tokenQueFalla + `"}`, http.StatusUnprocessableEntity, "DNS_PROVIDER_TOKEN_INVALID"},
		"formato no valido":                 {`{"api_token":"` + tokenSecreto + `!"}`, http.StatusUnprocessableEntity, "DNS_PROVIDER_TOKEN_INVALID"},
		"JSON roto con el token":            {`{"api_token":"` + tokenSecreto + `"`, http.StatusBadRequest, "BAD_REQUEST"},
		"campo desconocido con el token":    {`{"token":"` + tokenSecreto + `"}`, http.StatusBadRequest, "BAD_REQUEST"},
		"sin token":                         {`{"api_token":"  "}`, http.StatusUnprocessableEntity, "VALIDATION_ERROR"},
	} {
		rec := c.do(http.MethodPost, path+"/connect", caso.body, step...)
		if rec.Code != caso.status || errorCode(t, rec) != caso.code {
			t.Errorf("%s: %d %s", nombre, rec.Code, rec.Body.String())
		}
	}

	id := c.repo.domain.ID.String()
	if rec := c.do(http.MethodPost, "/api/v1/domains/"+id+"/publish-dns", ""); rec.Code != http.StatusConflict || errorCode(t, rec) != "DNS_MODE_MANUAL" {
		t.Errorf("publicar en manual: %d %s", rec.Code, rec.Body.String())
	}
	if rec := c.do(http.MethodPost, "/api/v1/domains/"+id+"/dns-mode", `{"mode":"route53"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("modo no valido: %d", rec.Code)
	}
	rec = c.do(http.MethodPost, "/api/v1/domains/"+id+"/dns-mode", `{"mode":"cloudflare"}`)
	if rec.Code != http.StatusOK || data(t, rec)["dns_mode"] != "cloudflare" {
		t.Fatalf("modo: %d %s", rec.Code, rec.Body.String())
	}
	rec = c.do(http.MethodPost, "/api/v1/domains/"+id+"/publish-dns", `{"replace":["spf"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("publicar: %d %s", rec.Code, rec.Body.String())
	}
	pub, _ := data(t, rec)["dns_publication"].(map[string]interface{})
	if pub["zone"] != "acme.com" || pub["complete"] != true || len(pub["records"].([]interface{})) != 4 {
		t.Errorf("publicacion %v", pub)
	}
	if rec := c.do(http.MethodPost, "/api/v1/domains/"+id+"/publish-dns", `{"replace":["todo"]}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("replace no valido: %d", rec.Code)
	}

	rec = c.do(http.MethodPost, path+"/disconnect", "")
	if d := data(t, rec); rec.Code != http.StatusOK || d["connected"] != false || d["disconnected"] != true {
		t.Errorf("desconectar: %d %v", rec.Code, d)
	}
	if rec := c.do(http.MethodPost, "/api/v1/domains/"+id+"/dns-mode", `{"mode":"cloudflare"}`); rec.Code != http.StatusConflict || errorCode(t, rec) != "DNS_PROVIDER_NOT_CONNECTED" {
		t.Errorf("modo sin conexion: %d %s", rec.Code, rec.Body.String())
	}
	if rec := c.do(http.MethodGet, "/api/v1/domains/dns-providers/route53", ""); rec.Code != http.StatusNotFound || errorCode(t, rec) != "DNS_PROVIDER_UNSUPPORTED" {
		t.Errorf("proveedor no admitido: %d %s", rec.Code, rec.Body.String())
	}
}

// Ningun error del proveedor responde 401 ni 403: el cliente web los trata como sesion caducada o
// permiso del usuario.
func TestLosErroresDelProveedorNoSeConfundenConLaSesion(t *testing.T) {
	for _, e := range dnsErrors {
		if e.status == http.StatusUnauthorized || e.status == http.StatusForbidden {
			t.Errorf("%s responde %d", e.code, e.status)
		}
	}
}

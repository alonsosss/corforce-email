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
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
)

const platformMX = "mx.plataforma.example"

type stsDomains struct {
	ports.DomainRepository
	d *domain.Domain
}

func (f stsDomains) GetByName(_ context.Context, tenantID uuid.UUID, name string) (*domain.Domain, error) {
	if f.d != nil && f.d.TenantID == tenantID && f.d.Domain == name {
		return f.d, nil
	}
	return nil, domain.ErrNotFound
}

type stsRepo struct {
	policy *domain.MTASTSPolicy
}

func (f *stsRepo) ByDomain(_ context.Context, tenantID uuid.UUID, name string) (*domain.MTASTSPolicy, error) {
	if f.policy == nil || f.policy.TenantID != tenantID || f.policy.Domain != name {
		return nil, domain.ErrNotFound
	}
	c := *f.policy
	return &c, nil
}
func (f *stsRepo) States(context.Context, uuid.UUID, ports.Page) ([]domain.MTASTSState, int64, error) {
	if f.policy == nil {
		return []domain.MTASTSState{{Domain: "acme.test", DomainActive: true, Mode: domain.MTASTSNone}}, 1, nil
	}
	return []domain.MTASTSState{{Domain: f.policy.Domain, DomainActive: true, Mode: f.policy.Mode, MaxAge: f.policy.MaxAge, PolicyID: f.policy.PolicyID}}, 1, nil
}
func (f *stsRepo) Upsert(_ context.Context, p *domain.MTASTSPolicy) error {
	p.UpdatedAt = time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	c := *p
	f.policy = &c
	return nil
}
func (f *stsRepo) DeleteByDomain(context.Context, uuid.UUID, string) error { return nil }

type stsPublisher struct{ repo *stsRepo }

func (f stsPublisher) Published(_ context.Context, name string) (*domain.MTASTSPolicy, error) {
	if f.repo.policy == nil || f.repo.policy.Domain != name || !f.repo.policy.Mode.Published() {
		return nil, domain.ErrNotFound
	}
	c := *f.repo.policy
	return &c, nil
}

type stsMX struct {
	hosts []string
	err   error
}

func (f *stsMX) LookupMX(context.Context, string) ([]string, error) { return f.hosts, f.err }

type stsEnv struct {
	h      http.Handler
	repo   *stsRepo
	mx     *stsMX
	tenant uuid.UUID
}

func mtaSTSServer(t *testing.T) *stsEnv {
	t.Helper()
	tenant := uuid.New()
	repo, mx := &stsRepo{}, &stsMX{hosts: []string{platformMX}}
	uc := app.New(app.Deps{
		Tx: vacTx{}, Domains: stsDomains{d: &domain.Domain{ID: uuid.New(), TenantID: tenant, Domain: "acme.test", Active: true}},
		Retirements: vacRetirements{}, MTASTS: repo, MTASTSPublic: stsPublisher{repo: repo}, MX: mx, PlatformMX: platformMX,
	})
	return &stsEnv{h: NewHandler(uc, authz.NewChecker("http://127.0.0.1:9", "")).Routes(), repo: repo, mx: mx, tenant: tenant}
}

// call hace la peticion como el gateway la entrega: con identidad y rol de empresa. roles vacio es una
// peticion sin ningun permiso que se pueda comprobar.
func (e *stsEnv) call(method, path, body string, tenant uuid.UUID, roles ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := middleware.WithIdentity(req.Context(), uuid.NewString(), tenant.String())
	ctx = context.WithValue(ctx, middleware.CtxRoles, roles)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

// admin es quien administra su empresa.
func (e *stsEnv) admin(method, path, body string) *httptest.ResponseRecorder {
	return e.call(method, path, body, e.tenant, middleware.RoleTenantAdmin)
}

// stsContract escribe a mano los nombres JSON que consume la interfaz.
type stsContract struct {
	Domain       string     `json:"domain"`
	DomainActive bool       `json:"domain_active"`
	Mode         string     `json:"mode"`
	MaxAge       int        `json:"max_age"`
	PolicyID     string     `json:"policy_id"`
	UpdatedAt    *time.Time `json:"updated_at"`
	AllowedModes []string   `json:"allowed_modes"`
}

func decodeSTS(t *testing.T, rec *httptest.ResponseRecorder) stsContract {
	t.Helper()
	var env struct {
		Data stsContract `json:"data"`
	}
	dec := json.NewDecoder(rec.Body)
	if err := dec.Decode(&env); err != nil {
		t.Fatalf("cuerpo ilegible: %v", err)
	}
	return env.Data
}

func errorCode(rec *httptest.ResponseRecorder) string {
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return env.Error.Code
}

func TestMTASTSDeAdministracionLeeYCambiaElModo(t *testing.T) {
	e := mtaSTSServer(t)
	rec := e.admin(http.MethodGet, "/api/v1/mail-domains/mta-sts/acme.test", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body)
	}
	if got := decodeSTS(t, rec); got.Mode != "none" || got.Domain != "acme.test" || !got.DomainActive || len(got.AllowedModes) != 1 || got.AllowedModes[0] != "testing" {
		t.Fatalf("estado inicial: %+v", got)
	}

	rec = e.admin(http.MethodPut, "/api/v1/mail-domains/mta-sts/acme.test", `{"mode":"testing"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT testing: %d %s", rec.Code, rec.Body)
	}
	got := decodeSTS(t, rec)
	if got.Mode != "testing" || len(got.PolicyID) != 32 || got.MaxAge != domain.MTASTSTestingMaxAge || got.UpdatedAt == nil {
		t.Fatalf("activada: %+v", got)
	}

	rec = e.admin(http.MethodPut, "/api/v1/mail-domains/mta-sts/acme.test", `{"mode":"enforce"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT enforce: %d %s", rec.Code, rec.Body)
	}
	if enforced := decodeSTS(t, rec); enforced.Mode != "enforce" || enforced.PolicyID == got.PolicyID {
		t.Fatalf("enforce cambia la version: %+v", enforced)
	}

	rec = e.admin(http.MethodGet, "/api/v1/mail-domains/mta-sts", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"mode":"enforce"`) || !strings.Contains(rec.Body.String(), `"total"`) {
		t.Fatalf("listado: %d %s", rec.Code, rec.Body)
	}
}

func TestMTASTSRechazaLoQueNoProcedeConElCodigoQueLaInterfazEntiende(t *testing.T) {
	e := mtaSTSServer(t)
	casos := []struct {
		nombre, dominio, body string
		status                int
		prepara               func()
	}{
		{"modo desconocido", "acme.test", `{"mode":"strict"}`, http.StatusUnprocessableEntity, nil},
		{"campo desconocido", "acme.test", `{"mode":"testing","max_age":1}`, http.StatusBadRequest, nil},
		{"JSON roto", "acme.test", `no es json`, http.StatusBadRequest, nil},
		{"dominio invalido", "no-es-un-dominio", `{"mode":"testing"}`, http.StatusUnprocessableEntity, nil},
		{"dominio que el directorio no tiene", "otro.test", `{"mode":"testing"}`, http.StatusNotFound, nil},
		{"de none a enforce", "acme.test", `{"mode":"enforce"}`, http.StatusConflict, nil},
		{"MX que no casan", "acme.test", `{"mode":"enforce"}`, http.StatusConflict, func() {
			e.repo.policy = domain.NewMTASTSPolicy(e.tenant, "acme.test", domain.MTASTSTesting)
			e.mx.hosts = []string{"mail.otro.example"}
		}},
		{"DNS caido", "acme.test", `{"mode":"enforce"}`, http.StatusServiceUnavailable, func() {
			e.mx.hosts, e.mx.err = nil, errors.New("timeout")
		}},
	}
	for _, c := range casos {
		if c.prepara != nil {
			c.prepara()
		}
		rec := e.admin(http.MethodPut, "/api/v1/mail-domains/mta-sts/"+c.dominio, c.body)
		if rec.Code != c.status {
			t.Errorf("%s: %d %s", c.nombre, rec.Code, rec.Body)
		}
	}
	if code := errorCode(e.admin(http.MethodPut, "/api/v1/mail-domains/mta-sts/acme.test", `{"mode":"enforce"}`)); code != "DNS_UNAVAILABLE" {
		t.Errorf("codigo del DNS caido: %q", code)
	}
	if e.repo.policy.Mode != domain.MTASTSTesting {
		t.Fatalf("ningun rechazo cambia el modo: %s", e.repo.policy.Mode)
	}
}

// Las rutas de administracion exigen el permiso del recurso mta_sts: sin sesion con permiso no llegan
// al caso de uso.
func TestMTASTSDeAdministracionExigePermiso(t *testing.T) {
	e := mtaSTSServer(t)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/mail-domains/mta-sts", ""},
		{http.MethodGet, "/api/v1/mail-domains/mta-sts/acme.test", ""},
		{http.MethodPut, "/api/v1/mail-domains/mta-sts/acme.test", `{"mode":"testing"}`},
	} {
		if rec := e.call(c.method, c.path, c.body, e.tenant); rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden && rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s sin rol: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
	if e.repo.policy != nil {
		t.Fatal("sin permiso no se escribe")
	}
}

func TestMTASTSNoCruzaEmpresas(t *testing.T) {
	e := mtaSTSServer(t)
	e.admin(http.MethodPut, "/api/v1/mail-domains/mta-sts/acme.test", `{"mode":"testing"}`)
	ajena := uuid.New()
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		rec := e.call(method, "/api/v1/mail-domains/mta-sts/acme.test", `{"mode":"testing"}`, ajena, middleware.RoleTenantAdmin)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s desde otra empresa: %d %s", method, rec.Code, rec.Body)
		}
	}
}

func TestMTASTSInternoLoLeeDomainServiceConLaEmpresaEnLaCabecera(t *testing.T) {
	e := mtaSTSServer(t)
	e.admin(http.MethodPut, "/api/v1/mail-domains/mta-sts/acme.test", `{"mode":"testing"}`)
	rec := e.call(http.MethodGet, "/internal/mail-directory/mta-sts/acme.test", "", e.tenant)
	if rec.Code != http.StatusOK {
		t.Fatalf("interno: %d %s", rec.Code, rec.Body)
	}
	if got := decodeSTS(t, rec); got.Mode != "testing" || got.PolicyID != e.repo.policy.PolicyID {
		t.Fatalf("interno: %+v", got)
	}
	if rec := e.call(http.MethodGet, "/internal/mail-directory/mta-sts/acme.test", "", uuid.New()); rec.Code != http.StatusNotFound {
		t.Fatalf("otra empresa: %d", rec.Code)
	}
}

func TestLaPoliticaPublicaEsTextoPlanoSinSesion(t *testing.T) {
	e := mtaSTSServer(t)
	get := func(dominio string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/public/mail-directory/mta-sts/pe-01/"+dominio, nil))
		return rec
	}
	if rec := get("acme.test"); rec.Code != http.StatusNotFound {
		t.Fatalf("sin politica: %d", rec.Code)
	}
	e.admin(http.MethodPut, "/api/v1/mail-domains/mta-sts/acme.test", `{"mode":"testing"}`)
	rec := get("acme.test")
	want := "version: STSv1\r\nmode: testing\r\nmx: " + platformMX + "\r\nmax_age: 86400\r\n"
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("politica: %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type: %q", ct)
	}
	e.admin(http.MethodPut, "/api/v1/mail-domains/mta-sts/acme.test", `{"mode":"none"}`)
	if rec := get("acme.test"); rec.Code != http.StatusNotFound {
		t.Fatalf("modo none no se sirve: %d", rec.Code)
	}
	for _, malo := range []string{"no-es-un-dominio", "%2e%2e", "a%20b.test"} {
		if rec := get(malo); rec.Code != http.StatusNotFound {
			t.Errorf("%q: %d", malo, rec.Code)
		}
	}
}

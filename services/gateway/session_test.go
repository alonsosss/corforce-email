package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"go.uber.org/zap"
)

const (
	cuentaInexistente = `{"error":{"code":"USER_NOT_FOUND","message":"user not found"}}`
	cuentaNoActiva    = `{"error":{"code":"USER_NOT_ACTIVE","message":"user not active"}}`
)

func cuentaActiva(validFrom time.Time) string {
	return `{"data":{"is_admin":false,"modules":[],"write_actions":{},"disabled_modules":[],"tokens_valid_from":"` +
		validFrom.UTC().Format(time.RFC3339) + `"}}`
}

// accessControlFake responde GET /api/v1/access/my-modules como access-control.
type accessControlFake struct {
	mu     sync.Mutex
	status int
	body   string
	calls  int
}

func (f *accessControlFake) responder(status int, body string) {
	f.mu.Lock()
	f.status, f.body = status, body
	f.mu.Unlock()
}

func (f *accessControlFake) llamadas() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *accessControlFake) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	f.calls++
	status, body := f.status, f.body
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

type sesionDePrueba struct {
	t    *testing.T
	fake *accessControlFake
	e    *rbacEnforcer
	h    http.Handler
}

// nuevaSesion monta la sesion y el RBAC como en main.go, con campaigns como modulo gateado.
func nuevaSesion(t *testing.T, status int, body string) *sesionDePrueba {
	t.Helper()
	fake := &accessControlFake{status: status, body: body}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	e := newRBACEnforcer(srv.URL, "tok", "", "", "", map[string]string{"campaigns": "campaigns"}, nil, zap.NewNop())
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return &sesionDePrueba{t: t, fake: fake, e: e, h: e.sessionCheck(e.middleware(next))}
}

// pedir hace GET path con un token del usuario 7f0b... emitido en iat.
func (s *sesionDePrueba) pedir(path string, iat int64, roles ...string) *httptest.ResponseRecorder {
	s.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	ctx := context.WithValue(req.Context(), middleware.CtxUserID, "7f0b1e2c-0000-4000-8000-000000000001")
	ctx = context.WithValue(ctx, middleware.CtxTenantID, "7f0b1e2c-0000-4000-8000-0000000000aa")
	ctx = context.WithValue(ctx, middleware.CtxRoles, roles)
	ctx = context.WithValue(ctx, middleware.CtxTokenIssuedAt, iat)
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

func (s *sesionDePrueba) caducarCache() {
	s.e.mu.Lock()
	for k, v := range s.e.cache {
		v.expires = time.Now().Add(-time.Second)
		s.e.cache[k] = v
	}
	s.e.mu.Unlock()
}

func sesionRechazada(t *testing.T, rec *httptest.ResponseRecorder) bool {
	t.Helper()
	if rec.Code != http.StatusUnauthorized {
		return false
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("401 sin cuerpo legible: %q", rec.Body.String())
	}
	return env.Error.Code == "SESSION_REVOKED"
}

const rutaSinModulo = "/api/v1/sessions/mine"

func hace(segundos int64) int64 { return time.Now().Unix() - segundos }

// Una cuenta borrada o no activa no usa su token en ninguna ruta, tampoco en las de
// autoservicio que ningun modulo gatea, y tampoco si el token dice ser de un administrador.
func TestUnaCuentaCerradaNoPasaConSuTokenVigente(t *testing.T) {
	for _, c := range []struct {
		nombre string
		status int
		body   string
	}{
		{"borrada", http.StatusNotFound, cuentaInexistente},
		{"no activa", http.StatusForbidden, cuentaNoActiva},
	} {
		for _, roles := range [][]string{nil, {middleware.RoleTenantAdmin}, {middleware.RoleSuperadmin}} {
			s := nuevaSesion(t, c.status, c.body)
			for _, ruta := range []string{rutaSinModulo, "/api/v1/campaigns"} {
				if rec := s.pedir(ruta, hace(30), roles...); !sesionRechazada(t, rec) {
					t.Errorf("cuenta %s, roles %v, %s: %d %s; se esperaba 401 SESSION_REVOKED",
						c.nombre, roles, ruta, rec.Code, rec.Body.String())
				}
			}
		}
	}
}

// Solo las dos respuestas del contrato cierran la cuenta. Una caida, un 404 de ruta, un 403
// por otro motivo o el token interno mal configurado no pueden expulsar a todos: la sesion
// pasa, y una ruta con modulo sigue con RBAC_FAIL_MODE (closed: 403, no 401).
func TestSinRespuestaDefinitivaNoSeCierraLaSesion(t *testing.T) {
	for _, c := range []struct {
		nombre string
		status int
		body   string
	}{
		{"caida", http.StatusInternalServerError, `{"error":{"code":"INTERNAL_ERROR"}}`},
		{"ruta inexistente", http.StatusNotFound, "404 page not found"},
		{"404 con otro codigo", http.StatusNotFound, `{"error":{"code":"NOT_FOUND"}}`},
		{"403 con otro codigo", http.StatusForbidden, `{"error":{"code":"FORBIDDEN"}}`},
		{"codigo de cuenta con otro estado", http.StatusConflict, cuentaInexistente},
		{"token interno", http.StatusUnauthorized, `{"error":"unauthorized gateway"}`},
		{"limite", http.StatusTooManyRequests, `{"error":{"code":"RATE_LIMITED"}}`},
	} {
		s := nuevaSesion(t, c.status, c.body)
		if rec := s.pedir(rutaSinModulo, hace(30)); rec.Code != http.StatusOK {
			t.Errorf("%s, ruta sin modulo: %d; se esperaba 200", c.nombre, rec.Code)
		}
		if rec := s.pedir("/api/v1/campaigns", hace(30)); rec.Code != http.StatusForbidden {
			t.Errorf("%s, ruta con modulo: %d; se esperaba 403 por RBAC_FAIL_MODE=closed", c.nombre, rec.Code)
		}
	}
}

// La respuesta definitiva se recuerda para no consultar en cada peticion del token, pero
// solo para los tokens emitidos hasta entonces: si la cuenta vuelve a estar activa, el token
// nuevo que identity le emite obliga a preguntar.
func TestLaCuentaCerradaSeRecuerdaParaLosTokensAnteriores(t *testing.T) {
	s := nuevaSesion(t, http.StatusNotFound, cuentaInexistente)
	viejo := hace(60)
	for i := 0; i < 3; i++ {
		if rec := s.pedir(rutaSinModulo, viejo); !sesionRechazada(t, rec) {
			t.Fatalf("peticion %d: %d; se esperaba 401", i, rec.Code)
		}
	}
	if n := s.fake.llamadas(); n != 1 {
		t.Fatalf("access-control consultado %d veces; la respuesta definitiva debe quedar en cache", n)
	}

	s.fake.responder(http.StatusOK, cuentaActiva(time.Unix(0, 0)))
	if rec := s.pedir(rutaSinModulo, time.Now().Unix()+5); rec.Code != http.StatusOK {
		t.Fatalf("token posterior de la cuenta reactivada: %d; se esperaba 200", rec.Code)
	}
	if n := s.fake.llamadas(); n != 2 {
		t.Fatalf("access-control consultado %d veces; el token posterior debe volver a preguntar", n)
	}
	if rec := s.pedir(rutaSinModulo, viejo); rec.Code != http.StatusOK {
		t.Fatalf("token anterior con la cuenta ya activa y sin revocar: %d; se esperaba 200", rec.Code)
	}
}

// Con access-control caido se conserva lo ultimo que se supo, si aplica al token. Si no hay
// nada aplicable, la sesion no se puede juzgar y pasa.
func TestConAccessControlCaidoSeUsaLoUltimoConocido(t *testing.T) {
	s := nuevaSesion(t, http.StatusOK, cuentaActiva(time.Unix(0, 0)))
	if rec := s.pedir(rutaSinModulo, hace(30)); rec.Code != http.StatusOK {
		t.Fatalf("cuenta activa: %d", rec.Code)
	}
	s.caducarCache()
	s.fake.responder(http.StatusBadGateway, "")
	if rec := s.pedir(rutaSinModulo, hace(30)); rec.Code != http.StatusOK {
		t.Fatalf("cuenta activa en cache caducada con access-control caido: %d; se esperaba 200", rec.Code)
	}

	s = nuevaSesion(t, http.StatusNotFound, cuentaInexistente)
	viejo := hace(60)
	if rec := s.pedir(rutaSinModulo, viejo); !sesionRechazada(t, rec) {
		t.Fatalf("cuenta borrada: %d", rec.Code)
	}
	s.caducarCache()
	s.fake.responder(http.StatusBadGateway, "")
	if rec := s.pedir(rutaSinModulo, viejo); !sesionRechazada(t, rec) {
		t.Fatalf("cuenta borrada en cache caducada con access-control caido: %d; se esperaba 401", rec.Code)
	}
	if rec := s.pedir(rutaSinModulo, time.Now().Unix()+5); rec.Code != http.StatusOK {
		t.Fatalf("token posterior a la respuesta, sin poder preguntar: %d; se esperaba 200", rec.Code)
	}
}

// El plazo documentado: una cuenta borrada con acceso positivo en cache sigue pasando hasta
// que la entrada caduca (rbacCacheTTL), y a partir de ahi se rechaza.
func TestUnaCuentaBorradaConAccesoEnCacheCaeAlCaducar(t *testing.T) {
	s := nuevaSesion(t, http.StatusOK, cuentaActiva(time.Unix(0, 0)))
	if rec := s.pedir(rutaSinModulo, hace(30)); rec.Code != http.StatusOK {
		t.Fatalf("cuenta activa: %d", rec.Code)
	}
	s.fake.responder(http.StatusNotFound, cuentaInexistente)
	if rec := s.pedir(rutaSinModulo, hace(30)); rec.Code != http.StatusOK {
		t.Fatalf("dentro de la cache: %d; se esperaba 200 hasta que caduque", rec.Code)
	}
	s.caducarCache()
	if rec := s.pedir(rutaSinModulo, hace(30)); !sesionRechazada(t, rec) {
		t.Fatalf("tras caducar la cache: %d; se esperaba 401", rec.Code)
	}
}

func TestUnTokenAnteriorALaRevocacionSeRechaza(t *testing.T) {
	revocado := time.Now().Add(-10 * time.Second)
	s := nuevaSesion(t, http.StatusOK, cuentaActiva(revocado))
	if rec := s.pedir(rutaSinModulo, revocado.Unix()-60); !sesionRechazada(t, rec) {
		t.Fatalf("token anterior a la revocacion: %d; se esperaba 401", rec.Code)
	}
	if rec := s.pedir(rutaSinModulo, revocado.Unix()+1); rec.Code != http.StatusOK {
		t.Fatalf("token posterior a la revocacion: %d; se esperaba 200", rec.Code)
	}
}

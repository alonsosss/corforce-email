package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
)

func TestElAutoservicioNoAbreElDirectorioDeUsuarios(t *testing.T) {
	uid := "7f0b1e2c-0000-4000-8000-000000000001"
	casos := []struct {
		seg1, seg2 string
		quiere     bool
	}{
		{"auth", "refresh", true},
		{"sessions", "mine", true},
		{"access", "my-modules", true},
		{"access", "denials", false},
		{"users", "me", true},
		{"users", uid, true},
		{"users", "", false},
		{"users", "otro-id", false},
		{"organizations", "", false},
	}
	for _, c := range casos {
		if got := autoservicio(c.seg1, c.seg2, uid); got != c.quiere {
			t.Errorf("autoservicio(%q,%q) = %v; se esperaba %v", c.seg1, c.seg2, got, c.quiere)
		}
	}
}

// "me" como segundo segmento no exime a ningun modulo: solo lo es en users/me. Una ruta o un recurso
// que se llame "me" (o un parametro de ruta de texto libre) saltaria el gateo por modulo y el bloqueo
// de los modulos que la empresa no tiene contratados.
func TestMeNoEximeDelGateoPorModulo(t *testing.T) {
	s := nuevaSesion(t, http.StatusOK, cuentaActiva(time.Now().Add(-time.Hour)))
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/api/v1/campaigns/me", nil)
		ctx := context.WithValue(req.Context(), middleware.CtxUserID, "7f0b1e2c-0000-4000-8000-000000000001")
		ctx = context.WithValue(ctx, middleware.CtxTenantID, "7f0b1e2c-0000-4000-8000-0000000000aa")
		ctx = context.WithValue(ctx, middleware.CtxRoles, []string{})
		rec := httptest.NewRecorder()
		s.h.ServeHTTP(rec, req.WithContext(ctx))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s /campaigns/me sin permisos: %d, se esperaba 403", method, rec.Code)
		}
	}
	if rec := s.pedir("/api/v1/campaigns/me", hace(30)); rec.Code != http.StatusForbidden {
		t.Errorf("GET /campaigns/me sin el modulo: %d, se esperaba 403", rec.Code)
	}
}

func TestLaAccionExigidaSaleDelMetodo(t *testing.T) {
	casos := map[string]string{
		http.MethodDelete: "delete",
		http.MethodPut:    "update",
		http.MethodPatch:  "update",
		http.MethodPost:   "",
	}
	for metodo, quiere := range casos {
		if got := requiredAction(metodo); got != quiere {
			t.Errorf("requiredAction(%s) = %q; se esperaba %q", metodo, got, quiere)
		}
	}
}

// Los modos nacen cerrados: un despliegue que olvide configurarlos bloquea en vez de
// dejar pasar, que es el sentido de tener un gateway delante de datos personales.
func TestLosModosNacenEnEnforceYFailClosed(t *testing.T) {
	e := newRBACEnforcer("http://access", "tok", "", "", "", nil, nil, nil)
	if e.mode != "enforce" || e.failMode != "closed" || e.readMode != "enforce" {
		t.Fatalf("modos por defecto: %s/%s/%s", e.mode, e.failMode, e.readMode)
	}
}

func TestLaRespuestaDeAccessControlSeTraduceACaché(t *testing.T) {
	var p myModulesPayload
	p.Data.IsAdmin = false
	p.Data.Modules = []string{"campaigns", "contacts"}
	p.Data.WriteActions = map[string][]string{"campaigns": {"create", "update"}}
	p.Data.DisabledModules = []string{"marketing"}
	p.Data.TokensValidFrom = time.Unix(1_700_000_000, 0)
	e := entryFromPayload(p)
	if !e.modules["contacts"] || e.modules["audit"] {
		t.Errorf("modulos mal traducidos: %v", e.modules)
	}
	if !e.writeActions["campaigns"]["update"] || e.writeActions["campaigns"]["delete"] {
		t.Errorf("acciones mal traducidas: %v", e.writeActions)
	}
	if !e.disabledModules["marketing"] {
		t.Errorf("modulos deshabilitados mal traducidos")
	}
	if e.tokensValidFrom != 1_700_000_000 {
		t.Errorf("epoch de revocacion: %d", e.tokensValidFrom)
	}
	if e.expires.Before(time.Now()) {
		t.Errorf("la entrada nace caducada")
	}
}

func TestUnPostDeConsultaSeGateaComoLectura(t *testing.T) {
	e := newRBACEnforcer("http://access", "tok", "", "", "", nil,
		map[string]map[string]bool{"suppression": {"check": true}}, nil)
	req := func(m, p string) *http.Request { r, _ := http.NewRequest(m, p, nil); return r }
	if !e.isReadPost(req(http.MethodPost, "/api/v1/suppression/check")) {
		t.Fatal("suppression/check debe ser lectura")
	}
	if e.isReadPost(req(http.MethodPost, "/api/v1/suppression/entries")) {
		t.Fatal("crear entradas sigue siendo escritura")
	}
	if e.isReadPost(req(http.MethodGet, "/api/v1/suppression/check")) {
		t.Fatal("solo aplica a POST")
	}
}

// La verificacion de entregabilidad del editor es una consulta con cuerpo: con solo lectura de
// plantillas se usa, tanto sobre un contenido sin guardar como sobre una version; publicar no.
func TestLaVerificacionDePlantillasEsLectura(t *testing.T) {
	t.Setenv("GATEWAY_ROUTES_FILE", "")
	tbl, err := loadRouteTable()
	if err != nil {
		t.Fatal(err)
	}
	e := newRBACEnforcer("http://access", "tok", "", "", "", nil, tbl.readPostIndex(), nil)
	req := func(p string) *http.Request { r, _ := http.NewRequest(http.MethodPost, p, nil); return r }
	for _, p := range []string{"/api/v1/templates/check", "/api/v1/templates/0f0e/versions/3/check"} {
		if !e.isReadPost(req(p)) {
			t.Errorf("%s debe gatearse como lectura", p)
		}
	}
	if e.isReadPost(req("/api/v1/templates/0f0e/versions/3/publish")) {
		t.Error("publicar sigue siendo escritura")
	}
}

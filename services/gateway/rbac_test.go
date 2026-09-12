package main

import (
	"net/http"
	"testing"
	"time"
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

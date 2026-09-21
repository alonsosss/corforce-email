package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/app"
	"go.uber.org/zap"
)

// La verificacion de credenciales de destino la llama mail-auth con el token de gateway y nada mas: sin
// sesion, sin X-Tenant-ID (la empresa sale del token del trabajo) y sin pasar por la cadena de la API de
// administracion, que exige empresa en la cabecera.
func TestLaVerificacionDeCredencialesSoloExigeElTokenDeGateway(t *testing.T) {
	t.Setenv("INTERNAL_GATEWAY_TOKEN", strings.Repeat("g", 40))
	uc := app.New(app.Deps{})
	router := adminRouter(uc, settings{perms: authz.NewChecker("http://127.0.0.1:1", "t")}, nil, zap.NewNop())

	body := `{"token":"cfmj1.no","username":"ana@acme.test"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/mail-migration/credentials/verify", strings.NewReader(body))
	req.Header.Set("X-Gateway-Token", strings.Repeat("g", 40))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "credencial no valida") {
		t.Fatalf("con el token de gateway debe llegar al verificador: %d %s", rec.Code, rec.Body)
	}

	for name, header := range map[string]string{"sin token": "", "token ajeno": strings.Repeat("x", 40)} {
		req := httptest.NewRequest(http.MethodPost, "/internal/mail-migration/credentials/verify", strings.NewReader(body))
		if header != "" {
			req.Header.Set("X-Gateway-Token", header)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || strings.Contains(rec.Body.String(), "credencial no valida") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestLaAPIDeAdministracionSigueExigiendoEmpresaEnLaCabecera(t *testing.T) {
	t.Setenv("INTERNAL_GATEWAY_TOKEN", strings.Repeat("g", 40))
	router := adminRouter(app.New(app.Deps{}), settings{perms: authz.NewChecker("http://127.0.0.1:1", "t")}, nil, zap.NewNop())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail-migration/jobs", nil)
	req.Header.Set("X-Gateway-Token", strings.Repeat("g", 40))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "missing tenant") {
		t.Fatalf("la API de administracion no debe atenderse sin empresa: %d %s", rec.Code, rec.Body)
	}
}

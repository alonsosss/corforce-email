package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range []string{
		"MAIL_DAV_PORT", "MAIL_DAV_BASE_PATH", "MAIL_DAV_REALM", "MAIL_DAV_DEFAULT_ADDRESSBOOK_NAME", "MAIL_DAV_MAX_VCARD_BYTES",
		"MAIL_DAV_MAX_VCARD_PROPERTIES", "MAIL_DAV_MAX_CONTACTS_PER_MAILBOX", "MAIL_DAV_MAX_ADDRESSBOOKS_PER_MAILBOX",
		"MAIL_DAV_CHANGES_RETAINED", "MAIL_DAV_MAX_REQUEST_BYTES", "MAIL_DAV_RATE_LIMIT_PER_MIN", "MAIL_DAV_TLS_CA_FILE",
		"MAIL_DAV_TLS_INSECURE_SKIP_VERIFY", "MAIL_AUTH_URL", "MAIL_AUTH_CELL_URLS", "GATEWAY_BASE_CELL_CODE", "ORGANIZATION_URL",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("INTERNAL_GATEWAY_TOKEN", "token-interno")
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func TestLosValoresPorDefectoSonLosDelADR(t *testing.T) {
	setEnv(t, map[string]string{"MAIL_AUTH_URL": "https://mail-auth:9082"})
	st, err := loadSettings(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	lim := st.app.Limits
	if st.port != 8058 || st.basePath != "/api/v1/dav" || lim.MaxVCardBytes != 256<<10 || lim.MaxVCardProperties != 500 ||
		lim.MaxContactsPerMailbox != 10000 || lim.MaxAddressbooksPerMailbox != 10 || lim.MaxChangesRetained != 5000 ||
		st.maxXMLBytes != 256<<10 || st.app.DefaultAddressbookName == "" || st.realm == "" {
		t.Fatalf("configuracion: %+v", st)
	}
	if err := lim.Validate(); err != nil {
		t.Fatal(err)
	}
	if st.mailAuth.BaseURL != "https://mail-auth:9082" || len(st.mailAuth.CellURLs) != 0 || st.mailAuth.TLS == nil {
		t.Fatalf("mail-auth: %+v", st.mailAuth)
	}
}

func TestLaConfiguracionInvalidaImpideArrancar(t *testing.T) {
	good := map[string]string{"MAIL_AUTH_URL": "https://mail-auth:9082"}
	for name, extra := range map[string]map[string]string{
		"sin MAIL_AUTH_URL":               {"MAIL_AUTH_URL": ""},
		"puerto invalido":                 {"MAIL_DAV_PORT": "0"},
		"vCard mas grande que la fila":    {"MAIL_DAV_MAX_VCARD_BYTES": "4194305"},
		"vCard diminuto":                  {"MAIL_DAV_MAX_VCARD_BYTES": "10"},
		"limite no numerico":              {"MAIL_DAV_MAX_CONTACTS_PER_MAILBOX": "muchos"},
		"limite en cero":                  {"MAIL_DAV_MAX_ADDRESSBOOKS_PER_MAILBOX": "0"},
		"sin token interno":               {"INTERNAL_GATEWAY_TOKEN": ""},
		"TLS sin verificar en produccion": {"MAIL_DAV_TLS_INSECURE_SKIP_VERIFY": "true"},
		"booleano ilegible":               {"MAIL_DAV_TLS_INSECURE_SKIP_VERIFY": "quizas"},
		"CA inexistente":                  {"MAIL_DAV_TLS_CA_FILE": "/no/existe.pem"},
		"celda mal escrita":               {"MAIL_AUTH_CELL_URLS": "pe-02", "GATEWAY_BASE_CELL_CODE": "pe-01", "ORGANIZATION_URL": "http://organization:8003"},
		"celda repetida":                  {"MAIL_AUTH_CELL_URLS": "pe-02=https://a:1,pe-02=https://b:1", "GATEWAY_BASE_CELL_CODE": "pe-01", "ORGANIZATION_URL": "http://organization:8003"},
		"celdas sin celda base":           {"MAIL_AUTH_CELL_URLS": "pe-02=https://a:1", "ORGANIZATION_URL": "http://organization:8003"},
		"celdas sin organization":         {"MAIL_AUTH_CELL_URLS": "pe-02=https://a:1", "GATEWAY_BASE_CELL_CODE": "pe-01"},
		"codigo de celda invalido":        {"MAIL_AUTH_CELL_URLS": "PE 02=https://a:1", "GATEWAY_BASE_CELL_CODE": "pe-01", "ORGANIZATION_URL": "http://organization:8003"},
	} {
		env := map[string]string{}
		for k, v := range good {
			env[k] = v
		}
		for k, v := range extra {
			env[k] = v
		}
		setEnv(t, env)
		if _, err := loadSettings(zap.NewNop()); err == nil {
			t.Errorf("%s: debia rechazarse", name)
		}
	}
}

func TestSinLaConfiguracionNuevaElResto(t *testing.T) {
	setEnv(t, map[string]string{"MAIL_AUTH_URL": "https://mail-auth:9082", "ENVIRONMENT": "development", "MAIL_DAV_TLS_INSECURE_SKIP_VERIFY": "true"})
	st, err := loadSettings(zap.NewNop())
	if err != nil || !st.mailAuth.TLS.InsecureSkipVerify {
		t.Fatalf("en desarrollo se admite sin verificar: %v", err)
	}
}

func TestVariasCeldasSeLeenDelEntorno(t *testing.T) {
	setEnv(t, map[string]string{
		"MAIL_AUTH_URL": "https://mail-auth:9082", "GATEWAY_BASE_CELL_CODE": "pe-01",
		"MAIL_AUTH_CELL_URLS": " pe-02=https://mail-auth-pe02:9082 , pe-03=https://mail-auth-pe03:9082 ", "ORGANIZATION_URL": "http://organization:8003",
	})
	st, err := loadSettings(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if got := st.mailAuth.CellURLs; len(got) != 2 || got["pe-02"] != "https://mail-auth-pe02:9082" || st.mailAuth.BaseCell != "pe-01" || st.organization == "" {
		t.Fatalf("celdas: %+v", st.mailAuth)
	}
}

// La cadena de la peticion: solo el gateway entra (con su token), y el limitador cuenta por la IP real.
func TestElRouterExigeElTokenDelGatewayYLimitaPorIP(t *testing.T) {
	t.Setenv("INTERNAL_GATEWAY_TOKEN", "token-interno")
	var served int
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { served++ })
	h := router(inner, 2, zap.NewNop())

	call := func(token, ip string) int {
		req := httptest.NewRequest("PROPFIND", "/api/v1/dav/", nil)
		req.RemoteAddr = "10.0.0.5:1234"
		if token != "" {
			req.Header.Set("X-Gateway-Token", token)
		}
		req.Header.Set("X-Real-IP", ip)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := call("", "203.0.113.1"); got != http.StatusUnauthorized {
		t.Fatalf("sin token: %d", got)
	}
	if got := call("otro", "203.0.113.1"); got != http.StatusUnauthorized {
		t.Fatalf("token ajeno: %d", got)
	}
	if served != 0 {
		t.Fatal("sin el token del gateway no se sirve nada")
	}
	if a, b := call("token-interno", "203.0.113.1"), call("token-interno", "203.0.113.1"); a != http.StatusOK || b != http.StatusOK {
		t.Fatalf("dentro del cupo: %d %d", a, b)
	}
	if got := call("token-interno", "203.0.113.1"); got != http.StatusTooManyRequests {
		t.Fatalf("pasado el cupo: %d", got)
	}
	if got := call("token-interno", "203.0.113.2"); got != http.StatusOK {
		t.Fatalf("otra IP tiene su cupo: %d", got)
	}
}

// El prefijo publico del servicio es el que declara el gateway: una tabla y un servicio que discrepan
// darian 404 en produccion con todo "sano".
func TestElPrefijoCoincideConLaTablaDelGateway(t *testing.T) {
	raw, err := os.ReadFile("../gateway/routes.json")
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Services struct {
			MailDAV struct {
				HostEnv     string `json:"host_env"`
				DefaultHost string `json:"default_host"`
				DefaultPort string `json:"default_port"`
			} `json:"mail-dav"`
		} `json:"services"`
		SelfAuthenticated []struct {
			Prefix  string `json:"prefix"`
			Service string `json:"service"`
		} `json:"self_authenticated"`
		WellKnown []struct {
			Path   string `json:"path"`
			Prefix string `json:"prefix"`
		} `json:"well_known"`
	}
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatal(err)
	}
	var prefix string
	for _, s := range table.SelfAuthenticated {
		if s.Service == "mail-dav" {
			prefix = s.Prefix
		}
	}
	if prefix == "" || defaultBasePath != "/api/v1/"+prefix {
		t.Fatalf("prefijo del gateway %q, prefijo del servicio %q", prefix, defaultBasePath)
	}
	if len(table.WellKnown) != 1 || table.WellKnown[0].Prefix != prefix || !strings.HasSuffix(table.WellKnown[0].Path, "/carddav") {
		t.Fatalf("descubrimiento: %+v", table.WellKnown)
	}
	if table.Services.MailDAV.DefaultPort != "8058" || defaultPort != 8058 {
		t.Fatalf("puerto: gateway %q, servicio %d", table.Services.MailDAV.DefaultPort, defaultPort)
	}
}

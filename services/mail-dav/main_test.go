package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range []string{
		"MAIL_DAV_PORT", "MAIL_DAV_BASE_PATH", "MAIL_DAV_REALM", "MAIL_DAV_DEFAULT_ADDRESSBOOK_NAME", "MAIL_DAV_MAX_VCARD_BYTES",
		"MAIL_DAV_MAX_VCARD_PROPERTIES", "MAIL_DAV_MAX_CONTACTS_PER_MAILBOX", "MAIL_DAV_MAX_ADDRESSBOOKS_PER_MAILBOX",
		"MAIL_DAV_DEFAULT_CALENDAR_NAME", "MAIL_DAV_MAX_EVENT_BYTES", "MAIL_DAV_MAX_EVENT_PROPERTIES", "MAIL_DAV_MAX_EVENTS_PER_MAILBOX",
		"MAIL_DAV_MAX_CALENDARS_PER_MAILBOX", "MAIL_DAV_MAX_RECURRENCE_WORK", "MAIL_DAV_MAX_QUERY_RECURRENCE_WORK",
		"MAIL_DAV_CHANGES_RETAINED", "MAIL_DAV_MAX_REQUEST_BYTES", "MAIL_DAV_RATE_LIMIT_PER_MIN", "MAIL_DAV_TLS_CA_FILE",
		"MAIL_DAV_MAX_MAILBOX_BYTES", "MAIL_DAV_MAX_RESPONSE_BYTES", "MAIL_DAV_MAX_INFLIGHT", "MAIL_DAV_REQUEST_TIMEOUT",
		"MAIL_DAV_AUTH_CACHE_TTL", "MAIL_DAV_AUTH_MAX_CONCURRENT",
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
	if lim.MaxMailboxBytes != 64<<20 || lim.MaxReadBytes != 16<<20 || st.maxInflight != 64 || st.reqTimeout != 25*time.Second ||
		st.authGuard.CacheTTL != 10*time.Second || st.authGuard.MaxConcurrent != 16 || st.authGuard.MaxCached < 1 || st.authGuard.Wait <= 0 {
		t.Fatalf("limites de espacio, de concurrencia y de autenticacion: %+v %+v", lim, st)
	}
	cal := st.app.Calendar
	if cal.MaxEventBytes != 256<<10 || cal.MaxEventProperties != 1000 || cal.MaxEventsPerMailbox != 20000 || cal.MaxCalendarsPerMailbox != 10 ||
		cal.MaxRecurrenceWork != 20000 || cal.MaxQueryWork != 500000 || st.app.DefaultCalendarName != "Calendario" {
		t.Fatalf("calendarios: %+v", st.app)
	}
	if err := cal.Validate(); err != nil {
		t.Fatal(err)
	}
	if st.mailAuth.BaseURL != "https://mail-auth:9082" || len(st.mailAuth.CellURLs) != 0 || st.mailAuth.TLS == nil {
		t.Fatalf("mail-auth: %+v", st.mailAuth)
	}
}

func TestLaConfiguracionInvalidaImpideArrancar(t *testing.T) {
	good := map[string]string{"MAIL_AUTH_URL": "https://mail-auth:9082"}
	for name, extra := range map[string]map[string]string{
		"sin MAIL_AUTH_URL":                {"MAIL_AUTH_URL": ""},
		"puerto invalido":                  {"MAIL_DAV_PORT": "0"},
		"vCard mas grande que la fila":     {"MAIL_DAV_MAX_VCARD_BYTES": "4194305"},
		"vCard diminuto":                   {"MAIL_DAV_MAX_VCARD_BYTES": "10"},
		"limite no numerico":               {"MAIL_DAV_MAX_CONTACTS_PER_MAILBOX": "muchos"},
		"limite en cero":                   {"MAIL_DAV_MAX_ADDRESSBOOKS_PER_MAILBOX": "0"},
		"evento mas grande que la fila":    {"MAIL_DAV_MAX_EVENT_BYTES": "4194305"},
		"evento diminuto":                  {"MAIL_DAV_MAX_EVENT_BYTES": "10"},
		"calendarios en cero":              {"MAIL_DAV_MAX_CALENDARS_PER_MAILBOX": "0"},
		"espacio de buzon diminuto":        {"MAIL_DAV_MAX_MAILBOX_BYTES": "1000"},
		"respuesta diminuta":               {"MAIL_DAV_MAX_RESPONSE_BYTES": "1000"},
		"un vCard no cabe en la respuesta": {"MAIL_DAV_MAX_RESPONSE_BYTES": "65536"},
		"peticiones simultaneas en cero":   {"MAIL_DAV_MAX_INFLIGHT": "0"},
		"plazo de peticion en cero":        {"MAIL_DAV_REQUEST_TIMEOUT": "0s"},
		"plazo ilegible":                   {"MAIL_DAV_REQUEST_TIMEOUT": "mucho"},
		"cache de autenticacion eterna":    {"MAIL_DAV_AUTH_CACHE_TTL": "1h"},
		"cache de autenticacion negativa":  {"MAIL_DAV_AUTH_CACHE_TTL": "-1s"},
		"verificaciones en cero":           {"MAIL_DAV_AUTH_MAX_CONCURRENT": "0"},
		"eventos no numericos":             {"MAIL_DAV_MAX_EVENTS_PER_MAILBOX": "muchos"},
		"trabajo de recurrencia en cero":   {"MAIL_DAV_MAX_RECURRENCE_WORK": "0"},
		"trabajo de consulta desmedido":    {"MAIL_DAV_MAX_QUERY_RECURRENCE_WORK": "1000000000"},
		"sin token interno":                {"INTERNAL_GATEWAY_TOKEN": ""},
		"TLS sin verificar en produccion":  {"MAIL_DAV_TLS_INSECURE_SKIP_VERIFY": "true"},
		"booleano ilegible":                {"MAIL_DAV_TLS_INSECURE_SKIP_VERIFY": "quizas"},
		"CA inexistente":                   {"MAIL_DAV_TLS_CA_FILE": "/no/existe.pem"},
		"celda mal escrita":                {"MAIL_AUTH_CELL_URLS": "pe-02", "GATEWAY_BASE_CELL_CODE": "pe-01", "ORGANIZATION_URL": "http://organization:8003"},
		"celda repetida":                   {"MAIL_AUTH_CELL_URLS": "pe-02=https://a:1,pe-02=https://b:1", "GATEWAY_BASE_CELL_CODE": "pe-01", "ORGANIZATION_URL": "http://organization:8003"},
		"celdas sin celda base":            {"MAIL_AUTH_CELL_URLS": "pe-02=https://a:1", "ORGANIZATION_URL": "http://organization:8003"},
		"celdas sin organization":          {"MAIL_AUTH_CELL_URLS": "pe-02=https://a:1", "GATEWAY_BASE_CELL_CODE": "pe-01"},
		"codigo de celda invalido":         {"MAIL_AUTH_CELL_URLS": "PE 02=https://a:1", "GATEWAY_BASE_CELL_CODE": "pe-01", "ORGANIZATION_URL": "http://organization:8003"},
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
	h := router(inner, settings{ratePerMin: 2, maxInflight: 8, reqTimeout: time.Minute}, zap.NewNop())

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
	if len(table.WellKnown) != 2 {
		t.Fatalf("descubrimiento: %+v", table.WellKnown)
	}
	for i, suffix := range []string{"/carddav", "/caldav"} {
		if table.WellKnown[i].Prefix != prefix || !strings.HasSuffix(table.WellKnown[i].Path, suffix) {
			t.Fatalf("descubrimiento: %+v", table.WellKnown)
		}
	}
	if table.Services.MailDAV.DefaultPort != "8058" || defaultPort != 8058 {
		t.Fatalf("puerto: gateway %q, servicio %d", table.Services.MailDAV.DefaultPort, defaultPort)
	}
}

func TestLaCacheDeAutenticacionSePuedeApagar(t *testing.T) {
	setEnv(t, map[string]string{"MAIL_AUTH_URL": "https://mail-auth:9082", "MAIL_DAV_AUTH_CACHE_TTL": "0s"})
	st, err := loadSettings(zap.NewNop())
	if err != nil || st.authGuard.CacheTTL != 0 {
		t.Fatalf("sin cache: %v %+v", err, st.authGuard)
	}
}

// Pasado el tope de peticiones simultaneas el servicio responde 503 con Retry-After en vez de acumularlas
// esperando una conexion de la base, y la peticion que se atiende lleva un plazo.
func TestElRouterAcotaLasPeticionesSimultaneasYPoneUnPlazo(t *testing.T) {
	t.Setenv("INTERNAL_GATEWAY_TOKEN", "token-interno")
	entered, release := make(chan struct{}, 4), make(chan struct{})
	var deadline time.Time
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deadline, _ = r.Context().Deadline()
		entered <- struct{}{}
		<-release
	})
	h := router(inner, settings{ratePerMin: 1000, maxInflight: 1, reqTimeout: time.Minute}, zap.NewNop())
	call := func() int {
		req := httptest.NewRequest("PROPFIND", "/api/v1/dav/", nil)
		req.RemoteAddr = "10.0.0.5:1234"
		req.Header.Set("X-Gateway-Token", "token-interno")
		req.Header.Set("X-Real-IP", "203.0.113.1")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == http.StatusServiceUnavailable && rec.Header().Get("Retry-After") == "" {
			t.Error("el 503 debe llevar Retry-After")
		}
		return rec.Code
	}
	done := make(chan int)
	go func() { done <- call() }()
	<-entered
	if got := call(); got != http.StatusServiceUnavailable {
		t.Fatalf("con el servicio ocupado: %d", got)
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > time.Minute {
		t.Fatalf("la peticion debe llevar un plazo de reqTimeout: %v", remaining)
	}
	close(release)
	if got := <-done; got != http.StatusOK {
		t.Fatalf("la peticion en curso: %d", got)
	}
	if got := call(); got != http.StatusOK {
		t.Fatalf("liberado el turno vuelve a atender: %d", got)
	}
}

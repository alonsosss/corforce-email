package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

var originHeader = map[string]string{"Origin": allowedOrigin}

func namedCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func jsonBody(v any) *strings.Reader {
	b, _ := json.Marshal(v)
	return strings.NewReader(string(b))
}

// mfaLogin hace los dos pasos del buzon con verificacion en dos pasos y devuelve la cookie de sesion.
func mfaLogin(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	first := do(h, http.MethodPost, BasePath+"/session", loginBody(mfaUser, testPass), originHeader, nil)
	challenge := namedCookie(first, mfaCookieName)
	if first.Code != http.StatusOK || challenge == nil {
		t.Fatalf("primer paso: %d %s", first.Code, first.Body.String())
	}
	second := do(h, http.MethodPost, BasePath+"/session/mfa", jsonBody(map[string]string{"code": "123456"}), originHeader, challenge)
	session := namedCookie(second, cookieName)
	if second.Code != http.StatusOK || session == nil {
		t.Fatalf("segundo paso: %d %s", second.Code, second.Body.String())
	}
	return session
}

func TestPrimerPasoConVerificacionEmiteSoloLaCookieDelDesafio(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := do(h, http.MethodPost, BasePath+"/session", loginBody(mfaUser, testPass), originHeader, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Data["mfa_required"] != true || len(env.Data) != 1 {
		t.Fatalf("cuerpo: %s", rec.Body.String())
	}
	if sessionCookie(rec) != nil {
		t.Fatal("el primer paso no abre sesion")
	}
	c := namedCookie(rec, mfaCookieName)
	if c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/api/v1/webmail/session" || c.MaxAge != 300 {
		t.Fatalf("cookie del desafio: %+v", c)
	}
	if !strings.HasPrefix(c.Value, testCell+".") || strings.Contains(rec.Body.String(), c.Value) {
		t.Fatalf("el desafio lleva la celda y no viaja en el cuerpo: %q", c.Value)
	}
}

func TestSegundoPasoAbreLaSesionYBorraElDesafio(t *testing.T) {
	h, _ := newTestHandler(t)
	first := do(h, http.MethodPost, BasePath+"/session", loginBody(mfaUser, testPass), originHeader, nil)
	challenge := namedCookie(first, mfaCookieName)

	bad := do(h, http.MethodPost, BasePath+"/session/mfa", jsonBody(map[string]string{"code": "000000"}), originHeader, challenge)
	if bad.Code != http.StatusUnprocessableEntity || errorCode(t, bad) != "INVALID_MFA_CODE" || namedCookie(bad, mfaCookieName) != nil {
		t.Fatalf("codigo malo: %d %s", bad.Code, bad.Body.String())
	}

	rec := do(h, http.MethodPost, BasePath+"/session/mfa", jsonBody(map[string]string{"code": "123456"}), originHeader, challenge)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	session := sessionCookie(rec)
	if session == nil || !session.HttpOnly || !session.Secure || session.Path != BasePath || session.MaxAge != 12*3600 {
		t.Fatalf("cookie de sesion: %+v", session)
	}
	cleared := namedCookie(rec, mfaCookieName)
	if cleared == nil || cleared.MaxAge >= 0 || cleared.Path != "/api/v1/webmail/session" {
		t.Fatalf("la cookie del desafio se borra: %+v", cleared)
	}
	var env struct {
		Data sessionDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Data.Username != mfaUser || env.Data.DisplayName != "Doble" {
		t.Fatalf("mismo cuerpo que POST /session: %s", rec.Body.String())
	}
	if got := do(h, http.MethodGet, BasePath+"/session", nil, nil, session); got.Code != http.StatusOK {
		t.Fatalf("la sesion sirve: %d", got.Code)
	}

	again := do(h, http.MethodPost, BasePath+"/session/mfa", jsonBody(map[string]string{"code": "123456"}), originHeader, challenge)
	if again.Code != http.StatusUnauthorized || errorCode(t, again) != "MFA_CHALLENGE_EXPIRED" || namedCookie(again, mfaCookieName) == nil {
		t.Fatalf("un desafio usado caduca y se borra su cookie: %d %s", again.Code, again.Body.String())
	}
}

func TestSegundoPasoSinCookie(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := do(h, http.MethodPost, BasePath+"/session/mfa", jsonBody(map[string]string{"code": "123456"}), originHeader, nil)
	if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "MFA_CHALLENGE_EXPIRED" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestLasRutasNuevasExigenOrigin(t *testing.T) {
	h, _ := newTestHandler(t)
	cookie := login(t, h)
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/session/mfa"},
		{http.MethodPost, "/security/mfa/setup"},
		{http.MethodPost, "/security/mfa/activate"},
		{http.MethodPost, "/security/mfa/recovery-codes"},
		{http.MethodDelete, "/security/mfa"},
		{http.MethodPost, "/security/app-passwords"},
		{http.MethodDelete, "/security/app-passwords/33333333-3333-4333-8333-333333333333"},
	} {
		for _, origin := range []string{"", "https://evil.example.com"} {
			rec := do(h, c.method, BasePath+c.path, strings.NewReader("{}"), map[string]string{"Origin": origin}, cookie)
			if rec.Code != http.StatusForbidden || errorCode(t, rec) != "ORIGIN_NOT_ALLOWED" {
				t.Errorf("%s %s con Origin %q: %d %s", c.method, c.path, origin, rec.Code, rec.Body.String())
			}
		}
	}
}

func TestLaSeguridadExigeSesion(t *testing.T) {
	h, _ := newTestHandler(t)
	if rec := do(h, http.MethodGet, BasePath+"/security", nil, nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("%d", rec.Code)
	}
}

func TestSeguridadDevuelveEstadoYContrasenas(t *testing.T) {
	env := newTestEnv(t, nopSender{})
	cookie := login(t, env.h)
	env.settings.security.enabled = true
	rec := do(env.h, http.MethodGet, BasePath+"/security", nil, nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if string(out.Data["mfa"]) != `{"enabled":true,"enabled_at":null,"recovery_remaining":7}` || string(out.Data["app_passwords_max"]) != "null" {
		t.Fatalf("%s", rec.Body.String())
	}
	var list []map[string]any
	if err := json.Unmarshal(out.Data["app_passwords"], &list); err != nil || len(list) != 1 || list[0]["imap_access"] != true || list[0]["pop3_access"] != false || list[0]["name"] != "Movil" {
		t.Fatalf("contrasenas: %s", out.Data["app_passwords"])
	}
	if _, ok := list[0]["password"]; ok {
		t.Fatal("la lista nunca lleva la contrasena")
	}
}

func TestActivarLaVerificacionPorElAPI(t *testing.T) {
	env := newTestEnv(t, nopSender{})
	cookie := login(t, env.h)

	bad := do(env.h, http.MethodPost, BasePath+"/security/mfa/setup", jsonBody(map[string]string{"current_password": "mala"}), originHeader, cookie)
	if bad.Code != http.StatusUnauthorized || errorCode(t, bad) != "INVALID_CREDENTIALS" || sessionCookie(bad) != nil {
		t.Fatalf("contrasena mala, la sesion sigue: %d %s", bad.Code, bad.Body.String())
	}
	early := do(env.h, http.MethodPost, BasePath+"/security/mfa/activate", jsonBody(map[string]string{"secret": "JBSWY3DPEHPK3PXP", "code": "123456"}), originHeader, cookie)
	if early.Code != http.StatusConflict || errorCode(t, early) != "MFA_SETUP_EXPIRED" {
		t.Fatalf("activar sin preparar: %d %s", early.Code, early.Body.String())
	}

	setup := do(env.h, http.MethodPost, BasePath+"/security/mfa/setup", jsonBody(map[string]string{"current_password": testPass}), originHeader, cookie)
	var s struct {
		Data mfaSetupDTO `json:"data"`
	}
	if setup.Code != http.StatusOK || json.Unmarshal(setup.Body.Bytes(), &s) != nil || s.Data.Secret == "" || !strings.HasPrefix(s.Data.ProvisioningURI, "otpauth://") {
		t.Fatalf("preparar: %d %s", setup.Code, setup.Body.String())
	}
	wrong := do(env.h, http.MethodPost, BasePath+"/security/mfa/activate", jsonBody(map[string]string{"secret": s.Data.Secret, "code": "000000"}), originHeader, cookie)
	if wrong.Code != http.StatusUnprocessableEntity || errorCode(t, wrong) != "INVALID_MFA_CODE" {
		t.Fatalf("codigo malo: %d %s", wrong.Code, wrong.Body.String())
	}
	ok := do(env.h, http.MethodPost, BasePath+"/security/mfa/activate", jsonBody(map[string]string{"secret": s.Data.Secret, "code": "123456"}), originHeader, cookie)
	var codes struct {
		Data recoveryCodesDTO `json:"data"`
	}
	if ok.Code != http.StatusOK || json.Unmarshal(ok.Body.Bytes(), &codes) != nil || len(codes.Data.RecoveryCodes) != 2 {
		t.Fatalf("activar: %d %s", ok.Code, ok.Body.String())
	}
}

func TestAccionesQueExigenCodigoConVerificacionActiva(t *testing.T) {
	env := newTestEnv(t, nopSender{})
	cookie := mfaLogin(t, env.h)

	rec := do(env.h, http.MethodPost, BasePath+"/password", jsonBody(map[string]string{"current_password": testPass, "new_password": "otra-larga-segura"}), originHeader, cookie)
	if rec.Code != http.StatusForbidden || errorCode(t, rec) != "MFA_REQUIRED" || env.settings.password != "" {
		t.Fatalf("contrasena sin codigo: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(env.h, http.MethodPost, BasePath+"/security/app-passwords", jsonBody(map[string]any{"name": "Movil", "imap": true, "current_password": testPass}), originHeader, cookie)
	if rec.Code != http.StatusForbidden || errorCode(t, rec) != "MFA_REQUIRED" {
		t.Fatalf("contrasena de aplicacion sin codigo: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(env.h, http.MethodPost, BasePath+"/security/app-passwords", jsonBody(map[string]any{"name": "Movil", "imap": true, "smtp": true, "current_password": testPass, "code": "123456"}), originHeader, cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Data map[string]any `json:"data"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &created) != nil || created.Data["password"] != "abcd-efgh-ijkl-mnop" || created.Data["imap_access"] != true || created.Data["smtp_access"] != true || created.Data["pop3_access"] != false {
		t.Fatalf("%s", rec.Body.String())
	}
	if in := env.settings.security.created; in == nil || !in.Access.IMAP || !in.Access.SMTP || in.Access.DAV {
		t.Fatalf("lo que llega al directorio: %+v", in)
	}

	rec = do(env.h, http.MethodDelete, BasePath+"/security/mfa", jsonBody(map[string]string{"current_password": testPass}), originHeader, cookie)
	if rec.Code != http.StatusForbidden || errorCode(t, rec) != "MFA_REQUIRED" {
		t.Fatalf("desactivar sin codigo: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(env.h, http.MethodDelete, BasePath+"/security/mfa", jsonBody(map[string]string{"current_password": testPass, "code": "123456"}), originHeader, cookie)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("desactivar: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(env.h, http.MethodPost, BasePath+"/security/mfa/recovery-codes", jsonBody(map[string]string{"code": "000000"}), originHeader, cookie)
	if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != "INVALID_MFA_CODE" {
		t.Fatalf("regenerar con codigo malo: %d %s", rec.Code, rec.Body.String())
	}
}

func TestBorrarContrasenaDeAplicacionPorElAPI(t *testing.T) {
	env := newTestEnv(t, nopSender{})
	cookie := login(t, env.h)
	rec := do(env.h, http.MethodDelete, BasePath+"/security/app-passwords/33333333-3333-4333-8333-333333333333", nil, originHeader, cookie)
	if rec.Code != http.StatusNoContent || env.settings.security.deleted != "33333333-3333-4333-8333-333333333333" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = do(env.h, http.MethodDelete, BasePath+"/security/app-passwords/no-es-un-id", nil, originHeader, cookie)
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "APP_PASSWORD_NOT_FOUND" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	env.settings.security.err = domain.ErrAppPasswordLimit
	rec = do(env.h, http.MethodPost, BasePath+"/security/app-passwords", jsonBody(map[string]any{"name": "Movil", "imap": true, "current_password": testPass}), originHeader, cookie)
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "APP_PASSWORD_LIMIT" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestReglasPasanLosRechazosDelReenvioExterno(t *testing.T) {
	env := newTestEnv(t, nopSender{})
	cookie := login(t, env.h)
	body := map[string]any{"rules": []any{}, "forwarding": map[string]any{"enabled": true, "addresses": []string{"fuera@otra.pe"}, "keep_copy": true}}

	env.settings.err = &domain.ReauthRequiredError{Addresses: []string{"fuera@otra.pe", "otro@fuera.pe"}}
	rec := do(env.h, http.MethodPut, BasePath+"/filters", jsonBody(body), originHeader, cookie)
	var out struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details struct {
				Addresses []string `json:"addresses"`
			} `json:"details"`
		} `json:"error"`
	}
	if rec.Code != http.StatusForbidden || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Error.Code != "REAUTH_REQUIRED" ||
		strings.Join(out.Error.Details.Addresses, ",") != "fuera@otra.pe,otro@fuera.pe" || out.Error.Message == "" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if env.settings.filtersIn.Reauthenticated {
		t.Fatal("sin contrasena no va reautenticada")
	}

	env.settings.err = &domain.ExternalForwardingDisabledError{}
	rec = do(env.h, http.MethodPut, BasePath+"/filters", jsonBody(body), originHeader, cookie)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"code":"EXTERNAL_FORWARDING_DISABLED"`) || !strings.Contains(rec.Body.String(), `"addresses":[]`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}

	env.settings.err = nil
	body["current_password"] = testPass
	rec = do(env.h, http.MethodPut, BasePath+"/filters", jsonBody(body), originHeader, cookie)
	if rec.Code != http.StatusOK || !env.settings.filtersIn.Reauthenticated {
		t.Fatalf("con la contrasena va reautenticada: %d %s", rec.Code, rec.Body.String())
	}
	body["current_password"] = "mala"
	rec = do(env.h, http.MethodPut, BasePath+"/filters", jsonBody(body), originHeader, cookie)
	if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "INVALID_CREDENTIALS" || sessionCookie(rec) != nil {
		t.Fatalf("contrasena mala: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCupoPorBuzonEnLasRutasConSesion(t *testing.T) {
	mailbox, ip := newCountingLimiter(3), newCountingLimiter(100)
	env := newTestEnvWith(t, nopSender{}, func(c *Config) { c.MailboxRateLimiter, c.IPRateLimiter = mailbox, ip })
	ana := login(t, env.h)
	other := mfaLogin(t, env.h)
	ipAfterLogin := ip.count("ip:192.0.2.1")

	for i := 0; i < 3; i++ {
		if rec := do(env.h, http.MethodGet, BasePath+"/session", nil, nil, ana); rec.Code != http.StatusOK {
			t.Fatalf("peticion %d: %d", i, rec.Code)
		}
	}
	rec := do(env.h, http.MethodGet, BasePath+"/folders", nil, nil, ana)
	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "RATE_LIMITED" || rec.Header().Get("Retry-After") != "2" {
		t.Fatalf("cuarta: %d %s %v", rec.Code, rec.Body.String(), rec.Header())
	}
	if rec := do(env.h, http.MethodGet, BasePath+"/session", nil, nil, other); rec.Code != http.StatusOK {
		t.Fatalf("otro buzon tras la misma IP tiene su propio cupo: %d", rec.Code)
	}
	if mailbox.count("k:"+testUser) != 4 || mailbox.count("k:"+mfaUser) != 1 {
		t.Fatalf("cuenta por buzon: %v", mailbox.seen)
	}
	if ip.count("ip:192.0.2.1") != ipAfterLogin {
		t.Fatal("lo que tiene sesion no cuenta por IP")
	}
}

func TestCupoPorIPEnLoQueNoTieneSesion(t *testing.T) {
	mailbox, ip := newCountingLimiter(100), newCountingLimiter(2)
	env := newTestEnvWith(t, nopSender{}, func(c *Config) { c.MailboxRateLimiter, c.IPRateLimiter = mailbox, ip })
	headers := map[string]string{"Origin": allowedOrigin, "X-Real-IP": "203.0.113.9"}
	for i := 0; i < 2; i++ {
		if rec := do(env.h, http.MethodPost, BasePath+"/session", loginBody(testUser, "mala"), headers, nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%d", rec.Code)
		}
	}
	for _, path := range []string{"/session", "/session/mfa"} {
		rec := do(env.h, http.MethodPost, BasePath+path, loginBody(testUser, testPass), headers, nil)
		if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "RATE_LIMITED" || rec.Header().Get("Retry-After") == "" {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	// Una cookie que no abre sesion tambien cuenta por IP.
	bogus := &http.Cookie{Name: cookieName, Value: testCell + "." + strings.Repeat("x", 43)}
	if rec := do(env.h, http.MethodGet, BasePath+"/folders", nil, map[string]string{"X-Real-IP": "203.0.113.9"}, bogus); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("sin sesion: %d", rec.Code)
	}
	if rec := do(env.h, http.MethodGet, BasePath+"/folders", nil, map[string]string{"X-Real-IP": "203.0.113.10"}, bogus); rec.Code != http.StatusUnauthorized {
		t.Fatalf("otra IP: %d", rec.Code)
	}
	if len(mailbox.seen) != 0 {
		t.Fatalf("sin sesion no se cuenta ningun buzon: %v", mailbox.seen)
	}
}

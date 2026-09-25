package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// withMFA deja el buzon de prueba con la verificacion en dos pasos activa en mail-auth y en el
// directorio.
func withMFA(h *harness) {
	h.auth.mfa = map[string]bool{testUser: true}
	h.security.enabled = true
}

// startMFALogin hace el primer paso con verificacion en dos pasos y devuelve el token del desafio.
func startMFALogin(t *testing.T, h *harness) string {
	t.Helper()
	res, err := h.svc.Login(context.Background(), testUser, testPass, testIP, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.MFAChallenge == "" || res.Token != "" {
		t.Fatalf("se esperaba solo el desafio: %+v", res)
	}
	return res.MFAChallenge
}

func TestLoginSinVerificacionEnDosPasosAbreLaSesionDirecta(t *testing.T) {
	h := newHarness(t)
	res, err := h.svc.Login(context.Background(), testUser, testPass, testIP, "")
	if err != nil || res.Token == "" || res.MFAChallenge != "" {
		t.Fatalf("%+v %v", res, err)
	}
	if h.mfa.count() != 0 || len(h.security.calls) != 0 {
		t.Fatal("sin verificacion en dos pasos no hay desafio ni llamada al directorio")
	}
}

func TestLoginConVerificacionEnDosPasosNoAbreSesionHastaElCodigo(t *testing.T) {
	h := newHarness(t)
	withMFA(h)
	started := h.clock.Now()
	challenge := startMFALogin(t, h)

	if len(h.store.sessions) != 0 {
		t.Fatal("el primer paso no abre sesion")
	}
	if cell, ok := domain.ParseSessionToken(challenge); !ok || cell != testCell {
		t.Fatalf("el desafio lleva la celda y 256 bits: %q", challenge)
	}
	if h.mfa.count() != 1 || h.mfa.lastTTL != 5*time.Minute {
		t.Fatalf("desafio: %d ttl=%v", h.mfa.count(), h.mfa.lastTTL)
	}
	for key := range h.mfa.challenges {
		if strings.Contains(key, challenge) || key == challenge {
			t.Fatal("el almacen solo guarda el hash del token")
		}
	}

	h.clock.Advance(time.Minute)
	token, sess, err := h.svc.CompleteLogin(context.Background(), challenge, " 123456 ", testIP, "")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Username != testUser || sess.DisplayName != "Ana Perez" || !sess.CreatedAt.Equal(started) {
		t.Fatalf("la sesion hereda la identidad y el inicio de la verificacion: %+v", sess)
	}
	if got, err := h.svc.Authenticate(context.Background(), token); err != nil || got.Username != testUser {
		t.Fatalf("sesion: %+v %v", got, err)
	}
	if h.mfa.count() != 0 {
		t.Fatal("el desafio se borra al abrir la sesion")
	}
	if _, _, err := h.svc.CompleteLogin(context.Background(), challenge, "123456", testIP, ""); !errors.Is(err, domain.ErrMFAChallengeExpired) {
		t.Fatalf("un desafio usado no vuelve a servir: %v", err)
	}
}

func TestSegundoPasoRotaLaSesionPrevia(t *testing.T) {
	h := newHarness(t)
	previous, _ := h.login(t)
	withMFA(h)
	challenge := startMFALogin(t, h)
	if _, err := h.svc.Authenticate(context.Background(), previous); err != nil {
		t.Fatal("el primer paso no toca la sesion previa")
	}
	if _, _, err := h.svc.CompleteLogin(context.Background(), challenge, "123456", testIP, previous); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Authenticate(context.Background(), previous); !errors.Is(err, domain.ErrSessionInvalid) {
		t.Fatalf("la sesion previa se destruye al completar: %v", err)
	}
}

func TestSegundoPasoAgotaElDesafioAlQuintoFallo(t *testing.T) {
	h := newHarness(t)
	withMFA(h)
	challenge := startMFALogin(t, h)
	for i := 1; i <= 5; i++ {
		_, _, err := h.svc.CompleteLogin(context.Background(), challenge, fmt.Sprintf("00000%d", i), testIP, "")
		if !errors.Is(err, domain.ErrInvalidMFACode) {
			t.Fatalf("intento %d: %v", i, err)
		}
		if want := map[bool]int{true: 0, false: 1}[i == 5]; h.mfa.count() != want {
			t.Fatalf("intento %d: desafios %d", i, h.mfa.count())
		}
	}
	if _, _, err := h.svc.CompleteLogin(context.Background(), challenge, "123456", testIP, ""); !errors.Is(err, domain.ErrMFAChallengeExpired) {
		t.Fatalf("tras cinco fallos ni el codigo bueno sirve: %v", err)
	}
	if len(h.store.sessions) != 0 {
		t.Fatal("sin sesion")
	}
}

func TestSegundoPasoCuentaElCodigoVacioComoIntentoSinLlamarAlDirectorio(t *testing.T) {
	h := newHarness(t)
	withMFA(h)
	challenge := startMFALogin(t, h)
	for _, code := range []string{"", "   ", strings.Repeat("9", domain.MaxMFACodeLen+1)} {
		if _, _, err := h.svc.CompleteLogin(context.Background(), challenge, code, testIP, ""); !errors.Is(err, domain.ErrInvalidMFACode) {
			t.Fatalf("%q: %v", code, err)
		}
	}
	if len(h.security.calls) != 0 {
		t.Fatalf("un codigo imposible no llega al directorio: %v", h.security.calls)
	}
	if h.mfa.challenges[firstKey(h)].attempts != 3 {
		t.Fatal("cada intento cuenta")
	}
}

func firstKey(h *harness) string {
	for k := range h.mfa.challenges {
		return k
	}
	return ""
}

func TestSegundoPasoCaducado(t *testing.T) {
	h := newHarness(t)
	withMFA(h)
	challenge := startMFALogin(t, h)
	h.clock.Advance(5 * time.Minute)
	if _, _, err := h.svc.CompleteLogin(context.Background(), challenge, "123456", testIP, ""); !errors.Is(err, domain.ErrMFAChallengeExpired) {
		t.Fatalf("%v", err)
	}
	for _, token := range []string{"", "no-es-un-token", "otra-celda." + strings.Repeat("a", 43)} {
		if _, _, err := h.svc.CompleteLogin(context.Background(), token, "123456", testIP, ""); !errors.Is(err, domain.ErrMFAChallengeExpired) {
			t.Fatalf("%q: %v", token, err)
		}
	}
	if len(h.security.calls) != 0 {
		t.Fatal("sin desafio no se valida ningun codigo")
	}
}

func TestSegundoPasoConLaVerificacionRestablecidaVuelveAEmpezar(t *testing.T) {
	h := newHarness(t)
	withMFA(h)
	challenge := startMFALogin(t, h)
	h.security.enabled = false
	if _, _, err := h.svc.CompleteLogin(context.Background(), challenge, "123456", testIP, ""); !errors.Is(err, domain.ErrMFAChallengeExpired) {
		t.Fatalf("%v", err)
	}
	if h.mfa.count() != 0 || len(h.store.sessions) != 0 {
		t.Fatal("el desafio se borra y no hay sesion")
	}
}

func TestSegundoPasoConElDirectorioCaido(t *testing.T) {
	h := newHarness(t)
	withMFA(h)
	challenge := startMFALogin(t, h)
	h.security.err = fmt.Errorf("%w: mail-directory", domain.ErrUnavailable)
	if _, _, err := h.svc.CompleteLogin(context.Background(), challenge, "123456", testIP, ""); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("%v", err)
	}
	if len(h.store.sessions) != 0 {
		t.Fatal("sin validar el codigo no hay sesion")
	}
}

func TestSegundoPasoNoAbreSesionSiNoPuedeBorrarElDesafio(t *testing.T) {
	h := newHarness(t)
	withMFA(h)
	challenge := startMFALogin(t, h)
	h.mfa.failDelete = errors.New("redis caido")
	if _, _, err := h.svc.CompleteLogin(context.Background(), challenge, "123456", testIP, ""); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("%v", err)
	}
	if len(h.store.sessions) != 0 {
		t.Fatal("un desafio que sigue admitiendo codigos no abre sesion")
	}
}

func TestPrimerPasoConElAlmacenCaido(t *testing.T) {
	h := newHarness(t)
	withMFA(h)
	h.mfa.failCreate = errors.New("redis caido")
	if _, err := h.svc.Login(context.Background(), testUser, testPass, testIP, ""); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("%v", err)
	}
}

func TestPrepararExigeLaContrasenaActual(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	if _, err := h.svc.PrepareMFA(context.Background(), sess, "mala", testIP); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("%v", err)
	}
	if _, err := h.svc.PrepareMFA(context.Background(), sess, "", testIP); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("sin contrasena: %v", err)
	}
	if len(h.mfa.setups) != 0 {
		t.Fatal("sin la contrasena no se prepara nada")
	}
	setup, err := h.svc.PrepareMFA(context.Background(), sess, testPass, testIP)
	if err != nil {
		t.Fatal(err)
	}
	if setup.Secret != "JBSWY3DPEHPK3PXP" || !strings.Contains(setup.ProvisioningURI, testUser) {
		t.Fatalf("%+v", setup)
	}
	if h.mfa.setups[testUser] == "" || h.mfa.setups[testUser] == setup.Secret || h.mfa.setupTTL != 10*time.Minute {
		t.Fatalf("solo se guarda el hash del secreto: %+v ttl=%v", h.mfa.setups, h.mfa.setupTTL)
	}
	if h.auth.lastIP != testIP {
		t.Fatal("la IP real llega a mail-auth")
	}
}

func TestPrepararConLaVerificacionYaActiva(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	withMFA(h)
	if _, err := h.svc.PrepareMFA(context.Background(), sess, testPass, testIP); !errors.Is(err, domain.ErrMFAAlreadyEnabled) {
		t.Fatalf("%v", err)
	}
}

func TestActivarSoloConElSecretoPreparado(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	ctx := context.Background()

	if _, err := h.svc.ActivateMFA(ctx, sess, "JBSWY3DPEHPK3PXP", "123456"); !errors.Is(err, domain.ErrMFASetupExpired) {
		t.Fatalf("sin preparar (sesion robada con su propio secreto): %v", err)
	}
	if _, err := h.svc.PrepareMFA(ctx, sess, testPass, testIP); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.ActivateMFA(ctx, sess, "OTROSECRETOAJENO", "123456"); !errors.Is(err, domain.ErrMFASetupExpired) {
		t.Fatalf("otro secreto: %v", err)
	}
	if _, err := h.svc.ActivateMFA(ctx, sess, "JBSWY3DPEHPK3PXP", "000000"); !errors.Is(err, domain.ErrInvalidMFACode) {
		t.Fatalf("codigo malo: %v", err)
	}
	if h.mfa.setups[testUser] == "" {
		t.Fatal("un codigo malo no gasta la preparacion")
	}
	codes, err := h.svc.ActivateMFA(ctx, sess, " jbswy3dpehpk3pxp ", "123456")
	if err != nil || len(codes) != 1 {
		t.Fatalf("%v %v", codes, err)
	}
	if h.security.activated != "JBSWY3DPEHPK3PXP" || len(h.mfa.setups) != 0 {
		t.Fatalf("activado con %q; preparaciones %v", h.security.activated, h.mfa.setups)
	}
	if _, err := h.svc.ActivateMFA(ctx, sess, "", "123456"); err == nil {
		t.Fatal("sin secreto")
	}
}

func TestRegenerarCodigosExigeCodigo(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	withMFA(h)
	if _, err := h.svc.RegenerateRecoveryCodes(context.Background(), sess, " "); !errors.Is(err, domain.ErrMFARequired) {
		t.Fatalf("%v", err)
	}
	if _, err := h.svc.RegenerateRecoveryCodes(context.Background(), sess, "000000"); !errors.Is(err, domain.ErrInvalidMFACode) {
		t.Fatalf("%v", err)
	}
	codes, err := h.svc.RegenerateRecoveryCodes(context.Background(), sess, "123456")
	if err != nil || len(codes) != 1 {
		t.Fatalf("%v %v", codes, err)
	}
}

func TestDesactivarExigeContrasenaYCodigo(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	withMFA(h)
	ctx := context.Background()

	if err := h.svc.DisableMFA(ctx, sess, domain.Reauthentication{CurrentPassword: "mala", Code: "123456"}, testIP); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("%v", err)
	}
	if err := h.svc.DisableMFA(ctx, sess, domain.Reauthentication{CurrentPassword: testPass}, testIP); !errors.Is(err, domain.ErrMFARequired) {
		t.Fatalf("%v", err)
	}
	if len(h.security.calls) != 0 {
		t.Fatalf("sin contrasena y codigo no se llama al directorio: %v", h.security.calls)
	}
	if err := h.svc.DisableMFA(ctx, sess, domain.Reauthentication{CurrentPassword: testPass, Code: "000000"}, testIP); !errors.Is(err, domain.ErrInvalidMFACode) {
		t.Fatalf("%v", err)
	}
	if err := h.svc.DisableMFA(ctx, sess, domain.Reauthentication{CurrentPassword: testPass, Code: "123456"}, testIP); err != nil {
		t.Fatal(err)
	}
	// El codigo lo valida el directorio al desactivar; validarlo antes por separado lo gastaria.
	if !h.security.disabled || strings.Contains(strings.Join(h.security.calls, ","), "verify") {
		t.Fatalf("llamadas: %v", h.security.calls)
	}

	h.auth.mfa = nil
	if err := h.svc.DisableMFA(ctx, sess, domain.Reauthentication{CurrentPassword: testPass, Code: "123456"}, testIP); !errors.Is(err, domain.ErrMFANotEnabled) {
		t.Fatalf("sin verificacion activa: %v", err)
	}
}

func TestCrearContrasenaDeAplicacionExigeReautenticacion(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	ctx := context.Background()
	in := domain.AppPasswordInput{Name: " Thunderbird ", Access: domain.AppPasswordAccess{IMAP: true, SMTP: true}}

	if _, err := h.svc.CreateAppPassword(ctx, sess, in, domain.Reauthentication{}, testIP); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("sin contrasena: %v", err)
	}
	if h.security.created != nil {
		t.Fatal("sin reautenticacion no se crea")
	}
	created, err := h.svc.CreateAppPassword(ctx, sess, in, domain.Reauthentication{CurrentPassword: testPass}, testIP)
	if err != nil || created.Password == "" || h.security.created.Name != "Thunderbird" {
		t.Fatalf("sin verificacion en dos pasos basta la contrasena: %+v %v", created, err)
	}

	withMFA(h)
	h.security.created = nil
	if _, err := h.svc.CreateAppPassword(ctx, sess, in, domain.Reauthentication{CurrentPassword: testPass}, testIP); !errors.Is(err, domain.ErrMFARequired) {
		t.Fatalf("con verificacion falta el codigo: %v", err)
	}
	if _, err := h.svc.CreateAppPassword(ctx, sess, in, domain.Reauthentication{CurrentPassword: testPass, Code: "000000"}, testIP); !errors.Is(err, domain.ErrInvalidMFACode) {
		t.Fatalf("codigo malo: %v", err)
	}
	if h.security.created != nil {
		t.Fatal("sin codigo valido no se crea")
	}
	if _, err := h.svc.CreateAppPassword(ctx, sess, in, domain.Reauthentication{CurrentPassword: testPass, Code: "123456"}, testIP); err != nil {
		t.Fatal(err)
	}
}

func TestCrearContrasenaDeAplicacionValidaAntesDeReautenticar(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	calls := h.auth.calls
	var verr *domain.ValidationError
	if _, err := h.svc.CreateAppPassword(context.Background(), sess, domain.AppPasswordInput{Name: " ", Access: domain.AppPasswordAccess{IMAP: true}}, domain.Reauthentication{CurrentPassword: testPass}, testIP); !errors.As(err, &verr) || verr.Field != "name" {
		t.Fatalf("%v", err)
	}
	if _, err := h.svc.CreateAppPassword(context.Background(), sess, domain.AppPasswordInput{Name: "Movil"}, domain.Reauthentication{CurrentPassword: testPass}, testIP); !errors.As(err, &verr) || verr.Field != "protocols" {
		t.Fatalf("%v", err)
	}
	if h.auth.calls != calls {
		t.Fatal("un dato invalido no gasta un intento en mail-auth")
	}
	h.security.err = domain.ErrAppPasswordLimit
	if _, err := h.svc.CreateAppPassword(context.Background(), sess, domain.AppPasswordInput{Name: "Movil", Access: domain.AppPasswordAccess{DAV: true}}, domain.Reauthentication{CurrentPassword: testPass}, testIP); !errors.Is(err, domain.ErrAppPasswordLimit) {
		t.Fatalf("tope: %v", err)
	}
}

func TestBorrarContrasenaDeAplicacion(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	if err := h.svc.DeleteAppPassword(context.Background(), sess, "../otro"); !errors.Is(err, domain.ErrAppPasswordNotFound) {
		t.Fatalf("%v", err)
	}
	if len(h.security.calls) != 0 {
		t.Fatal("un id imposible no llega al directorio")
	}
	id := "33333333-3333-4333-8333-333333333333"
	if err := h.svc.DeleteAppPassword(context.Background(), sess, strings.ToUpper(id)); err != nil || h.security.deleted != id {
		t.Fatalf("%v %q", err, h.security.deleted)
	}
	h.security.err = domain.ErrAppPasswordNotFound
	if err := h.svc.DeleteAppPassword(context.Background(), sess, id); !errors.Is(err, domain.ErrAppPasswordNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestSeguridadReuneEstadoYContrasenas(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	withMFA(h)
	h.security.list = domain.AppPasswordList{Items: []domain.AppPassword{{ID: "a", Name: "Movil"}}, Max: 25}
	o, err := h.svc.Security(context.Background(), sess)
	if err != nil || !o.MFA.Enabled || len(o.AppPasswords.Items) != 1 || o.AppPasswords.Max != 25 {
		t.Fatalf("%+v %v", o, err)
	}
	h.security.err = errors.New("caido")
	if _, err := h.svc.Security(context.Background(), sess); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("%v", err)
	}
}

func TestReglasSinContrasenaNoVanReautenticadas(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	in := domain.MailFiltersInput{Forwarding: domain.Forwarding{Enabled: true, Addresses: []string{"fuera@otra.pe"}}, Reauthenticated: true}
	if _, err := h.svc.SetFilters(context.Background(), sess, in, domain.Reauthentication{}, testIP); err != nil {
		t.Fatal(err)
	}
	if h.directory.filtersIn.Reauthenticated {
		t.Fatal("el cliente no puede declararse reautenticado")
	}
}

func TestReglasPasanElRechazoDeReautenticacionConSusDestinos(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.directory.settingsErr = &domain.ReauthRequiredError{Addresses: []string{"fuera@otra.pe"}}
	_, err := h.svc.SetFilters(context.Background(), sess, domain.MailFiltersInput{}, domain.Reauthentication{}, testIP)
	var reauth *domain.ReauthRequiredError
	if !errors.As(err, &reauth) || len(reauth.Addresses) != 1 {
		t.Fatalf("%v", err)
	}
	h.directory.settingsErr = &domain.ExternalForwardingDisabledError{Addresses: []string{"fuera@otra.pe"}}
	_, err = h.svc.SetFilters(context.Background(), sess, domain.MailFiltersInput{}, domain.Reauthentication{CurrentPassword: testPass}, testIP)
	var forbidden *domain.ExternalForwardingDisabledError
	if !errors.As(err, &forbidden) {
		t.Fatalf("%v", err)
	}
}

func TestReglasReautenticadasConContrasenaYCodigo(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	ctx := context.Background()

	if _, err := h.svc.SetFilters(ctx, sess, domain.MailFiltersInput{}, domain.Reauthentication{CurrentPassword: "mala"}, testIP); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("%v", err)
	}
	if h.directory.filtersIn != nil {
		t.Fatal("con la contrasena mala no se guarda nada")
	}
	if _, err := h.svc.SetFilters(ctx, sess, domain.MailFiltersInput{}, domain.Reauthentication{CurrentPassword: testPass}, testIP); err != nil || !h.directory.filtersIn.Reauthenticated {
		t.Fatalf("sin verificacion en dos pasos basta la contrasena: %v", err)
	}

	withMFA(h)
	h.directory.filtersIn = nil
	if _, err := h.svc.SetFilters(ctx, sess, domain.MailFiltersInput{}, domain.Reauthentication{CurrentPassword: testPass}, testIP); !errors.Is(err, domain.ErrMFARequired) {
		t.Fatalf("%v", err)
	}
	if _, err := h.svc.SetFilters(ctx, sess, domain.MailFiltersInput{}, domain.Reauthentication{CurrentPassword: testPass, Code: "000000"}, testIP); !errors.Is(err, domain.ErrInvalidMFACode) {
		t.Fatalf("%v", err)
	}
	if h.directory.filtersIn != nil {
		t.Fatal("sin codigo valido no se guarda nada")
	}
	if _, err := h.svc.SetFilters(ctx, sess, domain.MailFiltersInput{}, domain.Reauthentication{CurrentPassword: testPass, Code: "123456"}, testIP); err != nil || !h.directory.filtersIn.Reauthenticated {
		t.Fatalf("%v", err)
	}
}

func TestCambiarContrasenaConVerificacionExigeCodigo(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	withMFA(h)
	ctx := context.Background()
	if err := h.svc.ChangePassword(ctx, sess, testPass, "nueva-larga-segura", "", testIP); !errors.Is(err, domain.ErrMFARequired) {
		t.Fatalf("%v", err)
	}
	if err := h.svc.ChangePassword(ctx, sess, testPass, "nueva-larga-segura", "000000", testIP); !errors.Is(err, domain.ErrInvalidMFACode) {
		t.Fatalf("%v", err)
	}
	if h.directory.passwordSet != "" {
		t.Fatal("sin codigo valido no cambia")
	}
	if err := h.svc.ChangePassword(ctx, sess, testPass, "nueva-larga-segura", "123456", testIP); err != nil {
		t.Fatal(err)
	}
	if h.directory.passwordSet != "nueva-larga-segura" {
		t.Fatal("cambiada")
	}
}

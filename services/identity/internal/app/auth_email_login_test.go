package app

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/alonsosss/corforce-email/services/identity/internal/ports"
	"github.com/google/uuid"
)

// El inicio de sesion que no indica la empresa la resuelve por la credencial. Estas pruebas
// cubren el caso que la resolucion por correo no podia servir: la misma direccion dada de alta
// en varias empresas.

const sharedEmail = "ana@example.test"

// comparedSince son los hashes contra los que se comparo desde before.
func comparedSince(f *authFixture, before int) []string {
	return f.hasher.compared[before:]
}

// allDecoy dice si todas las comparaciones fueron contra el hash de relleno.
func allDecoy(f *authFixture, compared []string) bool {
	for _, h := range compared {
		if h != f.uc.decoyHash {
			return false
		}
	}
	return true
}

// Dos empresas con el mismo correo y contrasenas distintas: cada persona entra en la suya sin
// indicar la empresa. Antes se resolvia la empresa por el correo con una sola cuenta, asi que
// una de las dos no podia entrar nunca. El intento gasta las mismas comparaciones que cualquier
// otro sin empresa, y no se resuelve ninguna empresa por correo.
func TestSinEmpresaEntraLaCuentaDeLaCredencial(t *testing.T) {
	f := newAuthFixture(t)
	otra := uuid.New()
	primera := f.accountIn(t, f.tenant, sharedEmail, "Primera-2026!", domain.UserStatusActive, authNow.Add(-48*time.Hour))
	segunda := f.accountIn(t, otra, sharedEmail, "Segunda-2026!", domain.UserStatusActive, authNow.Add(-24*time.Hour))

	for _, c := range []struct {
		name     string
		password string
		want     *domain.User
	}{
		{"la cuenta mas antigua", "Primera-2026!", primera},
		{"la cuenta de la otra empresa", "Segunda-2026!", segunda},
	} {
		before := len(f.hasher.compared)
		res, err := f.loginByEmail(sharedEmail, c.password)
		if err != nil || res == nil || res.AccessToken == "" {
			t.Fatalf("%s: res=%v err=%v, se esperaba sesion", c.name, res, err)
		}
		if res.UserID != c.want.ID.String() || res.TenantID != c.want.TenantID.String() {
			t.Errorf("%s: entro %s de la empresa %s, se esperaba %s de %s",
				c.name, res.UserID, res.TenantID, c.want.ID, c.want.TenantID)
		}
		if n := len(comparedSince(f, before)); n != loginCandidateLimit {
			t.Errorf("%s: %d comparaciones, se esperaban %d", c.name, n, loginCandidateLimit)
		}
	}
	if f.users.increments != 0 {
		t.Errorf("%d intentos fallidos contados, se esperaba ninguno", f.users.increments)
	}
	if f.tenants.emailCalls != 0 {
		t.Errorf("el inicio de sesion resolvio %d veces la empresa por el correo", f.tenants.emailCalls)
	}
}

// Una contrasena que no es de ninguna de las cuentas del correo responde como cualquier otra
// mala y cuenta el intento en TODAS las que podian entrar: omitir la empresa no puede ser la
// forma de probar contrasenas sin gastar los intentos de ninguna cuenta.
func TestSinEmpresaLaContrasenaMalaCuentaEnCadaCuentaQuePodiaEntrar(t *testing.T) {
	f := newAuthFixture(t)
	f.accountIn(t, f.tenant, sharedEmail, "Primera-2026!", domain.UserStatusActive, authNow.Add(-48*time.Hour))
	f.accountIn(t, uuid.New(), sharedEmail, "Segunda-2026!", domain.UserStatusActive, authNow.Add(-24*time.Hour))

	if _, err := f.loginByEmail(sharedEmail, "no-es-la-contrasena"); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("err = %v, se esperaba ErrInvalidCredentials", err)
	}
	if f.users.increments != 2 {
		t.Errorf("%d intentos contados, se esperaba uno por cada cuenta", f.users.increments)
	}
	if n := len(f.hasher.compared); n != loginCandidateLimit {
		t.Errorf("%d comparaciones, se esperaban %d", n, loginCandidateLimit)
	}
}

// El bloqueo de la cuenta de una empresa no cierra la de la otra: se entra en la que coincide y
// la bloqueada ni se compara. Si ninguna puede tener sesion, la respuesta es la de una sola.
func TestSinEmpresaUnBloqueoDeUnaEmpresaNoCierraLaOtra(t *testing.T) {
	f := newAuthFixture(t)
	otra := uuid.New()
	bloqueada := f.accountIn(t, f.tenant, sharedEmail, "Primera-2026!", domain.UserStatusLocked, authNow.Add(-48*time.Hour))
	bloqueada.LockedUntil = authAt(time.Minute)
	activa := f.accountIn(t, otra, sharedEmail, "Segunda-2026!", domain.UserStatusActive, authNow.Add(-24*time.Hour))

	res, err := f.loginByEmail(sharedEmail, "Segunda-2026!")
	if err != nil || res == nil || res.TenantID != activa.TenantID.String() {
		t.Fatalf("res=%v err=%v, se esperaba la sesion de la empresa activa", res, err)
	}
	// La contrasena de la cuenta bloqueada no abre nada y no se compara contra su hash.
	before := len(f.hasher.compared)
	if _, err := f.loginByEmail(sharedEmail, "Primera-2026!"); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("con la contrasena de la cuenta bloqueada: %v, se esperaba ErrInvalidCredentials", err)
	}
	if got := comparedSince(f, before); slices.Contains(got, bloqueada.PasswordHash) {
		t.Errorf("se comparo contra el hash de la cuenta bloqueada: %v", got)
	}

	// Con las dos bloqueadas, la respuesta es la misma que da una sola cuenta bloqueada.
	activa.Status, activa.LockedUntil = domain.UserStatusLocked, authAt(time.Minute)
	before = len(f.hasher.compared)
	if _, err := f.loginByEmail(sharedEmail, "Segunda-2026!"); !errors.Is(err, domain.ErrAccountLocked) {
		t.Errorf("con las dos bloqueadas: %v, se esperaba ErrAccountLocked", err)
	}
	if got := comparedSince(f, before); len(got) != loginCandidateLimit || !allDecoy(f, got) {
		t.Errorf("con las dos bloqueadas comparo %v, se esperaban %d de relleno", got, loginCandidateLimit)
	}
}

// Sin empresa, una cuenta inactive o pending sigue respondiendo como un correo sin cuenta, con su
// contrasena buena incluida: su estado solo lo ve quien indica la empresa. Y no impide entrar en
// la otra empresa donde la direccion si tiene una cuenta activa.
func TestSinEmpresaUnaCuentaSinSesionNoSeDistingueDeUnCorreoSinCuenta(t *testing.T) {
	for _, status := range []domain.UserStatus{domain.UserStatusInactive, domain.UserStatusPending} {
		f := newAuthFixture(t)
		f.accountIn(t, f.tenant, sharedEmail, "Primera-2026!", status, authNow.Add(-48*time.Hour))

		sinCuenta := newAuthFixture(t)
		sinCuenta.tenants.missing = true
		esperado, errEsperado := sinCuenta.loginByEmail("nadie@example.test", "Primera-2026!")
		got, err := f.loginByEmail(sharedEmail, "Primera-2026!")
		if got != nil || esperado != nil || err == nil || !errors.Is(err, errEsperado) {
			t.Fatalf("%s: res=%v err=%v; un correo sin cuenta da res=%v err=%v", status, got, err, esperado, errEsperado)
		}
		if n := len(f.hasher.compared); n != loginCandidateLimit || !allDecoy(f, f.hasher.compared) {
			t.Errorf("%s: comparo %v, se esperaban %d de relleno", status, f.hasher.compared, loginCandidateLimit)
		}
		if f.users.increments != 0 {
			t.Errorf("%s: %d intentos contados en una cuenta sin sesion", status, f.users.increments)
		}

		activa := f.accountIn(t, uuid.New(), sharedEmail, "Segunda-2026!", domain.UserStatusActive, authNow.Add(-24*time.Hour))
		if res, err := f.loginByEmail(sharedEmail, "Segunda-2026!"); err != nil || res == nil || res.TenantID != activa.TenantID.String() {
			t.Errorf("%s: res=%v err=%v, se esperaba la sesion de la empresa con la cuenta activa", status, res, err)
		}
	}
}

// El tope de cuentas por correo es el que se paga en comparaciones: la cuenta que queda fuera no
// entra sin indicar la empresa, pero entra indicandola, y el intento que falla cuesta lo mismo
// que cualquier otro.
func TestSinEmpresaElTopeDeCuentasPorCorreo(t *testing.T) {
	f := newAuthFixture(t)
	for i := range loginCandidateLimit {
		f.accountIn(t, uuid.New(), sharedEmail, "Dentro-2026!", domain.UserStatusActive,
			authNow.Add(-time.Duration(loginCandidateLimit-i+1)*time.Hour))
	}
	fuera := f.accountIn(t, f.tenant, sharedEmail, "Fuera-2026!", domain.UserStatusActive, authNow)

	if _, err := f.loginByEmail(sharedEmail, "Fuera-2026!"); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("la cuenta fuera del tope: %v, se esperaba ErrInvalidCredentials", err)
	}
	if n := len(f.hasher.compared); n != loginCandidateLimit {
		t.Errorf("%d comparaciones, se esperaban %d", n, loginCandidateLimit)
	}
	res, err := f.uc.Login(context.Background(), ports.LoginRequest{
		TenantSlug: "acme", Email: sharedEmail, Password: "Fuera-2026!", IPAddress: "203.0.113.7",
	})
	if err != nil || res == nil || res.UserID != fuera.ID.String() {
		t.Fatalf("indicando la empresa: res=%v err=%v, se esperaba la sesion de la cuenta fuera del tope", res, err)
	}
}

// Una cuenta comparte su contador de intentos entre los dos caminos: bloquearla nombrando su
// empresa la deja bloqueada tambien para quien entra solo con el correo, con la contrasena buena.
func TestLaCuentaComparteSuContadorEntreLosDosCaminos(t *testing.T) {
	f := newAuthFixture(t)
	u := f.accountIn(t, f.tenant, sharedEmail, "Primera-2026!", domain.UserStatusActive, authNow.Add(-48*time.Hour))
	for range domain.DefaultPasswordPolicy(f.tenant).MaxFailedAttempts {
		if _, err := f.uc.Login(context.Background(), ports.LoginRequest{
			TenantSlug: "acme", Email: u.Email, Password: "no-es-la-contrasena", IPAddress: "203.0.113.7",
		}); !errors.Is(err, domain.ErrInvalidCredentials) {
			t.Fatalf("intento con la empresa: %v", err)
		}
	}
	if _, err := f.loginByEmail(sharedEmail, "Primera-2026!"); !errors.Is(err, domain.ErrAccountLocked) {
		t.Fatalf("tras el bloqueo, sin empresa: %v, se esperaba ErrAccountLocked", err)
	}
}

// Si no se pueden leer las cuentas del correo no se autentica a nadie y se responde como a un
// correo sin cuenta, gastando las mismas comparaciones: una base que no responde no se distingue
// por el tiempo de un correo que no existe.
func TestSinEmpresaConLaLecturaDeCuentasCaida(t *testing.T) {
	f := newAuthFixture(t)
	f.accountIn(t, f.tenant, sharedEmail, "Primera-2026!", domain.UserStatusActive, authNow.Add(-48*time.Hour))
	f.users.listErr = errors.New("registro no disponible")

	if res, err := f.loginByEmail(sharedEmail, "Primera-2026!"); res != nil || !errors.Is(err, domain.ErrTenantNotFound) {
		t.Fatalf("res=%v err=%v, se esperaba ErrTenantNotFound", res, err)
	}
	if n := len(f.hasher.compared); n != loginCandidateLimit || !allDecoy(f, f.hasher.compared) {
		t.Errorf("comparo %v, se esperaban %d de relleno", f.hasher.compared, loginCandidateLimit)
	}
}

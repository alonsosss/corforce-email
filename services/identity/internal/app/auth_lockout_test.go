package app

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/alonsosss/corforce-email/services/identity/internal/ports"
	"golang.org/x/crypto/bcrypt"
)

// responseClass reduce una respuesta del caso de uso a lo que ve el cliente: el handler da el
// mismo 401 a ErrInvalidCredentials y a ErrTenantNotFound.
func responseClass(res *ports.LoginResponse, err error) string {
	switch {
	case err == nil && res != nil:
		return "200"
	case errors.Is(err, domain.ErrInvalidCredentials), errors.Is(err, domain.ErrTenantNotFound):
		return "401"
	case errors.Is(err, domain.ErrAccountLocked):
		return "403"
	default:
		return fmt.Sprintf("inesperado: %v", err)
	}
}

type loginStep struct {
	advance  time.Duration
	attempts int
}

// lockoutScript recorre el bloqueo con la politica por defecto (5 intentos, 30 minutos).
var lockoutScript = []loginStep{
	// Cinco fallos cuentan y el quinto bloquea; durante el bloqueo no se cuenta nada.
	{0, 7},
	// Vencido el bloqueo, dentro de la ventana, el primer fallo vuelve a bloquear.
	{31 * time.Minute, 2},
	// Un segundo antes de que la ventana, contada desde el final del bloqueo, olvide los fallos.
	{30*time.Minute + domain.FailedLoginWindow - time.Second, 2},
	// Pasada la ventana, se cuenta desde uno.
	{30*time.Minute + domain.FailedLoginWindow + time.Second, 6},
}

var lockoutExpected = strings.Fields("401 401 401 401 401 403 403  401 403  401 403  401 401 401 401 401 403")

// runLockoutScript devuelve la respuesta de cada intento y comprueba que cada uno gasta las
// comparaciones de su camino, sean contra la cuenta o contra el relleno: una con la empresa
// indicada y loginCandidateLimit cuando solo trae el correo.
func runLockoutScript(t *testing.T, f *authFixture, req ports.LoginRequest) []string {
	t.Helper()
	comparaciones := loginCandidateLimit
	if req.TenantSlug != "" {
		comparaciones = 1
	}
	var got []string
	for _, step := range lockoutScript {
		f.clock = f.clock.Add(step.advance)
		for range step.attempts {
			before := len(f.hasher.compared)
			got = append(got, responseClass(f.uc.Login(context.Background(), req)))
			if n := len(f.hasher.compared) - before; n != comparaciones {
				t.Fatalf("un intento comparo %d contrasenas, se esperaban %d", n, comparaciones)
			}
		}
	}
	return got
}

// Un correo sin cuenta cuenta sus fallos como una cuenta real: se bloquea al mismo umbral, con la
// misma respuesta, vuelve a bloquearse al vencer y lo olvida con la misma ventana, por cualquiera
// de los caminos de busqueda. El 403 ACCOUNT_LOCKED ya no confirma que la cuenta exista.
func TestUnCorreoSinCuentaSeBloqueaComoUnaCuenta(t *testing.T) {
	const wrong = "no-es-la-contrasena"
	known := newAuthFixture(t)
	u := known.account(domain.UserStatusActive, nil)
	want := runLockoutScript(t, known, ports.LoginRequest{TenantSlug: "acme", Email: u.Email, Password: wrong})
	if !slices.Equal(want, lockoutExpected) {
		t.Fatalf("cuenta real: %v, se esperaba %v", want, lockoutExpected)
	}

	knownNoSlug := newAuthFixture(t)
	v := knownNoSlug.account(domain.UserStatusActive, nil)
	if got := runLockoutScript(t, knownNoSlug, ports.LoginRequest{Email: v.Email, Password: wrong}); !slices.Equal(got, want) {
		t.Errorf("cuenta real sin empresa: %v, se esperaba %v", got, want)
	}

	cases := []struct {
		name     string
		slug     string
		noTenant bool
	}{
		{"correo sin cuenta en la empresa", "acme", false},
		{"correo que no resuelve empresa", "", true},
		{"slug que no existe", "no-existe", true},
	}
	for _, c := range cases {
		f := newAuthFixture(t)
		f.tenants.missing = c.noTenant
		got := runLockoutScript(t, f, ports.LoginRequest{TenantSlug: c.slug, Email: "nadie@example.test", Password: wrong})
		if !slices.Equal(got, want) {
			t.Errorf("%s: %v, se esperaba %v", c.name, got, want)
		}
		// El contador de un correo sin cuenta nunca pasa por identity.users.
		if f.users.increments != 0 || f.users.lockedTo != nil || len(f.users.users) != 0 {
			t.Errorf("%s: el correo sin cuenta toco las cuentas", c.name)
		}
		for _, compared := range f.hasher.compared {
			if compared != f.uc.decoyHash {
				t.Fatalf("%s: un correo sin cuenta comparo contra otro hash que el de relleno", c.name)
			}
		}
	}
}

// El contador de un correo sin cuenta es el de su ambito, como el de una cuenta es el de la
// cuenta: todo camino que nombra la misma empresa lo comparte, y otro correo, otra grafia o el
// camino que no la nombra tienen el suyo y siguen respondiendo como una contrasena mala. Sin
// empresa, un correo sin cuenta no resuelve ninguna, asi que su ambito es el de "ninguna
// empresa", nunca el de la empresa que nombro otro intento.
func TestElBloqueoDeUnCorreoSinCuentaEsDeSuAmbito(t *testing.T) {
	f := newAuthFixture(t)
	lock := ports.LoginRequest{TenantSlug: "acme", Email: "nadie@example.test", Password: "mala"}
	for range domain.DefaultPasswordPolicy(f.tenant).MaxFailedAttempts {
		_, _ = f.uc.Login(context.Background(), lock)
	}
	cases := []struct {
		name     string
		req      ports.LoginRequest
		noTenant bool
		want     string
	}{
		{"el mismo correo", lock, false, "403"},
		{"otra contrasena", ports.LoginRequest{TenantSlug: "acme", Email: "nadie@example.test", Password: "otra"}, false, "403"},
		{"la misma empresa por otro slug", ports.LoginRequest{TenantSlug: "acme-2", Email: "nadie@example.test", Password: "mala"}, false, "403"},
		{"el mismo correo sin nombrar la empresa", ports.LoginRequest{Email: "nadie@example.test", Password: "mala"}, false, "401"},
		{"otro correo", ports.LoginRequest{TenantSlug: "acme", Email: "otro@example.test", Password: "mala"}, false, "401"},
		{"otra grafia", ports.LoginRequest{TenantSlug: "acme", Email: "Nadie@example.test", Password: "mala"}, false, "401"},
		{"sin empresa y sin registro que resolverla", ports.LoginRequest{Email: "nadie@example.test", Password: "mala"}, true, "401"},
		{"con un slug que no existe", ports.LoginRequest{TenantSlug: "beta", Email: "nadie@example.test", Password: "mala"}, true, "401"},
	}
	for _, c := range cases {
		f.tenants.missing = c.noTenant
		if got := responseClass(f.uc.Login(context.Background(), c.req)); got != c.want {
			t.Errorf("%s: %s, se esperaba %s", c.name, got, c.want)
		}
	}
}

// La clave guarda un resumen: ni el correo ni el ambito en claro. Separa ambitos y correos, sin
// ambiguedad en la frontera entre los dos, y no normaliza, igual que la busqueda.
func TestLaClaveDeUnCorreoSinCuentaNoLoGuarda(t *testing.T) {
	key := unknownSubject(scopeTenant+"acme", "ana@example.test")
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(key) || strings.Contains(key, "ana") {
		t.Fatalf("clave %q", key)
	}
	if key != unknownSubject(scopeTenant+"acme", "ana@example.test") {
		t.Fatal("la clave no es estable")
	}
	for name, other := range map[string]string{
		"otro correo": unknownSubject(scopeTenant+"acme", "eva@example.test"),
		"otro ambito": unknownSubject(scopeSlug+"acme", "ana@example.test"),
		"otra grafia": unknownSubject(scopeTenant+"acme", "Ana@example.test"),
		"frontera":    unknownSubject(scopeTenant+"acmeana", "@example.test"),
	} {
		if other == key {
			t.Errorf("%s da la misma clave", name)
		}
	}
}

// Un inicio correcto con un hash de otro coste lo rehace con el coste del hasher en el mismo
// apunte del inicio. Con el coste vigente, o con la contrasena mala, no se rehace nada.
func TestElInicioCorrectoRehaceElHashDeOtroCoste(t *testing.T) {
	f := newAuthFixture(t)
	old, err := bcrypt.GenerateFromPassword([]byte(authPassword), bcrypt.MinCost+1)
	if err != nil {
		t.Fatal(err)
	}
	u := f.account(domain.UserStatusActive, nil)
	u.PasswordHash = string(old)

	if _, err := f.login(u, "no-es-la-contrasena"); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("contrasena mala: %v", err)
	}
	if len(f.users.logins) != 0 || f.hasher.hashes != 1 {
		t.Fatalf("un fallo apunto %d inicios y calculo %d hashes", len(f.users.logins), f.hasher.hashes)
	}

	if res, err := f.login(u, authPassword); err != nil || res == nil {
		t.Fatalf("contrasena buena: %v", err)
	}
	if len(f.users.logins) != 1 || f.users.logins[0] == nil {
		t.Fatalf("inicio correcto sin rehash: %v", f.users.logins)
	}
	rehash := f.users.logins[0]
	if rehash.Current != string(old) {
		t.Errorf("el rehash no se condiciona al hash comparado")
	}
	if cost, err := bcrypt.Cost([]byte(rehash.Replacement)); err != nil || cost != bcrypt.MinCost {
		t.Errorf("hash nuevo con coste %d (%v), se esperaba %d", cost, err, bcrypt.MinCost)
	}
	if bcrypt.CompareHashAndPassword([]byte(rehash.Replacement), []byte(authPassword)) != nil {
		t.Error("el hash nuevo no corresponde a la contrasena")
	}
	if f.users.users[u.ID].PasswordHash != rehash.Replacement {
		t.Error("el hash nuevo no quedo en la cuenta")
	}

	if _, err := f.login(u, authPassword); err != nil {
		t.Fatal(err)
	}
	if len(f.users.logins) != 2 || f.users.logins[1] != nil || f.hasher.hashes != 2 {
		t.Errorf("con el coste vigente no se rehace: %v, %d hashes", f.users.logins, f.hasher.hashes)
	}
}

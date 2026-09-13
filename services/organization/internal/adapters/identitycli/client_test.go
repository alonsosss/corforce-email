package identitycli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/alonsosss/corforce-email/services/organization/internal/ports"
	"github.com/google/uuid"
)

const secret = "Primera-Clave-2030!"

// fakeIdentity hace de identity: exige el token interno y ningun usuario, y guarda cada
// cuerpo recibido.
type fakeIdentity struct {
	mu      sync.Mutex
	calls   []string
	bodies  []firstUserBody
	failing int
	status  int
	body    string
}

func (f *fakeIdentity) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("X-Gateway-Token") != "tok" || r.Header.Get("X-User-ID") != "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	if r.Method == http.MethodPut {
		var b firstUserBody
		if r.Header.Get("Content-Type") != "application/json" || json.NewDecoder(r.Body).Decode(&b) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.bodies = append(f.bodies, b)
	}
	if f.failing > 0 {
		f.failing--
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if f.status != 0 {
		w.WriteHeader(f.status)
		fmt.Fprint(w, f.body)
		return
	}
	if r.Method == http.MethodPut {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"data":{"created":true}}`)
		return
	}
	fmt.Fprint(w, `{"data":{"users_removed":1}}`)
}

func newClient(t *testing.T, f *fakeIdentity) *Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return New(srv.URL, "tok")
}

func admin() ports.FirstAdmin {
	return ports.FirstAdmin{UserID: uuid.New(), Email: "admin@acme.test", Password: secret, FirstName: "Ana", LastName: "Perez"}
}

func TestElPrimerUsuarioViajaEnElCuerpo(t *testing.T) {
	f := &fakeIdentity{}
	c := newClient(t, f)
	tenant, a := uuid.New(), admin()
	if err := c.CreateFirstUser(context.Background(), tenant, a); err != nil {
		t.Fatalf("alta: %v", err)
	}
	if want := "PUT /internal/identity/tenants/" + tenant.String() + "/first-user"; len(f.calls) != 1 || f.calls[0] != want {
		t.Fatalf("llamadas = %v; want [%s]", f.calls, want)
	}
	want := firstUserBody{UserID: a.UserID.String(), Email: a.Email, Password: secret, FirstName: "Ana", LastName: "Perez"}
	if f.bodies[0] != want {
		t.Fatalf("cuerpo = %+v; want %+v", f.bodies[0], want)
	}
}

// Una caida transitoria se reintenta con el mismo cuerpo: el id del usuario lo eligio la
// saga, asi que repetir es el mismo alta.
func TestElReintentoRepiteElMismoCuerpo(t *testing.T) {
	f := &fakeIdentity{failing: 1}
	c := newClient(t, f)
	if err := c.CreateFirstUser(context.Background(), uuid.New(), admin()); err != nil {
		t.Fatalf("alta tras un 503: %v", err)
	}
	if len(f.bodies) != 2 || f.bodies[0] != f.bodies[1] {
		t.Fatalf("cuerpos = %+v; want dos iguales", f.bodies)
	}
}

func TestLosRechazosDeIdentityNoLlevanLaContrasena(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		check  func(error) bool
	}{
		{"politica", http.StatusUnprocessableEntity,
			`{"error":{"code":"PASSWORD_BREACHED","message":"esa contrasena aparece en filtraciones publicas"}}`,
			func(err error) bool {
				var rejected *domain.AdminRejectedError
				return errors.As(err, &rejected) && rejected.Code == "PASSWORD_BREACHED" && errors.Is(err, domain.ErrAdminRejected)
			}},
		{"conflicto", http.StatusConflict, `{"error":{"code":"FIRST_USER_CONFLICT","message":"x"}}`,
			func(err error) bool { return errors.Is(err, domain.ErrAdminUserConflict) }},
		{"fallo inesperado", http.StatusInternalServerError, `{"error":{"code":"INTERNAL_ERROR","message":"x"}}`,
			func(err error) bool { return err != nil && strings.Contains(err.Error(), "500") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cl := newClient(t, &fakeIdentity{status: c.status, body: c.body})
			err := cl.CreateFirstUser(context.Background(), uuid.New(), admin())
			if !c.check(err) {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("el error lleva la contrasena: %v", err)
			}
		})
	}
}

func TestRetirarLasCuentasDeUnaEmpresa(t *testing.T) {
	f := &fakeIdentity{}
	c := newClient(t, f)
	tenant := uuid.New()
	if err := c.RemoveTenantUsers(context.Background(), tenant); err != nil {
		t.Fatalf("retirar: %v", err)
	}
	if want := "DELETE /internal/identity/tenants/" + tenant.String() + "/users"; f.calls[0] != want {
		t.Fatalf("llamada = %s; want %s", f.calls[0], want)
	}
	f.status, f.body = http.StatusConflict, `{"error":{"code":"TENANT_ACTIVE","message":"x"}}`
	if err := c.RemoveTenantUsers(context.Background(), tenant); err == nil || !strings.Contains(err.Error(), "TENANT_ACTIVE") {
		t.Fatalf("empresa activa: %v", err)
	}
}

package accesscontrolcli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// fakeAccessControl hace de access-control: exige el token interno y ningun usuario, y
// registra cada llamada.
type fakeAccessControl struct {
	mu      sync.Mutex
	calls   []string
	roleID  uuid.UUID
	failing int
	status  int
	body    string
}

func (f *fakeAccessControl) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("X-Gateway-Token") != "tok" || r.Header.Get("X-User-ID") != "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
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
	switch {
	case strings.HasSuffix(r.URL.Path, "/system-role"):
		fmt.Fprintf(w, `{"data":{"role_id":%q,"name":"tenant_admin","created":true,"permissions_granted":5}}`, f.roleID)
	case strings.HasSuffix(r.URL.Path, "/reseed"):
		fmt.Fprint(w, `{"data":{"roles":7,"permissions_granted":0}}`)
	default:
		fmt.Fprint(w, `{"data":{"status":"ok"}}`)
	}
}

func newClient(t *testing.T, f *fakeAccessControl) *Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return New(srv.URL+"/", "tok")
}

func TestClienteHablaElContratoInterno(t *testing.T) {
	f := &fakeAccessControl{roleID: uuid.New()}
	c := newClient(t, f)
	ctx := context.Background()
	tenant, user := uuid.New(), uuid.New()

	roleID, err := c.SeedTenantAdminRole(ctx, tenant)
	if err != nil || roleID != f.roleID {
		t.Fatalf("siembra = %s, %v", roleID, err)
	}
	if roles, err := c.ReseedSystemRoles(ctx); err != nil || roles != 7 {
		t.Fatalf("resiembra = %d, %v", roles, err)
	}
	for _, fn := range []func() error{
		func() error { return c.AssignRole(ctx, tenant, user, roleID) },
		func() error { return c.RevokeRole(ctx, tenant, user, roleID) },
		func() error { return c.RemoveTenantRoles(ctx, tenant) },
	} {
		if err := fn(); err != nil {
			t.Fatal(err)
		}
	}
	base := "/internal/access-control/tenants/" + tenant.String()
	userRole := base + "/users/" + user.String() + "/roles/" + roleID.String()
	want := []string{
		"PUT " + base + "/system-role",
		"POST /internal/access-control/system-role/reseed",
		"PUT " + userRole,
		"DELETE " + userRole,
		"DELETE " + base + "/roles",
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("llamadas = %v\nwant %v", f.calls, want)
	}
}

// Una caida transitoria de access-control se reintenta: todas las operaciones son
// idempotentes, tambien el POST de la resiembra.
func TestClienteReintentaUnaCaidaTransitoria(t *testing.T) {
	f := &fakeAccessControl{roleID: uuid.New(), failing: 1}
	c := newClient(t, f)
	if _, err := c.ReseedSystemRoles(context.Background()); err != nil {
		t.Fatalf("resiembra tras un 503: %v", err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("intentos = %d; want 2", len(f.calls))
	}
}

func TestClienteDescribeElRechazo(t *testing.T) {
	f := &fakeAccessControl{status: http.StatusConflict,
		body: `{"error":{"code":"TENANT_ACTIVE","message":"los roles de una empresa activa no se retiran"}}`}
	c := newClient(t, f)
	err := c.RemoveTenantRoles(context.Background(), uuid.New())
	if err == nil || !strings.Contains(err.Error(), "409 TENANT_ACTIVE") {
		t.Fatalf("err = %v; debe llevar el estado y el codigo", err)
	}
	if _, err := c.SeedTenantAdminRole(context.Background(), uuid.New()); err == nil {
		t.Fatal("un rechazo de la siembra es un error")
	}
}

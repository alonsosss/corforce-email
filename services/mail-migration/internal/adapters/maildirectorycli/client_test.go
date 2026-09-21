package maildirectorycli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type recibido struct{ metodo, ruta, empresa, token string }

func nuevo(t *testing.T, respond func(w http.ResponseWriter, id string)) (*Client, func() []recibido) {
	t.Helper()
	var mu sync.Mutex
	var got []recibido
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, recibido{r.Method, r.URL.EscapedPath(), r.Header.Get("X-Tenant-ID"), r.Header.Get("X-Gateway-Token")})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		respond(w, r.URL.Path[len("/internal/mail-directory/mailboxes/"):])
	}))
	t.Cleanup(srv.Close)
	targets, err := tenantcell.Instances{ByEnv: map[string]map[string]string{"X": {}}}.Targets("X", srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := New(tenantcell.NewCaller("mail-directory", targets, "token-interno", zap.NewNop(), tenantcell.CallerOptions{MaxAttempts: 1}))
	return c, func() []recibido {
		mu.Lock()
		defer mu.Unlock()
		return append([]recibido(nil), got...)
	}
}

func TestLookupPideElBuzonALaCeldaConLaEmpresaYElToken(t *testing.T) {
	c, recibidos := nuevo(t, func(w http.ResponseWriter, id string) {
		_, _ = w.Write([]byte(`{"data":{"id":"` + id + `","username":"ana@acme.test","active":1,"kind":""}}`))
	})
	tenant, mailbox := uuid.New(), uuid.New()
	ref, err := c.Lookup(context.Background(), tenant, mailbox)
	if err != nil {
		t.Fatal(err)
	}
	if ref.ID != mailbox || ref.Username != "ana@acme.test" || !ref.Active {
		t.Fatalf("ref: %+v", ref)
	}
	want := recibido{http.MethodGet, "/internal/mail-directory/mailboxes/" + mailbox.String(), tenant.String(), "token-interno"}
	if got := recibidos(); len(got) != 1 || got[0] != want {
		t.Fatalf("recibido %+v, se esperaba %+v", got, want)
	}
}

func TestLookupDistingueInactivoAusenteYFallo(t *testing.T) {
	inactivo, _ := nuevo(t, func(w http.ResponseWriter, id string) {
		_, _ = w.Write([]byte(`{"data":{"id":"` + id + `","username":"ana@acme.test","active":2}}`))
	})
	if ref, err := inactivo.Lookup(context.Background(), uuid.New(), uuid.New()); err != nil || ref.Active {
		t.Fatalf("un buzon solo de recepcion no acepta sesion: %+v %v", ref, err)
	}

	ausente, _ := nuevo(t, func(w http.ResponseWriter, _ string) { w.WriteHeader(http.StatusNotFound) })
	if _, err := ausente.Lookup(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, domain.ErrMailboxNotFound) {
		t.Fatalf("404: %v", err)
	}

	roto, _ := nuevo(t, func(w http.ResponseWriter, _ string) { w.WriteHeader(http.StatusInternalServerError) })
	if _, err := roto.Lookup(context.Background(), uuid.New(), uuid.New()); err == nil || errors.Is(err, domain.ErrMailboxNotFound) {
		t.Fatalf("un 500 no es un buzon ausente: %v", err)
	}

	ajeno, _ := nuevo(t, func(w http.ResponseWriter, _ string) {
		_, _ = w.Write([]byte(`{"data":{"id":"` + uuid.NewString() + `","username":"otro@acme.test","active":1}}`))
	})
	if _, err := ajeno.Lookup(context.Background(), uuid.New(), uuid.New()); err == nil {
		t.Fatal("la respuesta de otro buzon debe rechazarse")
	}

	basura, _ := nuevo(t, func(w http.ResponseWriter, _ string) { _, _ = w.Write([]byte(`no es json`)) })
	if _, err := basura.Lookup(context.Background(), uuid.New(), uuid.New()); err == nil {
		t.Fatal("una respuesta ilegible debe fallar")
	}

	sinNombre, _ := nuevo(t, func(w http.ResponseWriter, id string) {
		_, _ = w.Write([]byte(`{"data":{"id":"` + id + `","username":"","active":1}}`))
	})
	if _, err := sinNombre.Lookup(context.Background(), uuid.New(), uuid.New()); err == nil {
		t.Fatal("un buzon sin nombre debe rechazarse")
	}
}

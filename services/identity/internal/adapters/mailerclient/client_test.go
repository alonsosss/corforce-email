package mailerclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func enviarYLeerTenant(t *testing.T, platform, usuario uuid.UUID) string {
	t.Helper()
	var visto string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		visto = r.Header.Get("X-Tenant-ID")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := New(srv.URL, "token", platform).Send(context.Background(), usuario, "ana@acme.test", "asunto", "<p>hola</p>"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	return visto
}

func TestElCorreoDelSistemaSaleComoLaEmpresaDePlataforma(t *testing.T) {
	plataforma, usuario := uuid.New(), uuid.New()
	if got := enviarYLeerTenant(t, plataforma, usuario); got != plataforma.String() {
		t.Fatalf("X-Tenant-ID = %s; se esperaba la empresa de plataforma %s", got, plataforma)
	}
}

func TestSinEmpresaDePlataformaSaleComoLaDelUsuario(t *testing.T) {
	usuario := uuid.New()
	if got := enviarYLeerTenant(t, uuid.Nil, usuario); got != usuario.String() {
		t.Fatalf("X-Tenant-ID = %s; se esperaba la del usuario %s", got, usuario)
	}
}

func TestSinURLNoEnvia(t *testing.T) {
	if err := New("", "token", uuid.Nil).Send(context.Background(), uuid.New(), "a@b.test", "s", "b"); err == nil {
		t.Fatal("un cliente sin configurar envio")
	}
}

func TestUnaRespuestaQueNoEs200EsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnprocessableEntity) }))
	defer srv.Close()
	if err := New(srv.URL, "token", uuid.Nil).Send(context.Background(), uuid.New(), "a@b.test", "s", "b"); err == nil {
		t.Fatal("un 422 no se trato como error")
	}
}

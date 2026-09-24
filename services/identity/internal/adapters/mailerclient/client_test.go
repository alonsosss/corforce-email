package mailerclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alonsosss/corforce-email/services/identity/internal/ports"
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
	mail := ports.OutgoingMail{To: "ana@acme.test", Subject: "asunto", HTMLBody: "<p>hola</p>", TextBody: "hola"}
	if err := New(srv.URL, "token", platform).Send(context.Background(), usuario, mail); err != nil {
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
	mail := ports.OutgoingMail{To: "a@b.test", Subject: "s", TextBody: "b"}
	if err := New("", "token", uuid.Nil).Send(context.Background(), uuid.New(), mail); err == nil {
		t.Fatal("un cliente sin configurar envio")
	}
}

func TestUnaRespuestaQueNoEs200EsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnprocessableEntity) }))
	defer srv.Close()
	mail := ports.OutgoingMail{To: "a@b.test", Subject: "s", TextBody: "b"}
	if err := New(srv.URL, "token", uuid.Nil).Send(context.Background(), uuid.New(), mail); err == nil {
		t.Fatal("un 422 no se trato como error")
	}
}

// Con las dos partes, transactional recibe html_body y text_body y el correo sale como
// multipart/alternative; la parte que falta no viaja como cadena vacia.
func TestEnviaLaParteDeTextoJuntoAlHTML(t *testing.T) {
	var cuerpos []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("cuerpo: %v", err)
		}
		cuerpos = append(cuerpos, got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := New(srv.URL, "token", uuid.Nil)
	for _, mail := range []ports.OutgoingMail{
		{To: "ana@acme.test", Subject: "asunto", HTMLBody: "<p>hola</p>", TextBody: "hola"},
		{To: "ana@acme.test", Subject: "asunto", HTMLBody: "<p>hola</p>"},
	} {
		if err := c.Send(context.Background(), uuid.New(), mail); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}
	if got := cuerpos[0]; got["to"] != "ana@acme.test" || got["subject"] != "asunto" || got["html_body"] != "<p>hola</p>" || got["text_body"] != "hola" {
		t.Errorf("cuerpo con las dos partes = %v", got)
	}
	if _, ok := cuerpos[1]["text_body"]; ok {
		t.Errorf("sin texto no se envia text_body: %v", cuerpos[1])
	}
}

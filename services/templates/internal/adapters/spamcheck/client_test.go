package spamcheck

import (
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/templates/internal/ports"
)

var sample = ports.SpamSample{
	Marketing: true, Subject: "Novedades de otono", Text: "Hola Ana",
	HTML: "<p>Hola Ana</p>", UnsubscribeURL: "https://example.com/unsubscribe/verificacion",
}

func server(t *testing.T, status int, body string, seen func(*http.Request, string)) (*Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req checkRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("cuerpo: %v", err)
		}
		if seen != nil {
			seen(r, req.Message)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "token-interno", "no-reply@avisos.example.com")
	if err != nil {
		t.Fatal(err)
	}
	return c, &calls
}

func TestCheckEnviaUnMIMECompletoYLeeElSobre(t *testing.T) {
	const body = `{"data":{"score":1.8,"required":15,"action":"no action","symbols":[{"name":"MIME_GOOD","score":-0.1,"description":"Bien"},{"name":"","score":1}]}}`
	c, _ := server(t, http.StatusOK, body, func(r *http.Request, raw string) {
		if r.URL.Path != checkPath || r.Header.Get("X-Gateway-Token") != "token-interno" || r.Header.Get("X-Tenant-ID") != "" {
			t.Errorf("peticion: %s %v", r.URL.Path, r.Header)
		}
		msg, err := mail.ReadMessage(strings.NewReader(raw))
		if err != nil {
			t.Fatalf("MIME ilegible: %v", err)
		}
		for _, h := range []string{"From", "To", "Subject", "Date", "Message-Id", "Mime-Version", "List-Unsubscribe", "List-Unsubscribe-Post"} {
			if msg.Header.Get(h) == "" {
				t.Errorf("falta la cabecera %s", h)
			}
		}
		if _, err := msg.Header.Date(); err != nil {
			t.Errorf("Date: %v", err)
		}
		mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/alternative" {
			t.Fatalf("Content-Type: %v %v", mediaType, err)
		}
		mr := multipart.NewReader(msg.Body, params["boundary"])
		var types []string
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(p)
			types = append(types, p.Header.Get("Content-Type"))
			if !strings.Contains(string(data), "Hola Ana") {
				t.Errorf("parte %s sin contenido: %q", p.Header.Get("Content-Type"), data)
			}
		}
		if strings.Join(types, ",") != "text/plain; charset=utf-8,text/html; charset=utf-8" {
			t.Errorf("partes: %v", types)
		}
	})
	spam, err := c.Check(t.Context(), sample)
	if err != nil {
		t.Fatal(err)
	}
	if !spam.Available || spam.Score != 1.8 || spam.Required != 15 || spam.Action != "no action" || len(spam.Symbols) != 1 {
		t.Fatalf("resultado: %+v", spam)
	}
}

func TestCheckReutilizaLaPuntuacionDelMismoContenido(t *testing.T) {
	c, calls := server(t, http.StatusOK, `{"data":{"score":2,"required":15,"action":"no action","symbols":[]}}`, nil)
	for range 3 {
		if _, err := c.Check(t.Context(), sample); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("el mismo contenido se consulto %d veces", calls.Load())
	}
	other := sample
	other.HTML = "<p>Otro</p>"
	if _, err := c.Check(t.Context(), other); err != nil || calls.Load() != 2 {
		t.Fatalf("otro contenido: %v %d", err, calls.Load())
	}
	c.now = func() time.Time { return time.Now().Add(cacheTTL + time.Second) }
	if _, err := c.Check(t.Context(), sample); err != nil || calls.Load() != 3 {
		t.Fatalf("caducado: %v %d", err, calls.Load())
	}
}

func TestCheckFallaSinCachearLosErrores(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{http.StatusTooManyRequests, `{"error":{"code":"RATE_LIMITED"}}`},
		{http.StatusServiceUnavailable, `{"error":{"code":"SPAM_CHECK_UNAVAILABLE"}}`},
		{http.StatusOK, `{"data":{"required":15}}`},
		{http.StatusOK, `no es json`},
	} {
		c, calls := server(t, tc.status, tc.body, nil)
		for range 2 {
			if _, err := c.Check(t.Context(), sample); err == nil {
				t.Fatalf("%d %s debe ser un error", tc.status, tc.body)
			}
		}
		if calls.Load() != 2 {
			t.Fatalf("un error no se cachea: %d llamadas", calls.Load())
		}
	}
}

func TestLaCacheEstaAcotada(t *testing.T) {
	c, _ := server(t, http.StatusOK, `{"data":{"score":1,"required":15,"action":"no action"}}`, nil)
	for i := range cacheMax + 10 {
		c.store(strings.Repeat("k", i+1), c.cache[""].spam)
	}
	if len(c.cache) > cacheMax {
		t.Fatalf("la cache crecio a %d", len(c.cache))
	}
}

func TestNewExigeUnRemitenteValido(t *testing.T) {
	for _, from := range []string{"", "no es correo", "Plataforma <no-reply@example.com>"} {
		if _, err := New("http://mail-security:8042", "t", from); err == nil {
			t.Errorf("%q deberia rechazarse", from)
		}
	}
}

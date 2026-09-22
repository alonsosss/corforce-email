package rspamd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
)

func controller(t *testing.T, status *int) (*Learner, *[]string) {
	t.Helper()
	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Password")+" "+r.Header.Get("Content-Type")+" "+string(body))
		w.WriteHeader(*status)
	}))
	t.Cleanup(ts.Close)
	return New(ts.URL, "secreto"), &seen
}

func TestCadaClaseVaASuRutaConLaContrasenaYElMensaje(t *testing.T) {
	status := http.StatusOK
	l, seen := controller(t, &status)
	if err := l.LearnSpam(context.Background(), []byte("m1")); err != nil {
		t.Fatal(err)
	}
	if err := l.LearnHam(context.Background(), []byte("m2")); err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /learnspam secreto message/rfc822 m1", "POST /learnham secreto message/rfc822 m2"}
	if strings.Join(*seen, "|") != strings.Join(want, "|") {
		t.Fatalf("peticiones: %q", *seen)
	}
}

func TestUnMensajeYaAprendidoNoEsUnError(t *testing.T) {
	status := http.StatusAlreadyReported
	l, _ := controller(t, &status)
	if err := l.LearnHam(context.Background(), []byte("m")); err != nil {
		t.Fatal(err)
	}
}

func TestSinContrasenaOConUnaRechazadaEsNotConfiguredYNoSeIntenta(t *testing.T) {
	status := http.StatusForbidden
	l, seen := controller(t, &status)
	if err := l.LearnHam(context.Background(), []byte("m")); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("contrasena rechazada: %v", err)
	}
	empty := New("http://127.0.0.1:1", "")
	if err := empty.LearnHam(context.Background(), []byte("m")); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("sin contrasena: %v", err)
	}
	if len(*seen) != 1 {
		t.Fatalf("solo se intento con contrasena: %q", *seen)
	}
}

func TestUnFalloDelControllerSeDevuelve(t *testing.T) {
	status := http.StatusInternalServerError
	l, _ := controller(t, &status)
	if err := l.LearnSpam(context.Background(), []byte("m")); err == nil || errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("%v", err)
	}
}

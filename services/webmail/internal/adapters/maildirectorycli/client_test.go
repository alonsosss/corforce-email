package maildirectorycli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

func TestSenderIdentitiesLlamadaInterna(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != identitiesPath {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Gateway-Token") != "token-interno" || r.Header.Get("X-Tenant-ID") != "" {
			t.Errorf("cabeceras: %v", r.Header)
		}
		if r.URL.Query().Get("username") != "ana+x@empresa.pe" {
			t.Errorf("username: %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"addresses":["Ventas@Empresa.PE","ñ@empresa.pe","ana+x@empresa.pe","a@b@c"]}}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL+"/", "token-interno")
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.SenderIdentities(context.Background(), "ana+x@empresa.pe")
	if err != nil {
		t.Fatal(err)
	}
	// Las que el webmail no podria poner en el sobre SMTP no se ofrecen.
	if strings.Join(got, ",") != "ventas@empresa.pe,ana+x@empresa.pe" {
		t.Fatalf("got %v", got)
	}
}

func TestSenderIdentitiesFallos(t *testing.T) {
	var calls atomic.Int32
	status, body := http.StatusServiceUnavailable, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SenderIdentities(context.Background(), "ana@empresa.pe"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("503: %v", err)
	}
	if calls.Load() < 2 {
		t.Fatalf("una lectura se reintenta ante un 503: %d", calls.Load())
	}
	status, body = http.StatusOK, "<html>"
	if _, err := c.SenderIdentities(context.Background(), "ana@empresa.pe"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("respuesta ilegible: %v", err)
	}
}

func TestNewExigeURLValida(t *testing.T) {
	for _, bad := range []string{"", "mail-directory:8040", "ftp://mail-directory", "http://usuario:clave@mail-directory:8040"} {
		if _, err := New(bad, "x"); err == nil {
			t.Errorf("%q: se esperaba error", bad)
		}
	}
}

const vacationJSON = `{"data":{"enabled":true,"subject":"Ausente","message":"Vuelvo el lunes.","interval_days":2,"starts_on":"2026-09-21","ends_on":null,"updated_at":"2026-09-20T10:00:00Z","limits":{"subject_max_length":200,"message_max_length":8192,"interval_min_days":1,"interval_max_days":30}}}`

func TestRespuestaAutomaticaSeLeeConLosTopesDelDirectorio(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != vacationPath || r.URL.Query().Get("username") != "ana@empresa.pe" {
			t.Errorf("%s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		if r.Header.Get("X-Gateway-Token") != "token-interno" || r.Header.Get("X-Tenant-ID") != "" {
			t.Errorf("cabeceras: %v", r.Header)
		}
		_, _ = w.Write([]byte(vacationJSON))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "token-interno")
	if err != nil {
		t.Fatal(err)
	}
	v, err := c.Vacation(context.Background(), "ana@empresa.pe")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Enabled || v.Subject != "Ausente" || v.IntervalDays != 2 || v.StartsOn == nil || *v.StartsOn != "2026-09-21" || v.EndsOn != nil ||
		v.UpdatedAt == nil || v.Limits.MessageMaxLength != 8192 || v.Limits.IntervalMaxDays != 30 {
		t.Fatalf("respuesta: %+v", v)
	}
}

func TestRespuestaAutomaticaSeGuardaConElCuerpoYElBuzon(t *testing.T) {
	var got vacationBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Query().Get("username") != "ana+x@empresa.pe" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%s ?%s %v", r.Method, r.URL.RawQuery, r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(vacationJSON))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	start := "2026-09-21"
	if _, err := c.SetVacation(context.Background(), "ana+x@empresa.pe", domain.VacationInput{Enabled: true, Subject: "s", Message: "m", IntervalDays: 4, StartsOn: &start}); err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.Subject != "s" || got.Message != "m" || got.IntervalDays != 4 || got.StartsOn == nil || *got.StartsOn != start || got.EndsOn != nil {
		t.Fatalf("cuerpo enviado: %+v", got)
	}
}

func TestRespuestaAutomaticaFallos(t *testing.T) {
	status, body := http.StatusUnprocessableEntity, `{"error":{"code":"VALIDATION_ERROR","message":"el mensaje supera los 8192"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	_, err := c.SetVacation(context.Background(), "ana@empresa.pe", domain.VacationInput{Message: "x"})
	var verr *domain.ValidationError
	if !errors.As(err, &verr) || verr.Reason != "el mensaje supera los 8192" || errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("422 con motivo: %v", err)
	}
	status, body = http.StatusUnprocessableEntity, `no es json`
	if _, err := c.SetVacation(context.Background(), "ana@empresa.pe", domain.VacationInput{}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("422 sin motivo legible es un fallo del directorio: %v", err)
	}
	status, body = http.StatusNotFound, `{}`
	if _, err := c.Vacation(context.Background(), "nadie@empresa.pe"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("404: %v", err)
	}
	status, body = http.StatusOK, `basura`
	if _, err := c.Vacation(context.Background(), "ana@empresa.pe"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("200 ilegible: %v", err)
	}
}

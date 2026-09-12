package httpclient

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func fastOptions() Options {
	return Options{
		Timeout:          2 * time.Second,
		MaxAttempts:      3,
		BaseBackoff:      time.Millisecond,
		MaxBackoff:       2 * time.Millisecond,
		FailureThreshold: 3,
		Cooldown:         50 * time.Millisecond,
	}
}

// Un GET contra un destino temporalmente caido se reintenta hasta que responde.
func TestGetSeReintentaHasta503Transitorio(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New("destino", fastOptions())
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do error inesperado: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d; want 200", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("intentos = %d; want 3", got)
	}
}

// Un POST NO se repite: repetir "consumir stock" duplicaria el efecto.
func TestPostNoSeReintenta(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := New("destino", fastOptions())
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader([]byte(`{}`)))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do error inesperado: %v", err)
	}
	defer resp.Body.Close()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("intentos = %d; want 1 (un POST no se repite solo)", got)
	}
}

// Un POST declarado idempotente si se repite, y su cuerpo llega intacto en cada intento.
func TestPostIdempotenteSeReintentaConCuerpoIntacto(t *testing.T) {
	var calls int32
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if atomic.AddInt32(&calls, 1) < 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New("destino", fastOptions())
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader([]byte(`{"k":1}`)))
	resp, err := c.Do(Idempotent(req))
	if err != nil {
		t.Fatalf("Do error inesperado: %v", err)
	}
	defer resp.Body.Close()
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("intentos = %d; want 2", got)
	}
	for i, b := range bodies {
		if b != `{"k":1}` {
			t.Errorf("intento %d recibio cuerpo %q; want el original", i+1, b)
		}
	}
}

// Un 500 no se reintenta (puede haber dejado efecto parcial) pero si cuenta como fallo.
func TestError500NoSeReintentaPeroCuenta(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New("destino", fastOptions())
	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("Do error inesperado en intento %d: %v", i, err)
		}
		resp.Body.Close()
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("llamadas = %d; want 3 (un 500 no se repite)", got)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	if _, err := c.Do(req); !errors.Is(err, ErrCircuitOpen) {
		t.Errorf("tras 3 fallos el circuito debe abrirse; err = %v", err)
	}
}

// Un 4xx es el destino respondiendo: ni se reintenta ni abre el circuito.
func TestError4xxNoAbreCircuito(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()

	c := New("destino", fastOptions())
	for i := 0; i < 5; i++ {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("Do error inesperado: %v", err)
		}
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d; want 409", resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// Tras el cooldown el circuito deja pasar, y una respuesta sana lo cierra del todo.
func TestCircuitoSeCierraTrasCooldownYRespuestaSana(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New("destino", fastOptions())
	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		if resp, err := c.Do(req); err == nil {
			resp.Body.Close()
		}
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	if _, err := c.Do(req); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("el circuito debia estar abierto; err = %v", err)
	}

	fail.Store(false)
	time.Sleep(60 * time.Millisecond)

	req, _ = http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("tras el cooldown debe dejar pasar; err = %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d; want 200", resp.StatusCode)
	}
	// Ya cerrado: la siguiente pasa sin esperar otro cooldown.
	req, _ = http.NewRequest(http.MethodGet, srv.URL, nil)
	if resp, err := c.Do(req); err != nil {
		t.Errorf("el circuito debia quedar cerrado; err = %v", err)
	} else {
		resp.Body.Close()
	}
}

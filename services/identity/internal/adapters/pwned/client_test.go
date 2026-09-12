package pwned

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sufijo(password string) string {
	sum := sha1.Sum([]byte(password))
	return strings.ToUpper(hex.EncodeToString(sum[:]))[5:]
}

// El servidor de prueba responde como el real: lineas SUFIJO:RECUENTO, con relleno a cero.
func servidor(t *testing.T, respuesta string, quierePrefijo string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Add-Padding") != "true" {
			t.Errorf("falta Add-Padding: la respuesta sin relleno delata el prefijo por tamano")
		}
		if got := strings.TrimPrefix(r.URL.Path, "/"); got != quierePrefijo {
			t.Errorf("prefijo enviado %q, se esperaba %q: solo deben viajar 5 caracteres", got, quierePrefijo)
		}
		_, _ = w.Write([]byte(respuesta))
	}))
}

func TestFiltradaCuandoElSufijoApareceConRecuento(t *testing.T) {
	pw := "Password1!"
	sum := sha1.Sum([]byte(pw))
	digest := strings.ToUpper(hex.EncodeToString(sum[:]))
	srv := servidor(t, "0000000000000000000000000000000000A:0\r\n"+sufijo(pw)+":1234\r\nFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF:7\r\n", digest[:5])
	defer srv.Close()

	got, err := New(srv.URL, true).IsBreached(context.Background(), pw)
	if err != nil || !got {
		t.Fatalf("filtrada=%v err=%v; se esperaba true sin error", got, err)
	}
}

func TestNoFiltradaCuandoSoloHayRelleno(t *testing.T) {
	pw := "correcto-caballo-bateria-grapa-9"
	sum := sha1.Sum([]byte(pw))
	digest := strings.ToUpper(hex.EncodeToString(sum[:]))
	// El relleno trae nuestro propio sufijo con recuento 0: no cuenta como filtracion.
	srv := servidor(t, sufijo(pw)+":0\r\n0000000000000000000000000000000000B:3\r\n", digest[:5])
	defer srv.Close()

	got, err := New(srv.URL, true).IsBreached(context.Background(), pw)
	if err != nil || got {
		t.Fatalf("filtrada=%v err=%v; se esperaba false sin error", got, err)
	}
}

func TestErrorDelServicioNoSeConfundeConSegura(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	got, err := New(srv.URL, true).IsBreached(context.Background(), "cualquiera")
	if err == nil || got {
		t.Fatalf("filtrada=%v err=%v; un servicio caido debe devolver error, no 'segura'", got, err)
	}
}

func TestApagadoNoConsultaNada(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("apagado no debe llamar al servicio")
	}))
	defer srv.Close()

	got, err := New(srv.URL, false).IsBreached(context.Background(), "Password1!")
	if err != nil || got {
		t.Fatalf("filtrada=%v err=%v; apagado devuelve false sin error", got, err)
	}
}

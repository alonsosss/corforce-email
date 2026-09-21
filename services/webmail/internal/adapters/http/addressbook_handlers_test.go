package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

func TestLaLibretaExigeSesion(t *testing.T) {
	h, _, _, book := newTestHandlerAll(t, nopSender{})
	rec := do(h, http.MethodGet, BasePath+"/address-book?q=ana", nil, nil, nil)
	if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "SESSION_EXPIRED" {
		t.Fatalf("sin sesion: %d %s", rec.Code, rec.Body)
	}
	if book.username != "" {
		t.Fatal("sin sesion no se llama al directorio")
	}
}

func TestLaLibretaSeBuscaConElBuzonDeLaSesionYDevuelveSoloDireccionYNombre(t *testing.T) {
	h, _, _, book := newTestHandlerAll(t, nopSender{})
	cookie := login(t, h)
	book.entries = []domain.AddressBookEntry{{Address: "ana@empresa.pe", DisplayName: "Ana Diaz"}, {Address: "bea@empresa.pe"}}
	rec := do(h, http.MethodGet, BasePath+"/address-book?q=an&limit=5&username=otro@otra.pe&tenant=x", nil, nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if book.username != testUser || book.query != "an" || book.limit != 5 {
		t.Fatalf("el buzon sale de la sesion, no de la URL: %q %q %d", book.username, book.query, book.limit)
	}
	var env struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data) != 2 || env.Data[0]["address"] != "ana@empresa.pe" || env.Data[0]["display_name"] != "Ana Diaz" || len(env.Data[1]) != 2 {
		t.Fatalf("cuerpo: %s", rec.Body)
	}
}

func TestLaLibretaSinResultadosEsUnaListaVaciaYNoNull(t *testing.T) {
	h, _, _, _ := newTestHandlerAll(t, nopSender{})
	rec := do(h, http.MethodGet, BasePath+"/address-book", nil, nil, login(t, h))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"data":[]}` {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
}

func TestLaLibretaTraduceLosErrores(t *testing.T) {
	h, _, _, book := newTestHandlerAll(t, nopSender{})
	cookie := login(t, h)
	get := func(path string) (int, string) {
		rec := do(h, http.MethodGet, BasePath+path, nil, nil, cookie)
		return rec.Code, errorCode(t, rec)
	}
	for _, limit := range []string{"0", "-2", "x"} {
		if code, _ := get("/address-book?limit=" + limit); code != http.StatusBadRequest {
			t.Errorf("limit=%s: %d", limit, code)
		}
	}
	book.err = domain.NewValidationError("q", "el texto supera los 100")
	if code, ec := get("/address-book?q=x"); code != http.StatusUnprocessableEntity || ec != "VALIDATION_ERROR" {
		t.Errorf("texto rechazado: %d %s", code, ec)
	}
	book.err = domain.ErrUnavailable
	if code, ec := get("/address-book?q=x"); code != http.StatusServiceUnavailable || ec != "SERVICE_UNAVAILABLE" {
		t.Errorf("directorio caido: %d %s", code, ec)
	}
	book.err = errors.New("otra cosa")
	if code, _ := get("/address-book?q=x"); code != http.StatusServiceUnavailable {
		t.Errorf("un fallo del directorio no es un 500: %d", code)
	}
}

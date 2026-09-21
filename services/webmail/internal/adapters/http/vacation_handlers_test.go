package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

var vacationOrigin = map[string]string{"Origin": allowedOrigin, "Content-Type": "application/json"}

func TestLaRespuestaAutomaticaExigeSesion(t *testing.T) {
	h, _, vac := newTestHandlerFull(t, nopSender{})
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		rec := do(h, method, BasePath+"/vacation", strings.NewReader(`{"enabled":true,"message":"x"}`), vacationOrigin, nil)
		if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "SESSION_EXPIRED" {
			t.Errorf("%s sin sesion: %d %s", method, rec.Code, rec.Body)
		}
	}
	if vac.username != "" {
		t.Fatal("sin sesion no se llama al directorio")
	}
}

func TestLaRespuestaAutomaticaSeLeeConElBuzonDeLaSesionYLosTopes(t *testing.T) {
	h, _, vac := newTestHandlerFull(t, nopSender{})
	cookie := login(t, h)
	vac.current = domain.Vacation{Enabled: true, Subject: "Ausente", Message: "Vuelvo.", IntervalDays: 2,
		Limits: domain.VacationLimits{SubjectMaxLength: 200, MessageMaxLength: 8192, IntervalMinDays: 1, IntervalMaxDays: 30}}
	rec := do(h, http.MethodGet, BasePath+"/vacation?username=otro@empresa.pe", nil, nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if vac.username != testUser {
		t.Fatalf("el buzon sale de la sesion y no de la URL: %q", vac.username)
	}
	var env struct {
		Data vacationDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if !env.Data.Enabled || env.Data.Message != "Vuelvo." || env.Data.Limits.MessageMaxLength != 8192 || env.Data.Limits.IntervalMaxDays != 30 {
		t.Fatalf("respuesta: %+v", env.Data)
	}
}

func TestGuardarLaRespuestaAutomaticaExigeOrigenYUsaLaSesion(t *testing.T) {
	h, _, vac := newTestHandlerFull(t, nopSender{})
	cookie := login(t, h)
	body := `{"enabled":true,"subject":"Ausente","message":"Hola","interval_days":3,"starts_on":"2026-09-21","ends_on":"2026-09-30"}`

	if rec := do(h, http.MethodPut, BasePath+"/vacation", strings.NewReader(body), map[string]string{"Content-Type": "application/json"}, cookie); rec.Code != http.StatusForbidden {
		t.Fatalf("sin Origin: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, http.MethodPut, BasePath+"/vacation", strings.NewReader(body), map[string]string{"Origin": "https://malo.example", "Content-Type": "application/json"}, cookie); rec.Code != http.StatusForbidden {
		t.Fatalf("Origin ajeno: %d %s", rec.Code, rec.Body)
	}
	if vac.input != nil {
		t.Fatal("una escritura sin Origin permitido no debe llegar al directorio")
	}

	rec := do(h, http.MethodPut, BasePath+"/vacation?username=otro@empresa.pe", strings.NewReader(body), vacationOrigin, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("guardar: %d %s", rec.Code, rec.Body)
	}
	if vac.username != testUser || vac.input == nil || vac.input.Message != "Hola" || vac.input.IntervalDays != 3 ||
		vac.input.StartsOn == nil || *vac.input.StartsOn != "2026-09-21" || vac.input.EndsOn == nil || *vac.input.EndsOn != "2026-09-30" {
		t.Fatalf("directorio: %q %+v", vac.username, vac.input)
	}
	if !strings.Contains(rec.Body.String(), `"enabled":true`) {
		t.Fatalf("respuesta: %s", rec.Body)
	}
}

func TestLaRespuestaAutomaticaTraduceLosErrores(t *testing.T) {
	h, _, vac := newTestHandlerFull(t, nopSender{})
	cookie := login(t, h)
	put := func(body string) (int, string) {
		rec := do(h, http.MethodPut, BasePath+"/vacation", strings.NewReader(body), vacationOrigin, cookie)
		return rec.Code, errorCode(t, rec)
	}
	vac.err = domain.NewValidationError("vacation", "el mensaje supera los 8192")
	if code, ec := put(`{"enabled":true,"message":"x"}`); code != http.StatusUnprocessableEntity || ec != "VALIDATION_ERROR" {
		t.Errorf("texto rechazado por el directorio: %d %s", code, ec)
	}
	vac.err = domain.ErrUnavailable
	if code, ec := put(`{"enabled":true,"message":"x"}`); code != http.StatusServiceUnavailable || ec != "SERVICE_UNAVAILABLE" {
		t.Errorf("directorio caido: %d %s", code, ec)
	}
	vac.err = errors.New("otra cosa")
	if rec := do(h, http.MethodGet, BasePath+"/vacation", nil, nil, cookie); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("un fallo al leer no es un 500: %d", rec.Code)
	}
	vac.err = nil
	if code, _ := put(`no es json`); code != http.StatusBadRequest {
		t.Errorf("JSON roto: %d", code)
	}
	if code, _ := put(`{"enabled":true,"message":"x","script_data":"discard;"}`); code != http.StatusBadRequest {
		t.Errorf("campo desconocido: %d", code)
	}
	if code, _ := put(`{"enabled":true,"message":"` + strings.Repeat("a", 1<<20) + `"}`); code != http.StatusBadRequest {
		t.Errorf("cuerpo enorme: %d", code)
	}
}

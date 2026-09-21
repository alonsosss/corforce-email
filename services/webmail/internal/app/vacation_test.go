package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

func TestLaRespuestaAutomaticaSeLeeConElBuzonDeLaSesion(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	end := "2026-09-30"
	h.directory.vacation = domain.Vacation{Enabled: true, Subject: "Ausente", Message: "Vuelvo el lunes.", IntervalDays: 2, EndsOn: &end,
		Limits: domain.VacationLimits{MessageMaxLength: 8192, IntervalMinDays: 1, IntervalMaxDays: 30}}
	got, err := h.svc.Vacation(context.Background(), sess)
	if err != nil {
		t.Fatal(err)
	}
	if h.directory.vacationFor != testUser {
		t.Fatalf("el buzon sale de la sesion: %q", h.directory.vacationFor)
	}
	if !got.Enabled || got.Message != "Vuelvo el lunes." || got.EndsOn == nil || *got.EndsOn != end || got.Limits.MessageMaxLength != 8192 {
		t.Fatalf("respuesta: %+v", got)
	}
}

func TestGuardarLaRespuestaAutomaticaUsaElBuzonDeLaSesionYPasaLosDatos(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	start := "2026-09-21"
	in := domain.VacationInput{Enabled: true, Subject: "Ausente", Message: "Hola", IntervalDays: 3, StartsOn: &start}
	got, err := h.svc.SetVacation(context.Background(), sess, in)
	if err != nil {
		t.Fatal(err)
	}
	if h.directory.vacationFor != testUser || h.directory.vacationIn == nil || h.directory.vacationIn.Message != "Hola" ||
		h.directory.vacationIn.IntervalDays != 3 || *h.directory.vacationIn.StartsOn != start {
		t.Fatalf("llamada al directorio: %q %+v", h.directory.vacationFor, h.directory.vacationIn)
	}
	if !got.Enabled || got.Subject != "Ausente" {
		t.Fatalf("resultado: %+v", got)
	}
}

func TestUnTextoRechazadoPorElDirectorioLlegaAlUsuarioTalCual(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.directory.vacationErr = domain.NewValidationError("vacation", "el mensaje supera los 8192")
	_, err := h.svc.SetVacation(context.Background(), sess, domain.VacationInput{Enabled: true, Message: "x"})
	var verr *domain.ValidationError
	if !errors.As(err, &verr) || verr.Reason != "el mensaje supera los 8192" {
		t.Fatalf("debe seguir siendo un ValidationError con el motivo del directorio: %v", err)
	}
	if errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("un texto invalido no es una caida del directorio")
	}
}

func TestUnFalloDelDirectorioEsServicioNoDisponible(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.directory.vacationErr = errors.New("conexion rechazada")
	if _, err := h.svc.Vacation(context.Background(), sess); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("leer: %v", err)
	}
	if _, err := h.svc.SetVacation(context.Background(), sess, domain.VacationInput{Message: "x"}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("guardar: %v", err)
	}
}

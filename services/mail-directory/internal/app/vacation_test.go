package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

func TestBuzonSinRespuestaAutomaticaLaVeDesactivadaYVacia(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	m := h.addMailbox(tenant, "ana@acme.test", 0)
	v, err := h.uc.GetMailboxVacation(context.Background(), tenant, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Enabled || v.Message != "" || v.Username != "ana@acme.test" || v.IntervalDays != domain.DefaultVacationIntervalDays {
		t.Fatalf("respuesta por defecto: %+v", v)
	}
}

func TestGuardarLaRespuestaAutomaticaGeneraElScriptYSeLee(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	m := h.addMailbox(tenant, "ana@acme.test", 0)
	start := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	saved, err := h.uc.PutMailboxVacation(context.Background(), tenant, m.ID, PutVacationRequest{
		Enabled: true, Subject: " Ausente ", Message: "Vuelvo el lunes.", IntervalDays: 2, StartsOn: &start,
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Username != "ana@acme.test" || saved.Subject != "Ausente" || saved.TenantID != tenant {
		t.Fatalf("guardada: %+v", saved)
	}
	if !strings.Contains(saved.ScriptData, `:subject "Ausente"`) || !strings.Contains(saved.ScriptData, `"2026-09-21"`) {
		t.Fatalf("script:\n%s", saved.ScriptData)
	}
	got, err := h.uc.GetMailboxVacation(context.Background(), tenant, m.ID)
	if err != nil || !got.Enabled || got.Message != "Vuelvo el lunes." || got.IntervalDays != 2 {
		t.Fatalf("lectura: %+v %v", got, err)
	}
	// Reemplaza, no acumula: una fila por buzon.
	if _, err := h.uc.PutMailboxVacation(context.Background(), tenant, m.ID, PutVacationRequest{Enabled: false, Message: "otro"}); err != nil {
		t.Fatal(err)
	}
	if len(h.vacation.items) != 1 {
		t.Fatalf("filas: %d", len(h.vacation.items))
	}
	off, _ := h.uc.GetMailboxVacation(context.Background(), tenant, m.ID)
	if off.Enabled || off.ScriptData != "" {
		t.Fatalf("desactivada no sirve script: %+v", off)
	}
}

func TestUnaRespuestaInvalidaNoTocaLaBaseNiTomaElCandado(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	m := h.addMailbox(tenant, "ana@acme.test", 0)
	_, err := h.uc.PutMailboxVacation(context.Background(), tenant, m.ID, PutVacationRequest{Enabled: true})
	if !errors.Is(err, domain.ErrVacationMessageRequired) {
		t.Fatalf("error: %v", err)
	}
	if h.vacation.upserts != 0 || h.tx.calls != 0 {
		t.Fatalf("una entrada invalida no debe abrir transaccion: upserts %d, tx %d", h.vacation.upserts, h.tx.calls)
	}
}

func TestLaRespuestaAutomaticaNoCruzaEmpresas(t *testing.T) {
	h := newHarness()
	mine, other := uuid.New(), uuid.New()
	m := h.addMailbox(mine, "ana@acme.test", 0)
	if _, err := h.uc.PutMailboxVacation(context.Background(), other, m.ID, PutVacationRequest{Enabled: true, Message: "x"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("otra empresa escribiendo: %v", err)
	}
	if _, err := h.uc.GetMailboxVacation(context.Background(), other, m.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("otra empresa leyendo: %v", err)
	}
	if h.vacation.upserts != 0 {
		t.Fatal("no debe haberse escrito nada")
	}
}

func TestUnaEmpresaDadaDeBajaNoCambiaSuRespuestaAutomatica(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	m := h.addMailbox(tenant, "ana@acme.test", 0)
	h.retirements.retired[tenant] = time.Now()
	_, err := h.uc.PutMailboxVacation(context.Background(), tenant, m.ID, PutVacationRequest{Enabled: true, Message: "x"})
	if !errors.Is(err, domain.ErrTenantRetired) || h.vacation.upserts != 0 {
		t.Fatalf("empresa de baja: %v, upserts %d", err, h.vacation.upserts)
	}
}

func TestBorrarUnBuzonBorraSuRespuestaAutomatica(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	m := h.addMailbox(tenant, "ana@acme.test", 0)
	if _, err := h.uc.PutMailboxVacation(context.Background(), tenant, m.ID, PutVacationRequest{Enabled: true, Message: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := h.uc.DeleteMailbox(context.Background(), tenant, m.ID); err != nil {
		t.Fatal(err)
	}
	if len(h.vacation.deleted) != 1 || h.vacation.deleted[0] != "ana@acme.test" || len(h.vacation.items) != 0 {
		t.Fatalf("no se borro la respuesta: %v, filas %d", h.vacation.deleted, len(h.vacation.items))
	}
}

func TestElWebmailResuelveElBuzonPorSuNombreEnTodaLaCelda(t *testing.T) {
	h := newHarness()
	tenantA, tenantB := uuid.New(), uuid.New()
	h.addMailbox(tenantA, "ana@acme.test", 0)
	h.addMailbox(tenantB, "luis@otra.test", 0)
	saved, err := h.uc.PutVacationByUsername(context.Background(), " Luis@Otra.TEST ", PutVacationRequest{Enabled: true, Message: "Hola"})
	if err != nil || saved.TenantID != tenantB || saved.Username != "luis@otra.test" {
		t.Fatalf("guardada: %+v %v", saved, err)
	}
	if _, ok := h.vacation.items[vacationKey(tenantA, "ana@acme.test")]; ok {
		t.Fatal("la respuesta de luis no puede caer en la empresa de ana")
	}
	got, err := h.uc.VacationByUsername(context.Background(), "luis@otra.test")
	if err != nil || !got.Enabled || got.Message != "Hola" {
		t.Fatalf("lectura: %+v %v", got, err)
	}
	if _, err := h.uc.VacationByUsername(context.Background(), "nadie@acme.test"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("buzon inexistente: %v", err)
	}
	for _, malo := range []string{"", "sin-arroba", "@x.test"} {
		if _, err := h.uc.PutVacationByUsername(context.Background(), malo, PutVacationRequest{Message: "x"}); err == nil {
			t.Errorf("%q deberia rechazarse", malo)
		}
	}
}

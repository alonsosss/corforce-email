package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

func TestLibretaSoloMuestraLosBuzonesActivosDeLaEmpresaDeQuienPregunta(t *testing.T) {
	h := newHarness()
	acme, otra := uuid.New(), uuid.New()
	h.addMailbox(acme, "ana@acme.test", 0).DisplayName = "Ana Diaz"
	h.addMailbox(acme, "bea@acme.test", 0)
	h.addMailbox(acme, "baja@acme.test", 0).Active = 0
	h.addMailbox(otra, "eva@otra.test", 0)

	got, err := h.uc.SearchDirectoryByUsername(context.Background(), "ana@acme.test", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if h.mailboxes.lastTenant != acme || !h.mailboxes.lastFilter.ActiveOnly {
		t.Fatalf("la consulta debe ir a la empresa del buzon y solo con activos: %v %+v", h.mailboxes.lastTenant, h.mailboxes.lastFilter)
	}
	if len(got) != 2 || got[0].Address != "ana@acme.test" || got[0].DisplayName != "Ana Diaz" || got[1].Address != "bea@acme.test" {
		t.Fatalf("entradas: %+v", got)
	}
	for _, e := range got {
		if e.Address == "eva@otra.test" || e.Address == "baja@acme.test" {
			t.Fatalf("no debe aparecer %s", e.Address)
		}
	}
}

func TestLibretaBuscaPorTextoYAcotaElTope(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.addMailbox(tenant, "ana@acme.test", 0)
	h.addMailbox(tenant, "bea@acme.test", 0)

	got, err := h.uc.SearchDirectoryByUsername(context.Background(), "ana@acme.test", "  BEA ", 5)
	if err != nil || len(got) != 1 || got[0].Address != "bea@acme.test" {
		t.Fatalf("busqueda: %+v %v", got, err)
	}
	if h.mailboxes.lastFilter.Search != "BEA" || h.mailboxes.lastPage.Limit != 5 {
		t.Fatalf("filtro y pagina: %+v %+v", h.mailboxes.lastFilter, h.mailboxes.lastPage)
	}
	for limit, want := range map[int]int{0: DefaultDirectorySearchLimit, -3: DefaultDirectorySearchLimit, 1000: MaxDirectorySearchLimit} {
		if _, err := h.uc.SearchDirectoryByUsername(context.Background(), "ana@acme.test", "", limit); err != nil {
			t.Fatal(err)
		}
		if h.mailboxes.lastPage.Limit != want {
			t.Fatalf("limite %d -> %d, esperado %d", limit, h.mailboxes.lastPage.Limit, want)
		}
	}
}

func TestLibretaRechazaUnBuzonQueNoExisteYUnTextoDemasiadoLargo(t *testing.T) {
	h := newHarness()
	h.addMailbox(uuid.New(), "ana@acme.test", 0)
	if _, err := h.uc.SearchDirectoryByUsername(context.Background(), "nadie@acme.test", "", 0); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("buzon desconocido: %v", err)
	}
	long := make([]rune, domain.MaxSearchLength+1)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := h.uc.SearchDirectoryByUsername(context.Background(), "ana@acme.test", string(long), 0); !errors.Is(err, domain.ErrSearchTooLong) {
		t.Fatalf("texto largo: %v", err)
	}
}

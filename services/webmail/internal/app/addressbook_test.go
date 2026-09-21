package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

func TestLaLibretaSeBuscaConElBuzonDeLaSesion(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.directory.book = []domain.AddressBookEntry{{Address: "bea@empresa.pe", DisplayName: "Beatriz"}}
	got, err := h.svc.SearchAddressBook(context.Background(), sess, "be", 8)
	if err != nil {
		t.Fatal(err)
	}
	if h.directory.bookFor != testUser || h.directory.bookQuery != "be" || h.directory.bookLimit != 8 {
		t.Fatalf("llamada al directorio: %q %q %d", h.directory.bookFor, h.directory.bookQuery, h.directory.bookLimit)
	}
	if len(got) != 1 || got[0].Address != "bea@empresa.pe" || got[0].DisplayName != "Beatriz" {
		t.Fatalf("resultado: %+v", got)
	}
}

func TestUnTextoRechazadoPorLaLibretaLlegaAlUsuarioYUnaCaidaEsUn503(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.directory.bookErr = domain.NewValidationError("q", "el texto es demasiado largo")
	_, err := h.svc.SearchAddressBook(context.Background(), sess, "x", 0)
	var verr *domain.ValidationError
	if !errors.As(err, &verr) || errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("debe seguir siendo un ValidationError: %v", err)
	}
	h.directory.bookErr = errors.New("conexion rechazada")
	if _, err := h.svc.SearchAddressBook(context.Background(), sess, "x", 0); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("un fallo del directorio es domain.ErrUnavailable: %v", err)
	}
}

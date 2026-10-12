package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

func TestFijarYQuitarUnRemitenteEnPrincipal(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	ctx := context.Background()

	if err := h.svc.AddPrimarySender(ctx, sess, "Soporte@Proveedor.example"); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.AddPrimarySender(ctx, sess, "soporte@proveedor.example"); err != nil {
		t.Fatalf("repetirlo no es un error: %v", err)
	}
	list, err := h.svc.PrimarySenders(ctx, sess)
	if err != nil || len(list) != 1 || list[0] != "soporte@proveedor.example" {
		t.Fatalf("listado: %v %v", list, err)
	}
	if err := h.svc.RemovePrimarySender(ctx, sess, "SOPORTE@proveedor.example"); err != nil {
		t.Fatal(err)
	}
	if list, _ := h.svc.PrimarySenders(ctx, sess); len(list) != 0 {
		t.Fatalf("quitar: %v", list)
	}
}

func TestFijarUnRemitenteDevuelveLosErroresDelDirectorioSinEnmascararlos(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	ctx := context.Background()

	var invalid *domain.ValidationError
	if err := h.svc.AddPrimarySender(ctx, sess, "sin-arroba"); !errors.As(err, &invalid) {
		t.Fatalf("direccion invalida: %v", err)
	}
	h.primary.limit = 1
	if err := h.svc.AddPrimarySender(ctx, sess, "a@x.example"); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.AddPrimarySender(ctx, sess, "b@x.example"); !errors.Is(err, domain.ErrPrimarySenderLimit) {
		t.Fatalf("tope: %v", err)
	}
	h.primary.err = errors.New("directorio caido")
	if err := h.svc.AddPrimarySender(ctx, sess, "c@x.example"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("un fallo del directorio es servicio no disponible: %v", err)
	}
}

func TestElListadoDeLaBandejaLlevaLosRemitentesFijadosYSigueSinElDirectorio(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	ctx := context.Background()
	q, _ := domain.NewListQuery(1, 10, "")

	if err := h.svc.AddPrimarySender(ctx, sess, "soporte@proveedor.example"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.ListMessages(ctx, sess, "INBOX", q); err != nil {
		t.Fatal(err)
	}
	if got := h.mb.listQuery.PrimarySenders; len(got) != 1 || got[0] != "soporte@proveedor.example" {
		t.Fatalf("la consulta no lleva los remitentes: %v", got)
	}

	if _, err := h.svc.ListMessages(ctx, sess, "Sent", q); err != nil {
		t.Fatal(err)
	}
	if len(h.mb.listQuery.PrimarySenders) != 0 {
		t.Fatalf("solo la bandeja de entrada se clasifica con ellos: %v", h.mb.listQuery.PrimarySenders)
	}

	h.primary.err = errors.New("directorio caido")
	page, err := h.svc.ListMessages(ctx, sess, "INBOX", q)
	if err != nil || len(h.mb.listQuery.PrimarySenders) != 0 {
		t.Fatalf("sin directorio se lista igual: %+v %v", page, err)
	}
}

func TestLaFichaDelRemitenteRespetaLosRemitentesFijados(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	ctx := context.Background()
	h.mb.insight.source = domain.InsightSource{
		From:    []domain.Address{{Name: "Soporte", Email: "Soporte@Proveedor.example"}},
		Headers: domain.MessageHeaders{domain.HeaderAutoSubmitted: {"auto-generated"}},
	}
	got, err := h.svc.SenderInsight(ctx, sess, "INBOX", 3)
	if err != nil || got.Category != domain.CategoryNotifications {
		t.Fatalf("sin fijar: %+v %v", got, err)
	}
	if err := h.svc.AddPrimarySender(ctx, sess, "soporte@proveedor.example"); err != nil {
		t.Fatal(err)
	}
	got, err = h.svc.SenderInsight(ctx, sess, "INBOX", 3)
	if err != nil || got.Category != domain.CategoryPrimary {
		t.Fatalf("fijado: %+v %v", got, err)
	}
}

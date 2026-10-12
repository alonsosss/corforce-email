package app

import (
	"context"
	"errors"
	"testing"
	"time"

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

	h.svc.primaryCache.drop(sess.Username)
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

func TestLosRemitentesFijadosSeCachean(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	ctx := context.Background()
	q, _ := domain.NewListQuery(1, 10, "")

	for i := 0; i < 5; i++ {
		if _, err := h.svc.ListMessages(ctx, sess, "INBOX", q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.svc.PrimarySenders(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if h.primary.reads != 1 {
		t.Fatalf("cinco listados y una lectura piden una sola vez al directorio: %d", h.primary.reads)
	}

	if err := h.svc.AddPrimarySender(ctx, sess, "soporte@proveedor.example"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.ListMessages(ctx, sess, "INBOX", q); err != nil {
		t.Fatal(err)
	}
	if h.primary.reads != 2 || len(h.mb.listQuery.PrimarySenders) != 1 {
		t.Fatalf("fijar invalida la cache: lecturas=%d remitentes=%v", h.primary.reads, h.mb.listQuery.PrimarySenders)
	}
}

func TestUnDirectorioCaidoNoSeInsisteEnCadaListado(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	ctx := context.Background()
	q, _ := domain.NewListQuery(1, 10, "")
	h.primary.err = errors.New("mail-directory: respondio 429")

	for i := 0; i < 5; i++ {
		if _, err := h.svc.ListMessages(ctx, sess, "INBOX", q); err != nil {
			t.Fatalf("la bandeja se lista igual: %v", err)
		}
	}
	if h.primary.reads != 1 {
		t.Fatalf("un fallo se recuerda unos segundos: %d lecturas", h.primary.reads)
	}
}

func TestLaCacheDeRemitentesFijadosCaducaYEsPorBuzon(t *testing.T) {
	c := &primarySendersCache{}
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	c.put("ana@acme.pe", primarySendersEntry{addresses: []string{"a@x.pe"}, expires: now.Add(time.Minute)})
	if e, ok := c.get("ana@acme.pe", now.Add(59*time.Second)); !ok || len(e.addresses) != 1 {
		t.Fatalf("vigente: %+v %v", e, ok)
	}
	if _, ok := c.get("ana@acme.pe", now.Add(time.Minute)); ok {
		t.Fatal("caducada")
	}
	if _, ok := c.get("luis@acme.pe", now); ok {
		t.Fatal("otro buzon no comparte entrada")
	}
}

func TestSinPestanasLaBandejaNoConsultaLosRemitentesFijados(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	d := h.deps()
	d.Config.InboxCategories = nil
	svc, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	if got := svc.Meta().InboxCategories; len(got) != 0 {
		t.Fatalf("la meta no ofrece pestanas: %v", got)
	}
	q, _ := domain.NewListQuery(1, 10, "")
	if _, err := svc.ListMessages(context.Background(), sess, "INBOX", q); err != nil {
		t.Fatal(err)
	}
	if h.primary.reads != 0 {
		t.Fatalf("sin pestanas no se pide nada al directorio: %d lecturas", h.primary.reads)
	}
}

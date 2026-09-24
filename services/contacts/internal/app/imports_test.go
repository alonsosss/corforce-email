package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

func TestImportacionSaltaInvalidosYRegistraMotivos(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.declare(t, "plan", domain.AttrString, true)
	f.declare(t, "score", domain.AttrNumber, false)

	rows := []ImportRow{
		{Email: "ok1@example.com", Attributes: domain.RawAttributes{"plan": raw(`"pro"`)}},
		{Email: "no-es-email"},
		{Email: "OK1@example.com", Attributes: domain.RawAttributes{"plan": raw(`"pro"`)}},
		{Email: "locale@example.com", Locale: "english", Attributes: domain.RawAttributes{"plan": raw(`"pro"`)}},
		{Email: "tz@example.com", Timezone: "Mars/Base", Attributes: domain.RawAttributes{"plan": raw(`"pro"`)}},
		{Email: "attr@example.com", Attributes: domain.RawAttributes{"plan": raw(`"pro"`), "otro": raw(`1`)}},
		{Email: "tipo@example.com", Attributes: domain.RawAttributes{"plan": raw(`"pro"`), "score": raw(`"alto"`)}},
		{Email: "sinplan@example.com"},
		{Email: "ok2@example.com", FirstName: "Eva", Tags: []string{"Feria"}, Attributes: domain.RawAttributes{"plan": raw(`"free"`), "score": raw(`null`)}},
	}
	imp, err := f.uc.Import(ctx, f.tenant, ImportInput{Rows: rows, CreatedBy: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if imp.Total != 9 || imp.Created != 2 || imp.Updated != 0 || imp.Skipped != 7 {
		t.Fatalf("conteos: %+v", imp)
	}
	lines := make([]string, len(imp.Errors))
	for i, e := range imp.Errors {
		lines[i] = fmt.Sprintf("%d", e.Line)
		if e.Reason == "" {
			t.Fatalf("fila %d sin motivo", e.Line)
		}
		if strings.Contains(e.Reason, "@") {
			t.Fatalf("el motivo no guarda la direccion: %q", e.Reason)
		}
	}
	if strings.Join(lines, ",") != "2,3,4,5,6,7,8" {
		t.Fatalf("lineas con error: %v (%+v)", lines, imp.Errors)
	}
	if !strings.Contains(imp.Errors[1].Reason, "línea 1") {
		t.Fatalf("la repetida dice cual es la original: %q", imp.Errors[1].Reason)
	}
	// Un evento por importacion, ninguno por contacto.
	if f.ev.count("import.completed") != 1 || f.ev.count("contact.created") != 0 || len(f.ev.events) != 1 {
		t.Fatalf("eventos: %v", f.ev.events)
	}
	if len(f.s.imports) != 1 || f.s.imports[0].Status != domain.ImportCompleted {
		t.Fatalf("rastro: %+v", f.s.imports)
	}
	for _, c := range f.s.contacts {
		if c.Source != domain.SourceImport || c.ConsentStatus != domain.ConsentNone {
			t.Fatalf("sin base declarada no hay consentimiento: %+v", c)
		}
	}
}

func TestImportacionNuncaResuscribe(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	baja := f.addContact(t, "baja@example.com", domain.StatusUnsubscribed, domain.ConsentRevoked)
	rebota := f.addContact(t, "rebota@example.com", domain.StatusBounced, domain.ConsentNone)
	queja := f.addContact(t, "queja@example.com", domain.StatusComplained, domain.ConsentNone)
	retiro := f.addContact(t, "retiro@example.com", domain.StatusActive, domain.ConsentRevoked)
	sinConsent := f.addContact(t, "nuevo-consent@example.com", domain.StatusActive, domain.ConsentNone)
	list, err := f.uc.CreateList(ctx, f.tenant, "Feria 2026", "")
	if err != nil {
		t.Fatal(err)
	}

	rows := []ImportRow{
		{Email: "baja@example.com", FirstName: "Bea"},
		{Email: "rebota@example.com"},
		{Email: "queja@example.com"},
		{Email: "retiro@example.com"},
		{Email: "nuevo-consent@example.com"},
		{Email: "fresco@example.com"},
	}
	imp, err := f.uc.Import(ctx, f.tenant, ImportInput{
		Rows: rows, ListID: &list.ID, UpdateExisting: true, GrantConsent: true,
		ConsentBasis: "Formulario de la feria, casilla marcada", CreatedBy: uuid.New(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if imp.Created != 1 || imp.Updated != 5 || imp.Skipped != 0 {
		t.Fatalf("conteos: %+v", imp)
	}
	for _, c := range []*domain.Contact{baja, rebota, queja, retiro} {
		got := f.contact(t, c.ID)
		if got.Status != c.Status || got.ConsentStatus == domain.ConsentGranted {
			t.Fatalf("la importacion re-suscribio a %s: %+v", c.Email, got)
		}
	}
	if f.contact(t, baja.ID).FirstName != "Bea" {
		t.Fatal("update_existing actualiza los datos aunque no conceda")
	}
	if f.contact(t, sinConsent.ID).ConsentStatus != domain.ConsentGranted {
		t.Fatal("un activo sin consentimiento recibe el de la base declarada")
	}
	grants := 0
	for _, cs := range f.s.consents {
		if cs.Status == domain.ConsentGranted {
			grants++
			if cs.Method != domain.MethodImport || cs.Evidence["basis"] != "Formulario de la feria, casilla marcada" || cs.Source != "import:"+imp.ID.String() {
				t.Fatalf("evidencia de importacion: %+v", cs)
			}
		}
	}
	if grants != 2 {
		t.Fatalf("solo el existente activo y el nuevo reciben consentimiento: %d", grants)
	}
	if l, _ := f.uc.GetList(ctx, f.tenant, list.ID); l.MemberCount != 6 {
		t.Fatalf("todos los procesados entran en la lista: %d", l.MemberCount)
	}
}

func TestImportacionSinActualizarNoTocaExistentes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.addContact(t, "ya@example.com", domain.StatusActive, domain.ConsentNone)
	imp, err := f.uc.Import(ctx, f.tenant, ImportInput{
		Rows:         []ImportRow{{Email: "ya@example.com", FirstName: "Otro"}, {Email: "nuevo@example.com"}},
		GrantConsent: true, ConsentBasis: "contrato", CreatedBy: uuid.New(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if imp.Created != 1 || imp.Updated != 0 || imp.Skipped != 1 || len(imp.Errors) != 0 {
		t.Fatalf("conteos: %+v", imp)
	}
	if got := f.contact(t, c.ID); got.FirstName != "" || got.ConsentStatus != domain.ConsentNone {
		t.Fatalf("un existente no se toca sin update_existing: %+v", got)
	}
}

func TestImportacionLimites(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.uc.Import(ctx, f.tenant, ImportInput{}); !errors.Is(err, domain.ErrNoImportRows) {
		t.Fatalf("vacia: %v", err)
	}
	if _, err := f.uc.Import(ctx, f.tenant, ImportInput{Rows: make([]ImportRow, 1001)}); !errors.Is(err, domain.ErrTooManyRows) {
		t.Fatalf("demasiadas: %v", err)
	}
	if _, err := f.uc.Import(ctx, f.tenant, ImportInput{Rows: []ImportRow{{Email: "a@b.co"}}, GrantConsent: true, ConsentBasis: "  "}); !errors.Is(err, domain.ErrConsentBasis) {
		t.Fatalf("sin base legal: %v", err)
	}
	missing := uuid.New()
	if _, err := f.uc.Import(ctx, f.tenant, ImportInput{Rows: []ImportRow{{Email: "a@b.co"}}, ListID: &missing}); !errors.Is(err, domain.ErrListNotFound) {
		t.Fatalf("lista inexistente: %v", err)
	}

	// Mas de 100 filas invalidas: se cuentan todas, se guardan 100.
	rows := make([]ImportRow, 150)
	for i := range rows {
		rows[i] = ImportRow{Email: "mala"}
	}
	imp, err := f.uc.Import(ctx, f.tenant, ImportInput{Rows: rows, CreatedBy: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if imp.Skipped != 150 || len(imp.Errors) != MaxImportErrors {
		t.Fatalf("tope de errores: skipped=%d errores=%d", imp.Skipped, len(imp.Errors))
	}

	// Mas de un lote (500): todos entran.
	many := make([]ImportRow, 999)
	for i := range many {
		many[i] = ImportRow{Email: fmt.Sprintf("c%04d@example.com", i)}
	}
	imp, err = f.uc.Import(ctx, f.tenant, ImportInput{Rows: many, CreatedBy: uuid.New()})
	if err != nil || imp.Created != 999 {
		t.Fatalf("varios lotes: %+v %v", imp, err)
	}
}

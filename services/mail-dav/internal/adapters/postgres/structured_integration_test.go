//go:build integration

package postgres

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
)

// La API estructurada del webmail sobre el repositorio real: la busqueda usa las columnas indexadas (nombre y
// correos, leidas sin el vCard), las apariciones pasan por el descarte por tiempo de la base y lo que se escribe
// por la API se lee como cualquier objeto DAV, con las politicas de fila del rol de servicio.
func TestAPIEstructuradaSobrePostgres(t *testing.T) {
	e := setup(t)
	uc, err := app.New(app.Deps{
		Tenant: apptest.Binder{}, Store: e.repo, Calendars: e.repo, Index: e.repo,
		Config: app.Config{
			Limits: domain.Limits{MaxVCardBytes: 64 << 10, MaxVCardProperties: 200, MaxContactsPerMailbox: maxContacts,
				MaxAddressbooksPerMailbox: maxBooks, MaxChangesRetained: 50, MaxMailboxBytes: 1 << 20, MaxReadBytes: 1 << 20},
			Calendar: domain.CalendarLimits{MaxEventBytes: 64 << 10, MaxEventProperties: 200, MaxEventsPerMailbox: 20,
				MaxCalendarsPerMailbox: 3, MaxRecurrenceWork: 5000, MaxQueryWork: 50000},
			DefaultAddressbookName: "Contactos", DefaultCalendarName: "Calendario",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ana, otro := newPrincipal("ana"), newPrincipal("otro")
	ctx := e.ctx

	carla, err := uc.CreateContact(ctx, ana, domain.ContactFields{Name: "Carla Ruiz", Emails: []domain.TypedValue{{Value: "carla@beta.test", Type: "work"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uc.CreateContact(ctx, ana, domain.ContactFields{Name: "Alberto"}); err != nil {
		t.Fatal(err)
	}
	page, total, err := uc.ContactPage(ctx, ana, "BETA.test", 1, 10)
	if err != nil || total != 1 || len(page) != 1 || page[0].ID != carla.ID || page[0].Fields.Emails[0].Value != "carla@beta.test" {
		t.Fatalf("busqueda por correo: %v %d %+v", err, total, page)
	}
	page, total, _ = uc.ContactPage(ctx, ana, "", 1, 1)
	if total != 2 || len(page) != 1 || page[0].Fields.Name != "Alberto" {
		t.Fatalf("orden y pagina: %d %+v", total, page)
	}
	if _, total, _ := uc.ContactPage(ctx, otro, "", 1, 10); total != 0 {
		t.Fatalf("otro buzon ve %d contactos", total)
	}

	upd := carla.Fields
	upd.Title = "Directora"
	if _, err := uc.UpdateContact(ctx, ana, carla.ID, upd, domain.Precondition{IfMatch: []string{"viejo"}}); !errors.Is(err, domain.ErrPreconditionFailed) {
		t.Fatalf("If-Match ajeno: %v", err)
	}
	got, err := uc.UpdateContact(ctx, ana, carla.ID, upd, domain.Precondition{IfMatch: []string{carla.ETag}})
	if err != nil || got.Fields.Title != "Directora" || got.ETag == carla.ETag {
		t.Fatalf("actualizacion: %v %+v", err, got)
	}

	file := "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:" + carla.ID + "\r\nFN:Carla importada\r\nEND:VCARD\r\n" +
		"BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Sin UID\r\nEND:VCARD\r\n"
	res, err := uc.ImportContacts(ctx, ana, file, 10)
	if err != nil || res.Imported != 1 || res.Updated != 1 || len(res.Skipped) != 0 {
		t.Fatalf("importacion: %v %+v", err, res)
	}
	var exported strings.Builder
	if err := uc.ExportContacts(ctx, ana, func(card string) error { exported.WriteString(card); return nil }); err != nil ||
		strings.Count(exported.String(), "BEGIN:VCARD") != 3 || !strings.Contains(exported.String(), "FN:Carla importada") {
		t.Fatalf("exportacion: %v\n%s", err, exported.String())
	}

	start := time.Date(2026, 10, 5, 14, 0, 0, 0, time.UTC)
	ev, err := uc.CreateEvent(ctx, ana, domain.EventFields{Title: "Comite", Start: start, End: start.Add(time.Hour),
		Recurrence: &domain.Recurrence{Freq: "weekly", Count: 4}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uc.CreateEvent(ctx, ana, domain.EventFields{Title: "Pasado", Start: start.AddDate(-1, 0, 0), End: start.AddDate(-1, 0, 0).Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	occs, err := uc.EventOccurrences(ctx, ana, start.AddDate(0, 0, -1), start.AddDate(0, 0, 60), 100)
	if err != nil || len(occs) != 4 || occs[0].ID != ev.ID || !occs[3].Start.Equal(start.AddDate(0, 0, 21)) {
		t.Fatalf("apariciones: %v %+v", err, occs)
	}
	f := ev.Fields
	f.Location = "Sala 2"
	if got, err := uc.UpdateEvent(ctx, ana, ev.ID, f, domain.Precondition{}); err != nil || got.Fields.Location != "Sala 2" || got.Fields.Recurrence == nil {
		t.Fatalf("actualizar evento: %v %+v", err, got)
	}
	if _, err := uc.EventByID(ctx, otro, ev.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("otro buzon: %v", err)
	}
	if err := uc.DeleteEventByID(ctx, ana, ev.ID, domain.Precondition{}); err != nil {
		t.Fatal(err)
	}
	if occs, _ := uc.EventOccurrences(ctx, ana, start.AddDate(0, 0, -1), start.AddDate(0, 0, 60), 100); len(occs) != 0 {
		t.Fatalf("tras borrar: %+v", occs)
	}
}

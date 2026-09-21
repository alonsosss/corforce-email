package app_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
)

var (
	ana = apptest.Account{Password: "clave-ana", Principal: domain.Principal{TenantID: uuid.New(), MailboxID: uuid.New(), Username: "ana@acme.test"}}
	bea = apptest.Account{Password: "clave-bea", Principal: domain.Principal{TenantID: uuid.New(), MailboxID: uuid.New(), Username: "bea@beta.test"}}
	// cris comparte empresa con ana: el aislamiento es por buzon, no solo por empresa.
	cris = apptest.Account{Password: "clave-cris", Principal: domain.Principal{TenantID: ana.Principal.TenantID, MailboxID: uuid.New(), Username: "cris@acme.test"}}
)

var (
	limits         = domain.Limits{MaxVCardBytes: 2048, MaxVCardProperties: 20, MaxContactsPerMailbox: 3, MaxAddressbooksPerMailbox: 2, MaxChangesRetained: 3, MaxMailboxBytes: 1 << 20, MaxReadBytes: 1 << 20}
	calendarLimits = domain.CalendarLimits{MaxEventBytes: 4096, MaxEventProperties: 60, MaxEventsPerMailbox: 3, MaxCalendarsPerMailbox: 2, MaxRecurrenceWork: 5000, MaxQueryWork: 20000}
	testConfig     = app.Config{Limits: limits, Calendar: calendarLimits, DefaultAddressbookName: "Contactos", DefaultCalendarName: "Calendario"}
)

func newUseCase(t *testing.T) (*app.UseCase, *apptest.Auth, *apptest.Store) {
	t.Helper()
	return newUseCaseWith(t, testConfig)
}

func newUseCaseWith(t *testing.T, cfg app.Config) (*app.UseCase, *apptest.Auth, *apptest.Store) {
	t.Helper()
	auth, store := apptest.NewAuth(ana, bea, cris), apptest.NewStore()
	uc, err := app.New(app.Deps{Auth: auth, Tenant: apptest.Binder{}, Store: store, Calendars: store, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	return uc, auth, store
}

func card(uid, name string) string {
	return fmt.Sprintf("BEGIN:VCARD\r\nVERSION:3.0\r\nUID:%s\r\nFN:%s\r\nEMAIL:%s@acme.test\r\nEND:VCARD\r\n", uid, name, uid)
}

func put(t *testing.T, uc *app.UseCase, p domain.Principal, slug, res, raw string, cond domain.Precondition) (bool, error) {
	t.Helper()
	_, created, err := uc.Put(context.Background(), p, slug, res, raw, cond)
	return created, err
}

func TestNewExigeConfiguracionCompleta(t *testing.T) {
	if _, err := app.New(app.Deps{Config: app.Config{DefaultAddressbookName: "x", DefaultCalendarName: "x"}}); err == nil {
		t.Fatal("unos limites en cero no pueden arrancar")
	}
	if _, err := app.New(app.Deps{Config: app.Config{Limits: limits, DefaultAddressbookName: "x", DefaultCalendarName: "x"}}); err == nil {
		t.Fatal("unos limites de calendario en cero no pueden arrancar")
	}
	if _, err := app.New(app.Deps{Config: app.Config{Limits: limits, Calendar: calendarLimits, DefaultAddressbookName: "  ", DefaultCalendarName: "x"}}); err == nil {
		t.Fatal("sin nombre de libreta por defecto no puede arrancar")
	}
	if _, err := app.New(app.Deps{Config: app.Config{Limits: limits, Calendar: calendarLimits, DefaultAddressbookName: "x", DefaultCalendarName: " "}}); err == nil {
		t.Fatal("sin nombre de calendario por defecto no puede arrancar")
	}
}

func TestAutenticaContraMailAuthEnCadaLlamada(t *testing.T) {
	uc, auth, _ := newUseCase(t)
	ctx := context.Background()
	if _, p, err := uc.Authenticate(ctx, "ANA@acme.test", "clave-ana", "203.0.113.9"); err != nil || p != ana.Principal {
		t.Fatalf("credencial buena: %v %+v", err, p)
	}
	if auth.LastIP != "203.0.113.9" {
		t.Fatalf("la IP del cliente debe llegar a mail-auth: %q", auth.LastIP)
	}
	if _, _, err := uc.Authenticate(ctx, "ana@acme.test", "otra", "203.0.113.9"); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("contrasena mala: %v", err)
	}
	auth.Unavailable = true
	if _, _, err := uc.Authenticate(ctx, "ana@acme.test", "clave-ana", "203.0.113.9"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("mail-auth caido no es una credencial mala: %v", err)
	}
	if auth.Calls != 3 {
		t.Fatalf("cada intento pregunta a mail-auth: %d llamadas", auth.Calls)
	}
}

func TestLaLibretaPorDefectoSeCreaUnaVez(t *testing.T) {
	uc, _, _ := newUseCase(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		books, err := uc.Addressbooks(ctx, ana.Principal)
		if err != nil || len(books) != 1 || books[0].Slug != app.DefaultSlug || books[0].DisplayName != "Contactos" {
			t.Fatalf("pasada %d: %v %+v", i, err, books)
		}
	}
	if books, _ := uc.Addressbooks(ctx, bea.Principal); len(books) != 1 || books[0].ID == uuid.Nil {
		t.Fatalf("cada buzon tiene la suya: %+v", books)
	}
}

func TestLibretasValidacionYLimite(t *testing.T) {
	uc, _, _ := newUseCase(t)
	ctx := context.Background()
	for _, slug := range []string{"", "Mayus", "a/b", "..", "a b"} {
		if _, err := uc.CreateAddressbook(ctx, ana.Principal, slug, "x", ""); !errors.Is(err, domain.ErrInvalidName) {
			t.Errorf("slug %q: %v", slug, err)
		}
	}
	if _, err := uc.CreateAddressbook(ctx, ana.Principal, "largo", strings.Repeat("n", domain.MaxDisplayNameLength+1), ""); !errors.Is(err, domain.ErrInvalidName) {
		t.Errorf("nombre demasiado largo: %v", err)
	}
	b, err := uc.CreateAddressbook(ctx, ana.Principal, "familia", "  ", "")
	if err != nil || b.DisplayName != "familia" {
		t.Fatalf("sin nombre toma el slug: %v %+v", err, b)
	}
	if _, err := uc.CreateAddressbook(ctx, ana.Principal, "familia", "otra", ""); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Errorf("repetida: %v", err)
	}
	if _, err := uc.CreateAddressbook(ctx, ana.Principal, "trabajo", "Trabajo", "d"); err != nil {
		t.Fatal(err)
	}
	if _, err := uc.CreateAddressbook(ctx, ana.Principal, "tercera", "x", ""); !errors.Is(err, domain.ErrAddressbookLimit) {
		t.Errorf("limite de libretas: %v", err)
	}
	if err := uc.DeleteAddressbook(ctx, ana.Principal, "familia"); err != nil {
		t.Fatal(err)
	}
	if err := uc.DeleteAddressbook(ctx, ana.Principal, "familia"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("borrar dos veces: %v", err)
	}
	if _, err := uc.CreateAddressbook(ctx, ana.Principal, "tercera", "x", ""); err != nil {
		t.Errorf("tras borrar una cabe otra: %v", err)
	}
}

func TestGuardarLeerYBorrarUnContacto(t *testing.T) {
	uc, _, _ := newUseCase(t)
	ctx := context.Background()
	if _, err := uc.Addressbooks(ctx, ana.Principal); err != nil {
		t.Fatal(err)
	}
	created, err := put(t, uc, ana.Principal, "contacts", "u1.vcf", card("u1", "Uno"), domain.Precondition{IfNoneMatchAny: true})
	if err != nil || !created {
		t.Fatalf("alta: %v %v", created, err)
	}
	c, err := uc.Contact(ctx, ana.Principal, "contacts", "u1.vcf")
	if err != nil || c.DisplayName != "Uno" || c.ETag != domain.ETagOf(card("u1", "Uno")) {
		t.Fatalf("lectura: %v %+v", err, c)
	}
	if _, err := put(t, uc, ana.Principal, "contacts", "u1.vcf", card("u1", "Uno"), domain.Precondition{IfNoneMatchAny: true}); !errors.Is(err, domain.ErrPreconditionFailed) {
		t.Fatalf("crear encima de uno existente con If-None-Match *: %v", err)
	}
	if created, err = put(t, uc, ana.Principal, "contacts", "u1.vcf", card("u1", "Uno editado"), domain.Precondition{IfMatch: []string{c.ETag}}); err != nil || created {
		t.Fatalf("edicion con If-Match: %v %v", created, err)
	}
	if _, err = put(t, uc, ana.Principal, "contacts", "u1.vcf", card("u1", "Otro"), domain.Precondition{IfMatch: []string{c.ETag}}); !errors.Is(err, domain.ErrPreconditionFailed) {
		t.Fatalf("If-Match con un etag viejo: %v", err)
	}
	if err := uc.Delete(ctx, ana.Principal, "contacts", "u1.vcf", domain.Precondition{IfMatch: []string{c.ETag}}); !errors.Is(err, domain.ErrPreconditionFailed) {
		t.Fatalf("borrar con etag viejo: %v", err)
	}
	if err := uc.Delete(ctx, ana.Principal, "contacts", "u1.vcf", domain.Precondition{}); err != nil {
		t.Fatal(err)
	}
	if _, err := uc.Contact(ctx, ana.Principal, "contacts", "u1.vcf"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("borrado: %v", err)
	}
}

func TestPutRechazaLoInvalidoAntesDeGuardar(t *testing.T) {
	uc, _, store := newUseCase(t)
	ctx := context.Background()
	_, _ = uc.Addressbooks(ctx, ana.Principal)
	var bad *domain.VCardError
	if _, err := put(t, uc, ana.Principal, "contacts", "x.vcf", "basura", domain.Precondition{}); !errors.As(err, &bad) {
		t.Fatalf("un vCard invalido: %v", err)
	}
	big := card("g", strings.Repeat("a", limits.MaxVCardBytes))
	if _, err := put(t, uc, ana.Principal, "contacts", "g.vcf", big, domain.Precondition{}); !errors.As(err, &bad) || !bad.TooLarge {
		t.Fatalf("un vCard enorme: %v", err)
	}
	for _, name := range []string{"../x.vcf", "x.txt", "a/b.vcf"} {
		if _, err := put(t, uc, ana.Principal, "contacts", name, card("n", "N"), domain.Precondition{}); !errors.Is(err, domain.ErrInvalidName) {
			t.Errorf("recurso %q: %v", name, err)
		}
	}
	if _, err := put(t, uc, ana.Principal, "No Valida", "x.vcf", card("n", "N"), domain.Precondition{}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("libreta con nombre invalido: %v", err)
	}
	if _, err := put(t, uc, ana.Principal, "noexiste", "x.vcf", card("n", "N"), domain.Precondition{}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("libreta inexistente: %v", err)
	}
	if _, contacts, _ := store.ListContacts(ctx, ana.Principal, "contacts", domain.ReadOptions{WithData: true}); len(contacts) != 0 {
		t.Fatalf("nada de lo rechazado se guarda: %+v", contacts)
	}
}

func TestLimiteDeContactosPorBuzonYUidUnico(t *testing.T) {
	uc, _, _ := newUseCase(t)
	ctx := context.Background()
	_, _ = uc.Addressbooks(ctx, ana.Principal)
	if _, err := uc.CreateAddressbook(ctx, ana.Principal, "trabajo", "Trabajo", ""); err != nil {
		t.Fatal(err)
	}
	for i, slug := range []string{"contacts", "trabajo", "contacts"} {
		if _, err := put(t, uc, ana.Principal, slug, fmt.Sprintf("c%d.vcf", i), card(fmt.Sprintf("c%d", i), "C"), domain.Precondition{}); err != nil {
			t.Fatalf("contacto %d: %v", i, err)
		}
	}
	if _, err := put(t, uc, ana.Principal, "trabajo", "c9.vcf", card("c9", "C"), domain.Precondition{}); !errors.Is(err, domain.ErrContactLimit) {
		t.Fatalf("el limite cuenta todas las libretas del buzon: %v", err)
	}
	if _, err := put(t, uc, ana.Principal, "contacts", "c0.vcf", card("c0", "Editado"), domain.Precondition{}); err != nil {
		t.Fatalf("editar uno existente no cuenta contra el limite: %v", err)
	}
	if err := uc.Delete(ctx, ana.Principal, "contacts", "c2.vcf", domain.Precondition{}); err != nil {
		t.Fatal(err)
	}
	_, err := put(t, uc, ana.Principal, "contacts", "otro.vcf", card("c0", "Mismo UID"), domain.Precondition{})
	var conflict *domain.UIDConflictError
	if !errors.As(err, &conflict) || conflict.Resource != "c0.vcf" {
		t.Fatalf("el UID no se repite en una libreta: %v", err)
	}
	if _, err := put(t, uc, ana.Principal, "trabajo", "otro.vcf", card("c0", "Mismo UID, otra libreta"), domain.Precondition{}); err != nil {
		t.Fatalf("el mismo UID en otra libreta es valido: %v", err)
	}
}

func TestAislamientoEntreBuzonesYEmpresas(t *testing.T) {
	uc, _, _ := newUseCase(t)
	ctx := context.Background()
	for _, p := range []domain.Principal{ana.Principal, bea.Principal, cris.Principal} {
		_, _ = uc.Addressbooks(ctx, p)
	}
	if _, err := put(t, uc, ana.Principal, "contacts", "secreto.vcf", card("s", "Secreto de Ana"), domain.Precondition{}); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]domain.Principal{"otra empresa": bea.Principal, "misma empresa, otro buzon": cris.Principal} {
		if _, err := uc.Contact(ctx, p, "contacts", "secreto.vcf"); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s lee el contacto de ana: %v", name, err)
		}
		if _, contacts, err := uc.Contacts(ctx, p, "contacts", true); err != nil || len(contacts) != 0 {
			t.Errorf("%s lista contactos de ana: %v %+v", name, err, contacts)
		}
		if got, err := uc.ContactsByName(ctx, p, "contacts", []string{"secreto.vcf"}, true); err != nil || len(got) != 0 {
			t.Errorf("%s pide por nombre: %v %+v", name, err, got)
		}
		if err := uc.Delete(ctx, p, "contacts", "secreto.vcf", domain.Precondition{}); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s borra el contacto de ana: %v", name, err)
		}
		if _, book, err := uc.Query(ctx, p, "contacts", domain.Filter{}, 0); err != nil || len(book) != 0 {
			t.Errorf("%s consulta contactos de ana: %v %+v", name, err, book)
		}
	}
	if _, err := uc.Contact(ctx, ana.Principal, "contacts", "secreto.vcf"); err != nil {
		t.Fatalf("ana sigue viendo el suyo: %v", err)
	}
	// La libreta de un buzon no existe para el otro aunque tenga el mismo nombre.
	if err := uc.DeleteAddressbook(ctx, cris.Principal, "contacts"); err != nil {
		t.Fatal(err)
	}
	if _, err := uc.Contact(ctx, ana.Principal, "contacts", "secreto.vcf"); err != nil {
		t.Fatalf("borrar la libreta de cris no toca la de ana: %v", err)
	}
}

func TestMultigetOmiteLoQueNoExiste(t *testing.T) {
	uc, _, _ := newUseCase(t)
	ctx := context.Background()
	_, _ = uc.Addressbooks(ctx, ana.Principal)
	_, _ = put(t, uc, ana.Principal, "contacts", "a.vcf", card("a", "A"), domain.Precondition{})
	got, err := uc.ContactsByName(ctx, ana.Principal, "contacts", []string{"a.vcf", "no.vcf", "../a.vcf"}, true)
	if err != nil || len(got) != 1 || got[0].ResourceName != "a.vcf" {
		t.Fatalf("multiget: %v %+v", err, got)
	}
	if got, err := uc.ContactsByName(ctx, ana.Principal, "contacts", []string{"../a.vcf"}, true); err != nil || len(got) != 0 {
		t.Fatalf("solo nombres invalidos: %v %+v", err, got)
	}
	if _, err := uc.ContactsByName(ctx, ana.Principal, "noexiste", []string{"a.vcf"}, true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("libreta inexistente: %v", err)
	}
}

func TestConsultaFiltraYRespetaElLimite(t *testing.T) {
	uc, _, store := newUseCase(t)
	ctx := context.Background()
	_, _ = uc.Addressbooks(ctx, ana.Principal)
	for _, n := range []string{"Ana Uno", "Ana Dos", "Beto"} {
		id := strings.ReplaceAll(strings.ToLower(n), " ", "-")
		if _, err := put(t, uc, ana.Principal, "contacts", id+".vcf", card(id, n), domain.Precondition{}); err != nil {
			t.Fatal(err)
		}
	}
	filter := domain.Filter{Props: []domain.PropFilter{{Name: "FN", Matches: []domain.TextMatch{{Text: "ana", Collation: domain.CollationUnicodeCasemap, Type: domain.MatchContains}}}}}
	_, got, err := uc.Query(ctx, ana.Principal, "contacts", filter, 0)
	if err != nil || len(got) != 2 {
		t.Fatalf("filtro: %v %+v", err, got)
	}
	if _, got, _ = uc.Query(ctx, ana.Principal, "contacts", filter, 1); len(got) != 1 {
		t.Fatalf("limite de resultados: %+v", got)
	}
	// Un vCard guardado que ya no se puede leer no rompe la consulta.
	_, _ = store.PutContact(ctx, ana.Principal, "contacts", domain.Contact{ResourceName: "roto.vcf", UID: "roto", VCard: "basura", ETag: domain.ETagOf("basura")}, domain.Precondition{}, domain.WriteLimits{MaxItems: 10, MaxBytes: 1 << 20, MaxChanges: 10})
	if _, got, err = uc.Query(ctx, ana.Principal, "contacts", domain.Filter{}, 0); err != nil || len(got) != 3 {
		t.Fatalf("consulta con un vCard ilegible: %v %d", err, len(got))
	}
}

func TestSincronizacionPorToken(t *testing.T) {
	uc, _, _ := newUseCase(t)
	ctx := context.Background()
	_, _ = uc.Addressbooks(ctx, ana.Principal)

	initial, err := uc.Sync(ctx, ana.Principal, "contacts", "", true)
	if err != nil || len(initial.Changed) != 0 || len(initial.Removed) != 0 || initial.Token == "" {
		t.Fatalf("inicial vacia: %v %+v", err, initial)
	}
	_, _ = put(t, uc, ana.Principal, "contacts", "a.vcf", card("a", "A"), domain.Precondition{})
	_, _ = put(t, uc, ana.Principal, "contacts", "b.vcf", card("b", "B"), domain.Precondition{})
	first, err := uc.Sync(ctx, ana.Principal, "contacts", initial.Token, true)
	if err != nil || len(first.Changed) != 2 || len(first.Removed) != 0 {
		t.Fatalf("diferencia: %v %+v", err, first)
	}
	if again, _ := uc.Sync(ctx, ana.Principal, "contacts", first.Token, true); len(again.Changed)+len(again.Removed) != 0 || again.Token != first.Token {
		t.Fatalf("sin cambios: %+v", again)
	}
	_, _ = put(t, uc, ana.Principal, "contacts", "a.vcf", card("a", "A editado"), domain.Precondition{})
	_ = uc.Delete(ctx, ana.Principal, "contacts", "b.vcf", domain.Precondition{})
	delta, err := uc.Sync(ctx, ana.Principal, "contacts", first.Token, true)
	if err != nil || len(delta.Changed) != 1 || delta.Changed[0].DisplayName != "A editado" || len(delta.Removed) != 1 || delta.Removed[0] != "b.vcf" {
		t.Fatalf("edicion y borrado: %v %+v", err, delta)
	}
	full, _ := uc.Sync(ctx, ana.Principal, "contacts", "", true)
	if len(full.Changed) != 1 || len(full.Removed) != 0 || full.Token != delta.Token {
		t.Fatalf("la inicial no lista bajas: %+v", full)
	}
}

func TestSincronizacionRechazaTokensQueNoSePuedenResolver(t *testing.T) {
	uc, _, _ := newUseCase(t)
	ctx := context.Background()
	_, _ = uc.Addressbooks(ctx, ana.Principal)
	start, _ := uc.Sync(ctx, ana.Principal, "contacts", "", true)
	// MaxChangesRetained es 3: tras cinco cambios el token inicial ya se podo.
	for i := 0; i < 5; i++ {
		_, _ = put(t, uc, ana.Principal, "contacts", fmt.Sprintf("p%d.vcf", i%3), card(fmt.Sprintf("p%d", i%3), fmt.Sprint("v", i)), domain.Precondition{})
	}
	if _, err := uc.Sync(ctx, ana.Principal, "contacts", start.Token, true); !errors.Is(err, domain.ErrInvalidSyncToken) {
		t.Fatalf("token podado: %v", err)
	}
	recent, _ := uc.Sync(ctx, ana.Principal, "contacts", "", true)
	future := domain.SyncToken(uuid.New(), 1)
	for name, token := range map[string]string{"basura": "xyz", "de otra libreta": future, "del futuro": strings.Replace(recent.Token, ":"+strings.Split(recent.Token, ":")[4], ":999", 1)} {
		if _, err := uc.Sync(ctx, ana.Principal, "contacts", token, true); !errors.Is(err, domain.ErrInvalidSyncToken) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Una libreta borrada y creada de nuevo con el mismo nombre no acepta el token de la anterior.
	if err := uc.DeleteAddressbook(ctx, ana.Principal, "contacts"); err != nil {
		t.Fatal(err)
	}
	if _, err := uc.CreateAddressbook(ctx, ana.Principal, "contacts", "Nueva", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := uc.Sync(ctx, ana.Principal, "contacts", recent.Token, true); !errors.Is(err, domain.ErrInvalidSyncToken) {
		t.Fatalf("token de la libreta anterior: %v", err)
	}
}

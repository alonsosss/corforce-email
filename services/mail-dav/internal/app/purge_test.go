package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
)

func seedBook(t *testing.T, uc *app.UseCase, p domain.Principal, slug, uid string) {
	t.Helper()
	if _, err := uc.CreateAddressbook(context.Background(), p, slug, slug, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := put(t, uc, p, slug, uid+".vcf", card(uid, uid), domain.Precondition{}); err != nil {
		t.Fatal(err)
	}
}

func booksOf(t *testing.T, uc *app.UseCase, p domain.Principal) []domain.Addressbook {
	t.Helper()
	books, err := uc.Addressbooks(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return books
}

func TestPurgeBorraLibretasYContactosDelBuzon(t *testing.T) {
	uc, _, _ := newUseCase(t)
	ctx := context.Background()
	seedBook(t, uc, ana.Principal, "personal", "a1")
	seedBook(t, uc, ana.Principal, "trabajo", "a2")

	removed, err := uc.PurgeMailbox(ctx, ana.Principal.TenantID, ana.Principal.MailboxID)
	if err != nil || removed != 2 {
		t.Fatalf("PurgeMailbox: %d %v", removed, err)
	}
	if _, _, err := uc.Contacts(ctx, ana.Principal, "personal"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("la libreta borrada no existe: %v", err)
	}
	// Lo unico que queda es la libreta por defecto que el buzon recibiria de nuevo, vacia.
	books := booksOf(t, uc, ana.Principal)
	if len(books) != 1 || books[0].Slug != app.DefaultSlug || books[0].SyncSeq != 0 {
		t.Fatalf("estado tras el borrado: %+v", books)
	}
}

func TestPurgeNoTocaOtroBuzonDeLaMismaEmpresaNiOtraEmpresa(t *testing.T) {
	uc, _, _ := newUseCase(t)
	seedBook(t, uc, ana.Principal, "personal", "a1")
	seedBook(t, uc, cris.Principal, "personal", "c1")
	seedBook(t, uc, bea.Principal, "personal", "b1")

	if removed, err := uc.PurgeMailbox(context.Background(), ana.Principal.TenantID, ana.Principal.MailboxID); err != nil || removed != 1 {
		t.Fatalf("PurgeMailbox: %d %v", removed, err)
	}
	for name, p := range map[string]domain.Principal{"otro buzon de la empresa": cris.Principal, "otra empresa": bea.Principal} {
		if _, contacts, err := uc.Contacts(context.Background(), p, "personal"); err != nil || len(contacts) != 1 {
			t.Errorf("%s perdio sus contactos: %v %d", name, err, len(contacts))
		}
	}
}

// El mismo id de buzon con la empresa equivocada tampoco borra: el par (empresa, buzon) es la clave.
func TestPurgeExigeQueLaEmpresaSeaLaDelBuzon(t *testing.T) {
	uc, _, _ := newUseCase(t)
	seedBook(t, uc, ana.Principal, "personal", "a1")

	if removed, err := uc.PurgeMailbox(context.Background(), bea.Principal.TenantID, ana.Principal.MailboxID); err != nil || removed != 0 {
		t.Fatalf("PurgeMailbox con otra empresa: %d %v", removed, err)
	}
	if _, contacts, err := uc.Contacts(context.Background(), ana.Principal, "personal"); err != nil || len(contacts) != 1 {
		t.Fatalf("los contactos de ana siguen: %v %d", err, len(contacts))
	}
}

// Un buzon recreado con el mismo nombre tiene otro id: el evento del anterior no se lleva sus contactos.
func TestPurgeIdentificaPorIdNoPorNombre(t *testing.T) {
	uc, _, _ := newUseCase(t)
	recreated := domain.Principal{TenantID: ana.Principal.TenantID, MailboxID: uuid.New(), Username: ana.Principal.Username}
	seedBook(t, uc, ana.Principal, "personal", "a1")
	seedBook(t, uc, recreated, "personal", "n1")

	if removed, err := uc.PurgeMailbox(context.Background(), ana.Principal.TenantID, ana.Principal.MailboxID); err != nil || removed != 1 {
		t.Fatalf("PurgeMailbox: %d %v", removed, err)
	}
	if _, contacts, err := uc.Contacts(context.Background(), recreated, "personal"); err != nil || len(contacts) != 1 {
		t.Fatalf("el buzon recreado perdio sus contactos: %v %d", err, len(contacts))
	}
}

func TestPurgeEsIdempotenteYUnBuzonSinDatosNoEsUnError(t *testing.T) {
	uc, _, _ := newUseCase(t)
	seedBook(t, uc, ana.Principal, "personal", "a1")
	for i, want := range []int{1, 0, 0} {
		removed, err := uc.PurgeMailbox(context.Background(), ana.Principal.TenantID, ana.Principal.MailboxID)
		if err != nil || removed != want {
			t.Fatalf("llamada %d: %d %v", i+1, removed, err)
		}
	}
	if removed, err := uc.PurgeMailbox(context.Background(), ana.Principal.TenantID, uuid.New()); err != nil || removed != 0 {
		t.Fatalf("buzon inexistente: %d %v", removed, err)
	}
}

func TestPurgeRechazaIdentificadoresNulos(t *testing.T) {
	uc, _, _ := newUseCase(t)
	seedBook(t, uc, ana.Principal, "personal", "a1")
	for _, c := range []struct{ tenant, mailbox uuid.UUID }{{uuid.Nil, ana.Principal.MailboxID}, {ana.Principal.TenantID, uuid.Nil}} {
		if _, err := uc.PurgeMailbox(context.Background(), c.tenant, c.mailbox); !errors.Is(err, domain.ErrInvalidMailbox) {
			t.Errorf("%v: %v", c, err)
		}
	}
	if _, contacts, err := uc.Contacts(context.Background(), ana.Principal, "personal"); err != nil || len(contacts) != 1 {
		t.Fatalf("un identificador nulo no debe tocar nada: %v %d", err, len(contacts))
	}
}

func TestPurgePropagaElFalloDeLaBaseDeLaEmpresa(t *testing.T) {
	uc, err := app.New(app.Deps{
		Auth: apptest.NewAuth(ana), Tenant: apptest.Binder{Err: domain.ErrTenantUnknown}, Store: apptest.NewStore(),
		Config: app.Config{Limits: limits, DefaultAddressbookName: "Contactos"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uc.PurgeMailbox(context.Background(), ana.Principal.TenantID, ana.Principal.MailboxID); !errors.Is(err, domain.ErrTenantUnknown) {
		t.Fatalf("empresa desconocida: %v", err)
	}
}

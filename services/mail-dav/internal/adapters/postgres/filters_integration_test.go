//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
)

// Como dueno de las tablas (exento de las politicas, el caso de desarrollo sin credencial propia) el
// aislamiento lo dan solo los filtros de cada consulta: aqui se comprueba que no dependen de la politica.
func TestLosFiltrosDeLasConsultasAislanSinLaPolitica(t *testing.T) {
	e := setup(t)
	ana := newPrincipal("ana")
	cris := sameTenant(ana, "cris")
	bea := newPrincipal("bea")
	e.cleanup(t, ana, cris, bea)
	for _, p := range []domain.Principal{ana, cris, bea} {
		e.book(t, p, "contacts")
	}
	e.put(t, ana, "contacts", contact(t, "secreto.vcf", "secreto", "Secreto de Ana"), domain.Precondition{})

	asOwner := db.WithPool(context.Background(), e.owner)
	var visible int
	if err := e.owner.QueryRow(asOwner, `SELECT count(*) FROM mail_dav.contacts WHERE mailbox_id = $1`, ana.MailboxID).Scan(&visible); err != nil || visible != 1 {
		t.Fatalf("el dueno ve el contacto de Ana (no hay politica que lo limite): %d %v", visible, err)
	}
	for name, other := range map[string]domain.Principal{"misma empresa": cris, "otra empresa": bea} {
		if _, err := e.repo.GetContact(asOwner, other, "contacts", "secreto.vcf"); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s lee: %v", name, err)
		}
		if _, contacts, err := e.repo.ListContacts(asOwner, other, "contacts", withData); err != nil || len(contacts) != 0 {
			t.Errorf("%s lista: %v %+v", name, err, contacts)
		}
		if got, err := e.repo.GetContacts(asOwner, other, "contacts", []string{"secreto.vcf"}, withData); err != nil || len(got) != 0 {
			t.Errorf("%s pide por nombre: %v %+v", name, err, got)
		}
		if err := e.repo.DeleteContact(asOwner, other, "contacts", "secreto.vcf", domain.Precondition{}, maxChanges); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s borra: %v", name, err)
		}
		own := contact(t, "secreto.vcf", "otro", "De otro")
		own.TenantID, own.MailboxID = other.TenantID, other.MailboxID
		if _, err := e.repo.PutContact(asOwner, other, "contacts", own, domain.Precondition{}, writeLimits(maxContacts, maxChanges)); err != nil {
			t.Errorf("%s escribe en SU libreta, con el mismo nombre de recurso: %v", name, err)
		}
		if books, err := e.repo.ListAddressbooks(asOwner, other); err != nil || len(books) != 1 || books[0].MailboxID != other.MailboxID {
			t.Errorf("%s ve libretas ajenas: %v %+v", name, err, books)
		}
	}
	got, err := e.repo.GetContact(asOwner, ana, "contacts", "secreto.vcf")
	if err != nil || got.DisplayName != "Secreto de Ana" {
		t.Fatalf("lo de Ana no se toco: %v %+v", err, got)
	}
	if err := e.repo.DeleteAddressbook(asOwner, cris, "contacts"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.repo.GetContact(asOwner, ana, "contacts", "secreto.vcf"); err != nil {
		t.Fatalf("borrar la libreta de otro buzon con el mismo nombre toco la de Ana: %v", err)
	}
}

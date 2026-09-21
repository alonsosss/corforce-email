//go:build integration

package postgres

// Borrado de los datos de un buzon (mail.mailbox.deleted) contra Postgres real. Se corre dos veces: con el
// rol de servicio, sujeto a las politicas de fila, y como dueno de las tablas, exento de ellas, para que los
// filtros por empresa y por buzon de la consulta se prueben por si solos y no por la politica que los repite.

import (
	"context"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
)

// storedRows cuenta lo que la base guarda de un buzon, leido como dueno de las tablas.
func (e *env) storedRows(t *testing.T, p domain.Principal) int {
	t.Helper()
	var n int
	err := e.owner.QueryRow(context.Background(),
		`SELECT (SELECT count(*) FROM mail_dav.addressbooks WHERE mailbox_id = $1)
		      + (SELECT count(*) FROM mail_dav.contacts WHERE mailbox_id = $1)
		      + (SELECT count(*) FROM mail_dav.collection_changes WHERE mailbox_id = $1)`, p.MailboxID).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *env) seedMailbox(t *testing.T, p domain.Principal, uid string) {
	t.Helper()
	e.book(t, p, "contacts")
	e.book(t, p, "familia")
	e.put(t, p, "contacts", contact(t, uid+".vcf", uid, "Contacto "+uid), domain.Precondition{})
	e.put(t, p, "familia", contact(t, uid+"-f.vcf", uid+"-f", "Familiar "+uid), domain.Precondition{})
}

func TestBorrarLosDatosDeUnBuzonSoloTocaLosSuyos(t *testing.T) {
	e := setup(t)
	contexts := map[string]context.Context{
		"rol de servicio con politicas de fila": e.ctx,
		"dueno de las tablas, solo los filtros": db.WithPool(context.Background(), e.owner),
	}
	for name, ctx := range contexts {
		t.Run(name, func(t *testing.T) {
			ana := newPrincipal("ana")
			cris := sameTenant(ana, "cris")
			bea := newPrincipal("bea")
			recreated := domain.Principal{TenantID: ana.TenantID, MailboxID: uuid.New(), Username: ana.Username}
			all := []domain.Principal{ana, cris, bea, recreated}
			e.cleanup(t, all...)
			for i, p := range all {
				e.seedMailbox(t, p, string(rune('a'+i)))
			}
			kept := map[string]int{}
			for _, p := range all[1:] {
				kept[p.Username+p.MailboxID.String()] = e.storedRows(t, p)
			}
			if e.storedRows(t, ana) == 0 {
				t.Fatal("la siembra no dejo datos de Ana")
			}

			// La empresa equivocada con el id de un buzon ajeno no borra nada: el par es la clave.
			if n, err := e.repo.DeleteMailboxData(ctx, domain.Principal{TenantID: bea.TenantID, MailboxID: cris.MailboxID}); err != nil || n != 0 {
				t.Fatalf("empresa distinta: %d %v", n, err)
			}
			if got := e.storedRows(t, cris); got != kept[cris.Username+cris.MailboxID.String()] {
				t.Fatalf("Cris perdio datos por un borrado de otra empresa: %d", got)
			}

			n, err := e.repo.DeleteMailboxData(ctx, ana)
			if err != nil || n != 2 {
				t.Fatalf("DeleteMailboxData: %d %v", n, err)
			}
			if got := e.storedRows(t, ana); got != 0 {
				t.Fatalf("quedan %d filas de Ana entre libretas, contactos y cambios", got)
			}
			for name, p := range map[string]domain.Principal{"otro buzon de la empresa": cris, "otra empresa": bea, "buzon recreado con el mismo nombre": recreated} {
				if got := e.storedRows(t, p); got != kept[p.Username+p.MailboxID.String()] {
					t.Errorf("%s: tenia %d filas y ahora %d", name, kept[p.Username+p.MailboxID.String()], got)
				}
			}

			if n, err := e.repo.DeleteMailboxData(ctx, ana); err != nil || n != 0 {
				t.Fatalf("repetido: %d %v", n, err)
			}
			if n, err := e.repo.DeleteMailboxData(ctx, domain.Principal{TenantID: ana.TenantID, MailboxID: uuid.New()}); err != nil || n != 0 {
				t.Fatalf("buzon sin datos: %d %v", n, err)
			}
		})
	}
}

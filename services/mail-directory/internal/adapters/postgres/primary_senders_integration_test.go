//go:build integration

package postgres

import (
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

func TestRemitentesFijadosContraPostgres(t *testing.T) {
	f := newWebmailFixture(t)
	s, err := f.uc.AddPrimarySender(f.internal, f.ana.Username, "Soporte@Proveedor.example")
	if err != nil || s.Address != "soporte@proveedor.example" || s.CreatedAt.IsZero() || s.TenantID != f.tenantA {
		t.Fatalf("fijar: %+v %v", s, err)
	}
	again, err := f.uc.AddPrimarySender(f.internal, f.ana.Username, "soporte@proveedor.example")
	if err != nil || again.ID != s.ID {
		t.Fatalf("repetir es idempotente: %+v %v", again, err)
	}
	if _, err := f.uc.AddPrimarySender(f.internal, f.luis.Username, "soporte@proveedor.example"); err != nil {
		t.Fatalf("otro buzon puede fijar la misma direccion: %v", err)
	}
	if n, err := f.asRole(t, "mail_app", f.tenantB, `SELECT count(*) FROM mail.mailbox_primary_senders WHERE id = $1`, s.ID); err != nil || n != 0 {
		t.Fatalf("mail_app de B ve el remitente de A: %d %v", n, err)
	}
	_, err = f.asRole(t, "mail_engine", uuid.Nil, `SELECT count(*) FROM mail.mailbox_primary_senders`)
	expectSQLState(t, err, "42501", "mail_engine lee los remitentes fijados")
	_, err = f.pool.Exec(f.ctx, `INSERT INTO mail.mailbox_primary_senders (tenant_id, username, address) VALUES ($1, $2, 'MAYUS@x.example')`, f.tenantA, f.ana.Username)
	expectSQLState(t, err, "23514", "direccion con mayusculas")

	list, err := f.uc.PrimarySenders(f.internal, f.ana.Username)
	if err != nil || len(list) != 1 {
		t.Fatalf("listado: %+v %v", list, err)
	}
	if err := f.uc.RemovePrimarySender(f.internal, f.ana.Username, "soporte@proveedor.example"); err != nil {
		t.Fatal(err)
	}
	if list, _ := f.uc.PrimarySenders(f.internal, f.ana.Username); len(list) != 0 {
		t.Fatalf("quitar: %+v", list)
	}
	if _, err := f.uc.AddPrimarySender(f.internal, f.ana.Username, "otro@proveedor.example"); err != nil {
		t.Fatal(err)
	}
	if err := f.uc.DeleteMailbox(f.ctxA, f.tenantA, f.ana.ID); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM mail.mailbox_primary_senders WHERE username = $1`, f.ana.Username).Scan(&left); err != nil || left != 0 {
		t.Fatalf("quedaron remitentes del buzon borrado: %d %v", left, err)
	}
	if _, err := f.uc.AddPrimarySender(f.internal, f.ana.Username, "x@y.example"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("un buzon borrado no admite remitentes: %v", err)
	}
}

//go:build integration

package postgres

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Las contrasenas de aplicacion bajo TransactRLS (rol mail_app): cada cambio que les quita un inicio
// de sesion deja en la outbox, en su misma transaccion, un mail.mailbox.credentials_changed con
// credential app_password; lo que no quita nada no deja ninguno; sin permiso de INSERT sobre la
// outbox la revocacion no se confirma; apagar el buzon lo anuncia una vez; y la baja de la empresa,
// que ya anuncia cada buzon apagado, no anade ninguno.
func TestContrasenasDeAplicacionEnLaOutbox(t *testing.T) {
	dsn := integrationEnv(t, "MAIL_DIRECTORY_TEST_DSN")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	applyCellMigrations(t, ctx, pool)

	uc := newUseCase(&db.ContextPool{})
	tenant := uuid.New()
	dom := "ap-" + strings.Split(uuid.New().String(), "-")[0] + ".example"
	t.Cleanup(func() { cleanup(t, pool, dom, dom, tenant) })
	actx := middleware.WithIdentity(db.WithPool(ctx, pool), uuid.New().String(), tenant.String())
	fatal := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}

	_, err = uc.SetDomainActivation(actx, tenant, dom, true)
	fatal("activar", err)
	mb, err := uc.CreateMailbox(actx, tenant, app.CreateMailboxRequest{LocalPart: "ana", Domain: dom, Password: "contrasena-de-prueba-1"})
	fatal("alta de buzon", err)

	avisos := func(credential domain.Credential) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.event_outbox
			WHERE subject = 'mail.mailbox.credentials_changed' AND tenant_id = $1
			  AND payload->'data'->>'username' = $2 AND payload->'data'->>'credential' = $3`,
			tenant, mb.Username, string(credential)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	estado := func(id uuid.UUID) (existe, activa bool) {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*), coalesce(bool_or(active), false) FROM mail.app_passwords WHERE id = $1`,
			id).Scan(&n, &activa); err != nil {
			t.Fatal(err)
		}
		return n == 1, activa
	}
	apagados := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.event_outbox WHERE subject = 'mail.mailbox.updated'
			AND tenant_id = $1 AND payload->'data'->>'username' = $2 AND payload->'data'->>'active' = '0'`,
			tenant, mb.Username).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	ap, _, err := uc.CreateAppPassword(actx, tenant, mb.ID, app.CreateAppPasswordRequest{Name: "movil"})
	fatal("alta de contrasena de aplicacion", err)
	if n := avisos(domain.CredentialAppPassword); n != 0 {
		t.Fatalf("el alta no se anuncia: %d avisos", n)
	}

	yes, no, name := true, false, "tableta"
	want := 0
	for _, step := range []struct {
		desc   string
		req    app.UpdateAppPasswordRequest
		revoca bool
	}{
		{"renombrar", app.UpdateAppPasswordRequest{Name: &name}, false},
		{"quitar dav", app.UpdateAppPasswordRequest{DAVAccess: &no}, false},
		{"quitar imap", app.UpdateAppPasswordRequest{IMAPAccess: &no}, true},
		{"devolver imap", app.UpdateAppPasswordRequest{IMAPAccess: &yes}, false},
		{"desactivar", app.UpdateAppPasswordRequest{Active: &no}, true},
		{"reactivar", app.UpdateAppPasswordRequest{Active: &yes}, false},
	} {
		_, err := uc.UpdateAppPassword(actx, tenant, mb.ID, ap.ID, step.req)
		fatal(step.desc, err)
		if step.revoca {
			want++
		}
		if n := avisos(domain.CredentialAppPassword); n != want {
			t.Fatalf("%s: %d avisos, quiero %d", step.desc, n, want)
		}
	}

	// El payload que leen mail-security (username) y el webmail (credential y changed_at), en el
	// sobre que entrega el rele.
	var typ, src, envTenant, dataTenant, dataID, changedAt string
	if err := pool.QueryRow(ctx, `SELECT payload->>'type', payload->>'source', payload->>'tenant_id',
		payload->'data'->>'tenant_id', payload->'data'->>'id', payload->'data'->>'changed_at'
		FROM platform.event_outbox WHERE subject = 'mail.mailbox.credentials_changed' AND tenant_id = $1 LIMIT 1`,
		tenant).Scan(&typ, &src, &envTenant, &dataTenant, &dataID, &changedAt); err != nil {
		t.Fatal(err)
	}
	if _, perr := time.Parse(time.RFC3339, changedAt); typ != "mail.mailbox.credentials_changed" || src != "mail-directory" ||
		envTenant != tenant.String() || dataTenant != tenant.String() || dataID != mb.ID.String() || perr != nil {
		t.Fatalf("aviso: type=%q source=%q tenant=%q/%q id=%q changed_at=%q", typ, src, envTenant, dataTenant, dataID, changedAt)
	}

	// Sin permiso de INSERT sobre la outbox, desactivar y borrar fallan dentro de su transaccion y la
	// contrasena sigue activa: nunca queda revocada sin su aviso.
	if _, err := pool.Exec(ctx, `REVOKE INSERT ON platform.event_outbox FROM mail_app`); err != nil {
		t.Fatal(err)
	}
	grant := filepath.Join(repoRoot(), "migrations/cell/canonical/mail-directory/05_outbox_grants.sql")
	t.Cleanup(func() { _ = execFile(context.Background(), pool, grant) })
	if _, err := uc.UpdateAppPassword(actx, tenant, mb.ID, ap.ID, app.UpdateAppPasswordRequest{Active: &no}); err == nil {
		t.Fatal("sin outbox la desactivacion debe fallar")
	}
	if err := uc.DeleteAppPassword(actx, tenant, mb.ID, ap.ID); err == nil {
		t.Fatal("sin outbox el borrado debe fallar")
	}
	if existe, activa := estado(ap.ID); !existe || !activa || avisos(domain.CredentialAppPassword) != want {
		t.Fatalf("sin outbox: existe=%v activa=%v avisos=%d", existe, activa, avisos(domain.CredentialAppPassword))
	}
	fatal("restituir el permiso", execFile(ctx, pool, grant))

	fatal("borrar la activa", uc.DeleteAppPassword(actx, tenant, mb.ID, ap.ID))
	want++
	if existe, _ := estado(ap.ID); existe || avisos(domain.CredentialAppPassword) != want {
		t.Fatalf("borrado: existe=%v avisos=%d, quiero %d", existe, avisos(domain.CredentialAppPassword), want)
	}
	otra, _, err := uc.CreateAppPassword(actx, tenant, mb.ID, app.CreateAppPasswordRequest{Name: "portatil"})
	fatal("segunda contrasena", err)
	_, err = uc.UpdateAppPassword(actx, tenant, mb.ID, otra.ID, app.UpdateAppPasswordRequest{Active: &no})
	fatal("desactivar la segunda", err)
	want++
	fatal("borrar la inactiva", uc.DeleteAppPassword(actx, tenant, mb.ID, otra.ID))
	if n := avisos(domain.CredentialAppPassword); n != want {
		t.Fatalf("borrar una inactiva no se anuncia: %d avisos, quiero %d", n, want)
	}

	// Apagar el buzon con dos contrasenas activas: un aviso y las dos apagadas.
	var ids []uuid.UUID
	for _, n := range []string{"telefono", "tableta"} {
		p, _, err := uc.CreateAppPassword(actx, tenant, mb.ID, app.CreateAppPasswordRequest{Name: n})
		fatal("alta "+n, err)
		ids = append(ids, p.ID)
	}
	off, on := domain.ActiveOff, domain.ActiveOn
	apagados0 := apagados()
	_, err = uc.UpdateMailbox(actx, tenant, mb.ID, app.UpdateMailboxRequest{Active: &off})
	fatal("apagar el buzon", err)
	want++
	if n := avisos(domain.CredentialAppPassword); n != want || apagados() != apagados0+1 {
		t.Fatalf("apagar el buzon: %d avisos (quiero %d), %d cambios del buzon", n, want, apagados()-apagados0)
	}
	for _, id := range ids {
		if _, activa := estado(id); activa {
			t.Fatalf("la contrasena %s sigue activa con el buzon apagado", id)
		}
	}

	fatal("cambiar la contrasena principal", uc.SetMailboxPassword(actx, tenant, mb.ID, "otra-contrasena-de-prueba-2"))
	if n := avisos(domain.CredentialPassword); n != 1 {
		t.Fatalf("la contrasena principal se anuncia como password: %d", n)
	}

	// La baja apaga la contrasena activa y anuncia el buzon apagado, sin otro aviso de credencial.
	_, err = uc.UpdateMailbox(actx, tenant, mb.ID, app.UpdateMailboxRequest{Active: &on})
	fatal("reactivar el buzon", err)
	ultima, _, err := uc.CreateAppPassword(actx, tenant, mb.ID, app.CreateAppPasswordRequest{Name: "reloj"})
	fatal("contrasena antes de la baja", err)
	apagados0 = apagados()
	_, err = uc.RetireTenant(middleware.WithIdentity(db.WithPool(ctx, pool), "", tenant.String()), tenant)
	fatal("baja de la empresa", err)
	if _, activa := estado(ultima.ID); activa || apagados() != apagados0+1 || avisos(domain.CredentialAppPassword) != want {
		t.Fatalf("baja: activa=%v cambios del buzon=%d avisos=%d (quiero %d)", activa, apagados()-apagados0,
			avisos(domain.CredentialAppPassword), want)
	}
}

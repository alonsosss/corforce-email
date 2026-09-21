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

// Los protocolos y el estado del buzon bajo TransactRLS (rol mail_app): cada cambio que le quita un
// inicio de sesion deja en la outbox un mail.mailbox.credentials_changed con credential password en
// la misma transaccion que su mail.mailbox.updated (created_at es now(), la hora de la transaccion);
// lo que no retira nada deja solo el cambio del buzon; y sin permiso de INSERT sobre la outbox el
// protocolo no se retira.
func TestProtocolosDelBuzonEnLaOutbox(t *testing.T) {
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
	dom := "pr-" + strings.Split(uuid.New().String(), "-")[0] + ".example"
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

	// cuenta devuelve los avisos de credencial y los cambios del buzon en la outbox, y cuantos avisos
	// no comparten transaccion con un cambio del buzon.
	cuenta := func() (avisos, cambios, sueltos int) {
		t.Helper()
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE subject = 'mail.mailbox.credentials_changed'),
			       count(*) FILTER (WHERE subject = 'mail.mailbox.updated'),
			       coalesce(sum(CASE WHEN subject = 'mail.mailbox.credentials_changed' AND NOT EXISTS (
			           SELECT 1 FROM platform.event_outbox u WHERE u.subject = 'mail.mailbox.updated'
			              AND u.tenant_id = o.tenant_id AND u.created_at = o.created_at) THEN 1 ELSE 0 END), 0)
			  FROM platform.event_outbox o
			 WHERE tenant_id = $1 AND payload->'data'->>'username' = $2`,
			tenant, mb.Username).Scan(&avisos, &cambios, &sueltos); err != nil {
			t.Fatal(err)
		}
		return avisos, cambios, sueltos
	}
	imap := func() bool {
		t.Helper()
		var on bool
		if err := pool.QueryRow(ctx, `SELECT imap_access FROM mail.mailboxes WHERE id = $1`, mb.ID).Scan(&on); err != nil {
			t.Fatal(err)
		}
		return on
	}

	yes, no, name := true, false, "Ana P."
	receiveOnly, on := domain.ActiveReceiveOnly, domain.ActiveOn
	want, cambios0, _ := cuenta()
	for i, step := range []struct {
		desc   string
		req    app.UpdateMailboxRequest
		revoca bool
	}{
		{"nombre visible", app.UpdateMailboxRequest{DisplayName: &name}, false},
		{"quitar imap", app.UpdateMailboxRequest{IMAPAccess: &no}, true},
		{"devolver imap", app.UpdateMailboxRequest{IMAPAccess: &yes}, false},
		{"quitar sieve", app.UpdateMailboxRequest{SieveAccess: &no}, true},
		{"solo recepcion", app.UpdateMailboxRequest{Active: &receiveOnly}, true},
		{"quitar pop3 sin inicio de sesion", app.UpdateMailboxRequest{POP3Access: &no}, false},
		{"quitar dav", app.UpdateMailboxRequest{DAVAccess: &no}, false},
		{"devolver dav", app.UpdateMailboxRequest{DAVAccess: &yes}, false},
		{"reactivar", app.UpdateMailboxRequest{Active: &on}, false},
	} {
		_, err := uc.UpdateMailbox(actx, tenant, mb.ID, step.req)
		fatal(step.desc, err)
		if step.revoca {
			want++
		}
		if avisos, cambios, sueltos := cuenta(); avisos != want || cambios != cambios0+i+1 || sueltos != 0 {
			t.Fatalf("%s: %d avisos (quiero %d), %d cambios del buzon (quiero %d), %d avisos fuera de su transaccion",
				step.desc, avisos, want, cambios, cambios0+i+1, sueltos)
		}
	}

	// El payload que leen mail-security (username) y el webmail (credential y changed_at).
	var typ, src, dataID, username, credential, changedAt string
	if err := pool.QueryRow(ctx, `SELECT payload->>'type', payload->>'source', payload->'data'->>'id',
		payload->'data'->>'username', payload->'data'->>'credential', payload->'data'->>'changed_at'
		FROM platform.event_outbox WHERE subject = 'mail.mailbox.credentials_changed' AND tenant_id = $1
		ORDER BY created_at LIMIT 1`, tenant).Scan(&typ, &src, &dataID, &username, &credential, &changedAt); err != nil {
		t.Fatal(err)
	}
	if _, perr := time.Parse(time.RFC3339, changedAt); typ != "mail.mailbox.credentials_changed" || src != "mail-directory" ||
		dataID != mb.ID.String() || username != mb.Username || credential != string(domain.CredentialPassword) || perr != nil {
		t.Fatalf("aviso: type=%q source=%q id=%q username=%q credential=%q changed_at=%q", typ, src, dataID, username, credential, changedAt)
	}

	// Sin permiso de INSERT sobre la outbox, quitar imap falla dentro de su transaccion y el buzon
	// lo conserva: nunca queda retirado sin su aviso.
	if _, err := pool.Exec(ctx, `REVOKE INSERT ON platform.event_outbox FROM mail_app`); err != nil {
		t.Fatal(err)
	}
	grant := filepath.Join(repoRoot(), "migrations/cell/canonical/mail-directory/05_outbox_grants.sql")
	t.Cleanup(func() { _ = execFile(context.Background(), pool, grant) })
	if _, err := uc.UpdateMailbox(actx, tenant, mb.ID, app.UpdateMailboxRequest{IMAPAccess: &no}); err == nil {
		t.Fatal("sin outbox quitar imap debe fallar")
	}
	if avisos, _, _ := cuenta(); !imap() || avisos != want {
		t.Fatalf("sin outbox: imap_access=%v avisos=%d (quiero %d)", imap(), avisos, want)
	}
	fatal("restituir el permiso", execFile(ctx, pool, grant))
}

//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// La baja de una empresa en la celda contra Postgres real, con las migraciones de la celda
// aplicadas dos veces: apaga todo lo suyo y lo anuncia por la outbox en la misma transaccion, borra
// las contrasenas SASL en claro, no toca a otra empresa, se repite sin cambiar nada y deja su
// directorio sin escrituras. mail_app solo lee la baja de su empresa y no la escribe. El cerrojo
// por empresa ordena una escritura en curso y la baja: la baja espera y apaga lo que esa escritura
// dejo.
func TestBajaDeUnaEmpresaContraPostgres(t *testing.T) {
	dsn := integrationEnv(t, "MAIL_DIRECTORY_TEST_DSN")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	applyCellMigrations(t, ctx, pool)

	uc := newUseCase(&db.ContextPool{})
	tenantA, tenantB, tenantC := uuid.New(), uuid.New(), uuid.New()
	suffix := strings.Split(uuid.New().String(), "-")[0]
	domainA, domainB, domainC := "rt-"+suffix+"-a.example", "rt-"+suffix+"-b.example", "rt-"+suffix+"-c.example"
	t.Cleanup(func() {
		cleanup(t, pool, domainA, domainB, tenantA, tenantB)
		cleanup(t, pool, domainC, domainC, tenantC)
	})
	// Con usuario, el camino de una peticion por el gateway (rol mail_app); sin usuario, el de la
	// ruta interna que llama organization.
	as := func(tenant uuid.UUID) context.Context {
		return middleware.WithIdentity(db.WithPool(ctx, pool), uuid.New().String(), tenant.String())
	}
	internal := func(tenant uuid.UUID) context.Context {
		return middleware.WithIdentity(db.WithPool(ctx, pool), "", tenant.String())
	}
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}

	// ── Empresa A: todo lo que recibe, reenvia o autentica ────────────────────
	ctxA := as(tenantA)
	_, err = uc.SetDomainActivation(ctxA, tenantA, domainA, true)
	must("activar el dominio", err)
	_, err = uc.CreateAliasDomain(ctxA, tenantA, app.CreateAliasDomainRequest{AliasDomain: "alias-" + domainA, TargetDomain: domainA})
	must("dominio alias", err)
	mb, err := uc.CreateMailbox(ctxA, tenantA, app.CreateMailboxRequest{LocalPart: "ana", Domain: domainA, Password: "contrasena-de-prueba-1"})
	must("buzon", err)
	_, _, err = uc.CreateAppPassword(ctxA, tenantA, mb.ID, app.CreateAppPasswordRequest{Name: "movil"})
	must("contrasena de aplicacion", err)
	_, err = uc.CreateAlias(ctxA, tenantA, app.CreateAliasRequest{Address: "ventas@" + domainA, Goto: mb.Username})
	must("alias", err)
	_, err = uc.CreateRelayhost(ctxA, tenantA, app.CreateRelayhostRequest{Hostname: "smtp.relay.example:587", Username: "u", Password: "secreta"})
	must("relayhost", err)
	_, err = uc.CreateTransport(ctxA, app.Scope{TenantID: tenantA},
		app.CreateTransportRequest{Destination: "rt-" + suffix + ".partner.example", Nexthop: "[smtp.partner.example]:587", Username: "u", Password: "clave"})
	must("transporte", err)
	_, err = uc.CreateTLSPolicy(ctxA, tenantA, app.CreateTLSPolicyRequest{Dest: "tls-" + domainA, Policy: "encrypt"})
	must("politica tls", err)
	_, err = uc.CreateRecipientMap(ctxA, tenantA, app.CreateRecipientMapRequest{OldDest: "viejo@" + domainA, NewDest: mb.Username})
	must("mapa de destinatario", err)
	on := true
	_, err = uc.CreateBCCMap(ctxA, tenantA, app.CreateBCCMapRequest{LocalDest: "@" + domainA, BCCDest: "archivo@" + domainA, Type: "rcpt", Active: &on})
	must("copia", err)

	// ── Empresa B, en la misma celda ──────────────────────────────────────────
	ctxB := as(tenantB)
	_, err = uc.SetDomainActivation(ctxB, tenantB, domainB, true)
	must("activar el dominio de B", err)
	_, err = uc.CreateMailbox(ctxB, tenantB, app.CreateMailboxRequest{LocalPart: "eva", Domain: domainB, Password: "contrasena-de-prueba-1"})
	must("buzon de B", err)
	_, err = uc.CreateRelayhost(ctxB, tenantB, app.CreateRelayhostRequest{Hostname: "smtp.relay.example:587", Username: "u", Password: "secreta"})
	must("relayhost de B", err)

	encendido := func(tenant uuid.UUID) int {
		t.Helper()
		return count(`SELECT (SELECT count(*) FROM mail.domains WHERE tenant_id = $1 AND active)
		     + (SELECT count(*) FROM mail.alias_domains WHERE tenant_id = $1 AND active)
		     + (SELECT count(*) FROM mail.mailboxes WHERE tenant_id = $1 AND active <> 0)
		     + (SELECT count(*) FROM mail.aliases WHERE tenant_id = $1 AND active <> 0)
		     + (SELECT count(*) FROM mail.app_passwords WHERE tenant_id = $1 AND active)
		     + (SELECT count(*) FROM mail.relayhosts WHERE tenant_id = $1 AND active)
		     + (SELECT count(*) FROM mail.transports WHERE tenant_id = $1 AND active)
		     + (SELECT count(*) FROM mail.tls_policy_overrides WHERE tenant_id = $1 AND active)
		     + (SELECT count(*) FROM mail.recipient_maps WHERE tenant_id = $1 AND active)
		     + (SELECT count(*) FROM mail.bcc_maps WHERE tenant_id = $1 AND active)`, tenant)
	}
	claves := func(tenant uuid.UUID) int {
		t.Helper()
		return count(`SELECT (SELECT count(*) FROM mail.relayhosts WHERE tenant_id = $1 AND password <> '')
		     + (SELECT count(*) FROM mail.transports WHERE tenant_id = $1 AND password <> '')`, tenant)
	}
	anunciados := func(tenant uuid.UUID) int {
		t.Helper()
		return count(`SELECT count(*) FROM platform.event_outbox
		   WHERE tenant_id = $1 AND subject IN ('mail.domain.updated', 'mail.alias_domain.updated', 'mail.mailbox.updated', 'mail.alias.updated')
		     AND payload->'data'->>'active' IN ('false', '0')`, tenant)
	}
	if encendido(tenantA) != 10 || claves(tenantA) != 2 {
		t.Fatalf("preparacion de A: encendido %d, claves %d", encendido(tenantA), claves(tenantA))
	}
	encendidoB := encendido(tenantB)

	// ── La baja de A ──────────────────────────────────────────────────────────
	r, err := uc.RetireTenant(internal(tenantA), tenantA)
	must("baja", err)
	want := domain.RetirementCounts{Domains: 1, AliasDomains: 1, Mailboxes: 1, Aliases: 1, AppPasswords: 1,
		Relayhosts: 1, Transports: 1, TLSPolicies: 1, RecipientMaps: 1, BCCMaps: 1}
	if r.TenantID != tenantA || r.Deactivated != want || r.RetiredAt.IsZero() {
		t.Fatalf("baja = %+v; want %+v", r, want)
	}
	if encendido(tenantA) != 0 || claves(tenantA) != 0 {
		t.Fatalf("tras la baja de A: encendido %d, contrasenas en claro %d", encendido(tenantA), claves(tenantA))
	}
	if anunciados(tenantA) != 4 {
		t.Fatalf("eventos de la baja en la outbox: %d; want 4", anunciados(tenantA))
	}
	if encendido(tenantB) != encendidoB || claves(tenantB) != 1 {
		t.Fatalf("la baja de A toco a B: encendido %d (antes %d), claves %d", encendido(tenantB), encendidoB, claves(tenantB))
	}

	again, err := uc.RetireTenant(internal(tenantA), tenantA)
	must("repetir la baja", err)
	if again.Deactivated != (domain.RetirementCounts{}) || !again.RetiredAt.Equal(r.RetiredAt) || anunciados(tenantA) != 4 {
		t.Fatalf("repetir la baja = %+v (antes %v), eventos %d", again, r.RetiredAt, anunciados(tenantA))
	}

	// ── El directorio de A ya no admite escrituras ───────────────────────────
	if _, err := uc.SetDomainActivation(ctxA, tenantA, domainA, true); !errors.Is(err, domain.ErrTenantRetired) {
		t.Fatalf("activar el dominio de una empresa dada de baja: %v", err)
	}
	if _, err := uc.SetDomainActivation(internal(tenantA), tenantA, "otro-"+domainA, false); !errors.Is(err, domain.ErrTenantRetired) {
		t.Fatalf("dar de alta un dominio por la activacion: %v", err)
	}
	if _, err := uc.CreateMailbox(ctxA, tenantA, app.CreateMailboxRequest{LocalPart: "luis", Domain: domainA, Password: "contrasena-de-prueba-1"}); !errors.Is(err, domain.ErrTenantRetired) {
		t.Fatalf("alta de buzon: %v", err)
	}
	one := domain.ActiveOn
	if _, err := uc.UpdateMailbox(ctxA, tenantA, mb.ID, app.UpdateMailboxRequest{Active: &one}); !errors.Is(err, domain.ErrTenantRetired) {
		t.Fatalf("reactivar el buzon: %v", err)
	}
	if _, err := uc.SetDomainActivation(internal(tenantA), tenantA, domainA, false); err != nil {
		t.Fatalf("apagar un dominio que ya tiene: %v", err)
	}
	if encendido(tenantA) != 0 {
		t.Fatalf("una escritura rechazada encendio algo: %d", encendido(tenantA))
	}
	if _, err := uc.CreateAlias(ctxB, tenantB, app.CreateAliasRequest{Address: "soporte@" + domainB, Goto: "eva@" + domainB}); err != nil {
		t.Fatalf("B sigue escribiendo: %v", err)
	}

	// ── mail_app lee solo la baja de su empresa y no la escribe ──────────────
	comoApp := func(tenant uuid.UUID, sql string, args ...any) (int, error) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE mail_app`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant_id', $1, true)`, tenant.String()); err != nil {
			t.Fatal(err)
		}
		var n int
		err = tx.QueryRow(ctx, sql, args...).Scan(&n)
		return n, err
	}
	if n, err := comoApp(tenantA, `SELECT count(*) FROM mail.tenant_retirements`); err != nil || n != 1 {
		t.Fatalf("mail_app de A ve su baja: %d %v", n, err)
	}
	if n, err := comoApp(tenantB, `SELECT count(*) FROM mail.tenant_retirements WHERE tenant_id = $1`, tenantA); err != nil || n != 0 {
		t.Fatalf("mail_app de B ve la baja de A: %d %v", n, err)
	}
	_, err = comoApp(tenantB, `WITH x AS (INSERT INTO mail.tenant_retirements (tenant_id) VALUES ($1) RETURNING 1) SELECT count(*) FROM x`, tenantB)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("mail_app registra una baja: %v", err)
	}

	// ── Una escritura en curso y la baja ─────────────────────────────────────
	_, err = uc.SetDomainActivation(as(tenantC), tenantC, domainC, true)
	must("activar el dominio de C", err)
	tx, err := pool.Begin(ctx)
	must("transaccion de la escritura", err)
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1, hashtext($2))`, retirementLockSpace, tenantC.String())
	must("cerrojo compartido", err)
	_, err = tx.Exec(ctx, `INSERT INTO mail.mailboxes (tenant_id, username, local_part, domain, password_hash) VALUES ($1, $2, 'luis', $3, 'x')`,
		tenantC, "luis@"+domainC, domainC)
	must("buzon de la escritura en curso", err)
	done := make(chan error, 1)
	go func() {
		_, err := uc.RetireTenant(internal(tenantC), tenantC)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("la baja no espero a la escritura en curso: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	must("confirmar la escritura", tx.Commit(ctx))
	select {
	case err := <-done:
		must("baja de C", err)
	case <-time.After(30 * time.Second):
		t.Fatal("la baja no termino tras la escritura")
	}
	if n := encendido(tenantC); n != 0 {
		t.Fatalf("la baja dejo encendido lo que la escritura en curso creo: %d", n)
	}
}

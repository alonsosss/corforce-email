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
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Prueba contra una base DESECHABLE de celda (MAIL_DIRECTORY_TEST_DSN); las migraciones
// las aplica la propia prueba. Lo que importa comprobar no cabe en un falso: que el SQL es
// valido, que el rol mail_app y sus politicas dejan pasar lo que el servicio necesita, y
// que una segunda empresa no ve ni toca lo de la primera aunque comparta la base.
//
//	docker run -d --name md-pg -e POSTGRES_PASSWORD=t -e POSTGRES_USER=t -e POSTGRES_DB=cell -p 55433:5432 pgvector/pgvector:pg16
//	MAIL_DIRECTORY_TEST_DSN='postgres://t:t@127.0.0.1:55433/cell?sslmode=disable' go test -tags integration ./services/mail-directory/...
func TestDirectorioContraPostgres(t *testing.T) {
	dsn := integrationEnv(t, "MAIL_DIRECTORY_TEST_DSN")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	// Los Cleanup corren en orden inverso: el pool se cierra DESPUES de limpiar las filas.
	t.Cleanup(pool.Close)
	applyCellMigrations(t, ctx, pool)

	ctxPool := &db.ContextPool{}
	uc := newUseCase(ctxPool)

	tenantA, tenantB := uuid.New(), uuid.New()
	suffix := strings.Split(uuid.New().String(), "-")[0]
	domainA := "it-" + suffix + "-a.example"
	domainB := "it-" + suffix + "-b.example"
	t.Cleanup(func() { cleanup(t, pool, domainA, domainB, tenantA, tenantB) })

	// Con usuario en el contexto la transaccion cambia al rol mail_app: es el camino de
	// una peticion real. El pool queda en el contexto como hace StaticPoolMiddleware.
	as := func(tenant uuid.UUID) context.Context {
		return middleware.WithIdentity(db.WithPool(ctx, pool), uuid.New().String(), tenant.String())
	}
	ctxA, ctxB := as(tenantA), as(tenantB)

	// ── Empresa A: dominio, buzon, alias y lo que cuelga del buzon ──────────────
	d, err := uc.SetDomainActivation(ctxA, tenantA, domainA, true)
	if err != nil || !d.Active {
		t.Fatalf("activacion interna: %v", err)
	}
	if _, err := uc.SetDomainActivation(ctxA, tenantA, domainA, true); err != nil {
		t.Fatalf("activacion repetida debe ser idempotente: %v", err)
	}
	quota := int64(50 << 20)
	mb, err := uc.CreateMailbox(ctxA, tenantA, app.CreateMailboxRequest{
		LocalPart: "ana", Domain: domainA, Password: "contrasena-de-prueba-1", DisplayName: "Ana", QuotaBytes: &quota,
	})
	if err != nil {
		t.Fatalf("alta de buzon: %v", err)
	}
	if err := uc.SetMailboxPassword(ctxA, tenantA, mb.ID, "otra-contrasena-larga-2"); err != nil {
		t.Fatalf("cambio de contrasena: %v", err)
	}
	al, err := uc.CreateAlias(ctxA, tenantA, app.CreateAliasRequest{Address: "ventas@" + domainA, Goto: mb.Username + ", externo@proveedor.example"})
	if err != nil {
		t.Fatalf("alta de alias: %v", err)
	}
	if _, err := uc.CreateAlias(ctxA, tenantA, app.CreateAliasRequest{Address: mb.Username, Goto: "x@y.example"}); !errors.Is(err, domain.ErrAddressTaken) {
		t.Fatalf("alias sobre buzon debe chocar: %v", err)
	}
	ap, plain, err := uc.CreateAppPassword(ctxA, tenantA, mb.ID, app.CreateAppPasswordRequest{Name: "movil"})
	if err != nil || len(plain) < 24 || ap.PasswordHash == plain {
		t.Fatalf("contrasena de aplicacion: %v", err)
	}
	sieve, err := uc.PutMailboxSieve(ctxA, tenantA, mb.ID, app.PutSieveRequest{
		Prefilter: &app.SieveScript{ScriptData: `require ["fileinto"]; if header :contains "subject" "spam" { fileinto "Junk"; }`, Active: true},
	})
	if err != nil || sieve.Prefilter == nil || sieve.Postfilter != nil {
		t.Fatalf("sieve: %v %+v", err, sieve)
	}
	if _, err := uc.CreateSenderACL(ctxA, tenantA, app.SenderACLRequest{LoggedInAs: mb.Username, SendAs: al.Address}); err != nil {
		t.Fatalf("sender acl: %v", err)
	}
	if _, err := uc.CreateAliasDomain(ctxA, tenantA, app.CreateAliasDomainRequest{AliasDomain: "alias-" + domainA, TargetDomain: domainA}); err != nil {
		t.Fatalf("dominio alias: %v", err)
	}
	rh, err := uc.CreateRelayhost(ctxA, tenantA, app.CreateRelayhostRequest{Hostname: "smtp.relay.example:587", Username: "u", Password: "secreta"})
	if err != nil || !rh.HasPassword {
		t.Fatalf("relayhost: %v", err)
	}
	if _, err := uc.CreateTLSPolicy(ctxA, tenantA, app.CreateTLSPolicyRequest{Dest: "tls-" + domainA, Policy: "encrypt"}); err != nil {
		t.Fatalf("politica tls: %v", err)
	}
	if _, err := uc.CreateBCCMap(ctxA, tenantA, app.CreateBCCMapRequest{LocalDest: "@" + domainA, BCCDest: "archivo@" + domainA, Type: "rcpt"}); err != nil {
		t.Fatalf("bcc: %v", err)
	}
	if _, err := uc.CreateRecipientMap(ctxA, tenantA, app.CreateRecipientMapRequest{OldDest: "viejo@" + domainA, NewDest: mb.Username}); err != nil {
		t.Fatalf("recipient map: %v", err)
	}
	until := time.Now().Add(24 * time.Hour)
	if _, err := uc.CreateSpamAlias(ctxA, tenantA, app.CreateSpamAliasRequest{Address: "tmp@" + domainA, Goto: mb.Username, ValidUntil: &until}); err != nil {
		t.Fatalf("spam alias: %v", err)
	}
	// Dovecot escribe el uso de cuota como dueno; el servicio lo lee unido al buzon.
	if _, err := pool.Exec(ctx, `INSERT INTO mail.quota_usage (username, bytes, messages) VALUES ($1, 1234, 5)`, mb.Username); err != nil {
		t.Fatalf("uso de cuota: %v", err)
	}
	q, err := uc.MailboxQuota(ctxA, tenantA, mb.ID)
	if err != nil || q.UsedBytes != 1234 || q.Messages != 5 || q.QuotaBytes != quota {
		t.Fatalf("lectura de cuota: %v %+v", err, q)
	}
	if _, err := uc.MailboxLogins(ctxA, tenantA, mb.ID, 10); err != nil {
		t.Fatalf("logins: %v", err)
	}

	// ── Busqueda en el SQL, bajo RLS ─────────────────────────────────────────────
	_, _, firstPage := app.NormalizePage(1, 50)
	if found, total, err := uc.ListMailboxes(ctxA, tenantA, ports.MailboxFilter{Search: "ANA"}, firstPage); err != nil || total != 1 || found[0].ID != mb.ID {
		t.Fatalf("busqueda por username sin distinguir mayusculas: %v total=%d", err, total)
	}
	if _, total, err := uc.ListMailboxes(ctxA, tenantA, ports.MailboxFilter{Search: "an%"}, firstPage); err != nil || total != 0 {
		t.Fatalf("un %% del usuario se busca como texto: %v total=%d", err, total)
	}
	if _, total, err := uc.ListMailboxes(ctxA, tenantA, ports.MailboxFilter{Domain: "otro.example"}, firstPage); err != nil || total != 0 {
		t.Fatalf("filtro por dominio exacto: %v total=%d", err, total)
	}
	if found, total, err := uc.ListDomains(ctxA, tenantA, ports.DomainFilter{Search: suffix + "-A"}, firstPage); err != nil || total != 1 || found[0].ID != d.ID {
		t.Fatalf("busqueda de dominios: %v total=%d", err, total)
	}

	// ── Empresa B no ve ni toca lo de A ──────────────────────────────────────────
	items, total, err := uc.ListDomains(ctxB, tenantB, ports.DomainFilter{}, firstPage)
	if err != nil || total != 0 || len(items) != 0 {
		t.Fatalf("B lista dominios de A: %v total=%d", err, total)
	}
	if _, total, err := uc.ListMailboxes(ctxB, tenantB, ports.MailboxFilter{Search: "ana"}, firstPage); err != nil || total != 0 {
		t.Fatalf("B encuentra buzones de A buscando: %v total=%d", err, total)
	}
	if _, err := uc.GetMailbox(ctxB, tenantB, mb.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("B lee el buzon de A: %v", err)
	}
	if err := uc.DeleteMailbox(ctxB, tenantB, mb.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("B borra el buzon de A: %v", err)
	}
	if _, err := uc.CreateDomain(ctxB, tenantB, app.CreateDomainRequest{Domain: domainA}); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("B registra el dominio de A: %v", err)
	}
	if _, err := uc.SetDomainActivation(ctxB, tenantB, domainB, true); err != nil {
		t.Fatalf("dominio de B: %v", err)
	}
	if _, err := uc.CreateAliasDomain(ctxB, tenantB, app.CreateAliasDomainRequest{AliasDomain: domainA, TargetDomain: domainB}); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("B reclama el dominio de A como alias: %v", err)
	}
	if _, err := uc.CreateMailbox(ctxB, tenantB, app.CreateMailboxRequest{LocalPart: "x", Domain: domainA, Password: "contrasena-de-prueba-1"}); !errors.Is(err, domain.ErrDomainNotOwned) {
		t.Fatalf("B crea buzon en el dominio de A: %v", err)
	}
	// La politica RLS por si sola: sin filtro de tenant, B cuenta cero buzones.
	var visible int64
	err = ctxPool.TransactRLS(ctxB, func(ctx context.Context) error {
		return ctxPool.QueryRow(ctx, `SELECT COUNT(*) FROM mail.mailboxes`).Scan(&visible)
	})
	if err != nil || visible != 0 {
		t.Fatalf("RLS deja ver buzones ajenos: %v visibles=%d", err, visible)
	}
	// Una empresa no crea rutas de plataforma aunque lo pida: el servicio la frena.
	if _, err := uc.CreateTransport(ctxB, app.Scope{TenantID: tenantB}, app.CreateTransportRequest{Destination: domainB, Nexthop: "smtp:[relay]:25", Platform: true}); !errors.Is(err, domain.ErrPlatformOnly) {
		t.Fatalf("ruta de plataforma desde empresa: %v", err)
	}

	// ── Bajas: el dominio con buzones se niega; el buzon limpia su cuota ─────────
	if err := uc.DeleteDomain(ctxA, tenantA, d.ID); !errors.Is(err, domain.ErrDomainInUse) {
		t.Fatalf("borrar dominio con buzones: %v", err)
	}
	if err := uc.DeleteMailbox(ctxA, tenantA, mb.ID); err != nil {
		t.Fatalf("borrar buzon: %v", err)
	}
	var quotaRows, appRows, sieveRows int
	if err := pool.QueryRow(ctx,
		`SELECT (SELECT COUNT(*) FROM mail.quota_usage WHERE username = $1),
		        (SELECT COUNT(*) FROM mail.app_passwords WHERE mailbox_id = $2),
		        (SELECT COUNT(*) FROM mail.sieve_filters WHERE username = $1)`,
		mb.Username, mb.ID).Scan(&quotaRows, &appRows, &sieveRows); err != nil {
		t.Fatal(err)
	}
	if quotaRows != 0 || appRows != 0 || sieveRows != 0 {
		t.Fatalf("restos tras borrar el buzon: quota=%d app=%d sieve=%d", quotaRows, appRows, sieveRows)
	}
}

// cleanup borra como dueno (sin RLS) todo lo que dejo la prueba, eventos incluidos.
func cleanup(t *testing.T, pool *pgxpool.Pool, domainA, domainB string, tenants ...uuid.UUID) {
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `DELETE FROM platform.event_outbox WHERE tenant_id = ANY($1)`, tenants); err != nil {
		t.Logf("limpieza outbox: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM mail.quota_usage WHERE username LIKE '%@' || $1 OR username LIKE '%@' || $2`, domainA, domainB); err != nil {
		t.Logf("limpieza quota_usage: %v", err)
	}
	for _, table := range []string{"sasl_logins", "sieve_filters", "vacation_replies", "mta_sts_policies", "app_passwords", "sender_acl", "spam_aliases", "aliases",
		"bcc_maps", "recipient_maps", "tls_policy_overrides", "transports", "relayhosts", "mailboxes", "mailbox_deletions", "alias_domains", "domains",
		"tenant_retirements"} {
		if _, err := pool.Exec(ctx, `DELETE FROM mail.`+table+` WHERE tenant_id = ANY($1)`, tenants); err != nil {
			t.Logf("limpieza %s: %v", table, err)
		}
	}
}

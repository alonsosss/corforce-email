//go:build integration

// Prueba contra un Postgres real. Aplica las migraciones de mail-directory (el esquema
// mail y sus vistas publicadas) y la de este servicio sobre una base DESECHABLE, siembra
// un directorio minimo de dos empresas y ejecuta las consultas reales: expansion de
// aliases, generacion de /settings, RLS del API de administracion y cuarentena.
//
//	docker run -d --name ms-pg -e POSTGRES_PASSWORD=t -e POSTGRES_USER=t -e POSTGRES_DB=cell -p 55432:5432 pgvector/pgvector:pg16
//	MAIL_SECURITY_TEST_DSN='postgres://t:t@127.0.0.1:55432/cell?sslmode=disable' go test -tags integration ./services/mail-security/...
package postgres

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

var (
	tenantA = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	tenantB = uuid.MustParse("22222222-2222-2222-2222-222222222222")
)

func repoRoot(t *testing.T) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", ".."))
}

func applyMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool, root string) {
	t.Helper()
	// Todas las del directorio en orden (incluida la vista de bcc) y despues la propia.
	files, err := filepath.Glob(filepath.Join(root, "migrations/cell/canonical/mail-directory/*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("migraciones del directorio: %v", err)
	}
	sort.Strings(files)
	files = append(files, filepath.Join(root, "migrations/cell/canonical/mail-security/01_mail_security.sql"))
	// Dos pasadas: la migracion tiene que tolerar re-ejecutarse.
	for pass := 0; pass < 2; pass++ {
		for _, f := range files {
			sql, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, string(sql)); err != nil {
				t.Fatalf("aplicar %s (pasada %d): %v", f, pass+1, err)
			}
		}
	}
}

func seedDirectory(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	stmts := []string{
		`DELETE FROM mail.bcc_maps; DELETE FROM mail.aliases; DELETE FROM mail.alias_domains; DELETE FROM mail.mailboxes; DELETE FROM mail.domains`,
		`INSERT INTO mail.bcc_maps (tenant_id, local_dest, bcc_dest, domain, type, active) VALUES
		   ('` + tenantA.String() + `', '@acme.com', 'archivo@acme.com', 'acme.com', 'rcpt', true),
		   ('` + tenantA.String() + `', 'ana@acme.com', 'jefe@acme.com', 'acme.com', 'sender', false)`,
		`DELETE FROM mail_security.quarantine; DELETE FROM mail_security.quarantine_settings; DELETE FROM mail_security.spam_scores;
		 DELETE FROM mail_security.address_lists; DELETE FROM mail_security.settings_maps; DELETE FROM mail_security.rate_limits;
		 DELETE FROM mail_security.forwarding_hosts; DELETE FROM mail_security.mailbox_tags; DELETE FROM mail_security.domain_footers`,
		`INSERT INTO mail.domains (tenant_id, domain, active) VALUES
		   ('` + tenantA.String() + `', 'acme.com', true),
		   ('` + tenantB.String() + `', 'otra.com', true),
		   ('` + tenantB.String() + `', 'baja.com', false)`,
		`INSERT INTO mail.alias_domains (tenant_id, alias_domain, target_domain, active) VALUES
		   ('` + tenantA.String() + `', 'acme-alias.com', 'acme.com', true)`,
		`INSERT INTO mail.mailboxes (tenant_id, username, local_part, domain, password_hash, active, kind) VALUES
		   ('` + tenantA.String() + `', 'ana@acme.com', 'ana', 'acme.com', 'x', 1, ''),
		   ('` + tenantA.String() + `', 'luis@acme.com', 'luis', 'acme.com', 'x', 2, ''),
		   ('` + tenantA.String() + `', 'sala@acme.com', 'sala', 'acme.com', 'x', 1, 'location'),
		   ('` + tenantB.String() + `', 'pepe@otra.com', 'pepe', 'otra.com', 'x', 1, '')`,
		`INSERT INTO mail.aliases (tenant_id, address, goto, domain, active) VALUES
		   ('` + tenantA.String() + `', 'ventas@acme.com', 'soporte@acme.com', 'acme.com', 1),
		   ('` + tenantA.String() + `', 'soporte@acme.com', 'ana@acme.com, luis@acme.com', 'acme.com', 1),
		   ('` + tenantA.String() + `', 'solo@acme.com', 'ana@acme.com', 'acme.com', 1),
		   ('` + tenantA.String() + `', '@acme.com', 'ana@acme.com,null@localhost', 'acme.com', 1),
		   ('` + tenantA.String() + `', 'bucle1@acme.com', 'bucle2@acme.com', 'acme.com', 1),
		   ('` + tenantA.String() + `', 'bucle2@acme.com', 'bucle1@acme.com', 'acme.com', 1)`,
	}
	for _, s := range stmts {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatalf("sembrar: %v\n%s", err, s)
		}
	}
}

func adminCtx(pool *pgxpool.Pool, tenant uuid.UUID) context.Context {
	// Usuario y empresa en el contexto: es lo que hace que TransactRLS cambie al rol
	// mail_app y fije app.current_tenant_id.
	return middleware.WithIdentity(db.WithPool(context.Background(), pool), uuid.New().String(), tenant.String())
}

func TestIntegracionCelda(t *testing.T) {
	dsn := os.Getenv("MAIL_SECURITY_TEST_DSN")
	if dsn == "" {
		t.Skip("MAIL_SECURITY_TEST_DSN no definido")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	applyMigrations(t, ctx, pool, repoRoot(t))
	seedDirectory(t, ctx, pool)

	ctxPool := &db.ContextPool{}
	engineCtx := db.WithPool(ctx, pool)
	directory := NewDirectoryRepository(ctxPool)
	reader := NewPolicyReader(ctxPool)
	quarantine := NewQuarantineRepository(ctxPool)
	logger := zap.NewNop()

	t.Run("expansion de aliases sobre las vistas", func(t *testing.T) {
		ex := app.NewExpander(directory)
		cases := map[string][]string{
			"ventas+tag@ACME.com":  {"ana@acme.com", "luis@acme.com"},
			"solo@acme-alias.com":  {"ana@acme.com"},
			"cualquiera@acme.com":  {"ana@acme.com"},
			"sala@acme.com":        nil,
			"bucle1@acme.com":      nil,
			"x@desconocido.com":    nil,
			"pepe@otra.com":        {"pepe@otra.com"},
			"alguien@baja.com":     nil,
		}
		for in, want := range cases {
			boxes, err := ex.Expand(engineCtx, in)
			if err != nil {
				t.Fatalf("%s: %v", in, err)
			}
			var got []string
			for _, b := range boxes {
				got = append(got, b.Username)
			}
			sort.Strings(got)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("%s -> %v, quiero %v", in, got, want)
			}
		}
		if single, _ := ex.ExpandToSingle(engineCtx, "solo@acme.com"); single != "ana@acme.com" {
			t.Errorf("aliasexp: %q", single)
		}
		if aliases, _ := directory.AliasesTargeting(engineCtx, "ana@acme.com"); strings.Join(aliases, ",") != "@acme.com,solo@acme.com,soporte@acme.com" {
			t.Errorf("aliases que llegan a ana: %v", aliases)
		}
		if dest, found, err := directory.BCCDestination(engineCtx, "rcpt", "@acme.com"); err != nil || !found || dest != "archivo@acme.com" {
			t.Errorf("bcc por destinatario: %q %v %v", dest, found, err)
		}
		if _, found, err := directory.BCCDestination(engineCtx, "sender", "ana@acme.com"); err != nil || found {
			t.Errorf("una fila inactiva no genera copia: %v %v", found, err)
		}
		domains, _ := directory.ActiveDomains(engineCtx)
		if strings.Join(domains, ",") != "acme-alias.com,acme.com,otra.com" {
			t.Errorf("dominios activos: %v", domains)
		}
		if owner, found, _ := directory.DomainOwner(engineCtx, "otra.com"); !found || owner != tenantB {
			t.Errorf("dueno de otra.com: %v %v", owner, found)
		}
	})

	t.Run("API de administracion bajo RLS", func(t *testing.T) {
		policy := app.NewPolicyUseCase(app.PolicyDeps{Tx: ctxPool, Repo: NewPolicyRepository(ctxPool), Directory: directory,
			Sync: app.NewRedisSync(apptest.NewStore(), directory, reader, logger), Logger: logger})
		ctxA, ctxB := adminCtx(pool, tenantA), adminCtx(pool, tenantB)

		if _, err := policy.PutSpamScore(ctxA, tenantA, "ana@acme.com", decimal.NewFromInt(12), decimal.NewFromInt(6)); err != nil {
			t.Fatal(err)
		}
		if _, err := policy.PutSpamScore(ctxA, tenantA, "acme.com", decimal.RequireFromString("20.5"), decimal.NewFromInt(9)); err != nil {
			t.Fatal(err)
		}
		if _, err := policy.PutSpamScore(ctxA, tenantA, "otra.com", decimal.NewFromInt(1), decimal.NewFromInt(1)); err != domain.ErrObjectNotOwned {
			t.Fatalf("un dominio de otra empresa se rechaza: %v", err)
		}
		if _, err := policy.CreateAddressList(ctxA, tenantA, "acme.com", domain.ListAllow, "@partner.com"); err != nil {
			t.Fatal(err)
		}
		if _, err := policy.CreateSettingsMap(ctxA, tenantA, "extra", "extra_rule {\n  priority = 1;\n  from = \"x@y.com\";\n}", true); err != nil {
			t.Fatal(err)
		}
		if _, err := policy.PutRateLimit(ctxA, tenantA, "ana@acme.com", "100 / 1h"); err != nil {
			t.Fatal(err)
		}
		if _, err := policy.CreateForwardingHost(ctxA, tenantA, "10.9.8.7", "relay", false); err != nil {
			t.Fatal(err)
		}
		if _, err := policy.PutMailboxTags(ctxA, tenantA, "ana@acme.com", true, false); err != nil {
			t.Fatal(err)
		}
		if _, err := policy.PutFooter(ctxA, tenantA, domain.DomainFooter{Domain: "acme.com", HTML: "<p>pie</p>", MailboxExclude: []string{"luis@acme.com"}}); err != nil {
			t.Fatal(err)
		}
		qs := domain.DefaultQuarantineSettings(tenantA)
		qs.RetentionSize = 2
		qs.ExcludeDomains = []string{"zzz.com"}
		if _, err := policy.PutQuarantineSettings(ctxA, tenantA, qs); err != nil {
			t.Fatal(err)
		}

		// La otra empresa no ve nada de lo anterior, ni por RLS ni por filtro.
		if scores, err := policy.ListSpamScores(ctxB, tenantB); err != nil || len(scores) != 0 {
			t.Fatalf("aislamiento de umbrales: %v %v", scores, err)
		}
		if _, err := policy.GetSpamScore(ctxB, tenantB, "acme.com"); err != domain.ErrNotFound {
			t.Fatalf("aislamiento por objeto: %v", err)
		}
		if got, err := policy.GetQuarantineSettings(ctxB, tenantB); err != nil || got.RetentionSize != domain.DefaultQuarantineRetentionSize {
			t.Fatalf("la otra empresa recibe los defectos: %+v %v", got, err)
		}
		// Y la propia si.
		scores, err := policy.ListSpamScores(ctxA, tenantA)
		if err != nil || len(scores) != 2 || !scores[0].HighScore.Equal(decimal.RequireFromString("20.5")) {
			t.Fatalf("umbrales propios: %+v %v", scores, err)
		}
	})

	t.Run("documento de settings desde la base", func(t *testing.T) {
		engine := app.NewEngineUseCase(app.EngineDeps{Directory: directory, Policy: reader, Quarantine: quarantine,
			Sync: app.NewRedisSync(apptest.NewStore(), directory, reader, logger), Store: apptest.NewStore(), Events: &apptest.Publisher{}, Logger: logger, LogLines: 10})
		doc, _, err := engine.Settings(engineCtx, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"watchdog {",
			`rcpt = ["/@acme[.]com$/i", "/@acme-alias[.]com$/i"];`,
			"reject = 20.50;",
			`rcpt = ["/^ana@acme[.]com$/i", "/^ana@acme-alias[.]com$/i", "/^solo@acme[.]com$/i", "/^soporte@acme[.]com$/i"];`,
			`from = ["/^.*@partner[.]com$/i"];`,
			"MAILCOW_WHITE = -999.0;",
			"  extra_rule {\n    priority = 1;",
		} {
			if !strings.Contains(doc.Body, want) {
				t.Errorf("falta %q en:\n%s", want, doc.Body)
			}
		}
		if doc.LastModified.IsZero() || time.Since(doc.LastModified) > time.Minute {
			t.Errorf("Last-Modified debe salir del updated_at reciente: %v", doc.LastModified)
		}
		footer, err := engine.Footer(engineCtx, "acme.com", "ana@acme.com", "ana@acme.com")
		if err != nil || footer.HTML != "<p>pie</p>" {
			t.Errorf("footer: %+v %v", footer, err)
		}
		if footer, _ := engine.Footer(engineCtx, "acme.com", "luis@acme.com", "luis@acme.com"); footer.HTML != "" {
			t.Errorf("buzon excluido: %+v", footer)
		}
		if ok, _ := engine.ForwardingHostPermits(engineCtx, "10.9.8.7"); !ok {
			t.Error("el host de reenvio debe permitirse")
		}
		list, _ := engine.ForwardingHostList(engineCtx)
		if strings.Join(list, ",") != "240.240.240.240,10.9.8.7/32" {
			t.Errorf("mapa de hosts: %v", list)
		}

		// /pipe: una fila por buzon final, con la retencion (2) de la empresa.
		meta := domain.QuarantineMetadata{QID: "ABC123", Subject: "prueba", Score: decimal.RequireFromString("13.75"), Rcpt: []string{"soporte@acme.com"}, IP: "203.0.113.4", Action: "reject", From: "spam@x.com", Symbols: []string{"BAYES_SPAM"}}
		for i := 0; i < 3; i++ {
			out, err := engine.Pipe(engineCtx, meta, []byte("Subject: prueba\r\n\r\nhola\r\n"))
			if err != nil || out.Stored != 2 {
				t.Fatalf("pipe: %+v %v", out, err)
			}
		}
		items, total, err := quarantine.List(adminCtx(pool, tenantA), tenantA, domain.QuarantineFilter{Page: 1, PerPage: 50})
		if err != nil || total != 4 || len(items) != 4 {
			t.Fatalf("tras la poda deben quedar 2 por buzon: total=%d %v", total, err)
		}
		if items[0].IP != "203.0.113.4" || !items[0].Score.Equal(decimal.RequireFromString("13.75")) || items[0].Symbols[0] != "BAYES_SPAM" || items[0].Size == 0 {
			t.Errorf("fila de cuarentena: %+v", items[0])
		}
		filtered, _, _ := quarantine.List(adminCtx(pool, tenantA), tenantA, domain.QuarantineFilter{Rcpt: "luis@acme.com", Page: 1, PerPage: 10})
		if len(filtered) != 2 {
			t.Errorf("filtro por rcpt: %d", len(filtered))
		}
		if _, err := quarantine.Get(adminCtx(pool, tenantB), tenantB, items[0].ID); err != domain.ErrNotFound {
			t.Errorf("la otra empresa no ve la cuarentena: %v", err)
		}
		msg, err := quarantine.GetMessage(adminCtx(pool, tenantA), tenantA, items[0].ID)
		if err != nil || !strings.HasPrefix(string(msg), "Subject: prueba") {
			t.Errorf("mensaje: %q %v", msg, err)
		}
		if err := quarantine.Delete(adminCtx(pool, tenantA), tenantA, items[0].ID); err != nil {
			t.Errorf("borrar: %v", err)
		}
		if n, err := quarantine.PruneAged(engineCtx, tenantA, 1); err != nil || n != 0 {
			t.Errorf("poda por edad: %d %v", n, err)
		}
		if n, err := quarantine.PruneAged(engineCtx, uuid.Nil, 365); err != nil || n != 0 {
			t.Errorf("poda por edad (defecto): %d %v", n, err)
		}
	})

	t.Run("reconciliacion completa contra la base", func(t *testing.T) {
		store := apptest.NewStore()
		if err := app.NewRedisSync(store, directory, reader, logger).ReconcileAll(engineCtx); err != nil {
			t.Fatal(err)
		}
		if len(store.Hashes[domain.RedisDomainMap]) != 3 || store.Hashes[domain.RedisRateLimitValue]["ana@acme.com"] != "100 / 1h" ||
			store.Hashes[domain.RedisKeepSpam]["10.9.8.7/32"] != "1" || store.Hashes[domain.RedisWantsSubjectTag]["ana@acme.com"] != "1" ||
			store.Values[domain.RedisQuarantineExclude] != `["zzz.com"]` {
			t.Errorf("redis reconciliado: %v %v", store.Hashes, store.Values)
		}
	})
}

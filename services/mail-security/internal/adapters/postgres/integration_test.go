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
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	outboxadapter "github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/outbox"
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
	// La outbox de la celda, todas las del directorio en orden y despues las propias.
	var files []string
	for _, dir := range []string{"platform", "mail-directory", "mail-security"} {
		found, err := filepath.Glob(filepath.Join(root, "migrations/cell/canonical", dir, "*.sql"))
		if err != nil || len(found) == 0 {
			t.Fatalf("migraciones de %s: %v", dir, err)
		}
		sort.Strings(found)
		files = append(files, found...)
	}
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
		 DELETE FROM mail_security.forwarding_hosts; DELETE FROM mail_security.mailbox_tags; DELETE FROM mail_security.domain_footers;
		 DELETE FROM mail_security.smtp_access_networks; DELETE FROM mail_security.firewall_networks;
		 DELETE FROM mail_security.firewall_options; DELETE FROM mail_security.engine_documents; DELETE FROM platform.event_outbox`,
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
			"ventas+tag@ACME.com": {"ana@acme.com", "luis@acme.com"},
			"solo@acme-alias.com": {"ana@acme.com"},
			"cualquiera@acme.com": {"ana@acme.com"},
			"sala@acme.com":       nil,
			"bucle1@acme.com":     nil,
			"x@desconocido.com":   nil,
			"pepe@otra.com":       {"pepe@otra.com"},
			"alguien@baja.com":    nil,
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
		states, err := directory.DomainStates(engineCtx, []string{"otra.com", "baja.com", "acme-alias.com", "nadie.com"})
		if err != nil || len(states) != 2 || states["otra.com"] != (domain.DirectoryDomain{Name: "otra.com", TenantID: tenantB, Active: true}) ||
			states["baja.com"] != (domain.DirectoryDomain{Name: "baja.com", TenantID: tenantB}) {
			t.Errorf("estado de los dominios propios (sin dominios alias ni desconocidos): %+v %v", states, err)
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
		// La retencion de cuarentena tiene techo: lo exige el caso de uso y, por debajo, el CHECK de
		// la migracion 09. Sin el, una empresa fijaba por API lo que quisiera en la base compartida
		// de la celda y se llevaba con ello el tope que leen los motores de todas.
		muchos := make([]string, domain.MaxQuarantineExcludeDomains+1)
		for i := range muchos {
			muchos[i] = "d" + strconv.Itoa(i) + ".example"
		}
		fuera := map[string]domain.QuarantineSettings{
			"retention_size":  {MaxSizeBytes: qs.MaxSizeBytes, MaxAgeDays: qs.MaxAgeDays, RetentionSize: domain.MaxQuarantineRetentionSize + 1},
			"max_age_days":    {MaxSizeBytes: qs.MaxSizeBytes, MaxAgeDays: domain.MaxQuarantineMaxAgeDays + 1, RetentionSize: 1},
			"exclude_domains": {MaxSizeBytes: qs.MaxSizeBytes, MaxAgeDays: qs.MaxAgeDays, RetentionSize: 1, ExcludeDomains: muchos},
		}
		for campo, bad := range fuera {
			if _, err := policy.PutQuarantineSettings(ctxA, tenantA, bad); !errors.Is(err, domain.ErrValidation) {
				t.Fatalf("%s por encima del techo: %v", campo, err)
			}
		}
		// Y la base lo rechaza aunque se escriba sin pasar por el caso de uso.
		for campo, valor := range map[string]int{
			"retention_size": domain.MaxQuarantineRetentionSize + 1,
			"max_age_days":   domain.MaxQuarantineMaxAgeDays + 1,
		} {
			if _, err := pool.Exec(ctx, "UPDATE mail_security.quarantine_settings SET "+campo+" = $1 WHERE tenant_id = $2",
				valor, tenantA); err == nil {
				t.Fatalf("el CHECK de la migracion 09 debe rechazar %s = %d", campo, valor)
			}
		}
		if got, err := policy.GetQuarantineSettings(ctxA, tenantA); err != nil || got.RetentionSize != 2 {
			t.Fatalf("los ajustes validos siguen en pie tras los rechazos: %+v %v", got, err)
		}
		// Redes SMTP: cidr en la base, en la forma de SMTP_ACCESS al leer.
		access, err := policy.PutSMTPAccess(ctxA, tenantA, "Ana@acme.com", []string{"203.0.113.9/24", "198.51.100.7", "198.51.100.7/32"})
		if err != nil || strings.Join(access.Networks, ",") != "198.51.100.7,203.0.113.0/24" {
			t.Fatalf("redes SMTP: %+v %v", access, err)
		}
		if got, err := policy.GetSMTPAccess(ctxA, tenantA, "luis@acme.com"); err != nil || len(got.Networks) != 0 {
			t.Fatalf("un buzon propio sin redes no tiene restriccion: %+v %v", got, err)
		}
		if _, err := policy.PutSMTPAccess(ctxB, tenantB, "ana@acme.com", []string{"10.0.0.0/8"}); err != domain.ErrObjectNotOwned {
			t.Fatalf("un buzon ajeno se rechaza: %v", err)
		}
		if _, err := policy.GetSMTPAccess(ctxB, tenantB, "ana@acme.com"); err != domain.ErrNotFound {
			t.Fatalf("la otra empresa no ve las redes: %v", err)
		}
		if list, err := policy.ListSMTPAccess(ctxB, tenantB); err != nil || len(list) != 0 {
			t.Fatalf("aislamiento de redes SMTP: %+v %v", list, err)
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

	newEngine := func() *app.EngineUseCase {
		return app.NewEngineUseCase(app.EngineDeps{Tx: ctxPool, Documents: NewDocumentRepository(ctxPool),
			Directory: directory, Policy: reader, Quarantine: quarantine,
			Sync: app.NewRedisSync(apptest.NewStore(), directory, reader, logger), Store: apptest.NewStore(),
			Events: outboxadapter.NewPublisher(ctxPool), Logger: logger, LogLines: 10})
	}

	t.Run("documento de settings desde la base", func(t *testing.T) {
		engine := newEngine()
		doc, _, err := engine.Settings(engineCtx, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		// Otra replica sobre la misma base responde con la misma marca: 304 a quien ya
		// tiene el documento.
		doc2, notModified, err := newEngine().Settings(engineCtx, doc.LastModified)
		if err != nil || !notModified || !doc2.LastModified.Equal(doc.LastModified) {
			t.Fatalf("replica: %v 304=%v %v vs %v", err, notModified, doc2.LastModified, doc.LastModified)
		}
		// La marca no es de ninguna empresa: el rol de la aplicacion no la ve.
		if err := ctxPool.TransactRLS(adminCtx(pool, tenantA), func(ctx context.Context) error {
			var n int
			return ctxPool.QueryRow(ctx, `SELECT count(*) FROM mail_security.engine_documents`).Scan(&n)
		}); err == nil {
			t.Fatal("mail_app no debe poder leer engine_documents")
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
		// Cada fila guardada encolo su evento en la misma transaccion (la poda no los
		// retira: el evento dice que se guardo, no que siga ahi).
		var stored int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.event_outbox
			WHERE subject = 'mail_security.quarantine.stored' AND tenant_id = $1 AND payload->'data'->>'qid' = 'ABC123'`, tenantA).Scan(&stored); err != nil || stored != 6 {
			t.Fatalf("eventos de cuarentena: %d %v", stored, err)
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

	t.Run("liberacion por la outbox", func(t *testing.T) {
		reinj := &reinyectorFalso{}
		release := app.NewQuarantineUseCase(app.QuarantineDeps{Tx: ctxPool, Repo: quarantine, Reinjector: reinj,
			Events: outboxadapter.NewPublisher(ctxPool), Logger: logger})
		items, _, err := quarantine.List(adminCtx(pool, tenantA), tenantA, domain.QuarantineFilter{Page: 1, PerPage: 50})
		if err != nil || len(items) < 2 {
			t.Fatalf("filas para liberar: %d %v", len(items), err)
		}
		released := func(id uuid.UUID) (rows, events int) {
			t.Helper()
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM mail_security.quarantine WHERE id = $1),
				(SELECT count(*) FROM platform.event_outbox WHERE subject = 'mail_security.quarantine.released' AND payload->'data'->>'id' = $1::text)`,
				id).Scan(&rows, &events); err != nil {
				t.Fatal(err)
			}
			return rows, events
		}

		// Bajo mail_app: reinyecta, borra y encola en la misma transaccion.
		if err := release.Release(adminCtx(pool, tenantA), tenantA, items[0].ID, uuid.New().String()); err != nil {
			t.Fatalf("liberar: %v", err)
		}
		if r, e := released(items[0].ID); r != 0 || e != 1 || reinj.entregados != 1 {
			t.Fatalf("liberada: filas=%d eventos=%d entregas=%d", r, e, reinj.entregados)
		}
		// La otra empresa no libera lo ajeno.
		if err := release.Release(adminCtx(pool, tenantB), tenantB, items[1].ID, ""); err != domain.ErrNotFound {
			t.Fatalf("liberacion ajena: %v", err)
		}
		// Fallo inyectado en la outbox: la fila sigue y no queda evento.
		if _, err := pool.Exec(ctx, `REVOKE INSERT ON platform.event_outbox FROM mail_app`); err != nil {
			t.Fatal(err)
		}
		grant, err := os.ReadFile(filepath.Join(repoRoot(t), "migrations/cell/canonical/mail-security/02_outbox_grants.sql"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), string(grant)) })
		if err := release.Release(adminCtx(pool, tenantA), tenantA, items[1].ID, ""); err == nil {
			t.Fatal("sin outbox la liberacion no se confirma")
		}
		if r, e := released(items[1].ID); r != 1 || e != 0 {
			t.Fatalf("liberacion revertida: filas=%d eventos=%d", r, e)
		}
		if _, err := pool.Exec(ctx, string(grant)); err != nil {
			t.Fatal(err)
		}
		if err := release.Release(adminCtx(pool, tenantA), tenantA, items[1].ID, ""); err != nil {
			t.Fatalf("liberar tras restituir el permiso: %v", err)
		}
	})

	t.Run("cortafuegos de plataforma", func(t *testing.T) {
		store := apptest.NewStore()
		fw := app.NewFirewallUseCase(app.FirewallDeps{Tx: ctxPool, Repo: NewFirewallRepository(ctxPool), Policy: reader,
			Sync: app.NewRedisSync(store, directory, reader, logger), Store: store, Logger: logger})
		if _, err := fw.AddNetwork(engineCtx, false, domain.FirewallDeny, "203.0.113.0/24", ""); err != domain.ErrPlatformOnly {
			t.Fatalf("sin operador: %v", err)
		}
		n, err := fw.AddNetwork(engineCtx, true, domain.FirewallDeny, "203.0.113.7/24", "abuso")
		if err != nil || n.Network != "203.0.113.0/24" {
			t.Fatalf("alta: %+v %v", n, err)
		}
		if _, err := fw.AddNetwork(engineCtx, true, domain.FirewallAllow, "203.0.113.0/24", ""); err != domain.ErrAlreadyExists {
			t.Fatalf("red repetida: %v", err)
		}
		if _, err := fw.AddNetwork(engineCtx, true, domain.FirewallAllow, "198.51.100.7", "oficina"); err != nil {
			t.Fatal(err)
		}
		o := domain.DefaultFirewallOptions()
		o.BanTime, o.MaxBanTime = 3600, 86400
		if _, err := fw.PutOptions(engineCtx, true, o); err != nil {
			t.Fatal(err)
		}
		if _, err := fw.PutOptions(engineCtx, true, o); err != nil {
			t.Fatalf("las opciones son una fila por celda: %v", err)
		}
		got, err := fw.Options(engineCtx, true)
		if err != nil || got.BanTime != 3600 || got.UpdatedAt.IsZero() {
			t.Fatalf("opciones: %+v %v", got, err)
		}
		// El rol de la aplicacion no ve el cortafuegos, aunque sea de la empresa que opera.
		if err := ctxPool.TransactRLS(adminCtx(pool, tenantA), func(ctx context.Context) error {
			var n int
			return ctxPool.QueryRow(ctx, `SELECT count(*) FROM mail_security.firewall_networks`).Scan(&n)
		}); err == nil {
			t.Fatal("mail_app no debe poder leer firewall_networks")
		}
		if err := fw.DeleteNetwork(engineCtx, true, n.ID); err != nil {
			t.Fatal(err)
		}
		if err := fw.DeleteNetwork(engineCtx, true, n.ID); err != domain.ErrNotFound {
			t.Fatalf("baja repetida: %v", err)
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
		nets := store.Hashes[domain.SMTPAllowNetsKey("ana@acme.com")]
		if store.Hashes[domain.RedisSMTPLimitedAccess]["ana@acme.com"] != "1" || nets["198.51.100.7"] != "1" || nets["203.0.113.0/24"] != "1" {
			t.Errorf("redes SMTP en redis: %v", store.Hashes)
		}
		if store.Hashes[domain.RedisF2BWhitelist]["198.51.100.7/32"] != "1" || len(store.Hashes[domain.RedisF2BBlacklist]) != 0 ||
			!strings.Contains(store.Values[domain.RedisF2BOptions], `"ban_time":3600`) {
			t.Errorf("cortafuegos en redis: %v %v", store.Hashes, store.Values)
		}
		// Baja del buzon (evento del directorio): sus redes desaparecen de la base.
		if err := reader.DeleteSMTPAccessByUsername(engineCtx, "ana@acme.com"); err != nil {
			t.Fatal(err)
		}
		if all, err := reader.AllSMTPAccess(engineCtx); err != nil || len(all) != 0 {
			t.Errorf("redes tras la baja: %+v %v", all, err)
		}
	})
}

type reinyectorFalso struct{ entregados int }

func (r *reinyectorFalso) Reinject(context.Context, string, string, []byte) error {
	r.entregados++
	return nil
}

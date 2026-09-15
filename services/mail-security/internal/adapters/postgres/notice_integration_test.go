//go:build integration

// Aviso de cuarentena y enlaces sin sesion contra un Postgres real, con las migraciones de
// la celda aplicadas dos veces (applyMigrations): el marcado de notified va en la misma
// transaccion que el registro del aviso, y un enlace es de un solo uso aunque lleguen dos
// peticiones a la vez.
//
//	docker run -d --name ms-pg -e POSTGRES_PASSWORD=t -e POSTGRES_USER=t -e POSTGRES_DB=cell -p 27432:5432 pgvector/pgvector:pg16
//	MAIL_SECURITY_TEST_DSN='postgres://t:t@127.0.0.1:27432/cell?sslmode=disable' go test -tags integration -run Aviso ./services/mail-security/...
package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/db"
	handler "github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/http"
	outboxadapter "github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

const (
	integrationLinkKey = "clave-de-firma-de-integracion-con-mas-de-32-caracteres"
	integrationCell    = "pe-01"
)

const integrationTemplate = `<p>{{.Count}} mensajes para {{.Mailbox}}</p>{{range .Messages}}<p>{{.Subject}}
<a href="{{.ReleaseURL}}">Liberar</a> <a href="{{.DiscardURL}}">Descartar</a></p>{{end}}`

// reinyectorLento tarda en entregar para que dos liberaciones concurrentes se solapen.
type reinyectorLento struct{ entregas atomic.Int32 }

func (r *reinyectorLento) Reinject(context.Context, string, string, []byte) error {
	time.Sleep(300 * time.Millisecond)
	r.entregas.Add(1)
	return nil
}

func insertQuarantined(t *testing.T, ctx context.Context, repo *QuarantineRepository, tenant uuid.UUID, rcpt, subject string, score int64, age time.Duration) domain.QuarantineItem {
	t.Helper()
	id := uuid.New()
	sum := sha256.Sum256([]byte(id.String() + "QID"))
	it := domain.QuarantineItem{ID: id, TenantID: tenant, QID: "QID", Subject: subject, Score: decimal.NewFromInt(score),
		Action: "reject", Symbols: []string{}, FuzzyHashes: []string{}, Sender: "spam@x.test", Rcpt: rcpt,
		Domain: "acme.com", Msg: []byte("Subject: " + subject + "\r\n\r\nhola\r\n"), QHash: hex.EncodeToString(sum[:]),
		CreatedAt: time.Now().Add(-age)}
	if err := repo.Insert(ctx, &it); err != nil {
		t.Fatal(err)
	}
	return it
}

func count(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func TestIntegracionAvisoDeCuarentena(t *testing.T) {
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
	for _, s := range []string{`DELETE FROM mail_security.quarantine_notices`, `DELETE FROM mail_security.quarantine_link_uses`} {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	ctxPool := &db.ContextPool{}
	engineCtx := db.WithPool(ctx, pool)
	quarantine := NewQuarantineRepository(ctxPool)
	notices := NewQuarantineNoticeRepository(ctxPool)
	logger := zap.NewNop()
	links, err := domain.NewQuarantineLinkSigner(integrationLinkKey, "https://app.example.com", integrationCell, 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO mail_security.quarantine_settings (tenant_id, notify_enabled, notify_max_score, notify_sender, notify_subject, notify_html_template)
		VALUES ($1, true, 15, 'cuarentena@acme.com', 'Correo retenido', $2)`, tenantA, integrationTemplate); err != nil {
		t.Fatal(err)
	}

	ana1 := insertQuarantined(t, engineCtx, quarantine, tenantA, "ana@acme.com", "Factura", 5, 2*time.Hour)
	ana2 := insertQuarantined(t, engineCtx, quarantine, tenantA, "ana@acme.com", "Oferta", 8, time.Hour)
	luis := insertQuarantined(t, engineCtx, quarantine, tenantA, "luis@acme.com", "Reunion", 3, time.Hour)
	spammy := insertQuarantined(t, engineCtx, quarantine, tenantA, "ana@acme.com", "Casino", 40, time.Minute)
	other := insertQuarantined(t, engineCtx, quarantine, tenantB, "pepe@otra.com", "Ajeno", 1, time.Minute)

	sender := &apptest.NoticeSender{}
	notifier := app.NewQuarantineNotifier(app.NotifierDeps{Tx: ctxPool, Policy: NewPolicyReader(ctxPool), Notices: notices,
		Directory: NewDirectoryRepository(ctxPool), Sender: sender, Links: links, Interval: time.Minute, Logger: logger})
	notified := func(ids ...uuid.UUID) int {
		return count(t, ctx, pool, `SELECT count(*) FROM mail_security.quarantine WHERE notified AND id = ANY($1::uuid[])`, ids)
	}

	t.Run("marcado en la misma transaccion que el registro", func(t *testing.T) {
		// Fallo inyectado en el registro del aviso, DESPUES del UPDATE de notified.
		for _, s := range []string{
			`CREATE OR REPLACE FUNCTION public.test_fallo_aviso() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fallo inyectado'; END $$`,
			`DROP TRIGGER IF EXISTS test_fallo_aviso ON mail_security.quarantine_notices`,
			`CREATE TRIGGER test_fallo_aviso BEFORE INSERT ON mail_security.quarantine_notices FOR EACH ROW EXECUTE FUNCTION public.test_fallo_aviso()`,
		} {
			if _, err := pool.Exec(ctx, s); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS test_fallo_aviso ON mail_security.quarantine_notices`)
			_, _ = pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS public.test_fallo_aviso()`)
		})

		res, err := notifier.Sweep(engineCtx)
		if err != nil || res != (app.SweepResult{Retry: 2}) || len(sender.Sent) != 2 {
			t.Fatalf("con el registro fallando: %+v %v enviados=%d", res, err, len(sender.Sent))
		}
		if n := notified(ana1.ID, ana2.ID, luis.ID); n != 0 {
			t.Fatalf("el UPDATE de notified se deshace con el registro: %d marcadas", n)
		}
		if n := count(t, ctx, pool, `SELECT count(*) FROM mail_security.quarantine_notices`); n != 0 {
			t.Fatalf("avisos registrados: %d", n)
		}

		if _, err := pool.Exec(ctx, `DROP TRIGGER test_fallo_aviso ON mail_security.quarantine_notices`); err != nil {
			t.Fatal(err)
		}
		res, err = notifier.Sweep(engineCtx)
		if err != nil || res != (app.SweepResult{Sent: 2}) {
			t.Fatalf("tras quitar el fallo: %+v %v", res, err)
		}
		if sender.Sent[0].IdempotencyKey != "quarantine-notice:ana@acme.com:"+ana2.ID.String() || sender.Sent[2].IdempotencyKey != sender.Sent[0].IdempotencyKey ||
			sender.Sent[3].IdempotencyKey != sender.Sent[1].IdempotencyKey {
			t.Fatalf("el reintento repite la clave: %q %q", sender.Sent[0].IdempotencyKey, sender.Sent[2].IdempotencyKey)
		}
		if n := notified(ana1.ID, ana2.ID, luis.ID); n != 3 {
			t.Fatalf("marcadas: %d", n)
		}
		if n := notified(spammy.ID, other.ID); n != 0 {
			t.Fatal("ni lo que supera el umbral ni otra empresa")
		}
		if n := count(t, ctx, pool, `SELECT count(*) FROM mail_security.quarantine_notices
			WHERE tenant_id = $1 AND status = 'sent' AND message_id IS NOT NULL AND cardinality(quarantine_ids) >= 1`, tenantA); n != 2 {
			t.Fatalf("avisos registrados: %d", n)
		}
		if res, _ := notifier.Sweep(engineCtx); res != (app.SweepResult{}) {
			t.Fatalf("un tercer barrido no repite: %+v", res)
		}
	})

	t.Run("enlace de un solo uso con peticiones concurrentes", func(t *testing.T) {
		reinj := &reinyectorLento{}
		uc := app.NewQuarantineUseCase(app.QuarantineDeps{Tx: ctxPool, Repo: quarantine, Reinjector: reinj,
			Events: outboxadapter.NewPublisher(ctxPool), Notices: notices, Links: links, Logger: logger})
		srv := httptest.NewServer(db.StaticPoolMiddleware(pool)(handler.NewHandler(nil, uc, nil, nil, authz.NewChecker("http://127.0.0.1:9", "")).Routes()))
		defer srv.Close()
		target := func(it domain.QuarantineItem, action domain.QuarantineLinkAction) string {
			raw := links.URL(domain.QuarantineLinkClaims{TenantID: it.TenantID, MessageID: it.ID, Action: action,
				ExpiresAt: time.Now().Add(time.Hour).Unix()}, it.QHash)
			u, _ := url.Parse(raw)
			return srv.URL + u.RequestURI()
		}
		post := func(targets ...string) []int {
			statuses := make([]int, len(targets))
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i, tg := range targets {
				wg.Add(1)
				go func(i int, tg string) {
					defer wg.Done()
					<-start
					resp, err := http.Post(tg, "application/x-www-form-urlencoded", nil)
					if err != nil {
						t.Error(err)
						return
					}
					_, _ = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					statuses[i] = resp.StatusCode
				}(i, tg)
			}
			close(start)
			wg.Wait()
			return statuses
		}
		oneWinner := func(name string, statuses []int) {
			t.Helper()
			ok, forbidden := 0, 0
			for _, s := range statuses {
				switch s {
				case http.StatusOK:
					ok++
				case http.StatusForbidden:
					forbidden++
				}
			}
			if ok != 1 || forbidden != len(statuses)-1 {
				t.Fatalf("%s: una sola peticion gana: %v", name, statuses)
			}
		}

		if resp, err := http.Get(target(ana2, domain.LinkRelease)); err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("pagina del enlace: %v %v", resp, err)
		}
		oneWinner("liberar dos veces", post(target(ana2, domain.LinkRelease), target(ana2, domain.LinkRelease)))
		if reinj.entregas.Load() != 1 {
			t.Fatalf("una sola entrega: %d", reinj.entregas.Load())
		}
		if n := count(t, ctx, pool, `SELECT count(*) FROM mail_security.quarantine WHERE id = $1`, ana2.ID); n != 0 {
			t.Fatal("la fila liberada se borra")
		}
		if n := count(t, ctx, pool, `SELECT count(*) FROM mail_security.quarantine_link_uses WHERE quarantine_id = $1 AND action = 'release'`, ana2.ID); n != 1 {
			t.Fatalf("usos registrados: %d", n)
		}
		if n := count(t, ctx, pool, `SELECT count(*) FROM platform.event_outbox
			WHERE subject = 'mail_security.quarantine.released' AND payload->'data'->>'id' = $1::text`, ana2.ID); n != 1 {
			t.Fatalf("un solo evento de liberacion: %d", n)
		}
		if resp, err := http.Get(target(ana2, domain.LinkDiscard)); err != nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("el otro enlace del mensaje liberado: %v %v", resp, err)
		}

		oneWinner("liberar y descartar a la vez", post(target(ana1, domain.LinkRelease), target(ana1, domain.LinkDiscard)))
		if n := count(t, ctx, pool, `SELECT count(*) FROM mail_security.quarantine_link_uses WHERE quarantine_id = $1`, ana1.ID); n != 1 {
			t.Fatalf("un solo uso por mensaje: %d", n)
		}

		oneWinner("descartar tres veces", post(target(luis, domain.LinkDiscard), target(luis, domain.LinkDiscard), target(luis, domain.LinkDiscard)))
		if n := count(t, ctx, pool, `SELECT count(*) FROM mail_security.quarantine WHERE id = $1`, luis.ID); n != 0 {
			t.Fatal("la fila descartada se borra")
		}

		// Un enlace de la empresa B con la empresa A en la URL no encuentra nada.
		forged := target(other, domain.LinkDiscard)
		forged = forged[:len(forged)-len(other.TenantID.String())] + tenantA.String()
		if resp, err := http.Post(forged, "application/x-www-form-urlencoded", nil); err != nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("empresa cambiada en el enlace: %v %v", resp, err)
		}
		if n := count(t, ctx, pool, `SELECT count(*) FROM mail_security.quarantine WHERE id = $1`, other.ID); n != 1 {
			t.Fatal("el mensaje de la otra empresa sigue")
		}

		// Firmado por otra celda con la misma clave y el segmento cambiado a esta: no vale.
		pe02, err := domain.NewQuarantineLinkSigner(integrationLinkKey, "https://app.example.com", "pe-02", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		claims := domain.QuarantineLinkClaims{TenantID: spammy.TenantID, MessageID: spammy.ID, Action: domain.LinkDiscard, ExpiresAt: time.Now().Add(time.Hour).Unix()}
		u, _ := url.Parse(pe02.URL(claims, spammy.QHash))
		tampered := srv.URL + strings.Replace(u.RequestURI(), "/pe-02/", "/"+integrationCell+"/", 1)
		if resp, err := http.Post(tampered, "application/x-www-form-urlencoded", nil); err != nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("enlace de otra celda con el segmento cambiado: %v %v", resp, err)
		}
		if n := count(t, ctx, pool, `SELECT count(*) FROM mail_security.quarantine WHERE id = $1`, spammy.ID); n != 1 {
			t.Fatal("el enlace de otra celda no toca nada")
		}

		// El intento no gasta el enlace: el de esta celda sigue descartando.
		oneWinner("descartar tras el enlace de otra celda", post(target(spammy, domain.LinkDiscard), target(spammy, domain.LinkDiscard)))
		if n := count(t, ctx, pool, `SELECT count(*) FROM mail_security.quarantine WHERE id = $1`, spammy.ID); n != 0 {
			t.Fatal("el enlace de esta celda descarta")
		}

		// La constancia se poda con la retencion de la empresa.
		if _, err := pool.Exec(ctx, `UPDATE mail_security.quarantine_link_uses SET used_at = now() - interval '400 days' WHERE quarantine_id = $1`, luis.ID); err != nil {
			t.Fatal(err)
		}
		if n, err := notices.PruneHistory(engineCtx, domain.DefaultQuarantineMaxAgeDays); err != nil || n != 1 {
			t.Fatalf("poda del historial: %d %v", n, err)
		}
	})
}

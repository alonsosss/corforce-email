//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
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

type webmailFixture struct {
	ctx       context.Context
	pool      *pgxpool.Pool
	uc        *app.UseCase
	internal  context.Context
	ctxB      context.Context
	tenantA   uuid.UUID
	tenantB   uuid.UUID
	ana, luis *domain.Mailbox
	domainA   string
	suffix    string
}

// newWebmailFixture prepara dos empresas con un buzon cada una. internal es el contexto de una ruta
// interna del webmail: el pool y ningun usuario, asi que la transaccion no cambia a mail_app.
func newWebmailFixture(t *testing.T) *webmailFixture {
	t.Helper()
	dsn := integrationEnv(t, "MAIL_DIRECTORY_TEST_DSN")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	applyCellMigrations(t, ctx, pool)

	f := &webmailFixture{ctx: ctx, pool: pool, uc: newUseCase(&db.ContextPool{}), tenantA: uuid.New(), tenantB: uuid.New()}
	f.suffix = strings.Split(uuid.New().String(), "-")[0]
	f.domainA = "wm-" + f.suffix + "-a.example"
	domainB := "wm-" + f.suffix + "-b.example"
	t.Cleanup(func() { cleanup(t, pool, f.domainA, domainB, f.tenantA, f.tenantB) })
	as := func(tenant uuid.UUID) context.Context {
		return middleware.WithIdentity(db.WithPool(ctx, pool), uuid.New().String(), tenant.String())
	}
	ctxA := as(f.tenantA)
	f.ctxB = as(f.tenantB)
	f.internal = db.WithPool(ctx, pool)
	quota := int64(50 << 20)
	mk := func(c context.Context, tenant uuid.UUID, dom, local string) *domain.Mailbox {
		if _, err := f.uc.SetDomainActivation(c, tenant, dom, true); err != nil {
			t.Fatalf("activar %s: %v", dom, err)
		}
		m, err := f.uc.CreateMailbox(c, tenant, app.CreateMailboxRequest{
			LocalPart: local, Domain: dom, Password: "contrasena-de-prueba-1", DisplayName: local, QuotaBytes: &quota,
		})
		if err != nil {
			t.Fatalf("buzon de %s: %v", dom, err)
		}
		return m
	}
	f.ana = mk(ctxA, f.tenantA, f.domainA, "ana")
	f.luis = mk(f.ctxB, f.tenantB, domainB, "luis")
	return f
}

// asRole corre una consulta bajo un rol y, si se da, una empresa: es lo que ven mail_app y mail_engine.
func (f *webmailFixture) asRole(t *testing.T, role string, tenant uuid.UUID, sql string, args ...any) (int, error) {
	t.Helper()
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	if _, err := tx.Exec(f.ctx, "SET LOCAL ROLE "+role); err != nil {
		t.Fatal(err)
	}
	if tenant != uuid.Nil {
		if _, err := tx.Exec(f.ctx, `SELECT set_config('app.current_tenant_id', $1, true)`, tenant.String()); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	err = tx.QueryRow(f.ctx, sql, args...).Scan(&n)
	return n, err
}

func expectSQLState(t *testing.T, err error, code, what string) {
	t.Helper()
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != code {
		t.Errorf("%s: se esperaba SQLSTATE %s y salio %v", what, code, err)
	}
}

func TestFirmaYReglasContraPostgres(t *testing.T) {
	f := newWebmailFixture(t)

	// ── Firma: una fila por buzon, aislada por empresa ─────────────────────────
	if _, err := f.uc.PutSignatureByUsername(f.internal, f.ana.Username, app.PutSignatureRequest{Enabled: true, HTML: "<p>Ana</p>", Text: "Ana"}); err != nil {
		t.Fatalf("guardar firma: %v", err)
	}
	if _, err := f.uc.PutSignatureByUsername(f.internal, f.ana.Username, app.PutSignatureRequest{Enabled: true, HTML: "<p>Ana 2</p>", Text: "Ana 2", OnReplies: true}); err != nil {
		t.Fatalf("reemplazar firma: %v", err)
	}
	sig, err := f.uc.SignatureByUsername(f.internal, f.ana.Username)
	if err != nil || sig.HTML != "<p>Ana 2</p>" || !sig.OnReplies || sig.UpdatedAt.IsZero() {
		t.Fatalf("releer firma: %+v %v", sig, err)
	}
	if n, err := f.asRole(t, "mail_app", f.tenantB, `SELECT count(*) FROM mail.mailbox_signatures WHERE username = $1`, f.ana.Username); err != nil || n != 0 {
		t.Fatalf("mail_app de B ve la firma de A: %d %v", n, err)
	}
	if n, err := f.asRole(t, "mail_app", f.tenantA, `SELECT count(*) FROM mail.mailbox_signatures WHERE username = $1`, f.ana.Username); err != nil || n != 1 {
		t.Fatalf("mail_app de A no ve la suya: %d %v", n, err)
	}
	_, err = f.asRole(t, "mail_engine", uuid.Nil, `SELECT count(*) FROM mail.mailbox_signatures`)
	expectSQLState(t, err, "42501", "mail_engine lee las firmas")

	// ── Reglas: jsonb de ida y vuelta, y la vista que lee Dovecot ──────────────
	req := app.PutFiltersRequest{
		Rules: []domain.FilterRule{{Name: `Cliente "VIP"`, Enabled: true, Match: domain.FilterMatchAny,
			Conditions: []domain.FilterCondition{{Field: "from", Op: "contains", Value: `a\b"c`}, {Field: "subject", Op: "is", Value: "Pedido"}},
			Actions:    []domain.FilterAction{{Type: "move", Folder: "Clientes/VIP"}, {Type: "flag"}}, Stop: true}},
		Forwarding: domain.Forwarding{Enabled: true, Addresses: []string{"copia@otro.example"}, KeepCopy: true},
	}
	saved, err := f.uc.PutFiltersByUsername(f.internal, f.ana.Username, req)
	if err != nil {
		t.Fatalf("guardar reglas: %v", err)
	}
	got, err := f.uc.FiltersByUsername(f.internal, f.ana.Username)
	if err != nil || len(got.Rules) != 1 || got.Rules[0].ID != saved.Rules[0].ID || got.Rules[0].Conditions[0].Value != `a\b"c` ||
		got.Rules[0].Actions[0].Folder != "Clientes/VIP" || !got.Forwarding.KeepCopy || got.ScriptData != saved.ScriptData {
		t.Fatalf("releer reglas: %+v %v", got, err)
	}
	viewRows := func(username string) int {
		n, err := f.asRole(t, "mail_engine", uuid.Nil,
			`SELECT count(*) FROM mail.v_sieve_user WHERE username = $1 AND script_name = 'active' AND id = md5(script_data)
			    AND script_data LIKE '%if not header :contains "X-Spam-Flag" "YES"%'`, username)
		if err != nil {
			t.Fatalf("mail_engine sobre v_sieve_user: %v", err)
		}
		return n
	}
	if viewRows(f.ana.Username) != 1 {
		t.Fatal("las reglas activas de A deben verse en v_sieve_user con el formato del diccionario")
	}
	if viewRows(f.luis.Username) != 0 {
		t.Fatal("un buzon sin reglas no aparece en la vista")
	}
	_, err = f.asRole(t, "mail_engine", uuid.Nil, `SELECT count(*) FROM mail.mailbox_filters`)
	expectSQLState(t, err, "42501", "mail_engine lee la tabla de reglas")
	if n, err := f.asRole(t, "mail_app", f.tenantB, `SELECT count(*) FROM mail.mailbox_filters WHERE username = $1`, f.ana.Username); err != nil || n != 0 {
		t.Fatalf("mail_app de B ve las reglas de A: %d %v", n, err)
	}
	if _, err := f.uc.PutFiltersByUsername(f.internal, f.ana.Username, app.PutFiltersRequest{}); err != nil {
		t.Fatal(err)
	}
	if viewRows(f.ana.Username) != 0 {
		t.Fatal("sin reglas activas el buzon sale de la vista")
	}

	// ── Restricciones de la tabla ──────────────────────────────────────────────
	user := "chk-" + f.suffix + "@" + f.domainA
	_, err = f.pool.Exec(f.ctx, `INSERT INTO mail.mailbox_filters (tenant_id, username, rules) VALUES ($1, $2, '{}'::jsonb)`, f.tenantA, user)
	expectSQLState(t, err, "23514", "reglas que no son un array")
	_, err = f.pool.Exec(f.ctx, `INSERT INTO mail.mailbox_filters (tenant_id, username, script_data) VALUES ($1, $2, repeat('a', 1048577))`, f.tenantA, user)
	expectSQLState(t, err, "23514", "script mayor que sieve_max_script_size")
	_, err = f.pool.Exec(f.ctx, `INSERT INTO mail.mailbox_signatures (tenant_id, username, enabled) VALUES ($1, $2, true)`, f.tenantA, user)
	expectSQLState(t, err, "23514", "firma activa vacia")

	// ── Contrasena del propio buzon: mismo camino que la del administrador ─────
	if err := f.uc.SetPasswordByUsername(f.internal, f.ana.Username, "otra-contrasena-larga-9"); err != nil {
		t.Fatalf("cambio de contrasena: %v", err)
	}
	var events int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM platform.event_outbox WHERE tenant_id = $1 AND subject = 'mail.mailbox.credentials_changed'`, f.tenantA).Scan(&events); err != nil || events != 1 {
		t.Fatalf("evento de credenciales en la outbox: %d %v", events, err)
	}

	// ── Borrar el buzon borra sus filas ────────────────────────────────────────
	if _, err := f.uc.PutSignatureByUsername(f.internal, f.luis.Username, app.PutSignatureRequest{Enabled: true, Text: "Luis"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.PutFiltersByUsername(f.internal, f.luis.Username, app.PutFiltersRequest{Forwarding: domain.Forwarding{Enabled: true, Addresses: []string{"x@otro.example"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.CreateScheduledSend(f.internal, app.CreateScheduledSendRequest{Username: f.luis.Username, MessageID: "<del@x>", Folder: "Scheduled",
		UIDValidity: 1, UID: 1, SendAt: time.Now().Add(time.Hour), Recipients: []string{"a@b.example"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.uc.DeleteMailbox(f.ctxB, f.tenantB, f.luis.ID); err != nil {
		t.Fatalf("borrar buzon: %v", err)
	}
	var rows int
	if err := f.pool.QueryRow(f.ctx,
		`SELECT (SELECT count(*) FROM mail.mailbox_signatures WHERE username = $1) + (SELECT count(*) FROM mail.mailbox_filters WHERE username = $1)
		      + (SELECT count(*) FROM mail.scheduled_sends WHERE username = $1)`, f.luis.Username).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("filas que sobrevivieron al buzon: %d %v", rows, err)
	}
}

func TestEnviosProgramadosContraPostgres(t *testing.T) {
	f := newWebmailFixture(t)
	create := func(messageID string, sendAt time.Time) *domain.ScheduledSend {
		t.Helper()
		s, err := f.uc.CreateScheduledSend(f.internal, app.CreateScheduledSendRequest{
			Username: f.ana.Username, MessageID: messageID, Folder: "Scheduled", UIDValidity: 4294967295, UID: 17,
			SendAt: sendAt, Subject: "Propuesta", Recipients: []string{"cliente@otro.example", "José@correo.example"},
		})
		if err != nil {
			t.Fatalf("crear %s: %v", messageID, err)
		}
		return s
	}
	future := create("<futuro-"+f.suffix+"@x>", time.Now().Add(time.Hour))
	if future.UIDValidity != 4294967295 || len(future.Recipients) != 2 || future.CreatedAt.IsZero() {
		t.Fatalf("fila creada: %+v", future)
	}
	if _, err := f.uc.CreateScheduledSend(f.internal, app.CreateScheduledSendRequest{
		Username: f.ana.Username, MessageID: future.MessageID, Folder: "Scheduled", UIDValidity: 1, UID: 2,
		SendAt: time.Now().Add(time.Hour), Recipients: []string{"a@b.example"},
	}); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("el mismo mensaje activo no se programa dos veces: %v", err)
	}
	if n, err := f.asRole(t, "mail_app", f.tenantB, `SELECT count(*) FROM mail.scheduled_sends WHERE id = $1`, future.ID); err != nil || n != 0 {
		t.Fatalf("mail_app de B ve el envio de A: %d %v", n, err)
	}
	_, err := f.asRole(t, "mail_engine", uuid.Nil, `SELECT count(*) FROM mail.scheduled_sends`)
	expectSQLState(t, err, "42501", "mail_engine lee los envios programados")

	// ── Reclamacion concurrente: cada fila vencida la toma un solo trabajador ──
	const due = 24
	mine := map[uuid.UUID]bool{}
	for i := 0; i < due; i++ {
		s := create("<vencido-"+f.suffix+"-"+uuid.NewString()+"@x>", time.Now().Add(-30*time.Second))
		mine[s.ID] = true
	}
	var (
		mu      sync.Mutex
		claimed = map[uuid.UUID]int{}
		wg      sync.WaitGroup
		start   = make(chan struct{})
		errs    = make(chan error, 6)
	)
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for {
				rows, err := f.uc.ClaimScheduledSends(f.internal, 3, 60)
				if err != nil {
					errs <- err
					return
				}
				if len(rows) == 0 {
					return
				}
				mu.Lock()
				for _, r := range rows {
					claimed[r.ID]++
					if r.Status != domain.ScheduledSending || r.Attempts != 1 || r.LeaseUntil == nil {
						errs <- errors.New("fila reclamada sin estado, intento o arriendo: " + r.ID.String())
					}
				}
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for id := range mine {
		if claimed[id] != 1 {
			t.Fatalf("la fila %s se reclamo %d veces", id, claimed[id])
		}
	}
	if claimed[future.ID] != 0 {
		t.Fatal("una fila futura no se reclama")
	}

	// ── Cierre: reintento con espera, enviado, y una fila no reclamada no se cierra ──
	var retried, sent uuid.UUID
	for id := range mine {
		if retried == uuid.Nil {
			retried = id
		} else if sent == uuid.Nil {
			sent = id
			break
		}
	}
	r, err := f.uc.FinishScheduledSend(f.internal, retried, domain.ScheduledOutcome{Status: domain.ScheduledFailed, Error: "421 luego", Retry: true})
	if err != nil || r.Status != domain.ScheduledPending || r.LeaseUntil != nil || !r.SendAt.After(time.Now().Add(30*time.Second)) || r.LastError != "421 luego" {
		t.Fatalf("reintento: %+v %v", r, err)
	}
	s, err := f.uc.FinishScheduledSend(f.internal, sent, domain.ScheduledOutcome{Status: domain.ScheduledSent})
	if err != nil || s.Status != domain.ScheduledSent || s.SentAt == nil {
		t.Fatalf("enviado: %+v %v", s, err)
	}
	if _, err := f.uc.FinishScheduledSend(f.internal, sent, domain.ScheduledOutcome{Status: domain.ScheduledSent}); !errors.Is(err, domain.ErrScheduledSendNotClaimed) {
		t.Fatalf("cerrar dos veces: %v", err)
	}
	if _, err := f.uc.FinishScheduledSend(f.internal, uuid.New(), domain.ScheduledOutcome{Status: domain.ScheduledSent}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("fila inexistente: %v", err)
	}
	if err := f.uc.CancelScheduledSend(f.internal, f.ana.Username, sent); !errors.Is(err, domain.ErrScheduledSendNotPending) {
		t.Fatalf("una enviada no se cancela: %v", err)
	}
	if err := f.uc.CancelScheduledSend(f.internal, f.ana.Username, retried); err != nil {
		t.Fatalf("cancelar la pendiente: %v", err)
	}

	// ── Arriendos vencidos: se reclaman mientras queden intentos; sin ellos, failed ──
	var expiredRetry, expiredDone uuid.UUID
	for id := range mine {
		if id == retried || id == sent {
			continue
		}
		if expiredRetry == uuid.Nil {
			expiredRetry = id
		} else {
			expiredDone = id
			break
		}
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE mail.scheduled_sends SET lease_until = now() - interval '1 second', attempts = 2 WHERE id = $1`, expiredRetry); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE mail.scheduled_sends SET lease_until = now() - interval '1 second', attempts = $2 WHERE id = $1`, expiredDone, domain.MaxScheduledAttempts); err != nil {
		t.Fatal(err)
	}
	// Una fila terminada hace mas que la retencion se purga en la reclamacion.
	var old uuid.UUID
	if err := f.pool.QueryRow(f.ctx,
		`INSERT INTO mail.scheduled_sends (tenant_id, username, message_id, folder, uid_validity, uid, send_at, recipients, status, sent_at, updated_at)
		 VALUES ($1, $2, '<viejo@x>', 'Scheduled', 1, 1, now() - interval '40 days', ARRAY['a@b.example'], 'sent', now() - interval '40 days', now() - interval '31 days')
		 RETURNING id`, f.tenantA, f.ana.Username).Scan(&old); err != nil {
		t.Fatal(err)
	}
	rows, err := f.uc.ClaimScheduledSends(f.internal, domain.MaxScheduledClaim, 60)
	if err != nil {
		t.Fatal(err)
	}
	var again bool
	for _, row := range rows {
		if row.ID == expiredRetry {
			again = row.Attempts == 3
		}
		if row.ID == expiredDone {
			t.Fatal("una fila sin intentos no se vuelve a reclamar")
		}
	}
	if !again {
		t.Fatalf("el arriendo vencido con intentos se reclama con un intento mas: %+v", rows)
	}
	var status, lastError string
	if err := f.pool.QueryRow(f.ctx, `SELECT status, last_error FROM mail.scheduled_sends WHERE id = $1`, expiredDone).Scan(&status, &lastError); err != nil ||
		status != domain.ScheduledFailed || lastError != domain.ScheduledLeaseExpiredError {
		t.Fatalf("arriendo vencido sin intentos: %s %q %v", status, lastError, err)
	}
	var purged int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM mail.scheduled_sends WHERE id = $1`, old).Scan(&purged); err != nil || purged != 0 {
		t.Fatalf("la fila vieja no se purgo: %d %v", purged, err)
	}

	list, err := f.uc.ListScheduledSends(f.internal, f.ana.Username)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range list {
		if row.Status == domain.ScheduledSent || row.Status == domain.ScheduledCanceled {
			t.Fatalf("el listado no trae enviadas ni canceladas: %+v", row)
		}
	}
	for i := 1; i < len(list); i++ {
		if list[i].SendAt.Before(list[i-1].SendAt) {
			t.Fatal("el listado va por hora de envio")
		}
	}

	// ── Restricciones de la tabla ──────────────────────────────────────────────
	_, err = f.pool.Exec(f.ctx,
		`INSERT INTO mail.scheduled_sends (tenant_id, username, message_id, folder, uid_validity, uid, send_at, recipients, status)
		 VALUES ($1, $2, '<x@y>', 'Scheduled', 1, 1, now(), ARRAY['a@b.example'], 'sending')`, f.tenantA, f.ana.Username)
	expectSQLState(t, err, "23514", "en curso sin arriendo")
	_, err = f.pool.Exec(f.ctx,
		`INSERT INTO mail.scheduled_sends (tenant_id, username, message_id, folder, uid_validity, uid, send_at, recipients)
		 VALUES ($1, $2, '<x@y>', 'Scheduled', 0, 1, now(), ARRAY['a@b.example'])`, f.tenantA, f.ana.Username)
	expectSQLState(t, err, "23514", "uid_validity cero")
	_, err = f.pool.Exec(f.ctx,
		`INSERT INTO mail.scheduled_sends (tenant_id, username, message_id, folder, uid_validity, uid, send_at, recipients, status)
		 VALUES ($1, $2, '<x@y>', 'Scheduled', 1, 1, now(), ARRAY[]::text[], 'pending')`, f.tenantA, f.ana.Username)
	expectSQLState(t, err, "23514", "sin destinatarios")
}

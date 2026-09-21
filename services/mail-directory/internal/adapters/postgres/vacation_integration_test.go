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

// La respuesta automatica contra Postgres real: aislamiento por empresa, la vista que lee Dovecot,
// lo que el rol de los motores no puede ver y las restricciones de la tabla.
func TestRespuestaAutomaticaContraPostgres(t *testing.T) {
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
	tenantA, tenantB := uuid.New(), uuid.New()
	suffix := strings.Split(uuid.New().String(), "-")[0]
	domainA, domainB := "vac-"+suffix+"-a.example", "vac-"+suffix+"-b.example"
	t.Cleanup(func() { cleanup(t, pool, domainA, domainB, tenantA, tenantB) })
	as := func(tenant uuid.UUID) context.Context {
		return middleware.WithIdentity(db.WithPool(ctx, pool), uuid.New().String(), tenant.String())
	}
	ctxA, ctxB := as(tenantA), as(tenantB)

	for _, c := range []struct {
		ctx    context.Context
		tenant uuid.UUID
		domain string
	}{{ctxA, tenantA, domainA}, {ctxB, tenantB, domainB}} {
		if _, err := uc.SetDomainActivation(c.ctx, c.tenant, c.domain, true); err != nil {
			t.Fatalf("activar %s: %v", c.domain, err)
		}
	}
	quota := int64(50 << 20)
	mkMailbox := func(c context.Context, tenant uuid.UUID, dom string) *domain.Mailbox {
		m, err := uc.CreateMailbox(c, tenant, app.CreateMailboxRequest{
			LocalPart: "ana", Domain: dom, Password: "contrasena-de-prueba-1", DisplayName: "Ana", QuotaBytes: &quota,
		})
		if err != nil {
			t.Fatalf("buzon de %s: %v", dom, err)
		}
		return m
	}
	ana, luis := mkMailbox(ctxA, tenantA, domainA), mkMailbox(ctxB, tenantB, domainB)

	// ── Guardar, releer y reemplazar: una fila por buzon ──────────────────────
	starts := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	saved, err := uc.PutMailboxVacation(ctxA, tenantA, ana.ID, app.PutVacationRequest{
		Enabled: true, Subject: "Ausente", Message: "Vuelvo el lunes.", IntervalDays: 2, StartsOn: &starts,
	})
	if err != nil || saved.ScriptData == "" {
		t.Fatalf("guardar: %v %+v", err, saved)
	}
	got, err := uc.GetMailboxVacation(ctxA, tenantA, ana.ID)
	if err != nil || !got.Enabled || got.IntervalDays != 2 || got.StartsOn == nil || got.StartsOn.Format("2006-01-02") != "2026-09-21" || got.EndsOn != nil {
		t.Fatalf("releer: %v %+v", err, got)
	}
	if _, err := uc.PutMailboxVacation(ctxA, tenantA, ana.ID, app.PutVacationRequest{Enabled: true, Message: "Segunda version."}); err != nil {
		t.Fatalf("reemplazar: %v", err)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM mail.vacation_replies WHERE username = $1`, ana.Username).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("filas por buzon: %d %v", rows, err)
	}

	// ── Otra empresa no ve ni cambia la respuesta ─────────────────────────────
	if _, err := uc.GetMailboxVacation(ctxB, tenantB, ana.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("B leyendo el buzon de A: %v", err)
	}
	if _, err := uc.PutMailboxVacation(ctxB, tenantB, ana.ID, app.PutVacationRequest{Enabled: true, Message: "intruso"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("B escribiendo en el buzon de A: %v", err)
	}
	if v, _ := uc.GetMailboxVacation(ctxA, tenantA, ana.ID); v.Message != "Segunda version." {
		t.Fatalf("la respuesta de A cambio: %q", v.Message)
	}
	// Bajo RLS, mail_app de B no llega a la fila de A ni escribiendo directo.
	asRole := func(role string, tenant uuid.UUID, sql string, args ...any) (int, error) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+role); err != nil {
			t.Fatal(err)
		}
		if tenant != uuid.Nil {
			if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant_id', $1, true)`, tenant.String()); err != nil {
				t.Fatal(err)
			}
		}
		var n int
		err = tx.QueryRow(ctx, sql, args...).Scan(&n)
		return n, err
	}
	if n, err := asRole("mail_app", tenantB, `SELECT count(*) FROM mail.vacation_replies WHERE username = $1`, ana.Username); err != nil || n != 0 {
		t.Fatalf("mail_app de B ve la fila de A: %d %v", n, err)
	}
	if n, err := asRole("mail_app", tenantA, `SELECT count(*) FROM mail.vacation_replies WHERE username = $1`, ana.Username); err != nil || n != 1 {
		t.Fatalf("mail_app de A no ve la suya: %d %v", n, err)
	}
	if n, err := asRole("mail_app", tenantB, `WITH x AS (UPDATE mail.vacation_replies SET message = 'x' WHERE username = $1 RETURNING 1) SELECT count(*) FROM x`, ana.Username); err != nil || n != 0 {
		t.Fatalf("mail_app de B modifico la fila de A: %d %v", n, err)
	}

	// ── Lo que lee Dovecot: la vista, solo lo activo; nunca la tabla ──────────
	viewRows := func(username string) int {
		n, err := asRole("mail_engine", uuid.Nil, `SELECT count(*) FROM mail.v_sieve_vacation WHERE username = $1`, username)
		if err != nil {
			t.Fatalf("mail_engine sobre la vista: %v", err)
		}
		return n
	}
	if viewRows(ana.Username) != 1 {
		t.Fatal("la respuesta activa de A debe verse en v_sieve_vacation")
	}
	if n, err := asRole("mail_engine", uuid.Nil, `SELECT count(*) FROM mail.v_sieve_vacation WHERE username = $1 AND script_name = 'active' AND script_data LIKE '%require ["vacation"%' AND id = md5(script_data)`, ana.Username); err != nil || n != 1 {
		t.Fatalf("la vista sirve el script generado con el formato que espera Dovecot: %d %v", n, err)
	}
	if viewRows(luis.Username) != 0 {
		t.Fatal("un buzon sin respuesta no aparece en la vista")
	}
	_, err = asRole("mail_engine", uuid.Nil, `SELECT count(*) FROM mail.vacation_replies`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("mail_engine no debe leer la tabla: %v", err)
	}
	if _, err := uc.PutMailboxVacation(ctxA, tenantA, ana.ID, app.PutVacationRequest{Enabled: false, Message: "guardada, apagada"}); err != nil {
		t.Fatal(err)
	}
	if viewRows(ana.Username) != 0 {
		t.Fatal("una respuesta desactivada no debe verse en la vista")
	}

	// ── El webmail localiza el buzon por su nombre, sin empresa ───────────────
	loc := NewMailboxLocator(&db.ContextPool{})
	tenant, id, err := loc.Locate(db.WithPool(ctx, pool), luis.Username)
	if err != nil || tenant != tenantB || id != luis.ID {
		t.Fatalf("localizar: %v %v %v", tenant, id, err)
	}
	if _, _, err := loc.Locate(db.WithPool(ctx, pool), "nadie@"+domainA); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("buzon inexistente: %v", err)
	}
	if _, err := uc.PutVacationByUsername(db.WithPool(ctx, pool), luis.Username, app.PutVacationRequest{Enabled: true, Message: "desde el webmail"}); err != nil {
		t.Fatalf("guardar por nombre: %v", err)
	}
	if v, err := uc.GetMailboxVacation(ctxB, tenantB, luis.ID); err != nil || v.Message != "desde el webmail" {
		t.Fatalf("lo guardado por nombre cae en la empresa del buzon: %v %+v", err, v)
	}

	// ── Las restricciones de la tabla cierran lo que la aplicacion ya valida ──
	check := func(nombre, sql string) {
		t.Helper()
		_, err := pool.Exec(ctx, sql, tenantA, "chk-"+suffix+"@"+domainA)
		var pe *pgconn.PgError
		if !errors.As(err, &pe) || pe.Code != "23514" {
			t.Errorf("%s: se esperaba una violacion de CHECK y salio %v", nombre, err)
		}
	}
	base := `INSERT INTO mail.vacation_replies (tenant_id, username, enabled, message, script_data, interval_days, starts_on, ends_on, subject) VALUES ($1, $2, `
	check("activa sin mensaje", base+`true, '', 'x', 1, NULL, NULL, '')`)
	check("activa sin script", base+`true, 'hola', '', 1, NULL, NULL, '')`)
	check("fin antes del inicio", base+`false, '', '', 1, '2026-09-30', '2026-09-21', '')`)
	check("intervalo 0", base+`false, '', '', 0, NULL, NULL, '')`)
	check("intervalo 31", base+`false, '', '', 31, NULL, NULL, '')`)
	check("asunto de 201", base+`false, '', '', 1, NULL, NULL, repeat('a', 201))`)

	// ── Borrar el buzon borra su respuesta ────────────────────────────────────
	if err := uc.DeleteMailbox(ctxB, tenantB, luis.ID); err != nil {
		t.Fatalf("borrar buzon: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM mail.vacation_replies WHERE username = $1`, luis.Username).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("la respuesta sobrevivio al buzon: %d %v", rows, err)
	}
}

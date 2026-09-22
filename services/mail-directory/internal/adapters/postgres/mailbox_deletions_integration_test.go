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
	"github.com/jackc/pgx/v5/pgxpool"
)

// La marca de baja de un buzon (migracion 12) y la retencion de su direccion, contra Postgres real y con
// los roles de verdad: la marca queda en la MISMA transaccion que el borrado (sin poder dejarla, el buzon
// no se borra); mientras esta viva la direccion no se vuelve a crear, tambien para otra empresa (la
// funcion SECURITY DEFINER cubre la celda aunque mail_app no vea la fila); el rol de los motores la lee
// y la consume, y nada mas; consumida, o con la retencion a cero, el buzon se crea.
func TestMarcaDeBajaRetieneLaDireccionHastaQueDovecotLaConsume(t *testing.T) {
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
	dom := "md-" + suffix + ".example"
	t.Cleanup(func() { cleanup(t, pool, dom, dom, tenantA, tenantB) })
	ctxA := middleware.WithIdentity(db.WithPool(ctx, pool), uuid.New().String(), tenantA.String())
	if _, err := uc.SetDomainActivation(ctxA, tenantA, dom, true); err != nil {
		t.Fatalf("activar %s: %v", dom, err)
	}
	crear := func(uc *app.UseCase) (*domain.Mailbox, error) {
		return uc.CreateMailbox(ctxA, tenantA, app.CreateMailboxRequest{LocalPart: "ana", Domain: dom, Password: "contrasena-de-prueba-1"})
	}
	ana, err := crear(uc)
	if err != nil {
		t.Fatalf("alta: %v", err)
	}

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
		if err := tx.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			return 0, err
		}
		return n, tx.Commit(ctx)
	}
	marcas := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM mail.mailbox_deletions WHERE username = $1 AND tenant_id = $2 AND mailbox_id = $3`,
			ana.Username, tenantA, ana.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// ── Sin poder dejar la marca, el buzon no se borra ────────────────────────
	if _, err := pool.Exec(ctx, `REVOKE INSERT ON mail.mailbox_deletions FROM mail_app`); err != nil {
		t.Fatal(err)
	}
	// Se restituye tambien al limpiar, cuando el contexto de la prueba ya esta cancelado.
	regrant := func() {
		if _, err := pool.Exec(context.Background(), `GRANT INSERT ON mail.mailbox_deletions TO mail_app`); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(regrant)
	if err := uc.DeleteMailbox(ctxA, tenantA, ana.ID); err == nil {
		t.Fatal("el borrado sin poder dejar la marca debia fallar")
	}
	if _, err := uc.GetMailbox(ctxA, tenantA, ana.ID); err != nil {
		t.Fatalf("el buzon debe seguir existiendo tras el borrado fallido: %v", err)
	}
	if marcas() != 0 {
		t.Fatal("quedo una marca de un borrado que no se hizo")
	}
	regrant()

	// ── Borrado: marca en la transaccion y direccion retenida ─────────────────
	if err := uc.DeleteMailbox(ctxA, tenantA, ana.ID); err != nil {
		t.Fatalf("borrar: %v", err)
	}
	if marcas() != 1 {
		t.Fatal("el borrado no dejo su marca de baja")
	}
	if _, err := crear(uc); !errors.Is(err, domain.ErrAddressRecentlyDeleted) {
		t.Fatalf("recrear con la marca viva debia esperar al barrido de Dovecot, dio %v", err)
	}
	// Otra empresa de la celda no ve la marca, pero la funcion responde por toda la celda.
	if n, err := asRole("mail_app", tenantB, `SELECT count(*) FROM mail.mailbox_deletions WHERE username = $1`, ana.Username); err != nil || n != 0 {
		t.Fatalf("mail_app de B ve la marca de A: %d %v", n, err)
	}
	if n, err := asRole("mail_app", tenantB, `SELECT CASE WHEN mail.mailbox_deletion_pending($1, interval '15 minutes') THEN 1 ELSE 0 END`, ana.Username); err != nil || n != 1 {
		t.Fatalf("la retencion no cubre la celda desde otra empresa: %d %v", n, err)
	}
	if n, err := asRole("mail_app", tenantB, `SELECT CASE WHEN mail.mailbox_deletion_pending($1, interval '0') THEN 1 ELSE 0 END`, ana.Username); err != nil || n != 0 {
		t.Fatalf("una marca mas vieja que la retencion no retiene: %d %v", n, err)
	}
	// Con la retencion a cero (MAIL_DIRECTORY_MAILBOX_RECREATE_HOLD=0) no se espera al barrido.
	if _, err := crear(newUseCaseWith(&db.ContextPool{}, fixedMX{hosts: []string{testPlatformMX}}, 0)); err != nil {
		t.Fatalf("sin retencion: %v", err)
	}
	var recreated uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM mail.mailboxes WHERE username = $1`, ana.Username).Scan(&recreated); err != nil {
		t.Fatal(err)
	}
	if err := uc.DeleteMailbox(ctxA, tenantA, recreated); err != nil {
		t.Fatalf("borrar de nuevo: %v", err)
	}

	// ── El rol de los motores: lee y consume las marcas, nada mas ─────────────
	if n, err := asRole("mail_engine", uuid.Nil, `SELECT count(*) FROM mail.mailbox_deletions WHERE username = $1`, ana.Username); err != nil || n != 2 {
		t.Fatalf("mail_engine no lee las marcas: %d %v", n, err)
	}
	if _, err := asRole("mail_engine", uuid.Nil, `WITH x AS (INSERT INTO mail.mailbox_deletions (tenant_id, mailbox_id, username, local_part, domain) VALUES ($1, $2, $3, 'x', $4) RETURNING 1) SELECT count(*) FROM x`,
		tenantA, uuid.New(), "x@"+dom, dom); err == nil {
		t.Fatal("mail_engine no debe insertar marcas")
	}
	if _, err := asRole("mail_engine", uuid.Nil, `WITH x AS (UPDATE mail.mailbox_deletions SET deleted_at = now() WHERE username = $1 RETURNING 1) SELECT count(*) FROM x`, ana.Username); err == nil {
		t.Fatal("mail_engine no debe modificar marcas")
	}
	if n, err := asRole("mail_engine", uuid.Nil, `WITH x AS (DELETE FROM mail.mailbox_deletions WHERE username = $1 RETURNING 1) SELECT count(*) FROM x`, ana.Username); err != nil || n != 2 {
		t.Fatalf("mail_engine debe consumir las marcas: %d %v", n, err)
	}
	// Consumida la marca, la direccion se crea aunque la retencion siga en pie.
	if _, err := crear(uc); err != nil {
		t.Fatalf("tras el barrido: %v", err)
	}
}

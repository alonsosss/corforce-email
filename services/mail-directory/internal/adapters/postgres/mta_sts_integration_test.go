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
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// La politica MTA-STS contra Postgres real: aislamiento por empresa, lo que se sirve sin empresa en
// la peticion, lo que el rol de los motores no puede ver y las restricciones de la tabla.
func TestPoliticaMTASTSContraPostgres(t *testing.T) {
	dsn := integrationEnv(t, "MAIL_DIRECTORY_TEST_DSN")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	applyCellMigrations(t, ctx, pool)

	mx := &fixedMX{hosts: []string{testPlatformMX}}
	uc := newUseCaseWithMX(&db.ContextPool{}, mx)
	tenantA, tenantB := uuid.New(), uuid.New()
	suffix := strings.Split(uuid.New().String(), "-")[0]
	domainA, domainA2, domainB := "sts-"+suffix+"-a.example", "sts-"+suffix+"-a2.example", "sts-"+suffix+"-b.example"
	t.Cleanup(func() { cleanup(t, pool, domainA, domainB, tenantA, tenantB) })
	as := func(tenant uuid.UUID) context.Context {
		return middleware.WithIdentity(db.WithPool(ctx, pool), uuid.New().String(), tenant.String())
	}
	ctxA, ctxB := as(tenantA), as(tenantB)
	for _, c := range []struct {
		ctx    context.Context
		tenant uuid.UUID
		domain string
	}{{ctxA, tenantA, domainA}, {ctxA, tenantA, domainA2}, {ctxB, tenantB, domainB}} {
		if _, err := uc.SetDomainActivation(c.ctx, c.tenant, c.domain, true); err != nil {
			t.Fatalf("activar %s: %v", c.domain, err)
		}
	}

	// ── Activar, cambiar y releer ─────────────────────────────────────────────
	initial, err := uc.GetMTASTS(ctxA, tenantA, domainA)
	if err != nil || initial.Mode != domain.MTASTSNone || initial.PolicyID != "" || initial.UpdatedAt != nil {
		t.Fatalf("un dominio sin politica esta en none: %v %+v", err, initial)
	}
	testing1, err := uc.SetMTASTSMode(ctxA, tenantA, domainA, "testing")
	if err != nil || testing1.Mode != domain.MTASTSTesting || testing1.PolicyID == "" || testing1.UpdatedAt == nil {
		t.Fatalf("activar: %v %+v", err, testing1)
	}
	enforced, err := uc.SetMTASTSMode(ctxA, tenantA, domainA, "enforce")
	if err != nil || enforced.Mode != domain.MTASTSEnforce || enforced.PolicyID == testing1.PolicyID || enforced.MaxAge != domain.MTASTSEnforceMaxAge {
		t.Fatalf("enforce: %v %+v", err, enforced)
	}
	if !enforced.UpdatedAt.After(*testing1.UpdatedAt) {
		t.Fatalf("el trigger de updated_at no avanzo: %v -> %v", testing1.UpdatedAt, enforced.UpdatedAt)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM mail.mta_sts_policies WHERE domain = $1`, domainA).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("una fila por dominio: %d %v", rows, err)
	}

	// ── enforce exige los MX de la plataforma ─────────────────────────────────
	if _, err := uc.SetMTASTSMode(ctxA, tenantA, domainA, "testing"); err != nil {
		t.Fatal(err)
	}
	mx.hosts = []string{testPlatformMX + ".", "mail.otro.example"}
	if _, err := uc.SetMTASTSMode(ctxA, tenantA, domainA, "enforce"); !errors.Is(err, domain.ErrMTASTSMXMismatch) {
		t.Fatalf("un MX ajeno debe impedir enforce: %v", err)
	}
	mx.hosts = []string{testPlatformMX}
	if got, _ := uc.GetMTASTS(ctxA, tenantA, domainA); got.Mode != domain.MTASTSTesting {
		t.Fatalf("el rechazo no cambia el modo: %s", got.Mode)
	}

	// ── Otra empresa no ve ni cambia la politica ──────────────────────────────
	if _, err := uc.GetMTASTS(ctxB, tenantB, domainA); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("B leyendo el dominio de A: %v", err)
	}
	if _, err := uc.SetMTASTSMode(ctxB, tenantB, domainA, "testing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("B escribiendo en el dominio de A: %v", err)
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
		err = tx.QueryRow(ctx, sql, args...).Scan(&n)
		return n, err
	}
	if n, err := asRole("mail_app", tenantB, `SELECT count(*) FROM mail.mta_sts_policies WHERE domain = $1`, domainA); err != nil || n != 0 {
		t.Fatalf("mail_app de B ve la fila de A: %d %v", n, err)
	}
	if n, err := asRole("mail_app", tenantA, `SELECT count(*) FROM mail.mta_sts_policies WHERE domain = $1`, domainA); err != nil || n != 1 {
		t.Fatalf("mail_app de A no ve la suya: %d %v", n, err)
	}
	if n, err := asRole("mail_app", tenantB, `WITH x AS (UPDATE mail.mta_sts_policies SET mode = 'none' WHERE domain = $1 RETURNING 1) SELECT count(*) FROM x`, domainA); err != nil || n != 0 {
		t.Fatalf("mail_app de B modifico la fila de A: %d %v", n, err)
	}
	if n, err := asRole("mail_app", tenantB, `WITH x AS (DELETE FROM mail.mta_sts_policies WHERE domain = $1 RETURNING 1) SELECT count(*) FROM x`, domainA); err != nil || n != 0 {
		t.Fatalf("mail_app de B borro la fila de A: %d %v", n, err)
	}
	// Ni escribiendo con el tenant_id de otra empresa: la politica WITH CHECK lo impide.
	_, err = asRole("mail_app", tenantB, `WITH x AS (INSERT INTO mail.mta_sts_policies (tenant_id, domain, policy_id) VALUES ($1, $2, 'abc123') RETURNING 1) SELECT count(*) FROM x`, tenantA, domainA2)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("mail_app de B no debe insertar para A: %v", err)
	}
	// Los motores no leen la tabla.
	_, err = asRole("mail_engine", uuid.Nil, `SELECT count(*) FROM mail.mta_sts_policies`)
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("mail_engine no debe leer la tabla: %v", err)
	}

	// ── Lo que se sirve a los remitentes, sin empresa en la peticion ──────────
	public := NewMTASTSPublicReader(&db.ContextPool{})
	pubCtx := db.WithPool(ctx, pool)
	if p, err := public.Published(pubCtx, domainA); err != nil || p.Mode != domain.MTASTSTesting || p.MaxAge != domain.MTASTSTestingMaxAge {
		t.Fatalf("politica publicada: %v %+v", err, p)
	}
	body, err := uc.PublishedMTASTS(pubCtx, strings.ToUpper(domainA))
	if want := "version: STSv1\r\nmode: testing\r\nmx: " + testPlatformMX + "\r\nmax_age: 86400\r\n"; err != nil || body != want {
		t.Fatalf("cuerpo: %q %v", body, err)
	}
	if _, err := public.Published(pubCtx, domainB); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("un dominio sin politica no se sirve: %v", err)
	}
	// Una fila cuya empresa no es la del dominio no se sirve: el dominio manda.
	if _, err := pool.Exec(ctx, `INSERT INTO mail.mta_sts_policies (tenant_id, domain, mode, policy_id) VALUES ($1, $2, 'testing', 'ajena1')`, tenantB, domainA2); err != nil {
		t.Fatal(err)
	}
	if _, err := public.Published(pubCtx, domainA2); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("una politica de otra empresa sobre el dominio de A no se sirve: %v", err)
	}
	// Ni la de un dominio que dejo de estar activo, ni la que esta en none.
	if _, err := uc.SetDomainActivation(ctxA, tenantA, domainA, false); err != nil {
		t.Fatal(err)
	}
	if _, err := public.Published(pubCtx, domainA); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("dominio inactivo: %v", err)
	}
	if _, err := uc.SetMTASTSMode(ctxA, tenantA, domainA, "enforce"); !errors.Is(err, domain.ErrMTASTSDomainNotActive) {
		t.Fatalf("enforce en un dominio inactivo: %v", err)
	}
	if _, err := uc.SetDomainActivation(ctxA, tenantA, domainA, true); err != nil {
		t.Fatal(err)
	}
	if _, err := uc.SetMTASTSMode(ctxA, tenantA, domainA, "none"); err != nil {
		t.Fatal(err)
	}
	if _, err := public.Published(pubCtx, domainA); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("modo none: %v", err)
	}

	// ── El listado trae todos los dominios de la empresa, con none los que no tienen politica ──
	states, total, err := uc.ListMTASTS(ctxA, tenantA, ports.Page{Limit: 50})
	if err != nil || total != 2 || len(states) != 2 {
		t.Fatalf("listado de A: %d %v %+v", total, err, states)
	}
	byDomain := map[string]domain.MTASTSState{}
	for _, s := range states {
		byDomain[s.Domain] = s
	}
	if byDomain[domainA].Mode != domain.MTASTSNone || byDomain[domainA].PolicyID == "" || byDomain[domainA2].Mode != domain.MTASTSNone || byDomain[domainA2].PolicyID != "" {
		t.Fatalf("modos del listado: %+v", byDomain)
	}
	if _, ok := byDomain[domainB]; ok {
		t.Fatal("el listado de A trae un dominio de B")
	}

	// ── Las restricciones de la tabla cierran lo que la aplicacion ya valida ──
	check := func(nombre, mode string, maxAge int, policyID, name string) {
		t.Helper()
		_, err := pool.Exec(ctx, `INSERT INTO mail.mta_sts_policies (tenant_id, domain, mode, max_age, policy_id) VALUES ($1, $2, $3, $4, $5)`,
			tenantA, name, mode, maxAge, policyID)
		var pe *pgconn.PgError
		if !errors.As(err, &pe) || pe.Code != "23514" {
			t.Errorf("%s: se esperaba una violacion de CHECK y salio %v", nombre, err)
		}
	}
	check("modo desconocido", "strict", 86400, "v1", "chk1-"+suffix+".example")
	check("max_age cero", "testing", 0, "v1", "chk2-"+suffix+".example")
	check("max_age de mas de un ano", "testing", 31557601, "v1", "chk3-"+suffix+".example")
	check("version con guion", "testing", 86400, "a-b", "chk4-"+suffix+".example")
	check("version de 33", "testing", 86400, strings.Repeat("a", 33), "chk5-"+suffix+".example")
	check("version vacia", "testing", 86400, "", "chk6-"+suffix+".example")
	check("dominio en mayusculas", "testing", 86400, "v1", "CHK7-"+suffix+".example")
	if _, err := pool.Exec(ctx, `INSERT INTO mail.mta_sts_policies (tenant_id, domain, policy_id) VALUES ($1, $2, 'v1')`, tenantA, "chk8-"+suffix+".example"); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := pool.QueryRow(ctx, `SELECT mode FROM mail.mta_sts_policies WHERE domain = $1`, "chk8-"+suffix+".example").Scan(&mode); err != nil || mode != "testing" {
		t.Fatalf("el modo por defecto es testing: %q %v", mode, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO mail.mta_sts_policies (tenant_id, domain, policy_id) VALUES ($1, $2, 'v2')`, tenantB, "chk8-"+suffix+".example"); !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("un dominio solo tiene una politica: %v", err)
	}

	// ── Borrar el dominio borra su politica ───────────────────────────────────
	d, _, err := uc.ListDomains(ctxB, tenantB, ports.DomainFilter{}, ports.Page{Limit: 10})
	if err != nil || len(d) == 0 {
		t.Fatalf("dominios de B: %v", err)
	}
	if _, err := uc.SetMTASTSMode(ctxB, tenantB, domainB, "testing"); err != nil {
		t.Fatal(err)
	}
	if err := uc.DeleteDomain(ctxB, tenantB, d[0].ID); err != nil {
		t.Fatalf("borrar dominio: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM mail.mta_sts_policies WHERE domain = $1`, domainB).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("la politica sobrevivio al dominio: %d %v", rows, err)
	}
}

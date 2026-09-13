//go:build integration

package postgres

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// ORGANIZATION_TEST_DSN apunta a una base limpia de un Postgres desechable, con un rol que
// puede crear bases. La prueba migra el registro con el RegistryMigrator del servicio y
// comprueba contra Postgres real la saga de alta y baja (arriendo con el reloj de la base,
// token, activacion y retirada del registro) y la marca de las bases de empresa (el
// reintento adopta la propia; una ajena no se adopta ni se borra).

func organizationDB(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	dsn := integrationEnv(t, "ORGANIZATION_TEST_DSN")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	files, err := filepath.Glob(filepath.Join(repoRoot(t), "migrations", "registry", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("migraciones del registro: %v", err)
	}
	sort.Strings(files)
	migrations := make([]MigrationFile, 0, len(files))
	for _, f := range files {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		migrations = append(migrations, MigrationFile{Name: filepath.Base(f), SQL: string(sql)})
	}
	migrator := NewRegistryMigrator(pool, migrations, zap.NewNop())
	for pass := 1; pass <= 2; pass++ {
		if err := migrator.Run(ctx); err != nil {
			t.Fatalf("pasada %d del registro: %v", pass, err)
		}
	}
	return pool, dsn
}

func itSuffix() string { return strings.ReplaceAll(uuid.NewString(), "-", "")[:12] }

// staleSagas dice si la saga esta entre las que retomaria el barrido.
func staleSagas(t *testing.T, repo *TenantSagaRepo, id uuid.UUID) bool {
	t.Helper()
	stale, err := repo.ListStale(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range stale {
		if s.TenantID == id {
			return true
		}
	}
	return false
}

func TestSagaDeEmpresaContraPostgres(t *testing.T) {
	pool, _ := organizationDB(t)
	ctx := context.Background()
	repo := NewTenantSagaRepo(pool)
	suffix := itSuffix()

	var cellID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO organization.cells (code, region, db_host, db_port) VALUES ($1, 'it', '127.0.0.1', 5432) RETURNING id`,
		"it-"+suffix).Scan(&cellID); err != nil {
		t.Fatal(err)
	}
	tenant := &domain.Tenant{ID: uuid.New(), Slug: "it-" + suffix, Name: "IT", DBName: "mail_tenant_it_" + suffix,
		Status: domain.TenantStatusInactive, CellID: cellID, Settings: map[string]interface{}{}}
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = pool.Exec(cctx, `DELETE FROM organization.tenants WHERE id = $1`, tenant.ID)
		_, _ = pool.Exec(cctx, `DELETE FROM organization.cells WHERE id = $1`, cellID)
	})

	saga := &domain.TenantSaga{TenantID: tenant.ID, Operation: domain.SagaCreate, State: domain.SagaRunning,
		Step: domain.StepRegistered, AdminUserID: uuid.New()}
	if err := repo.BeginCreate(ctx, tenant, saga, time.Minute); err != nil {
		t.Fatalf("BeginCreate: %v", err)
	}
	if tenant.CreatedAt.IsZero() || saga.LeaseToken == uuid.Nil || saga.LeaseUntil == nil {
		t.Fatalf("alta registrada sin sellos o sin arriendo: %+v %+v", tenant, saga)
	}
	dup, dupSaga := *tenant, *saga
	dup.ID, dupSaga.TenantID = uuid.New(), dup.ID
	if err := repo.BeginCreate(ctx, &dup, &dupSaga, time.Minute); !errors.Is(err, domain.ErrTenantAlreadyExists) {
		t.Fatalf("mismo slug = %v; want ErrTenantAlreadyExists", err)
	}

	if _, err := repo.Claim(ctx, tenant.ID, time.Minute); !errors.Is(err, domain.ErrTenantBusy) {
		t.Fatalf("tomar una saga con arriendo vigente = %v; want ErrTenantBusy", err)
	}
	stranger := *saga
	stranger.LeaseToken = uuid.New()
	if err := repo.Save(ctx, &stranger, time.Minute); !errors.Is(err, domain.ErrLeaseLost) {
		t.Fatalf("guardar con otro token = %v; want ErrLeaseLost", err)
	}

	saga.Step, saga.RoleID = domain.StepRoleSeeded, uuid.New()
	if err := repo.Save(ctx, saga, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, tenant.ID)
	if err != nil || got.Step != domain.StepRoleSeeded || got.RoleID != saga.RoleID ||
		got.AdminUserID != saga.AdminUserID || got.Attempts != 1 {
		t.Fatalf("saga guardada = %+v, %v", got, err)
	}

	if _, err := pool.Exec(ctx,
		`UPDATE organization.tenant_sagas SET lease_until = now() - interval '1 second' WHERE tenant_id = $1`, tenant.ID); err != nil {
		t.Fatal(err)
	}
	if !staleSagas(t, repo, tenant.ID) {
		t.Fatal("una saga con el arriendo vencido es de las que retoma el barrido")
	}
	claimed, err := repo.Claim(ctx, tenant.ID, time.Minute)
	if err != nil || claimed.LeaseToken == saga.LeaseToken || claimed.Attempts != 2 {
		t.Fatalf("tomar la saga abandonada = %+v, %v; want token nuevo y 2 intentos", claimed, err)
	}
	if err := repo.Save(ctx, saga, time.Minute); !errors.Is(err, domain.ErrLeaseLost) {
		t.Fatalf("la instancia que perdio el arriendo guarda = %v; want ErrLeaseLost", err)
	}
	if staleSagas(t, repo, tenant.ID) {
		t.Fatal("una saga con arriendo vigente no se retoma")
	}

	claimed.Step = domain.StepRoleAssigned
	if err := repo.Save(ctx, claimed, 0); err != nil || claimed.LeaseUntil != nil {
		t.Fatalf("soltar el arriendo = %v (%v)", err, claimed.LeaseUntil)
	}
	if !staleSagas(t, repo, tenant.ID) {
		t.Fatal("una saga sin arriendo la retoma el barrido sin esperar a que venza")
	}
	if claimed, err = repo.Claim(ctx, tenant.ID, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteCreate(ctx, claimed); err != nil {
		t.Fatalf("activar: %v", err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM organization.tenants WHERE id = $1`, tenant.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	done, err := repo.Get(ctx, tenant.ID)
	if err != nil || status != domain.TenantStatusActive || done.State != domain.SagaCompleted ||
		done.Step != domain.StepActivated || done.LeaseUntil != nil {
		t.Fatalf("tras activar: empresa %s, saga %+v, %v", status, done, err)
	}
	if staleSagas(t, repo, tenant.ID) {
		t.Fatal("una saga completada no se retoma")
	}

	deletion := &domain.TenantSaga{TenantID: tenant.ID, Operation: domain.SagaDelete, State: domain.SagaRunning, Step: domain.StepDeletionStarted}
	if err := repo.Insert(ctx, deletion, time.Minute); !errors.Is(err, domain.ErrTenantBusy) {
		t.Fatalf("otra saga para la misma empresa = %v; want ErrTenantBusy", err)
	}
	orphan := &domain.TenantSaga{TenantID: uuid.New(), Operation: domain.SagaDelete, State: domain.SagaRunning, Step: domain.StepDeletionStarted}
	if err := repo.Insert(ctx, orphan, time.Minute); !errors.Is(err, domain.ErrTenantNotFound) {
		t.Fatalf("saga de una empresa que no existe = %v; want ErrTenantNotFound", err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO organization.tenant_modules (tenant_id, module, enabled) VALUES ($1, 'marketing', false)`, tenant.ID); err != nil {
		t.Fatal(err)
	}
	owned, err := repo.Claim(ctx, tenant.ID, time.Minute)
	if err != nil {
		t.Fatalf("tomar la saga para la baja: %v", err)
	}
	foreign := *owned
	foreign.LeaseToken = uuid.New()
	if err := repo.DeleteTenant(ctx, &foreign); !errors.Is(err, domain.ErrLeaseLost) {
		t.Fatalf("retirar sin el arriendo = %v; want ErrLeaseLost", err)
	}
	if err := repo.DeleteTenant(ctx, owned); err != nil {
		t.Fatalf("retirar: %v", err)
	}
	var left int
	if err := pool.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM organization.tenants WHERE id = $1)
		      + (SELECT count(*) FROM organization.tenant_modules WHERE tenant_id = $1)
		      + (SELECT count(*) FROM organization.tenant_sagas WHERE tenant_id = $1)`, tenant.ID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("filas de la empresa tras la baja = %d; want 0", left)
	}
}

func TestBaseDeEmpresaMarcadaContraPostgres(t *testing.T) {
	pool, dsn := organizationDB(t)
	ctx := context.Background()
	base, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	directDSN := func(_ string, _ int, db string) string {
		u := *base
		u.Path = "/" + db
		return u.String()
	}
	prov := NewDBProvisioner(pool,
		[]MigrationFile{{Name: "it/01_probe.sql", SQL: `CREATE SCHEMA IF NOT EXISTS probe; CREATE TABLE IF NOT EXISTS probe.items (id int)`}},
		directDSN, pool.Config().ConnConfig.Host)
	name := "mail_tenant_it_" + itSuffix()
	target := domain.DBTarget{DBName: name}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)") })
	owner, other := uuid.New(), uuid.New()
	exists := func() bool {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_database WHERE datname = $1`, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}

	if err := prov.CreateDatabase(ctx, target, owner); err != nil {
		t.Fatalf("crear: %v", err)
	}
	var comment string
	var publicConnect bool
	if err := pool.QueryRow(ctx,
		`SELECT shobj_description(oid, 'pg_database'), has_database_privilege('public', datname, 'CONNECT')
		   FROM pg_database WHERE datname = $1`, name).Scan(&comment, &publicConnect); err != nil {
		t.Fatal(err)
	}
	if comment != tenantDBMarker(owner) || publicConnect {
		t.Fatalf("marca %q, CONNECT de PUBLIC %v; want la marca de la empresa y la base cerrada", comment, publicConnect)
	}
	if err := prov.RunMigrations(ctx, target); err != nil {
		t.Fatalf("migrar: %v", err)
	}

	if err := prov.CreateDatabase(ctx, target, owner); err != nil {
		t.Fatalf("el reintento del mismo alta adopta su base: %v", err)
	}
	if st, err := prov.MigrationStatus(ctx, target); err != nil || st.Applied != 1 || len(st.Pending) != 0 {
		t.Fatalf("estado tras adoptar = %+v, %v; la adopcion no pierde lo migrado", st, err)
	}

	if err := prov.CreateDatabase(ctx, target, other); !errors.Is(err, domain.ErrDatabaseOccupied) {
		t.Fatalf("otra empresa con el mismo nombre = %v; want ErrDatabaseOccupied", err)
	}
	if err := prov.DropOwnedDatabase(ctx, target, other); err != nil || !exists() {
		t.Fatalf("otra empresa no borra la base (%v, existe %v)", err, exists())
	}
	if err := prov.DropOwnedDatabase(ctx, target, owner); err != nil || exists() {
		t.Fatalf("la empresa duena la borra (%v, existe %v)", err, exists())
	}
	if err := prov.DropOwnedDatabase(ctx, target, owner); err != nil {
		t.Fatalf("borrar una base que ya no esta: %v", err)
	}

	if _, err := pool.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	if err := prov.CreateDatabase(ctx, target, owner); !errors.Is(err, domain.ErrDatabaseOccupied) {
		t.Fatalf("una base sin marca = %v; want ErrDatabaseOccupied", err)
	}
	if err := prov.DropOwnedDatabase(ctx, target, owner); err != nil || !exists() {
		t.Fatalf("una base sin marca no se borra (%v, existe %v)", err, exists())
	}
}

//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Credencial propia de la celda contra un Postgres DESECHABLE: ops/db/cell-service-role.sh
// crea el rol, organization crea una base de empresa despues, y conexiones reales por TCP
// y con contrasena comprueban que el rol entra en su celda y en ninguna otra base, que no
// puede hacer DDL y que mail_app sigue acotado por RLS.
//
// El script cierra a PUBLIC todas las bases del cluster: solo contra un contenedor que se
// tira al terminar. Necesita python3 y psql (o el contenedor, para usar el suyo).
//
//	docker run -d --name cfm-cellrole-pg -e POSTGRES_USER=mail_admin -e POSTGRES_PASSWORD=<aleatoria> \
//	  -p 127.0.0.1:25433:5432 pgvector/pgvector:pg16
//	CELL_ROLE_TEST_DSN='postgres://mail_admin:<aleatoria>@127.0.0.1:25433/postgres?sslmode=disable' \
//	CELL_ROLE_TEST_CONTAINER=cfm-cellrole-pg \
//	  go test -tags integration -run TestCellServiceRole ./services/organization/internal/adapters/postgres/
func TestCellServiceRoleIsolation(t *testing.T) {
	dsn := integrationEnv(t, "CELL_ROLE_TEST_DSN")
	if _, err := exec.LookPath("python3"); err != nil {
		skipIntegration(t, "sin python3: el script calcula con el el verificador SCRAM")
	}
	shimDir := psqlShim(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	adminCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	admin, err := pgxpool.NewWithConfig(ctx, adminCfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(admin.Close)

	suffix := strings.Split(uuid.NewString(), "-")[0]
	code := "it-" + suffix
	cellDB := "mail_cell_it_" + suffix
	otherCellDB := cellDB + "_b"
	registryDB := "mail_registry_it_" + suffix
	tenantDB := "mail_tenant_it_" + suffix
	role := config.CellServiceRole(cellDB)
	t.Cleanup(func() {
		bg := context.Background()
		for _, name := range []string{cellDB, otherCellDB, registryDB, tenantDB} {
			_, _ = admin.Exec(bg, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		}
		_, _ = admin.Exec(bg, "DROP ROLE IF EXISTS "+pgx.Identifier{role}.Sanitize())
	})

	for _, name := range []string{cellDB, otherCellDB, registryDB} {
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
			t.Fatalf("crear %s: %v", name, err)
		}
	}
	applyCellMigrations(t, ctx, adminCfg, cellDB)

	password := randomSecret()
	runCellRoleScript(t, ctx, adminCfg, shimDir, code, password)

	// La base de empresa nace DESPUES del script: es organization quien la cierra a PUBLIC.
	prov := NewDBProvisioner(admin, nil, nil, adminCfg.ConnConfig.Host)
	if err := prov.CreateDatabase(ctx, domain.DBTarget{DBName: tenantDB}); err != nil {
		t.Fatalf("organization crea la base de empresa: %v", err)
	}

	cell := connectAs(t, ctx, adminCfg, role, password, cellDB)
	defer cell.Close(context.Background())

	t.Run("camino de servicio: DML en su celda sin ser dueno", func(t *testing.T) {
		tenantA, tenantB := uuid.New(), uuid.New()
		for i, tenant := range []uuid.UUID{tenantA, tenantB} {
			if _, err := cell.Exec(ctx, `INSERT INTO mail.domains (tenant_id, domain) VALUES ($1, $2)`,
				tenant, "it-"+suffix+"-"+strconv.Itoa(i)+".example"); err != nil {
				t.Fatalf("alta de dominio como servicio: %v", err)
			}
		}
		eventID := uuid.New()
		if _, err := cell.Exec(ctx, `INSERT INTO platform.event_outbox (id, subject, tenant_id, payload) VALUES ($1, 'mail.domain.created', $2, '{}')`, eventID, tenantA); err != nil {
			t.Fatalf("encolar: %v", err)
		}
		tx, err := cell.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var pending int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT id FROM platform.event_outbox WHERE id = $1 FOR UPDATE SKIP LOCKED) s`, eventID).Scan(&pending); err != nil || pending != 1 {
			t.Fatalf("el rele no reserva la fila: %d, %v", pending, err)
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.event_outbox SET published_at = now() WHERE id = $1`, eventID); err != nil {
			t.Fatalf("marcar publicado: %v", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM platform.event_outbox WHERE id = $1`, eventID); err != nil {
			t.Fatalf("podar: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}

		t.Run("mail_app sigue bajo RLS", func(t *testing.T) {
			count := func(tenant string) int {
				t.Helper()
				tx, err := cell.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback(ctx) }()
				if _, err := tx.Exec(ctx, `SET LOCAL ROLE mail_app`); err != nil {
					t.Fatalf("SET LOCAL ROLE mail_app: %v", err)
				}
				if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant_id', $1, true)`, tenant); err != nil {
					t.Fatal(err)
				}
				var n int
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM mail.domains WHERE domain LIKE $1`, "it-"+suffix+"-%").Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			if n := count(""); n != 0 {
				t.Errorf("sin empresa en la sesion mail_app ve %d dominios, se esperaba 0", n)
			}
			if n := count(tenantA.String()); n != 1 {
				t.Errorf("la empresa A ve %d dominios, se esperaba 1", n)
			}

			tx, err := cell.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := tx.Exec(ctx, `SET LOCAL ROLE mail_app; SELECT set_config('app.current_tenant_id', '`+tenantA.String()+`', true)`); err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(ctx, `INSERT INTO mail.domains (tenant_id, domain) VALUES ($1, $2)`, tenantB, "it-"+suffix+"-x.example")
			expectSQLState(t, err, "42501", "mail_app escribe en otra empresa")
		})

		t.Run("mail_app no lee la outbox", func(t *testing.T) {
			tx, err := cell.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := tx.Exec(ctx, `SET LOCAL ROLE mail_app`); err != nil {
				t.Fatal(err)
			}
			var n int
			expectSQLState(t, tx.QueryRow(ctx, `SELECT count(*) FROM platform.event_outbox`).Scan(&n), "42501", "mail_app lee la outbox")
		})
	})

	t.Run("sin DDL ni TRUNCATE", func(t *testing.T) {
		for _, stmt := range []string{
			`CREATE TABLE mail.it_forbidden (id int)`,
			`DROP TABLE mail.domains`,
			`TRUNCATE mail.domains`,
			`ALTER TABLE mail.mailboxes DISABLE ROW LEVEL SECURITY`,
			`CREATE TEMP TABLE it_forbidden (id int)`,
		} {
			_, err := cell.Exec(ctx, stmt)
			expectSQLState(t, err, "42501", stmt)
		}
	})

	t.Run("no entra en ninguna otra base", func(t *testing.T) {
		for _, name := range []string{registryDB, otherCellDB, tenantDB} {
			conn, err := connect(ctx, adminCfg, role, password, name)
			if err == nil {
				conn.Close(ctx)
			}
			expectSQLState(t, err, "42501", "conectar a "+name)
		}
	})

	t.Run("toda tabla con RLS de la celda tiene su politica service_all", func(t *testing.T) {
		pool := cellAdminPool(t, ctx, adminCfg, cellDB)
		rows, err := pool.Query(ctx, `
			SELECT n.nspname || '.' || c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			 WHERE n.nspname IN ('mail', 'mail_security') AND c.relrowsecurity
			   AND NOT EXISTS (SELECT 1 FROM pg_policy p WHERE p.polrelid = c.oid AND p.polname = 'service_all'
			                     AND 'mail_service'::regrole = ANY (p.polroles))`)
		if err != nil {
			t.Fatal(err)
		}
		missing, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		if len(missing) > 0 {
			t.Fatalf("tablas con RLS sin politica para mail_service: %v", missing)
		}
	})

	t.Run("idempotente y rotacion de contrasena", func(t *testing.T) {
		runCellRoleScript(t, ctx, adminCfg, shimDir, code, password)
		connectAs(t, ctx, adminCfg, role, password, cellDB).Close(ctx)

		rotated := randomSecret()
		runCellRoleScript(t, ctx, adminCfg, shimDir, code, rotated)
		conn, err := connect(ctx, adminCfg, role, password, cellDB)
		if err == nil {
			conn.Close(ctx)
		}
		expectSQLState(t, err, "28P01", "la contrasena retirada")
		connectAs(t, ctx, adminCfg, role, rotated, cellDB).Close(ctx)
	})
}

// psqlShim devuelve un directorio con un psql que ejecuta el del contenedor, o "" si hay
// psql en el PATH. Dentro del contenedor psql entra por el socket; la contrasena del rol
// la prueban las conexiones de la prueba, que van por TCP.
func psqlShim(t *testing.T) string {
	t.Helper()
	if container := os.Getenv("CELL_ROLE_TEST_CONTAINER"); container != "" {
		dir := t.TempDir()
		shim := "#!/bin/sh\nexec docker exec -i \"$CELL_ROLE_TEST_CONTAINER\" psql -U \"$PGUSER\" \"$@\"\n"
		if err := os.WriteFile(filepath.Join(dir, "psql"), []byte(shim), 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	if _, err := exec.LookPath("psql"); err != nil {
		skipIntegration(t, "sin psql ni CELL_ROLE_TEST_CONTAINER")
	}
	return ""
}

// skipIntegration salta la prueba por falta de infraestructura, salvo con
// INTEGRATION_REQUIRED=1 (make test-integration y CI): ahi es un fallo, porque un salto
// esconderia que la prueba no corrio.
func skipIntegration(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv("INTEGRATION_REQUIRED") == "1" {
		t.Fatalf("%s (INTEGRATION_REQUIRED=1)", reason)
	}
	t.Skip(reason)
}

// integrationEnv devuelve la variable de entorno que apunta a la infraestructura de la
// prueba, o salta la prueba (falla con INTEGRATION_REQUIRED=1) si no esta.
func integrationEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		skipIntegration(t, name+" no definida")
	}
	return v
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("raiz del repositorio no encontrada desde %s", file)
	}
	return root
}

func runCellRoleScript(t *testing.T, ctx context.Context, adminCfg *pgxpool.Config, shimDir, code, password string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "bash", filepath.Join(repoRoot(t), "ops/db/cell-service-role.sh"), "--cell", code)
	cc := adminCfg.ConnConfig
	cmd.Env = append(os.Environ(),
		"PGHOST="+cc.Host, "PGPORT="+strconv.Itoa(int(cc.Port)), "PGUSER="+cc.User, "PGPASSWORD="+cc.Password,
		"CELL_DB_PASSWORD="+password)
	if shimDir != "" {
		cmd.Env = append(cmd.Env, "PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cell-service-role.sh: %v\n%s", err, out)
	}
}

func applyCellMigrations(t *testing.T, ctx context.Context, adminCfg *pgxpool.Config, dbName string) {
	t.Helper()
	pool := cellAdminPool(t, ctx, adminCfg, dbName)
	var files []string
	for _, dir := range []string{"platform", "mail-directory", "mail-security"} {
		found, err := filepath.Glob(filepath.Join(repoRoot(t), "migrations/cell/canonical", dir, "*.sql"))
		if err != nil || len(found) == 0 {
			t.Fatalf("migraciones de %s: %v", dir, err)
		}
		sort.Strings(found)
		files = append(files, found...)
	}
	for pass := 1; pass <= 2; pass++ {
		for _, f := range files {
			sql, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, string(sql)); err != nil {
				t.Fatalf("pasada %d, %s: %v", pass, filepath.Base(f), err)
			}
		}
	}
}

func cellAdminPool(t *testing.T, ctx context.Context, adminCfg *pgxpool.Config, dbName string) *pgxpool.Pool {
	t.Helper()
	cfg := adminCfg.Copy()
	cfg.ConnConfig.Database = dbName
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func connect(ctx context.Context, adminCfg *pgxpool.Config, user, password, dbName string) (*pgx.Conn, error) {
	cc := adminCfg.ConnConfig.Copy()
	cc.User, cc.Password, cc.Database = user, password, dbName
	return pgx.ConnectConfig(ctx, cc)
}

func connectAs(t *testing.T, ctx context.Context, adminCfg *pgxpool.Config, user, password, dbName string) *pgx.Conn {
	t.Helper()
	conn, err := connect(ctx, adminCfg, user, password, dbName)
	if err != nil {
		t.Fatalf("%s no conecta a %s: %v", user, dbName, err)
	}
	return conn
}

func expectSQLState(t *testing.T, err error, code, what string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		t.Errorf("%s: se esperaba SQLSTATE %s, se obtuvo %v", what, code, err)
	}
}

func randomSecret() string {
	return strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")
}

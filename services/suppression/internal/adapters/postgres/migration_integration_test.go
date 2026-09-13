//go:build integration

package postgres

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// snapshotEntries vuelca todas las filas de suppression.entries en texto, en orden fijo.
func snapshotEntries(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT concat_ws('|', id, tenant_id, email, reason, source, detail,
		message_id, campaign_id, expires_at, created_at, updated_at) FROM suppression.entries ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Una base con el esquema anterior (una fila por direccion) y datos reales se migra sin
// perder ni cambiar ninguna fila, las migraciones se pueden repetir, y despues la direccion
// admite una fila por causa. Corre en una base temporal que crea y borra la propia prueba.
func TestMigracionAUnaFilaPorCausaConservaLosDatos(t *testing.T) {
	dsn := os.Getenv("SUPPRESSION_TEST_DSN")
	if dsn == "" {
		t.Skip("SUPPRESSION_TEST_DSN no definido")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "suppression_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`); err != nil {
			t.Errorf("borrar la base temporal %s: %v", name, err)
		}
		admin.Close(ctx)
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	migrations := suppressionMigrations(t)
	if filepath.Base(migrations[0]) != "01_suppression.sql" {
		t.Fatalf("la primera migracion debe ser el esquema anterior: %s", migrations[0])
	}
	applyFiles(t, ctx, pool,
		filepath.Join(repoRoot(t), "migrations", "tenant", "canonical", "platform", "00_outbox.sql"), migrations[0])

	a, b := uuid.New(), uuid.New()
	future := time.Now().Add(48 * time.Hour)
	if _, err := pool.Exec(ctx, `INSERT INTO suppression.entries
		(tenant_id, email, reason, source, detail, message_id, campaign_id, expires_at) VALUES
		($1, 'ana@example.com', 'unsubscribe', 'transactional', '', $3, $4, NULL),
		($1, 'luis@example.com', 'manual', 'api', 'temporal', NULL, NULL, $5),
		($1, 'eva@example.com', 'hard_bounce', 'ses', '5.1.1', $3, NULL, NULL),
		($2, 'ana@example.com', 'complaint', 'ses', '', NULL, NULL, NULL)`,
		a, b, uuid.New(), uuid.New(), future); err != nil {
		t.Fatal(err)
	}
	// El esquema anterior no admitia dos causas para la misma direccion.
	if _, err := pool.Exec(ctx, `INSERT INTO suppression.entries (tenant_id, email, reason) VALUES ($1, 'ana@example.com', 'manual')`, a); err == nil {
		t.Fatal("el esquema anterior debia rechazar una segunda fila de la misma direccion")
	}
	before := snapshotEntries(t, ctx, pool)

	applyFiles(t, ctx, pool, append(migrations, migrations...)...)

	if after := snapshotEntries(t, ctx, pool); !reflect.DeepEqual(before, after) {
		t.Fatalf("la migracion cambio filas:\nantes:   %v\ndespues: %v", before, after)
	}
	var constraints []string
	rows, err := pool.Query(ctx, `SELECT conname FROM pg_constraint
		WHERE conrelid = 'suppression.entries'::regclass AND contype = 'u' ORDER BY conname`)
	if err != nil {
		t.Fatal(err)
	}
	if constraints, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(constraints, []string{"suppression_entries_tenant_email_reason_key"}) {
		t.Fatalf("unicidad tras migrar: %v", constraints)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO suppression.entries (tenant_id, email, reason) VALUES ($1, 'ana@example.com', 'manual')`, a); err != nil {
		t.Fatalf("tras migrar, la baja y la manual conviven: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO suppression.entries (tenant_id, email, reason) VALUES ($1, 'ana@example.com', 'unsubscribe')`, a); err == nil {
		t.Fatal("la misma causa dos veces debe rechazarse")
	}
}

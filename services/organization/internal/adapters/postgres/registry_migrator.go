package postgres

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// RegistryMigrator aplica las migraciones de la base de registro (plano de control:
// organization, identity, access_control, ...) de forma idempotente y rastreada en
// public.schema_migrations, igual que el DBProvisioner hace con las bases de tenant.
// Corre en cada arranque: no depende del initdb del contenedor de Postgres, que solo
// se ejecuta la primera vez y nunca reaplica migraciones nuevas sobre una base viva.
type RegistryMigrator struct {
	pool       *pgxpool.Pool
	migrations []MigrationFile
	logger     *zap.Logger
}

func NewRegistryMigrator(pool *pgxpool.Pool, migrations []MigrationFile, logger *zap.Logger) *RegistryMigrator {
	return &RegistryMigrator{pool: pool, migrations: migrations, logger: logger}
}

// Run garantiza la tabla de control, hace un baseline en bases preexistentes y luego
// aplica las migraciones pendientes en orden.
func (m *RegistryMigrator) Run(ctx context.Context) error {
	if _, err := m.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.schema_migrations (
		name       TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("crear schema_migrations: %w", err)
	}

	if err := m.baselineExistingDatabase(ctx); err != nil {
		return err
	}

	for _, mig := range m.migrations {
		applied, err := m.isApplied(ctx, mig.Name)
		if err != nil {
			return err
		}
		if applied {
			continue
		}

		tx, err := m.pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("abrir transaccion para %s: %w", mig.Name, err)
		}
		if _, err = tx.Exec(ctx, mig.SQL); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migracion del registro %s fallo: %w", mig.Name, err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO public.schema_migrations (name) VALUES ($1)`, mig.Name); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("registrar migracion %s: %w", mig.Name, err)
		}
		if err = tx.Commit(ctx); err != nil {
			return fmt.Errorf("confirmar migracion %s: %w", mig.Name, err)
		}
		m.logger.Info("migracion del registro aplicada", zap.String("name", mig.Name))
	}
	return nil
}

func (m *RegistryMigrator) isApplied(ctx context.Context, name string) (bool, error) {
	var exists bool
	err := m.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM public.schema_migrations WHERE name = $1 OR name = $2)`,
		name, filepath.Base(name),
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("comprobar migracion %s: %w", name, err)
	}
	return exists, nil
}

// baselineExistingDatabase marca como aplicadas todas las migraciones presentes, sin
// ejecutarlas, cuando la base ya estaba inicializada (restaurada de un respaldo) pero
// aun no tenia registro en schema_migrations. Solo actua la primera vez (tabla de
// control vacia).
func (m *RegistryMigrator) baselineExistingDatabase(ctx context.Context) error {
	var tracked int
	if err := m.pool.QueryRow(ctx, `SELECT count(*) FROM public.schema_migrations`).Scan(&tracked); err != nil {
		return fmt.Errorf("contar schema_migrations: %w", err)
	}
	if tracked > 0 {
		return nil
	}

	// Indicador de "base ya inicializada": existe la tabla de tenants.
	var initialized bool
	if err := m.pool.QueryRow(ctx,
		`SELECT to_regclass('organization.tenants') IS NOT NULL`,
	).Scan(&initialized); err != nil {
		return fmt.Errorf("sondear organization.tenants: %w", err)
	}
	if !initialized {
		return nil
	}

	for _, mig := range m.migrations {
		if _, err := m.pool.Exec(ctx,
			`INSERT INTO public.schema_migrations (name) VALUES ($1) ON CONFLICT DO NOTHING`,
			mig.Name,
		); err != nil {
			return fmt.Errorf("baseline %s: %w", mig.Name, err)
		}
	}
	m.logger.Info("baseline de migraciones del registro aplicado sobre base existente",
		zap.Int("count", len(m.migrations)))
	return nil
}

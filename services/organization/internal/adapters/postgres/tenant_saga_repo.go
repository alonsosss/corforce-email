package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TenantSagaRepo persiste la saga de alta y baja de cada empresa (organization.tenant_sagas)
// y hace, en la misma transaccion, lo que la saga cambia en el registro de empresas.
//
// El arriendo se fija y se compara con el reloj de la base: dos instancias con relojes
// distintos no se disputan una saga.
type TenantSagaRepo struct {
	pool *pgxpool.Pool
}

func NewTenantSagaRepo(pool *pgxpool.Pool) *TenantSagaRepo {
	return &TenantSagaRepo{pool: pool}
}

const sagaColumns = `tenant_id, operation, state, step, admin_user_id, role_id, drop_database,
	attempts, last_error, lease_token, lease_until`

const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
)

func pgErrorCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func nullUUID(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}

func scanSaga(row pgx.Row) (*domain.TenantSaga, error) {
	s := &domain.TenantSaga{}
	var admin, role, token uuid.NullUUID
	if err := row.Scan(&s.TenantID, &s.Operation, &s.State, &s.Step, &admin, &role, &s.DropDatabase,
		&s.Attempts, &s.LastError, &token, &s.LeaseUntil); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrSagaNotFound
		}
		return nil, err
	}
	s.AdminUserID, s.RoleID, s.LeaseToken = admin.UUID, role.UUID, token.UUID
	return s, nil
}

func (r *TenantSagaRepo) BeginCreate(ctx context.Context, t *domain.Tenant, s *domain.TenantSaga, lease time.Duration) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// RETURNING: los sellos de tiempo los pone la base, y la respuesta del alta debe
	// llevarlos en vez de un cero.
	if err := tx.QueryRow(ctx,
		`INSERT INTO organization.tenants (id, slug, name, db_name, status, cell_id, settings)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING created_at, updated_at`,
		t.ID, t.Slug, t.Name, t.DBName, t.Status, t.CellID, t.Settings,
	).Scan(&t.CreatedAt, &t.UpdatedAt); err != nil {
		if pgErrorCode(err) == pgUniqueViolation {
			return domain.ErrTenantAlreadyExists
		}
		return fmt.Errorf("registrar la empresa: %w", err)
	}
	if err := insertSaga(ctx, tx, s, lease); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *TenantSagaRepo) Insert(ctx context.Context, s *domain.TenantSaga, lease time.Duration) error {
	return insertSaga(ctx, r.pool, s, lease)
}

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// insertSaga inserta la saga con un arriendo nuevo y lo deja en s. Una saga que ya existe
// es de otra peticion: ErrTenantBusy.
func insertSaga(ctx context.Context, q queryRower, s *domain.TenantSaga, lease time.Duration) error {
	token := uuid.New()
	var until time.Time
	err := q.QueryRow(ctx,
		`INSERT INTO organization.tenant_sagas (tenant_id, operation, state, step, admin_user_id, role_id,
		     drop_database, attempts, last_error, lease_token, lease_until)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, 1, $8, $9, now() + ($10::bigint * interval '1 millisecond'))
		 ON CONFLICT (tenant_id) DO NOTHING
		 RETURNING lease_until`,
		s.TenantID, s.Operation, s.State, s.Step, nullUUID(s.AdminUserID), nullUUID(s.RoleID),
		s.DropDatabase, s.LastError, token, lease.Milliseconds(),
	).Scan(&until)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return domain.ErrTenantBusy
	case pgErrorCode(err) == pgForeignKeyViolation:
		return domain.ErrTenantNotFound
	case err != nil:
		return fmt.Errorf("registrar la saga: %w", err)
	}
	s.LeaseToken, s.LeaseUntil, s.Attempts = token, &until, 1
	return nil
}

func (r *TenantSagaRepo) Get(ctx context.Context, tenantID uuid.UUID) (*domain.TenantSaga, error) {
	return scanSaga(r.pool.QueryRow(ctx,
		`SELECT `+sagaColumns+` FROM organization.tenant_sagas WHERE tenant_id = $1`, tenantID))
}

func (r *TenantSagaRepo) Claim(ctx context.Context, tenantID uuid.UUID, lease time.Duration) (*domain.TenantSaga, error) {
	s, err := scanSaga(r.pool.QueryRow(ctx,
		`UPDATE organization.tenant_sagas
		    SET lease_token = $2, lease_until = now() + ($3::bigint * interval '1 millisecond'),
		        attempts = attempts + 1
		  WHERE tenant_id = $1 AND (lease_until IS NULL OR lease_until < now())
		RETURNING `+sagaColumns,
		tenantID, uuid.New(), lease.Milliseconds()))
	if !errors.Is(err, domain.ErrSagaNotFound) {
		return s, err
	}
	var exists bool
	if err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM organization.tenant_sagas WHERE tenant_id = $1)`, tenantID,
	).Scan(&exists); err != nil {
		return nil, err
	}
	if exists {
		return nil, domain.ErrTenantBusy
	}
	return nil, domain.ErrSagaNotFound
}

func (r *TenantSagaRepo) Save(ctx context.Context, s *domain.TenantSaga, lease time.Duration) error {
	var until *time.Time
	err := r.pool.QueryRow(ctx,
		`UPDATE organization.tenant_sagas
		    SET operation = $3, state = $4, step = $5, role_id = $6, drop_database = $7, last_error = $8,
		        lease_until = CASE WHEN $9::bigint > 0 THEN now() + ($9::bigint * interval '1 millisecond') END
		  WHERE tenant_id = $1 AND lease_token = $2
		RETURNING lease_until`,
		s.TenantID, s.LeaseToken, s.Operation, s.State, s.Step, nullUUID(s.RoleID), s.DropDatabase,
		s.LastError, lease.Milliseconds(),
	).Scan(&until)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrLeaseLost
	}
	if err != nil {
		return fmt.Errorf("guardar la saga: %w", err)
	}
	s.LeaseUntil = until
	return nil
}

func (r *TenantSagaRepo) CompleteCreate(ctx context.Context, s *domain.TenantSaga) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx,
		`UPDATE organization.tenant_sagas
		    SET state = $3, step = $4, last_error = '', lease_until = NULL
		  WHERE tenant_id = $1 AND lease_token = $2 AND operation = $5`,
		s.TenantID, s.LeaseToken, domain.SagaCompleted, domain.StepActivated, domain.SagaCreate)
	if err != nil {
		return fmt.Errorf("cerrar la saga: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrLeaseLost
	}
	if _, err := tx.Exec(ctx,
		`UPDATE organization.tenants SET status = $2 WHERE id = $1`, s.TenantID, domain.TenantStatusActive); err != nil {
		return fmt.Errorf("activar la empresa: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.State, s.Step, s.LastError, s.LeaseUntil = domain.SagaCompleted, domain.StepActivated, "", nil
	return nil
}

// DeleteTenant borra primero la saga con su token: si otra instancia la tomo, no se borra
// nada. No hay claves foraneas hacia otros esquemas; lo de identity y access_control ya lo
// retiraron sus duenos en los pasos anteriores de la baja.
func (r *TenantSagaRepo) DeleteTenant(ctx context.Context, s *domain.TenantSaga) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx,
		`DELETE FROM organization.tenant_sagas WHERE tenant_id = $1 AND lease_token = $2`, s.TenantID, s.LeaseToken)
	if err != nil {
		return fmt.Errorf("borrar la saga: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrLeaseLost
	}
	for _, q := range []string{
		`DELETE FROM organization.tenant_modules WHERE tenant_id = $1`,
		`DELETE FROM organization.tenants WHERE id = $1`,
	} {
		if _, err := tx.Exec(ctx, q, s.TenantID); err != nil {
			return fmt.Errorf("retirar la empresa del registro: %w", err)
		}
	}
	return tx.Commit(ctx)
}

func (r *TenantSagaRepo) ListStale(ctx context.Context, limit int) ([]*domain.TenantSaga, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+sagaColumns+` FROM organization.tenant_sagas
		  WHERE state IN ($1, $2) AND (lease_until IS NULL OR lease_until < now())
		  ORDER BY updated_at
		  LIMIT $3`,
		domain.SagaRunning, domain.SagaCompensating, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.TenantSaga
	for rows.Next() {
		s, err := scanSaga(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

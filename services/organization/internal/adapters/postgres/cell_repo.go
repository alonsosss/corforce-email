package postgres

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CellRepo es el directorio de celdas en el esquema organization de la base de registro.
type CellRepo struct {
	pool *pgxpool.Pool
}

func NewCellRepo(pool *pgxpool.Pool) *CellRepo {
	return &CellRepo{pool: pool}
}

const cellColumns = `id, code, region, status, db_host, db_port, created_at`

// uniqueViolation es el SQLSTATE de una restriccion UNIQUE violada.
const uniqueViolation = "23505"

func scanCell(row pgx.Row) (*domain.Cell, error) {
	c := &domain.Cell{}
	if err := row.Scan(&c.ID, &c.Code, &c.Region, &c.Status, &c.DBHost, &c.DBPort, &c.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrCellNotFound
		}
		return nil, err
	}
	return c, nil
}

func (r *CellRepo) Create(ctx context.Context, c *domain.Cell) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO organization.cells (id, code, region, status, db_host, db_port)
 VALUES ($1, $2, $3, $4, $5, $6)`,
		c.ID, c.Code, c.Region, c.Status, c.DBHost, c.DBPort,
	)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return domain.ErrCellCodeExists
	}
	return err
}

func (r *CellRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Cell, error) {
	return scanCell(r.pool.QueryRow(ctx,
		`SELECT `+cellColumns+` FROM organization.cells WHERE id = $1`, id))
}

func (r *CellRepo) GetByCode(ctx context.Context, code string) (*domain.Cell, error) {
	return scanCell(r.pool.QueryRow(ctx,
		`SELECT `+cellColumns+` FROM organization.cells WHERE code = $1`, code))
}

func (r *CellRepo) List(ctx context.Context) ([]*domain.Cell, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+cellColumns+` FROM organization.cells ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cells []*domain.Cell
	for rows.Next() {
		c, err := scanCell(rows)
		if err != nil {
			return nil, err
		}
		cells = append(cells, c)
	}
	return cells, rows.Err()
}

func (r *CellRepo) Update(ctx context.Context, c *domain.Cell) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE organization.cells SET region = $1, status = $2 WHERE id = $3`,
		c.Region, c.Status, c.ID,
	)
	return err
}

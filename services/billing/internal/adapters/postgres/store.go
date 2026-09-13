// Package postgres guarda planes, suscripciones y contadores en el registro. billing no
// enruta por empresa: sus tablas viven en la base del registro y cada consulta filtra por
// tenant_id.
package postgres

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// uniqueViolation es el SQLSTATE de un choque con una restriccion UNIQUE.
const uniqueViolation = "23505"

// Store da acceso al registro. Usa la transaccion del contexto cuando la hay
// (db.ContextPool), de modo que los repositorios y outbox.Enqueue comparten la
// transaccion de negocio; fuera de ella, el pool del registro.
type Store struct {
	pool *pgxpool.Pool
	cp   db.ContextPool
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) bind(ctx context.Context) context.Context {
	if db.HasTx(ctx) {
		return ctx
	}
	return db.WithPool(ctx, s.pool)
}

func (s *Store) Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	return s.cp.Exec(s.bind(ctx), sql, args...)
}

func (s *Store) Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error) {
	return s.cp.Query(s.bind(ctx), sql, args...)
}

func (s *Store) QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	return s.cp.QueryRow(s.bind(ctx), sql, args...)
}

// Transact es reentrante: dentro de una transaccion abierta ejecuta fn en ella.
func (s *Store) Transact(ctx context.Context, fn func(ctx context.Context) error) error {
	if db.HasTx(ctx) {
		return fn(ctx)
	}
	return s.cp.Transact(s.bind(ctx), fn)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}

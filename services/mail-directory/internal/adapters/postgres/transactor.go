// Package postgres es el acceso al esquema mail de la base de la celda. Cada consulta
// filtra por tenant_id ademas de correr bajo el rol mail_app con RLS (Transactor):
// defensa en profundidad, ninguna de las dos capas se fia de la otra.
package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Transactor abre la transaccion sujeta a RLS de pkg/db. Sin ella el pool actua como
// dueno de las tablas y ve todas las empresas.
type Transactor struct {
	pool *db.ContextPool
}

func NewTransactor(pool *db.ContextPool) *Transactor { return &Transactor{pool: pool} }

func (t *Transactor) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return t.pool.TransactRLS(ctx, fn)
}

// uniqueViolation es el SQLSTATE de una restriccion UNIQUE violada.
const uniqueViolation = "23505"

// mapErr traduce los errores de pgx que tienen significado de dominio.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return domain.ErrAlreadyExists
	}
	return err
}

// affected convierte "cero filas tocadas" en ErrNotFound: bajo RLS una fila ajena no
// falla, simplemente no existe.
func affected(tag pgconn.CommandTag, err error) error {
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// likeEscaper deja como texto los comodines de LIKE que escriba el usuario.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// likePattern arma el patron de una busqueda por subcadena para ILIKE ... ESCAPE '\'.
// Vacio no filtra: las consultas comparan el parametro con ”.
func likePattern(search string) string {
	if search == "" {
		return ""
	}
	return "%" + likeEscaper.Replace(search) + "%"
}

// listPage ejecuta el conteo y la pagina de un listado con los mismos argumentos de
// filtro; limit y offset van al final de la consulta paginada.
func listPage[T any](ctx context.Context, pool *db.ContextPool, countSQL, pageSQL string, scan func(pgx.Row) (T, error), limit, offset int, args ...interface{}) ([]T, int64, error) {
	var total int64
	if err := pool.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	pageArgs := append(append([]interface{}{}, args...), limit, offset)
	rows, err := pool.Query(ctx, pageSQL, pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]T, 0, limit)
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

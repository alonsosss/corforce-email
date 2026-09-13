// Package postgres implementa los puertos sobre la base de la empresa (esquema
// automations). El pool o la transaccion salen del contexto (db.ContextPool).
package postgres

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// uniqueViolation es el SQLSTATE de un choque con una restriccion UNIQUE.
const uniqueViolation = "23505"

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}

// escapeLike neutraliza los comodines de LIKE en el texto del usuario.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// offset calcula el desplazamiento de una pagina (desde 1).
func offset(page, perPage int) int {
	if page < 1 {
		page = 1
	}
	return (page - 1) * perPage
}

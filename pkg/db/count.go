package db

import (
	"context"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// PageCountCap es hasta donde un listado paginado cuenta con exactitud. Un count(*) recorre
// todas las filas que casan y en la bitacora o el historial de una empresa grande cuesta
// decenas de milisegundos por pagina (17 a 21 ms con 100000 filas); acotado a este tope el
// coste deja de crecer con la tabla. Pasado el tope el listado dice "mas de" y no da el
// total: para llegar a lo antiguo se estrecha el filtro.
const PageCountCap int64 = 10000

// RowQuerier es lo que CountCapped necesita del pool: el pool de contexto, una transaccion o
// un pool de pgx.
type RowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// CountCapped cuenta las filas de fromWhere, la consulta desde FROM con su WHERE y sin ORDER BY
// ni LIMIT, sin pasar de limit+1. Devuelve el total exacto si hay limit o menos; si hay mas,
// devuelve limit y capped a true. limit es un entero que se escribe en la consulta, nunca
// texto del cliente.
func CountCapped(ctx context.Context, q RowQuerier, fromWhere string, args []any, limit int64) (total int64, capped bool, err error) {
	sql := `SELECT count(*) FROM (SELECT 1 ` + fromWhere + ` LIMIT ` + strconv.FormatInt(limit+1, 10) + `) AS bounded`
	if err = q.QueryRow(ctx, sql, args...).Scan(&total); err != nil {
		return 0, false, err
	}
	if total > limit {
		return limit, true, nil
	}
	return total, false, nil
}

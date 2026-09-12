package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TryLeaderLock toma un cerrojo de "una sola instancia a la vez" para un trabajo
// periodico (barridos, recordatorios, devengos) y devuelve como soltarlo. Si otra
// instancia ya lo tiene, devuelve ok=false sin esperar.
//
// Se implementa con pg_try_advisory_XACT_lock dentro de una transaccion que se deja
// abierta mientras dura el trabajo, y NO con pg_try_advisory_lock de sesion, por como
// llega la conexion a Postgres: pgbouncer en modo transaccion presta una conexion de
// servidor distinta a cada sentencia que no este dentro de una transaccion. Con el
// cerrojo de sesion, el lock se tomaba en una conexion, las siguientes sentencias iban
// por otra y el unlock por una tercera -"you don't own a lock of type ExclusiveLock" en
// el log de RDS-; el cerrojo original quedaba huerfano en el servidor y no protegia a
// nadie. Once servicios llevaban una copia de ese helper. Con la transaccion abierta,
// pgbouncer ancla la conexion de servidor al cliente hasta que se cierra, asi que el
// cerrojo vive exactamente lo que el trabajo y se suelta con el rollback, sin aviso.
//
// El coste es una conexion de servidor ocupada mientras corre el trabajo (los trabajos
// que lo usan duran segundos o minutos). La transaccion no ejecuta nada mas: no retiene
// filas para el vacuum ni bloquea a otros.
func TryLeaderLock(ctx context.Context, pool *pgxpool.Pool, key int64) (release func(), ok bool) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, false
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		conn.Release()
		return nil, false
	}
	var locked bool
	if err := tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1)", key).Scan(&locked); err != nil || !locked {
		_ = tx.Rollback(ctx)
		conn.Release()
		return nil, false
	}
	return func() {
		// context.Background: soltar debe ocurrir aunque el ctx del trabajo ya expirara.
		_ = tx.Rollback(context.Background())
		conn.Release()
	}, true
}

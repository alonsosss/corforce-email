package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/alonsosss/corforce-email/services/identity/internal/ports"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UnknownLoginRepo guarda en identity.unknown_login_failures los contadores de los correos sin
// cuenta. La clave es un resumen del ambito y el correo: la tabla no conserva lo que se probo.
type UnknownLoginRepo struct {
	pool *pgxpool.Pool
}

func NewUnknownLoginRepo(pool *pgxpool.Pool) *UnknownLoginRepo {
	return &UnknownLoginRepo{pool: pool}
}

// recordUnknownFailureSQL es domain.LoginFailures.RecordFailure en una sentencia. Con el bloqueo
// vigente el WHERE descarta la actualizacion y no vuelve ninguna fila: el intento no cuenta. La
// fila en conflicto queda bloqueada hasta el final, asi que dos fallos simultaneos cuentan dos.
// El contador vuelve a uno con la misma expresion que UserRepo.IncrementFailedAttempts.
// $1 clave, $2 ahora, $3 umbral, $4 fin del bloqueo si se alcanza, $5 limite de la ventana.
const recordUnknownFailureSQL = `
INSERT INTO identity.unknown_login_failures AS f (subject_hash, failed_attempts, last_failed_at, locked_until)
VALUES ($1, 1, $2, CASE WHEN 1 >= $3 THEN $4::timestamptz END)
ON CONFLICT (subject_hash) DO UPDATE
   SET failed_attempts = CASE WHEN GREATEST(f.last_failed_at, f.locked_until) < $5 THEN 1
                              ELSE f.failed_attempts + 1 END,
       last_failed_at  = $2,
       locked_until    = CASE WHEN (CASE WHEN GREATEST(f.last_failed_at, f.locked_until) < $5 THEN 1
                                         ELSE f.failed_attempts + 1 END) >= $3 THEN $4::timestamptz
                              ELSE f.locked_until END
 WHERE f.locked_until IS NULL OR f.locked_until <= $2
RETURNING failed_attempts`

func (r *UnknownLoginRepo) RecordFailure(ctx context.Context, subject string, now time.Time, maxAttempts int, lockout time.Duration) (bool, error) {
	var attempts int
	err := r.pool.QueryRow(ctx, recordUnknownFailureSQL,
		subject, now, maxAttempts, now.Add(lockout), now.Add(-domain.FailedLoginWindow)).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	return false, err
}

// PruneForgotten borra en lotes los contadores olvidados: los que el siguiente fallo reiniciaria
// a uno de todos modos, asi que borrarlos no cambia ninguna respuesta. SKIP LOCKED deja a las
// demas replicas y a los fallos en curso sin esperar.
func (r *UnknownLoginRepo) PruneForgotten(ctx context.Context, now time.Time, limit int) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM identity.unknown_login_failures
		  WHERE subject_hash IN (
		        SELECT subject_hash FROM identity.unknown_login_failures
		         WHERE GREATEST(last_failed_at, locked_until) < $1
		         LIMIT $2 FOR UPDATE SKIP LOCKED)`,
		now.Add(-domain.FailedLoginWindow), limit)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

var _ ports.UnknownLoginRepository = (*UnknownLoginRepo)(nil)

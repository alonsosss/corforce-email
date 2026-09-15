package postgres

import (
	"context"
	"fmt"

	"github.com/alonsosss/corforce-email/pkg/db"
)

// dkimLockSpace es la primera mitad de la clave de los cerrojos DKIM ("dkim"). La forma de dos
// enteros no comparte espacio de claves con los cerrojos de lider (un bigint, db.TryLeaderLock).
const dkimLockSpace int32 = 0x646b696d

// DKIMLock implementa ports.DKIMDomainLock con un cerrojo de transaccion en la base de la
// celda, que comparten todas las replicas: se suelta solo al terminar la transaccion, tambien
// si la replica muere a mitad.
type DKIMLock struct {
	pool *db.ContextPool
}

func NewDKIMLock(pool *db.ContextPool) *DKIMLock { return &DKIMLock{pool: pool} }

func (l *DKIMLock) WithDomainLock(ctx context.Context, domainName string, fn func(ctx context.Context) error) error {
	return l.pool.Transact(ctx, func(ctx context.Context) error {
		if _, err := l.pool.Exec(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, dkimLockSpace, domainName); err != nil {
			return fmt.Errorf("cerrojo DKIM de %s: %w", domainName, err)
		}
		return fn(ctx)
	})
}

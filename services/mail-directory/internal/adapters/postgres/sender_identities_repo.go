package postgres

import (
	"context"

	"github.com/alonsosss/corforce-email/pkg/db"
)

// SenderIdentityRepo lee mail.sender_identities, que aplica la misma regla que el mapa
// smtpd_sender_login_maps de Postfix (mail.sender_login_owners). Corre fuera de TransactRLS:
// la llamada interna no tiene empresa y el buzon se resuelve en toda la celda con el rol de
// servicio, igual que mail-auth.
type SenderIdentityRepo struct {
	pool *db.ContextPool
}

func NewSenderIdentityRepo(pool *db.ContextPool) *SenderIdentityRepo {
	return &SenderIdentityRepo{pool: pool}
}

func (r *SenderIdentityRepo) ForLogin(ctx context.Context, username string, limit int) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT address FROM mail.sender_identities($1) AS address ORDER BY address COLLATE "C" LIMIT $2`, username, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var address string
		if err := rows.Scan(&address); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, address)
	}
	return out, mapErr(rows.Err())
}

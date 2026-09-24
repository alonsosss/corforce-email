package postgres

import (
	"context"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// QuickReplyRepo guarda mail.mailbox_quick_replies. Filtra por tenant_id ademas de la RLS del Transactor.
type QuickReplyRepo struct{ pool *db.ContextPool }

func NewQuickReplyRepo(pool *db.ContextPool) *QuickReplyRepo { return &QuickReplyRepo{pool: pool} }

const quickReplyColumns = `id, tenant_id, username, name, html, text, created_at, updated_at`

func scanQuickReply(row pgx.Row, q *domain.QuickReply) error {
	return row.Scan(&q.ID, &q.TenantID, &q.Username, &q.Name, &q.HTML, &q.Text, &q.CreatedAt, &q.UpdatedAt)
}

func (r *QuickReplyRepo) ListByUsername(ctx context.Context, tenantID uuid.UUID, username string) ([]domain.QuickReply, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+quickReplyColumns+` FROM mail.mailbox_quick_replies
		  WHERE tenant_id = $1 AND username = $2 ORDER BY lower(name), id`,
		tenantID, username)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []domain.QuickReply{}
	for rows.Next() {
		var q domain.QuickReply
		if err := scanQuickReply(rows, &q); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, q)
	}
	return out, mapErr(rows.Err())
}

func (r *QuickReplyRepo) Count(ctx context.Context, tenantID uuid.UUID, username string) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM mail.mailbox_quick_replies WHERE tenant_id = $1 AND username = $2`,
		tenantID, username).Scan(&n)
	return n, mapErr(err)
}

func (r *QuickReplyRepo) Create(ctx context.Context, q *domain.QuickReply) error {
	return mapErr(scanQuickReply(r.pool.QueryRow(ctx,
		`INSERT INTO mail.mailbox_quick_replies (id, tenant_id, username, name, html, text)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING `+quickReplyColumns,
		q.ID, q.TenantID, q.Username, q.Name, q.HTML, q.Text), q))
}

func (r *QuickReplyRepo) Update(ctx context.Context, q *domain.QuickReply) error {
	return mapErr(scanQuickReply(r.pool.QueryRow(ctx,
		`UPDATE mail.mailbox_quick_replies SET name = $4, html = $5, text = $6
		  WHERE tenant_id = $1 AND username = $2 AND id = $3
		 RETURNING `+quickReplyColumns,
		q.TenantID, q.Username, q.ID, q.Name, q.HTML, q.Text), q))
}

func (r *QuickReplyRepo) Delete(ctx context.Context, tenantID uuid.UUID, username string, id uuid.UUID) error {
	return affected(r.pool.Exec(ctx,
		`DELETE FROM mail.mailbox_quick_replies WHERE tenant_id = $1 AND username = $2 AND id = $3`,
		tenantID, username, id))
}

func (r *QuickReplyRepo) DeleteByUsername(ctx context.Context, tenantID uuid.UUID, username string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM mail.mailbox_quick_replies WHERE tenant_id = $1 AND username = $2`, tenantID, username)
	return err
}

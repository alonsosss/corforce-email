package postgres

import (
	"context"
	"net"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// QuarantineNoticeRepository implementa ports.QuarantineNoticeRepository. El barrido del
// aviso corre sin empresa en la peticion, como el resto del camino de fondo, y filtra por
// tenant_id en cada sentencia; los enlaces corren dentro de la transaccion del caso de uso.
type QuarantineNoticeRepository struct {
	pool *db.ContextPool
}

func NewQuarantineNoticeRepository(pool *db.ContextPool) *QuarantineNoticeRepository {
	return &QuarantineNoticeRepository{pool: pool}
}

// PendingNotices numera las filas por buzon de la mas reciente a la mas antigua y se queda
// con las perMailbox primeras. La consulta recorre el indice parcial de pendientes.
func (r *QuarantineNoticeRepository) PendingNotices(ctx context.Context, tenantID uuid.UUID, maxScore decimal.Decimal, perMailbox int) ([]domain.QuarantineItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+quarantineColumns+` FROM (
			SELECT q.*, row_number() OVER (PARTITION BY q.rcpt ORDER BY q.created_at DESC, q.id DESC) AS rn
			  FROM mail_security.quarantine q
			 WHERE q.tenant_id = $1 AND q.notified = false AND q.score <= $2::numeric) pending
		 WHERE rn <= $3
		 ORDER BY rcpt, created_at DESC, id DESC`, tenantID, maxScore.String(), perMailbox)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.QuarantineItem
	for rows.Next() {
		it, err := scanQuarantine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *it)
	}
	return out, rows.Err()
}

func (r *QuarantineNoticeRepository) MarkNotified(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE mail_security.quarantine SET notified = true
		 WHERE tenant_id = $1 AND id = ANY($2::uuid[])`, tenantID, ids)
	return err
}

func (r *QuarantineNoticeRepository) InsertNotice(ctx context.Context, n *domain.QuarantineNotice) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO mail_security.quarantine_notices
		    (id, tenant_id, rcpt, idempotency_key, status, message_id, error_code, quarantine_ids, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::uuid[], $9)
		ON CONFLICT (tenant_id, idempotency_key) DO NOTHING`,
		n.ID, n.TenantID, n.Rcpt, n.IdempotencyKey, string(n.Status), n.MessageID, n.ErrorCode, n.QuarantineIDs, n.CreatedAt)
	return err
}

func (r *QuarantineNoticeRepository) FindByQHash(ctx context.Context, tenantID uuid.UUID, qhash string) (*domain.QuarantineItem, error) {
	it, err := scanQuarantine(r.pool.QueryRow(ctx,
		`SELECT `+quarantineColumns+` FROM mail_security.quarantine WHERE tenant_id = $1 AND qhash = $2`, tenantID, qhash))
	return it, notFound(err)
}

func (r *QuarantineNoticeRepository) LockForLink(ctx context.Context, tenantID, id uuid.UUID) (*domain.QuarantineItem, error) {
	it, err := scanQuarantine(r.pool.QueryRow(ctx,
		`SELECT `+quarantineColumns+` FROM mail_security.quarantine WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenantID, id))
	return it, notFound(err)
}

// InsertLinkUse: la unicidad (tenant_id, quarantine_id) es la que hace el enlace de un solo
// uso; si otra transaccion registro el uso a la vez, esta espera a que confirme y no inserta.
func (r *QuarantineNoticeRepository) InsertLinkUse(ctx context.Context, u *domain.QuarantineLinkUse) error {
	var ip *string
	if parsed := net.ParseIP(u.ClientIP); parsed != nil {
		s := parsed.String()
		ip = &s
	}
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO mail_security.quarantine_link_uses (tenant_id, quarantine_id, action, rcpt, client_ip, user_agent, used_at)
		VALUES ($1, $2, $3, $4, $5::inet, $6, $7)
		ON CONFLICT (tenant_id, quarantine_id) DO NOTHING`,
		u.TenantID, u.QuarantineID, string(u.Action), u.Rcpt, ip, u.UserAgent, u.UsedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrLinkUsed
	}
	return nil
}

func (r *QuarantineNoticeRepository) PruneHistory(ctx context.Context, defaultMaxAgeDays int) (int64, error) {
	var total int64
	for _, stmt := range []string{
		`DELETE FROM mail_security.quarantine_notices n
		  WHERE n.created_at < now() - make_interval(days => COALESCE(
		        (SELECT s.max_age_days FROM mail_security.quarantine_settings s WHERE s.tenant_id = n.tenant_id), $1))`,
		`DELETE FROM mail_security.quarantine_link_uses u
		  WHERE u.used_at < now() - make_interval(days => COALESCE(
		        (SELECT s.max_age_days FROM mail_security.quarantine_settings s WHERE s.tenant_id = u.tenant_id), $1))`,
	} {
		tag, err := r.pool.Exec(ctx, stmt, defaultMaxAgeDays)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
	}
	return total, nil
}

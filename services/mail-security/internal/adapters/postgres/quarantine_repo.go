package postgres

import (
	"context"
	"net"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// QuarantineRepository implementa ports.QuarantineRepository. Insert y las podas llegan
// del exportador de Rspamd sin empresa en el contexto (como duena del pool); el resto
// del API de administracion, bajo RLS y con tenant_id explicito.
type QuarantineRepository struct {
	pool *db.ContextPool
}

func NewQuarantineRepository(pool *db.ContextPool) *QuarantineRepository {
	return &QuarantineRepository{pool: pool}
}

const quarantineColumns = `id, tenant_id, qid, subject, score::text, COALESCE(host(ip), ''), action, symbols, fuzzy_hashes,
	sender, rcpt, domain, notified, user_name, qhash, octet_length(msg), created_at`

// scanQuarantine lee quarantineColumns y, detras, las columnas extra que pida la consulta.
func scanQuarantine(row pgx.Row, extra ...interface{}) (*domain.QuarantineItem, error) {
	var it domain.QuarantineItem
	var score string
	dest := append([]interface{}{&it.ID, &it.TenantID, &it.QID, &it.Subject, &score, &it.IP, &it.Action, &it.Symbols, &it.FuzzyHashes,
		&it.Sender, &it.Rcpt, &it.Domain, &it.Notified, &it.UserName, &it.QHash, &it.Size, &it.CreatedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	var err error
	if it.Score, err = scanDecimal(score); err != nil {
		return nil, err
	}
	if it.Symbols == nil {
		it.Symbols = []string{}
	}
	if it.FuzzyHashes == nil {
		it.FuzzyHashes = []string{}
	}
	return &it, nil
}

func (r *QuarantineRepository) Insert(ctx context.Context, it *domain.QuarantineItem) error {
	// Una IP que Rspamd no supo dar (vacia o no parseable) se guarda como NULL en vez
	// de tumbar el insert de todo el mensaje.
	var ip *string
	if parsed := net.ParseIP(it.IP); parsed != nil {
		s := parsed.String()
		ip = &s
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO mail_security.quarantine
		    (id, tenant_id, qid, subject, score, ip, action, symbols, fuzzy_hashes, sender, rcpt, msg, domain, user_name, qhash, created_at)
		VALUES ($1, $2, $3, $4, $5::numeric, $6::inet, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		it.ID, it.TenantID, it.QID, it.Subject, it.Score.String(), ip, it.Action, it.Symbols, it.FuzzyHashes,
		it.Sender, it.Rcpt, it.Msg, it.Domain, it.UserName, it.QHash, it.CreatedAt)
	return err
}

// PruneRcpt conserva las keep filas mas recientes del buzon y borra el resto.
func (r *QuarantineRepository) PruneRcpt(ctx context.Context, tenantID uuid.UUID, rcpt string, keep int) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM mail_security.quarantine WHERE id IN (
			SELECT id FROM mail_security.quarantine
			 WHERE tenant_id = $1 AND rcpt = $2
			 ORDER BY created_at DESC, id OFFSET $3)`, tenantID, rcpt, keep)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// PruneAged borra lo mas antiguo que maxAgeDays. Con tenantID nulo actua sobre las
// empresas SIN fila de ajustes, que se rigen por el valor por defecto.
func (r *QuarantineRepository) PruneAged(ctx context.Context, tenantID uuid.UUID, maxAgeDays int) (int64, error) {
	var tag interface{ RowsAffected() int64 }
	var err error
	if tenantID == uuid.Nil {
		tag, err = r.pool.Exec(ctx, `
			DELETE FROM mail_security.quarantine q
			 WHERE q.created_at < now() - make_interval(days => $1)
			   AND NOT EXISTS (SELECT 1 FROM mail_security.quarantine_settings s WHERE s.tenant_id = q.tenant_id)`, maxAgeDays)
	} else {
		tag, err = r.pool.Exec(ctx, `
			DELETE FROM mail_security.quarantine
			 WHERE tenant_id = $1 AND created_at < now() - make_interval(days => $2)`, tenantID, maxAgeDays)
	}
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (r *QuarantineRepository) List(ctx context.Context, tenantID uuid.UUID, f domain.QuarantineFilter) ([]domain.QuarantineItem, int64, error) {
	scoreMin := ""
	if f.ScoreMin != nil {
		scoreMin = f.ScoreMin.String()
	}
	var total int64
	if err := r.pool.QueryRow(ctx, `
		SELECT count(*) FROM mail_security.quarantine
		 WHERE tenant_id = $1 AND ($2 = '' OR rcpt = $2) AND ($3 = '' OR score >= $3::numeric)`,
		tenantID, f.Rcpt, scoreMin).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+quarantineColumns+` FROM mail_security.quarantine
		 WHERE tenant_id = $1 AND ($2 = '' OR rcpt = $2) AND ($3 = '' OR score >= $3::numeric)
		 ORDER BY created_at DESC, id
		 LIMIT $4 OFFSET $5`, tenantID, f.Rcpt, scoreMin, f.PerPage, (f.Page-1)*f.PerPage)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []domain.QuarantineItem{}
	for rows.Next() {
		it, err := scanQuarantine(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *it)
	}
	return out, total, rows.Err()
}

func (r *QuarantineRepository) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.QuarantineItem, error) {
	it, err := scanQuarantine(r.pool.QueryRow(ctx, `SELECT `+quarantineColumns+` FROM mail_security.quarantine WHERE tenant_id = $1 AND id = $2`, tenantID, id))
	return it, notFound(err)
}

func (r *QuarantineRepository) GetMessage(ctx context.Context, tenantID, id uuid.UUID) ([]byte, error) {
	var msg []byte
	err := r.pool.QueryRow(ctx, `SELECT msg FROM mail_security.quarantine WHERE tenant_id = $1 AND id = $2`, tenantID, id).Scan(&msg)
	return msg, notFound(err)
}

// LockForRelease lee la fila con su mensaje y la bloquea hasta el fin de la transaccion.
func (r *QuarantineRepository) LockForRelease(ctx context.Context, tenantID, id uuid.UUID) (*domain.QuarantineItem, error) {
	var msg []byte
	it, err := scanQuarantine(r.pool.QueryRow(ctx,
		`SELECT `+quarantineColumns+`, msg FROM mail_security.quarantine WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenantID, id), &msg)
	if err != nil {
		return nil, notFound(err)
	}
	it.Msg = msg
	return it, nil
}

func (r *QuarantineRepository) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM mail_security.quarantine WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

package postgres

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/alonsosss/corforce-email/services/contacts/internal/ports"
	"github.com/alonsosss/corforce-email/services/contacts/internal/segment"
	"github.com/google/uuid"
)

// EngagementRepository mantiene contacts.engagement.
type EngagementRepository struct {
	pool *db.ContextPool
}

func NewEngagementRepository(pool *db.ContextPool) *EngagementRepository {
	return &EngagementRepository{pool: pool}
}

// touchSQL inserta solo si el contacto existe en la empresa y, si la fila ya estaba, se
// queda con la recepcion mas antigua y la apertura y el clic mas recientes (GREATEST y
// LEAST ignoran los NULL). El WHERE del DO UPDATE evita reescribir una fila que el toque no
// cambia: una reentrega no genera escritura.
const touchSQL = `INSERT INTO contacts.engagement AS e
        (tenant_id, contact_id, campaign_id, received_at, last_opened_at, last_clicked_at, last_event_at)
 SELECT c.tenant_id, c.id, $3::uuid, $4::timestamptz, $5::timestamptz, $6::timestamptz,
        GREATEST($4::timestamptz, $5::timestamptz, $6::timestamptz)
   FROM contacts.contacts c
  WHERE c.tenant_id = $1 AND c.id = $2
 ON CONFLICT (contact_id, campaign_id) DO UPDATE
    SET received_at = LEAST(e.received_at, EXCLUDED.received_at),
        last_opened_at = GREATEST(e.last_opened_at, EXCLUDED.last_opened_at),
        last_clicked_at = GREATEST(e.last_clicked_at, EXCLUDED.last_clicked_at),
        last_event_at = GREATEST(e.last_event_at, EXCLUDED.last_event_at)
  WHERE EXCLUDED.received_at < e.received_at
     OR EXCLUDED.last_opened_at > e.last_opened_at OR (e.last_opened_at IS NULL AND EXCLUDED.last_opened_at IS NOT NULL)
     OR EXCLUDED.last_clicked_at > e.last_clicked_at OR (e.last_clicked_at IS NULL AND EXCLUDED.last_clicked_at IS NOT NULL)
 RETURNING true`

func (r *EngagementRepository) Touch(ctx context.Context, t domain.EngagementTouch) (bool, error) {
	tag, err := r.pool.Exec(ctx, touchSQL, t.TenantID, t.ContactID, t.CampaignID, t.ReceivedAt, t.OpenedAt, t.ClickedAt)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 1 {
		return true, nil
	}
	// Sin fila afectada: o el contacto no existe, o el toque no cambiaba nada.
	var exists bool
	err = r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM contacts.contacts WHERE tenant_id = $1 AND id = $2)`,
		t.TenantID, t.ContactID).Scan(&exists)
	return exists, err
}

// Prune borra por tandas sobre el indice (tenant_id, last_event_at): una poda grande no
// retiene bloqueos sobre la tabla entera.
func (r *EngagementRepository) Prune(ctx context.Context, tenantID uuid.UUID, before time.Time, limit int) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM contacts.engagement
		  WHERE ctid IN (SELECT ctid FROM contacts.engagement
		                  WHERE tenant_id = $1 AND last_event_at < $2
		                  LIMIT $3)`,
		tenantID, before, limit)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// MatchAmong evalua la definicion solo sobre los ids pedidos (clave primaria).
func (q *SegmentQuery) MatchAmong(ctx context.Context, tenantID uuid.UUID, def segment.Definition, schema segment.Schema, ids []uuid.UUID) ([]uuid.UUID, error) {
	args := segment.NewArgs(tenantID, ids)
	where, err := segment.Compile(def, schema, args)
	if err != nil {
		return nil, err
	}
	rows, err := q.pool.Query(ctx,
		`SELECT c.id FROM contacts.contacts c WHERE c.tenant_id = $1 AND c.id = ANY($2::uuid[]) AND `+where+
			` ORDER BY c.id`, args.Values()...)
	if err != nil {
		return nil, err
	}
	return collectIDs(rows)
}

// AnniversaryCandidates recorre por (tenant_id, id) los contactos cuyo valor de la clave
// tiene un dia y mes candidato. La clave y los candidatos viajan como argumentos.
func (q *SegmentQuery) AnniversaryCandidates(ctx context.Context, tenantID uuid.UUID, aq ports.AnniversaryQuery) ([]ports.AnniversaryCandidate, error) {
	rows, err := q.pool.Query(ctx,
		`SELECT c.id, c.attributes ->> $2::text, c.timezone
		   FROM contacts.contacts c
		  WHERE c.tenant_id = $1 AND c.id > $3
		    AND jsonb_typeof(c.attributes -> $2::text) = 'string'
		    AND substring(c.attributes ->> $2::text from 6 for 5) = ANY($4::text[])
		    AND ($5::uuid IS NULL OR EXISTS (
		          SELECT 1 FROM contacts.list_members lm
		           WHERE lm.tenant_id = $1 AND lm.list_id = $5::uuid AND lm.contact_id = c.id))
		  ORDER BY c.id
		  LIMIT $6`,
		tenantID, aq.Attribute, aq.After, aq.MonthDays, aq.ListID, aq.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ports.AnniversaryCandidate{}
	for rows.Next() {
		var c ports.AnniversaryCandidate
		if err := rows.Scan(&c.ID, &c.Value, &c.Timezone); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

var (
	_ ports.EngagementRepository = (*EngagementRepository)(nil)
	_ ports.ContactMatcher       = (*SegmentQuery)(nil)
)

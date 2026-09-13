package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ConsentRepository solo inserta y lee. La tabla rechaza por trigger cualquier UPDATE o
// DELETE fuera del borrado del titular (ContactRepository.Erase).
type ConsentRepository struct {
	pool *db.ContextPool
}

func NewConsentRepository(pool *db.ContextPool) *ConsentRepository {
	return &ConsentRepository{pool: pool}
}

func (r *ConsentRepository) Append(ctx context.Context, c *domain.Consent) error {
	evidence, err := jsonObject(c.Evidence)
	if err != nil {
		return err
	}
	return r.pool.QueryRow(ctx,
		`INSERT INTO contacts.consents (tenant_id, contact_id, purpose, status, method, source, ip, user_agent, evidence)
		 VALUES ($1, $2, $3, $4, $5, $6, $7::inet, $8, $9)
		 RETURNING id, occurred_at`,
		c.TenantID, c.ContactID, c.Purpose, c.Status, c.Method, c.Source, c.IP, c.UserAgent, evidence,
	).Scan(&c.ID, &c.OccurredAt)
}

type batchConsent struct {
	ContactID uuid.UUID      `json:"contact_id"`
	Purpose   string         `json:"purpose"`
	Status    string         `json:"status"`
	Method    string         `json:"method"`
	Source    string         `json:"source"`
	IP        *string        `json:"ip"`
	UserAgent *string        `json:"user_agent"`
	Evidence  map[string]any `json:"evidence"`
}

func (r *ConsentRepository) AppendMany(ctx context.Context, consents []domain.Consent) error {
	if len(consents) == 0 {
		return nil
	}
	rows := make([]batchConsent, len(consents))
	for i, c := range consents {
		ev := c.Evidence
		if ev == nil {
			ev = map[string]any{}
		}
		rows[i] = batchConsent{
			ContactID: c.ContactID, Purpose: c.Purpose, Status: string(c.Status), Method: string(c.Method),
			Source: c.Source, IP: c.IP, UserAgent: c.UserAgent, Evidence: ev,
		}
	}
	batch, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx,
		`INSERT INTO contacts.consents (tenant_id, contact_id, purpose, status, method, source, ip, user_agent, evidence)
		 SELECT $1, b.contact_id, b.purpose, b.status, b.method, b.source, b.ip::inet, b.user_agent, b.evidence
		   FROM jsonb_to_recordset($2::jsonb) AS b(contact_id uuid, purpose text, status text, method text,
		        source text, ip text, user_agent text, evidence jsonb)`,
		consents[0].TenantID, batch)
	return err
}

func (r *ConsentRepository) ListByContact(ctx context.Context, tenantID, contactID uuid.UUID) ([]domain.Consent, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, tenant_id, contact_id, purpose, status, method, source, host(ip), user_agent, evidence, occurred_at
		   FROM contacts.consents
		  WHERE tenant_id = $1 AND contact_id = $2
		  ORDER BY occurred_at, id`, tenantID, contactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Consent{}
	for rows.Next() {
		var (
			c        domain.Consent
			evidence []byte
		)
		if err := rows.Scan(&c.ID, &c.TenantID, &c.ContactID, &c.Purpose, &c.Status, &c.Method, &c.Source,
			&c.IP, &c.UserAgent, &evidence, &c.OccurredAt); err != nil {
			return nil, err
		}
		if c.Evidence, err = readObject(evidence); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// TokenRepository guarda las huellas de los enlaces de doble opt-in.
type TokenRepository struct {
	pool *db.ContextPool
}

func NewTokenRepository(pool *db.ContextPool) *TokenRepository {
	return &TokenRepository{pool: pool}
}

func (r *TokenRepository) Create(ctx context.Context, t *domain.ConfirmationToken) error {
	return r.pool.QueryRow(ctx,
		`INSERT INTO contacts.confirmation_tokens (tenant_id, contact_id, token_hash, expires_at)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, created_at`,
		t.TenantID, t.ContactID, t.TokenHash, t.ExpiresAt,
	).Scan(&t.ID, &t.CreatedAt)
}

func (r *TokenRepository) DeleteUnused(ctx context.Context, tenantID, contactID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM contacts.confirmation_tokens WHERE tenant_id = $1 AND contact_id = $2 AND used_at IS NULL`,
		tenantID, contactID)
	return err
}

const tokenColumns = `id, tenant_id, contact_id, token_hash, expires_at, used_at, created_at`

func (r *TokenRepository) get(ctx context.Context, sql string, tenantID uuid.UUID, hash string) (*domain.ConfirmationToken, error) {
	var t domain.ConfirmationToken
	err := r.pool.QueryRow(ctx, sql, tenantID, hash).Scan(&t.ID, &t.TenantID, &t.ContactID, &t.TokenHash, &t.ExpiresAt, &t.UsedAt, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrInvalidConfirmation
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *TokenRepository) GetByHash(ctx context.Context, tenantID uuid.UUID, hash string) (*domain.ConfirmationToken, error) {
	return r.get(ctx, `SELECT `+tokenColumns+` FROM contacts.confirmation_tokens WHERE tenant_id = $1 AND token_hash = $2`, tenantID, hash)
}

func (r *TokenRepository) GetByHashForUpdate(ctx context.Context, tenantID uuid.UUID, hash string) (*domain.ConfirmationToken, error) {
	return r.get(ctx, `SELECT `+tokenColumns+` FROM contacts.confirmation_tokens WHERE tenant_id = $1 AND token_hash = $2 FOR UPDATE`, tenantID, hash)
}

func (r *TokenRepository) MarkUsed(ctx context.Context, tenantID, id uuid.UUID, at time.Time) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE contacts.confirmation_tokens SET used_at = $3 WHERE tenant_id = $1 AND id = $2 AND used_at IS NULL`,
		tenantID, id, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrInvalidConfirmation
	}
	return nil
}

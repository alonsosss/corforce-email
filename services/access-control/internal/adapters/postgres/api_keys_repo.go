package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/services/access-control/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Subjects de las claves de API. Los consume audit (access.> en el stream ACCESS) y los declara
// tambien access-control al arrancar (APIKeyStream), para que la outbox no espere a audit.
const (
	APIKeyStream          = "ACCESS"
	SubjectAPIKeyCreated  = "access.api_key.created"
	SubjectAPIKeyRevoked  = "access.api_key.revoked"
	apiKeyEventSource     = "access-control-service"
	apiKeyEventTypePrefix = "api_key."
)

// APIKeySubjects son los subjects que el stream de access-control debe cubrir.
func APIKeySubjects() []string { return []string{"access.>"} }

type APIKeyRepo struct {
	pool *pgxpool.Pool
}

func NewAPIKeyRepo(pool *pgxpool.Pool) *APIKeyRepo {
	return &APIKeyRepo{pool: pool}
}

const apiKeyColumns = `k.id, k.tenant_id, k.name, k.kind, k.prefix, k.secret_hash, k.hash_key_id, k.created_by,
	k.expires_at, k.revoked_at, k.revoked_by, k.last_used_at, COALESCE(k.last_used_ip, ''), k.created_at`

func scanAPIKey(row pgx.Row) (*domain.APIKey, error) {
	k := &domain.APIKey{}
	err := row.Scan(&k.ID, &k.TenantID, &k.Name, &k.Kind, &k.Prefix, &k.SecretHash, &k.HashKeyID, &k.CreatedBy,
		&k.ExpiresAt, &k.RevokedAt, &k.RevokedBy, &k.LastUsedAt, &k.LastUsedIP, &k.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrAPIKeyNotFound
	}
	return k, err
}

// loadScopes rellena el alcance de las claves con una sola consulta.
func loadScopes(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, keys []*domain.APIKey) error {
	if len(keys) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(keys))
	byID := make(map[uuid.UUID]*domain.APIKey, len(keys))
	for i, k := range keys {
		ids[i] = k.ID
		byID[k.ID] = k
		k.Scopes = []domain.Permission{}
	}
	rows, err := q.Query(ctx,
		`SELECT s.api_key_id, p.id, p.module, p.resource, p.action, p.description, p.scope
		   FROM access_control.api_key_scopes s
		   JOIN access_control.permissions p ON p.id = s.permission_id
		  WHERE s.api_key_id = ANY($1)
		  ORDER BY p.module, p.resource, p.action`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var keyID uuid.UUID
		var p domain.Permission
		if err := rows.Scan(&keyID, &p.ID, &p.Module, &p.Resource, &p.Action, &p.Description, &p.Scope); err != nil {
			return err
		}
		byID[keyID].Scopes = append(byID[keyID].Scopes, p)
	}
	return rows.Err()
}

func (r *APIKeyRepo) Create(ctx context.Context, key *domain.APIKey, event domain.APIKeyEvent) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`INSERT INTO access_control.api_keys (id, tenant_id, name, kind, prefix, secret_hash, hash_key_id, created_by, expires_at, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		key.ID, key.TenantID, key.Name, domain.APIKeyKindOrDefault(key.Kind), key.Prefix, key.SecretHash, key.HashKeyID, key.CreatedBy, key.ExpiresAt, key.CreatedAt,
	); err != nil {
		return err
	}
	for _, p := range key.Scopes {
		if _, err := tx.Exec(ctx,
			`INSERT INTO access_control.api_key_scopes (api_key_id, permission_id) VALUES ($1, $2)`, key.ID, p.ID); err != nil {
			return err
		}
	}
	if err := outbox.Enqueue(ctx, tx, SubjectAPIKeyCreated, apiKeyEnvelope(event)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *APIKeyRepo) List(ctx context.Context, tenantID uuid.UUID) ([]*domain.APIKey, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+apiKeyColumns+` FROM access_control.api_keys k WHERE k.tenant_id = $1 ORDER BY k.created_at DESC, k.id`, tenantID)
	if err != nil {
		return nil, err
	}
	keys := make([]*domain.APIKey, 0)
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return keys, loadScopes(ctx, r.pool, keys)
}

func (r *APIKeyRepo) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.APIKey, error) {
	k, err := scanAPIKey(r.pool.QueryRow(ctx,
		`SELECT `+apiKeyColumns+` FROM access_control.api_keys k WHERE k.tenant_id = $1 AND k.id = $2`, tenantID, id))
	if err != nil {
		return nil, err
	}
	return k, loadScopes(ctx, r.pool, []*domain.APIKey{k})
}

func (r *APIKeyRepo) GetByPrefix(ctx context.Context, prefix string) (*domain.APIKey, error) {
	k, err := scanAPIKey(r.pool.QueryRow(ctx,
		`SELECT `+apiKeyColumns+` FROM access_control.api_keys k WHERE k.prefix = $1`, prefix))
	if err != nil {
		return nil, err
	}
	return k, loadScopes(ctx, r.pool, []*domain.APIKey{k})
}

func (r *APIKeyRepo) CountActive(ctx context.Context, tenantID uuid.UUID, now time.Time) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM access_control.api_keys
		  WHERE tenant_id = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > $2)`,
		tenantID, now).Scan(&n)
	return n, err
}

// Revoke bloquea la fila para que dos revocaciones simultaneas no dejen dos eventos.
func (r *APIKeyRepo) Revoke(ctx context.Context, tenantID, id, actor uuid.UUID, at time.Time, event func(*domain.APIKey) domain.APIKeyEvent) (*domain.APIKey, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	k, err := scanAPIKey(tx.QueryRow(ctx,
		`SELECT `+apiKeyColumns+` FROM access_control.api_keys k WHERE k.tenant_id = $1 AND k.id = $2 FOR UPDATE`, tenantID, id))
	if err != nil {
		return nil, err
	}
	if k.RevokedAt != nil {
		return nil, domain.ErrAPIKeyRevoked
	}
	var by any
	if actor != uuid.Nil {
		by = actor
	}
	if _, err := tx.Exec(ctx,
		`UPDATE access_control.api_keys SET revoked_at = $2, revoked_by = $3 WHERE id = $1`, k.ID, at, by); err != nil {
		return nil, err
	}
	k.RevokedAt = &at
	if actor != uuid.Nil {
		a := actor
		k.RevokedBy = &a
	}
	if err := loadScopes(ctx, tx, []*domain.APIKey{k}); err != nil {
		return nil, err
	}
	if err := outbox.Enqueue(ctx, tx, SubjectAPIKeyRevoked, apiKeyEnvelope(event(k))); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return k, nil
}

func (r *APIKeyRepo) TouchUsage(ctx context.Context, id uuid.UUID, at time.Time, ip string, minInterval time.Duration) error {
	var addr any
	if ip != "" {
		addr = ip
	}
	_, err := r.pool.Exec(ctx,
		`UPDATE access_control.api_keys SET last_used_at = $2, last_used_ip = $3
		  WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < $4)`,
		id, at, addr, at.Add(-minInterval))
	return err
}

func (r *APIKeyRepo) Rehash(ctx context.Context, id uuid.UUID, hash []byte, keyID string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE access_control.api_keys SET secret_hash = $2, hash_key_id = $3 WHERE id = $1`, id, hash, keyID)
	return err
}

func (r *APIKeyRepo) GrantablePermissions(ctx context.Context) ([]*domain.Permission, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+permissionColumns+` FROM access_control.permissions WHERE api_key_grantable ORDER BY module, resource, action`)
	if err != nil {
		return nil, err
	}
	return collectPermissions(rows)
}

// apiKeyEnvelope es el sobre del evento. Payload: tenant_id, api_key_id, name, prefix, scopes
// (module:resource:action), expires_at (RFC 3339 o nulo) y occurred_at. Nunca el hash.
func apiKeyEnvelope(e domain.APIKeyEvent) events.Event {
	scopes := make([]string, len(e.Scopes))
	for i, p := range e.Scopes {
		scopes[i] = fmt.Sprintf("%s:%s:%s", p.Module, p.Resource, p.Action)
	}
	var expires any
	if e.ExpiresAt != nil {
		expires = e.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	var actor string
	if e.ActorID != uuid.Nil {
		actor = e.ActorID.String()
	}
	return events.Event{
		Type:     apiKeyEventTypePrefix + e.Type,
		Source:   apiKeyEventSource,
		TenantID: e.TenantID.String(),
		UserID:   actor,
		Data: map[string]interface{}{
			"tenant_id":   e.TenantID.String(),
			"api_key_id":  e.KeyID.String(),
			"name":        e.Name,
			"prefix":      e.Prefix,
			"scopes":      scopes,
			"expires_at":  expires,
			"occurred_at": e.At.UTC().Format(time.RFC3339Nano),
		},
	}
}

package postgres

import (
	"context"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ── Sender ACL ────────────────────────────────────────────────────────────────

type SenderACLRepo struct {
	pool *db.ContextPool
}

func NewSenderACLRepo(pool *db.ContextPool) *SenderACLRepo { return &SenderACLRepo{pool: pool} }

const senderACLColumns = `id, tenant_id, logged_in_as, send_as, external, created_at`

func scanSenderACL(row pgx.Row) (domain.SenderACL, error) {
	var a domain.SenderACL
	err := row.Scan(&a.ID, &a.TenantID, &a.LoggedInAs, &a.SendAs, &a.External, &a.CreatedAt)
	return a, mapErr(err)
}

func (r *SenderACLRepo) List(ctx context.Context, tenantID uuid.UUID, page ports.Page) ([]domain.SenderACL, int64, error) {
	return listPage(ctx, r.pool,
		`SELECT COUNT(*) FROM mail.sender_acl WHERE tenant_id = $1`,
		`SELECT `+senderACLColumns+` FROM mail.sender_acl WHERE tenant_id = $1 ORDER BY logged_in_as, send_as LIMIT $2 OFFSET $3`,
		scanSenderACL, page.Limit, page.Offset, tenantID)
}

func (r *SenderACLRepo) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.SenderACL, error) {
	a, err := scanSenderACL(r.pool.QueryRow(ctx,
		`SELECT `+senderACLColumns+` FROM mail.sender_acl WHERE tenant_id = $1 AND id = $2`, tenantID, id))
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *SenderACLRepo) Create(ctx context.Context, a *domain.SenderACL) error {
	return mapErr(r.pool.QueryRow(ctx,
		`INSERT INTO mail.sender_acl (id, tenant_id, logged_in_as, send_as, external)
 VALUES ($1, $2, $3, $4, $5) RETURNING created_at`,
		a.ID, a.TenantID, a.LoggedInAs, a.SendAs, a.External,
	).Scan(&a.CreatedAt))
}

func (r *SenderACLRepo) Update(ctx context.Context, a *domain.SenderACL) error {
	return affected(r.pool.Exec(ctx,
		`UPDATE mail.sender_acl SET send_as = $3, external = $4 WHERE tenant_id = $1 AND id = $2`,
		a.TenantID, a.ID, a.SendAs, a.External))
}

func (r *SenderACLRepo) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	return affected(r.pool.Exec(ctx, `DELETE FROM mail.sender_acl WHERE tenant_id = $1 AND id = $2`, tenantID, id))
}

func (r *SenderACLRepo) DeleteByLoggedInAs(ctx context.Context, tenantID uuid.UUID, username string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM mail.sender_acl WHERE tenant_id = $1 AND logged_in_as = $2`, tenantID, username)
	return err
}

// ── Relayhosts ────────────────────────────────────────────────────────────────
// La columna password nunca se selecciona: solo se proyecta si esta vacia o no.

type RelayhostRepo struct {
	pool *db.ContextPool
}

func NewRelayhostRepo(pool *db.ContextPool) *RelayhostRepo { return &RelayhostRepo{pool: pool} }

const relayhostColumns = `id, tenant_id, hostname, username, password <> '', active, created_at, updated_at`

func scanRelayhost(row pgx.Row) (domain.Relayhost, error) {
	var h domain.Relayhost
	err := row.Scan(&h.ID, &h.TenantID, &h.Hostname, &h.Username, &h.HasPassword, &h.Active, &h.CreatedAt, &h.UpdatedAt)
	return h, mapErr(err)
}

func (r *RelayhostRepo) List(ctx context.Context, tenantID uuid.UUID, page ports.Page) ([]domain.Relayhost, int64, error) {
	return listPage(ctx, r.pool,
		`SELECT COUNT(*) FROM mail.relayhosts WHERE tenant_id = $1`,
		`SELECT `+relayhostColumns+` FROM mail.relayhosts WHERE tenant_id = $1 ORDER BY hostname LIMIT $2 OFFSET $3`,
		scanRelayhost, page.Limit, page.Offset, tenantID)
}

func (r *RelayhostRepo) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Relayhost, error) {
	h, err := scanRelayhost(r.pool.QueryRow(ctx,
		`SELECT `+relayhostColumns+` FROM mail.relayhosts WHERE tenant_id = $1 AND id = $2`, tenantID, id))
	if err != nil {
		return nil, err
	}
	return &h, nil
}

func (r *RelayhostRepo) Create(ctx context.Context, h *domain.Relayhost, password string) error {
	return mapErr(r.pool.QueryRow(ctx,
		`INSERT INTO mail.relayhosts (id, tenant_id, hostname, username, password, active)
 VALUES ($1, $2, $3, $4, $5, $6) RETURNING created_at, updated_at`,
		h.ID, h.TenantID, h.Hostname, h.Username, password, h.Active,
	).Scan(&h.CreatedAt, &h.UpdatedAt))
}

// Update conserva la contrasena cuando no viene (COALESCE con NULL).
func (r *RelayhostRepo) Update(ctx context.Context, h *domain.Relayhost, password *string) error {
	return mapErr(r.pool.QueryRow(ctx,
		`UPDATE mail.relayhosts SET hostname = $3, username = $4, password = COALESCE($5, password), active = $6
 WHERE tenant_id = $1 AND id = $2 RETURNING updated_at`,
		h.TenantID, h.ID, h.Hostname, h.Username, password, h.Active,
	).Scan(&h.UpdatedAt))
}

func (r *RelayhostRepo) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	return affected(r.pool.Exec(ctx, `DELETE FROM mail.relayhosts WHERE tenant_id = $1 AND id = $2`, tenantID, id))
}

// ── Transportes ───────────────────────────────────────────────────────────────
// Las filas con tenant_id NULL son de plataforma: todas las empresas las listan; solo el
// operador de la plataforma (scope.Platform) las modifica o borra.

type TransportRepo struct {
	pool *db.ContextPool
}

func NewTransportRepo(pool *db.ContextPool) *TransportRepo { return &TransportRepo{pool: pool} }

const transportColumns = `id, tenant_id, destination, nexthop, username, password <> '', is_mx_based, active, created_at, updated_at`

func scanTransport(row pgx.Row) (domain.Transport, error) {
	var t domain.Transport
	err := row.Scan(&t.ID, &t.TenantID, &t.Destination, &t.Nexthop, &t.Username, &t.HasPassword, &t.IsMXBased, &t.Active,
		&t.CreatedAt, &t.UpdatedAt)
	return t, mapErr(err)
}

func (r *TransportRepo) List(ctx context.Context, tenantID uuid.UUID, page ports.Page) ([]domain.Transport, int64, error) {
	return listPage(ctx, r.pool,
		`SELECT COUNT(*) FROM mail.transports WHERE tenant_id = $1 OR tenant_id IS NULL`,
		`SELECT `+transportColumns+` FROM mail.transports WHERE tenant_id = $1 OR tenant_id IS NULL
  ORDER BY tenant_id IS NULL, destination LIMIT $2 OFFSET $3`,
		scanTransport, page.Limit, page.Offset, tenantID)
}

func (r *TransportRepo) Get(ctx context.Context, scope ports.TransportScope, id uuid.UUID) (*domain.Transport, error) {
	t, err := scanTransport(r.pool.QueryRow(ctx,
		`SELECT `+transportColumns+` FROM mail.transports WHERE id = $1 AND (tenant_id = $2 OR tenant_id IS NULL)`,
		id, scope.TenantID))
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *TransportRepo) Create(ctx context.Context, t *domain.Transport, password string) error {
	return mapErr(r.pool.QueryRow(ctx,
		`INSERT INTO mail.transports (id, tenant_id, destination, nexthop, username, password, is_mx_based, active)
 VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING created_at, updated_at`,
		t.ID, t.TenantID, t.Destination, t.Nexthop, t.Username, password, t.IsMXBased, t.Active,
	).Scan(&t.CreatedAt, &t.UpdatedAt))
}

func (r *TransportRepo) Update(ctx context.Context, scope ports.TransportScope, t *domain.Transport, password *string) error {
	return mapErr(r.pool.QueryRow(ctx,
		`UPDATE mail.transports SET destination = $4, nexthop = $5, username = $6, password = COALESCE($7, password),
 is_mx_based = $8, active = $9
 WHERE id = $1 AND (tenant_id = $2 OR ($3 AND tenant_id IS NULL)) RETURNING updated_at`,
		t.ID, scope.TenantID, scope.Platform, t.Destination, t.Nexthop, t.Username, password, t.IsMXBased, t.Active,
	).Scan(&t.UpdatedAt))
}

func (r *TransportRepo) Delete(ctx context.Context, scope ports.TransportScope, id uuid.UUID) error {
	return affected(r.pool.Exec(ctx,
		`DELETE FROM mail.transports WHERE id = $1 AND (tenant_id = $2 OR ($3 AND tenant_id IS NULL))`,
		id, scope.TenantID, scope.Platform))
}

// ── Politicas TLS ─────────────────────────────────────────────────────────────

type TLSPolicyRepo struct {
	pool *db.ContextPool
}

func NewTLSPolicyRepo(pool *db.ContextPool) *TLSPolicyRepo { return &TLSPolicyRepo{pool: pool} }

const tlsPolicyColumns = `id, tenant_id, dest, policy, parameters, active, created_at, updated_at`

func scanTLSPolicy(row pgx.Row) (domain.TLSPolicy, error) {
	var p domain.TLSPolicy
	err := row.Scan(&p.ID, &p.TenantID, &p.Dest, &p.Policy, &p.Parameters, &p.Active, &p.CreatedAt, &p.UpdatedAt)
	return p, mapErr(err)
}

func (r *TLSPolicyRepo) List(ctx context.Context, tenantID uuid.UUID, page ports.Page) ([]domain.TLSPolicy, int64, error) {
	return listPage(ctx, r.pool,
		`SELECT COUNT(*) FROM mail.tls_policy_overrides WHERE tenant_id = $1`,
		`SELECT `+tlsPolicyColumns+` FROM mail.tls_policy_overrides WHERE tenant_id = $1 ORDER BY dest LIMIT $2 OFFSET $3`,
		scanTLSPolicy, page.Limit, page.Offset, tenantID)
}

func (r *TLSPolicyRepo) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.TLSPolicy, error) {
	p, err := scanTLSPolicy(r.pool.QueryRow(ctx,
		`SELECT `+tlsPolicyColumns+` FROM mail.tls_policy_overrides WHERE tenant_id = $1 AND id = $2`, tenantID, id))
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *TLSPolicyRepo) Create(ctx context.Context, p *domain.TLSPolicy) error {
	return mapErr(r.pool.QueryRow(ctx,
		`INSERT INTO mail.tls_policy_overrides (id, tenant_id, dest, policy, parameters, active)
 VALUES ($1, $2, $3, $4, $5, $6) RETURNING created_at, updated_at`,
		p.ID, p.TenantID, p.Dest, p.Policy, p.Parameters, p.Active,
	).Scan(&p.CreatedAt, &p.UpdatedAt))
}

func (r *TLSPolicyRepo) Update(ctx context.Context, p *domain.TLSPolicy) error {
	return mapErr(r.pool.QueryRow(ctx,
		`UPDATE mail.tls_policy_overrides SET policy = $3, parameters = $4, active = $5
 WHERE tenant_id = $1 AND id = $2 RETURNING updated_at`,
		p.TenantID, p.ID, p.Policy, p.Parameters, p.Active,
	).Scan(&p.UpdatedAt))
}

func (r *TLSPolicyRepo) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	return affected(r.pool.Exec(ctx, `DELETE FROM mail.tls_policy_overrides WHERE tenant_id = $1 AND id = $2`, tenantID, id))
}

// ── Mapas de destinatario ─────────────────────────────────────────────────────

type RecipientMapRepo struct {
	pool *db.ContextPool
}

func NewRecipientMapRepo(pool *db.ContextPool) *RecipientMapRepo { return &RecipientMapRepo{pool: pool} }

const recipientMapColumns = `id, tenant_id, old_dest, new_dest, active, created_at, updated_at`

func scanRecipientMap(row pgx.Row) (domain.RecipientMap, error) {
	var m domain.RecipientMap
	err := row.Scan(&m.ID, &m.TenantID, &m.OldDest, &m.NewDest, &m.Active, &m.CreatedAt, &m.UpdatedAt)
	return m, mapErr(err)
}

func (r *RecipientMapRepo) List(ctx context.Context, tenantID uuid.UUID, page ports.Page) ([]domain.RecipientMap, int64, error) {
	return listPage(ctx, r.pool,
		`SELECT COUNT(*) FROM mail.recipient_maps WHERE tenant_id = $1`,
		`SELECT `+recipientMapColumns+` FROM mail.recipient_maps WHERE tenant_id = $1 ORDER BY old_dest LIMIT $2 OFFSET $3`,
		scanRecipientMap, page.Limit, page.Offset, tenantID)
}

func (r *RecipientMapRepo) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.RecipientMap, error) {
	m, err := scanRecipientMap(r.pool.QueryRow(ctx,
		`SELECT `+recipientMapColumns+` FROM mail.recipient_maps WHERE tenant_id = $1 AND id = $2`, tenantID, id))
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *RecipientMapRepo) Create(ctx context.Context, m *domain.RecipientMap) error {
	return mapErr(r.pool.QueryRow(ctx,
		`INSERT INTO mail.recipient_maps (id, tenant_id, old_dest, new_dest, active)
 VALUES ($1, $2, $3, $4, $5) RETURNING created_at, updated_at`,
		m.ID, m.TenantID, m.OldDest, m.NewDest, m.Active,
	).Scan(&m.CreatedAt, &m.UpdatedAt))
}

func (r *RecipientMapRepo) Update(ctx context.Context, m *domain.RecipientMap) error {
	return mapErr(r.pool.QueryRow(ctx,
		`UPDATE mail.recipient_maps SET new_dest = $3, active = $4 WHERE tenant_id = $1 AND id = $2 RETURNING updated_at`,
		m.TenantID, m.ID, m.NewDest, m.Active,
	).Scan(&m.UpdatedAt))
}

func (r *RecipientMapRepo) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	return affected(r.pool.Exec(ctx, `DELETE FROM mail.recipient_maps WHERE tenant_id = $1 AND id = $2`, tenantID, id))
}

// ── Mapas BCC ─────────────────────────────────────────────────────────────────

type BCCMapRepo struct {
	pool *db.ContextPool
}

func NewBCCMapRepo(pool *db.ContextPool) *BCCMapRepo { return &BCCMapRepo{pool: pool} }

const bccMapColumns = `id, tenant_id, local_dest, bcc_dest, domain, type, active, created_at, updated_at`

func scanBCCMap(row pgx.Row) (domain.BCCMap, error) {
	var m domain.BCCMap
	err := row.Scan(&m.ID, &m.TenantID, &m.LocalDest, &m.BCCDest, &m.Domain, &m.Type, &m.Active, &m.CreatedAt, &m.UpdatedAt)
	return m, mapErr(err)
}

func (r *BCCMapRepo) List(ctx context.Context, tenantID uuid.UUID, page ports.Page) ([]domain.BCCMap, int64, error) {
	return listPage(ctx, r.pool,
		`SELECT COUNT(*) FROM mail.bcc_maps WHERE tenant_id = $1`,
		`SELECT `+bccMapColumns+` FROM mail.bcc_maps WHERE tenant_id = $1 ORDER BY local_dest, type LIMIT $2 OFFSET $3`,
		scanBCCMap, page.Limit, page.Offset, tenantID)
}

func (r *BCCMapRepo) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.BCCMap, error) {
	m, err := scanBCCMap(r.pool.QueryRow(ctx,
		`SELECT `+bccMapColumns+` FROM mail.bcc_maps WHERE tenant_id = $1 AND id = $2`, tenantID, id))
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *BCCMapRepo) Create(ctx context.Context, m *domain.BCCMap) error {
	return mapErr(r.pool.QueryRow(ctx,
		`INSERT INTO mail.bcc_maps (id, tenant_id, local_dest, bcc_dest, domain, type, active)
 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING created_at, updated_at`,
		m.ID, m.TenantID, m.LocalDest, m.BCCDest, m.Domain, m.Type, m.Active,
	).Scan(&m.CreatedAt, &m.UpdatedAt))
}

func (r *BCCMapRepo) Update(ctx context.Context, m *domain.BCCMap) error {
	return mapErr(r.pool.QueryRow(ctx,
		`UPDATE mail.bcc_maps SET bcc_dest = $3, type = $4, active = $5 WHERE tenant_id = $1 AND id = $2 RETURNING updated_at`,
		m.TenantID, m.ID, m.BCCDest, m.Type, m.Active,
	).Scan(&m.UpdatedAt))
}

func (r *BCCMapRepo) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	return affected(r.pool.Exec(ctx, `DELETE FROM mail.bcc_maps WHERE tenant_id = $1 AND id = $2`, tenantID, id))
}

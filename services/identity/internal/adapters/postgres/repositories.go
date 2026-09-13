package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type UserRepo struct {
	pool *pgxpool.Pool
}

func NewUserRepo(pool *pgxpool.Pool) *UserRepo {
	return &UserRepo{pool: pool}
}

func (r *UserRepo) Create(ctx context.Context, u *domain.User) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO identity.users (id, tenant_id, email, password_hash, first_name, last_name, status, mfa_enabled)
 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		u.ID, u.TenantID, u.Email, u.PasswordHash, u.FirstName, u.LastName, u.Status, u.MFAEnabled,
	)
	return err
}

func (r *UserRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	u := &domain.User{}
	err := r.pool.QueryRow(ctx,
		`SELECT id, tenant_id, email, password_hash, first_name, last_name, COALESCE(avatar_url, ''), status,
mfa_enabled, COALESCE(mfa_secret, ''), password_changed_at, failed_login_attempts,
locked_until, last_login_at, created_at, updated_at
 FROM identity.users WHERE id = $1`, id,
	).Scan(
		&u.ID, &u.TenantID, &u.Email, &u.PasswordHash, &u.FirstName, &u.LastName, &u.AvatarURL, &u.Status,
		&u.MFAEnabled, &u.MFASecret, &u.PasswordChangedAt, &u.FailedLoginAttempts,
		&u.LockedUntil, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		return nil, domain.ErrUserNotFound
	}
	return u, nil
}

func (r *UserRepo) GetByEmail(ctx context.Context, tenantID uuid.UUID, email string) (*domain.User, error) {
	u := &domain.User{}
	err := r.pool.QueryRow(ctx,
		`SELECT id, tenant_id, email, password_hash, first_name, last_name, COALESCE(avatar_url, ''), status,
mfa_enabled, COALESCE(mfa_secret, ''), password_changed_at, failed_login_attempts,
locked_until, last_login_at, created_at, updated_at
 FROM identity.users WHERE tenant_id = $1 AND email = $2`, tenantID, email,
	).Scan(
		&u.ID, &u.TenantID, &u.Email, &u.PasswordHash, &u.FirstName, &u.LastName, &u.AvatarURL, &u.Status,
		&u.MFAEnabled, &u.MFASecret, &u.PasswordChangedAt, &u.FailedLoginAttempts,
		&u.LockedUntil, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		return nil, domain.ErrUserNotFound
	}
	return u, nil
}

func (r *UserRepo) Update(ctx context.Context, u *domain.User) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE identity.users SET first_name=$1, last_name=$2, status=$3,
 mfa_enabled=$4, mfa_secret=$5, avatar_url=$6 WHERE id=$7`,
		u.FirstName, u.LastName, u.Status, u.MFAEnabled, u.MFASecret, u.AvatarURL, u.ID,
	)
	return err
}

func (r *UserRepo) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM identity.users WHERE id = $1`, id)
	return err
}

// List filtra por texto sobre correo y nombre. El filtro se aplica EN LA BASE: un
// buscador que recibe el texto y lo ignora devuelve el directorio entero, y quien
// recorre el resultado para actuar sobre el actua sobre todos.
func (r *UserRepo) List(ctx context.Context, tenantID uuid.UUID, offset, limit int, search string) ([]*domain.User, int64, error) {
	patron := "%" + strings.ToLower(strings.TrimSpace(search)) + "%"
	filtro := strings.TrimSpace(search) != ""

	var total int64
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM identity.users
 WHERE tenant_id = $1
   AND (NOT $2 OR lower(email) LIKE $3 OR lower(first_name || ' ' || last_name) LIKE $3)`,
		tenantID, filtro, patron,
	).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	rows, err := r.pool.Query(ctx,
		`SELECT id, tenant_id, email, first_name, last_name, COALESCE(avatar_url, ''), status,
mfa_enabled, last_login_at, created_at, updated_at
 FROM identity.users
 WHERE tenant_id = $1
   AND (NOT $4 OR lower(email) LIKE $5 OR lower(first_name || ' ' || last_name) LIKE $5)
 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		tenantID, limit, offset, filtro, patron,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var users []*domain.User
	for rows.Next() {
		u := &domain.User{}
		if err := rows.Scan(
			&u.ID, &u.TenantID, &u.Email, &u.FirstName, &u.LastName, &u.AvatarURL, &u.Status,
			&u.MFAEnabled, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt,
		); err != nil {
			return nil, 0, err
		}
		users = append(users, u)
	}
	return users, total, rows.Err()
}

func (r *UserRepo) IncrementFailedAttempts(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE identity.users SET failed_login_attempts = failed_login_attempts + 1 WHERE id = $1`, id,
	)
	return err
}

func (r *UserRepo) ResetFailedAttempts(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE identity.users SET failed_login_attempts = 0, locked_until = NULL WHERE id = $1`, id,
	)
	return err
}

func (r *UserRepo) LockUser(ctx context.Context, id uuid.UUID, until *time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE identity.users SET status = 'locked', locked_until = $1 WHERE id = $2`, until, id,
	)
	return err
}

func (r *UserRepo) UpdateLastLogin(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE identity.users SET last_login_at = NOW() WHERE id = $1`, id,
	)
	return err
}

func (r *UserRepo) UpdatePassword(ctx context.Context, id uuid.UUID, hash string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE identity.users SET password_hash = $1, password_changed_at = NOW() WHERE id = $2`, hash, id,
	)
	return err
}

func (r *UserRepo) EnableMFA(ctx context.Context, id uuid.UUID, secret string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE identity.users SET mfa_enabled = TRUE, mfa_secret = $1, updated_at = NOW() WHERE id = $2`,
		secret, id,
	)
	return err
}

func (r *UserRepo) DisableMFA(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE identity.users SET mfa_enabled = FALSE, mfa_secret = NULL, updated_at = NOW() WHERE id = $1`,
		id,
	)
	return err
}

func (r *UserRepo) BumpTokenEpoch(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE identity.users SET tokens_valid_from = NOW() WHERE id = $1`, id)
	return err
}

// RoleRepo lee los roles vigentes de un usuario para sellarlos en el access token. Lee
// la vista que publica access-control (access_control.v_user_roles, solo roles activos)
// y nunca sus tablas: conceder o retirar roles es cosa de access-control.
type RoleRepo struct {
	pool *pgxpool.Pool
}

func NewRoleRepo(pool *pgxpool.Pool) *RoleRepo {
	return &RoleRepo{pool: pool}
}

func (r *RoleRepo) RoleNames(ctx context.Context, userID uuid.UUID) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT DISTINCT role_name FROM access_control.v_user_roles WHERE user_id = $1`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	roles := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		roles = append(roles, name)
	}
	return roles, rows.Err()
}

type SessionRepo struct {
	pool *pgxpool.Pool
}

func NewSessionRepo(pool *pgxpool.Pool) *SessionRepo {
	return &SessionRepo{pool: pool}
}

func (r *SessionRepo) Create(ctx context.Context, s *domain.Session) error {
	loginAt := s.LoginAt
	if loginAt.IsZero() {
		loginAt = s.CreatedAt
	}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO identity.sessions (id, user_id, refresh_token_hash, ip_address, user_agent, expires_at, login_at)
 VALUES ($1, $2, $3, $4::inet, $5, $6, $7)`,
		s.ID, s.UserID, s.RefreshTokenHash, s.IPAddress, s.UserAgent, s.ExpiresAt, loginAt,
	)
	return err
}

func (r *SessionRepo) GetByRefreshTokenHash(ctx context.Context, hash string) (*domain.Session, error) {
	s := &domain.Session{}
	err := r.pool.QueryRow(ctx,
		`SELECT id, user_id, refresh_token_hash, host(ip_address), user_agent, expires_at, revoked, revoked_at, COALESCE(revoked_reason, ''), created_at, COALESCE(login_at, created_at)
 FROM identity.sessions WHERE refresh_token_hash = $1`, hash,
	).Scan(&s.ID, &s.UserID, &s.RefreshTokenHash, &s.IPAddress, &s.UserAgent, &s.ExpiresAt, &s.Revoked, &s.RevokedAt, &s.RevokedReason, &s.CreatedAt, &s.LoginAt)
	if err != nil {
		return nil, domain.ErrSessionNotFound
	}
	return s, nil
}

func (r *SessionRepo) ListByUser(ctx context.Context, userID uuid.UUID) ([]*domain.Session, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, user_id, ip_address::text, user_agent, expires_at, revoked, created_at
 FROM identity.sessions WHERE user_id = $1 AND revoked = FALSE
 ORDER BY created_at DESC`, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []*domain.Session
	for rows.Next() {
		s := &domain.Session{}
		if err := rows.Scan(&s.ID, &s.UserID, &s.IPAddress, &s.UserAgent, &s.ExpiresAt, &s.Revoked, &s.CreatedAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// sessionInfoCols son las columnas del listado de dispositivos: la sesion, su dueno y
// (para la vista de plataforma) la empresa, por la vista publicada organization.v_tenants.
const sessionInfoCols = `s.id, s.user_id, TRIM(u.first_name || ' ' || u.last_name), u.email,
	u.tenant_id, COALESCE(t.name, ''), host(s.ip_address), s.user_agent,
	COALESCE(s.login_at, s.created_at), s.created_at, s.expires_at, s.revoked, s.revoked_at`

const sessionInfoFrom = ` FROM identity.sessions s
	JOIN identity.users u ON u.id = s.user_id
	LEFT JOIN organization.v_tenants t ON t.tenant_id = u.tenant_id`

func scanSessionInfo(rows pgx.Rows) (*domain.SessionInfo, error) {
	si := &domain.SessionInfo{}
	var ip, ua *string
	if err := rows.Scan(&si.ID, &si.UserID, &si.UserName, &si.UserEmail, &si.TenantID,
		&si.TenantName, &ip, &ua, &si.LoginAt, &si.LastSeenAt,
		&si.ExpiresAt, &si.Revoked, &si.RevokedAt); err != nil {
		return nil, err
	}
	if ip != nil {
		si.IPAddress = *ip
	}
	if ua != nil {
		si.UserAgent = *ua
	}
	return si, nil
}

func (r *SessionRepo) ListInfo(ctx context.Context, f domain.SessionFilter) ([]*domain.SessionInfo, int64, error) {
	where := []string{"1=1"}
	args := []interface{}{}
	n := 1
	if f.TenantID != nil {
		where = append(where, fmt.Sprintf("u.tenant_id = $%d", n))
		args = append(args, *f.TenantID)
		n++
	}
	if f.UserID != nil {
		where = append(where, fmt.Sprintf("s.user_id = $%d", n))
		args = append(args, *f.UserID)
		n++
	}
	if f.ActiveOnly {
		where = append(where, "s.revoked = FALSE", "s.expires_at > NOW()")
	}
	cond := " WHERE " + strings.Join(where, " AND ")

	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*)`+sessionInfoFrom+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `SELECT ` + sessionInfoCols + sessionInfoFrom + cond +
		fmt.Sprintf(` ORDER BY s.created_at DESC LIMIT $%d OFFSET $%d`, n, n+1)
	rows, err := r.pool.Query(ctx, query, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []*domain.SessionInfo
	for rows.Next() {
		si, err := scanSessionInfo(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, si)
	}
	return out, total, rows.Err()
}

func (r *SessionRepo) GetInfo(ctx context.Context, id uuid.UUID) (*domain.SessionInfo, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+sessionInfoCols+sessionInfoFrom+` WHERE s.id = $1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, domain.ErrSessionNotFound
	}
	return scanSessionInfo(rows)
}

func (r *SessionRepo) CountActiveByUser(ctx context.Context, userID uuid.UUID) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM identity.sessions
		  WHERE user_id = $1 AND revoked = FALSE AND expires_at > NOW()`, userID).Scan(&n)
	return n, err
}

// RevokeOldestByUser cierra las sesiones activas mas antiguas del usuario hasta dejar
// como mucho keep. Se ordena por el inicio de sesion original (login_at), no por
// created_at: con la rotacion del refresh, created_at es la ultima renovacion y una
// sesion vieja pero activa pareceria la mas reciente.
func (r *SessionRepo) RevokeOldestByUser(ctx context.Context, userID uuid.UUID, keep int) (int, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE identity.sessions SET revoked = TRUE, revoked_at = NOW(), revoked_reason = 'revoked'
		  WHERE id IN (
		      SELECT id FROM identity.sessions
		       WHERE user_id = $1 AND revoked = FALSE AND expires_at > NOW()
		       ORDER BY COALESCE(login_at, created_at) DESC
		      OFFSET $2
		  )`, userID, keep)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (r *SessionRepo) Revoke(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `UPDATE identity.sessions SET revoked = TRUE, revoked_at = NOW(), revoked_reason = 'revoked' WHERE id = $1 AND revoked = FALSE`, id)
	return err
}

func (r *SessionRepo) RevokeForRotation(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `UPDATE identity.sessions SET revoked = TRUE, revoked_at = NOW(), revoked_reason = 'rotated' WHERE id = $1 AND revoked = FALSE`, id)
	return err
}

func (r *SessionRepo) RevokeAllByUser(ctx context.Context, userID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `UPDATE identity.sessions SET revoked = TRUE, revoked_at = NOW(), revoked_reason = 'revoked' WHERE user_id = $1 AND revoked = FALSE`, userID)
	return err
}

func (r *SessionRepo) DeleteExpired(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM identity.sessions WHERE expires_at < NOW()`)
	return err
}

type TokenBlocklistRepo struct {
	pool *pgxpool.Pool
}

func NewTokenBlocklistRepo(pool *pgxpool.Pool) *TokenBlocklistRepo {
	return &TokenBlocklistRepo{pool: pool}
}

func (r *TokenBlocklistRepo) Add(ctx context.Context, tokenHash string, expiresAt time.Time) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO identity.token_blocklist (token_hash, expires_at) VALUES ($1, $2)
 ON CONFLICT (token_hash) DO NOTHING`, tokenHash, expiresAt,
	)
	return err
}

func (r *TokenBlocklistRepo) Exists(ctx context.Context, tokenHash string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM identity.token_blocklist WHERE token_hash = $1 AND expires_at > NOW())`,
		tokenHash,
	).Scan(&exists)
	return exists, err
}

func (r *TokenBlocklistRepo) DeleteExpired(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM identity.token_blocklist WHERE expires_at < NOW()`)
	return err
}

type PasswordPolicyRepo struct {
	pool *pgxpool.Pool
}

func NewPasswordPolicyRepo(pool *pgxpool.Pool) *PasswordPolicyRepo {
	return &PasswordPolicyRepo{pool: pool}
}

func (r *PasswordPolicyRepo) Get(ctx context.Context, tenantID uuid.UUID) (*domain.PasswordPolicy, error) {
	p := &domain.PasswordPolicy{}
	err := r.pool.QueryRow(ctx,
		`SELECT tenant_id, min_length, require_uppercase, require_lowercase,
require_digit, require_special, max_age_days, history_count,
max_failed_attempts, lockout_duration_minutes
 FROM identity.password_policies WHERE tenant_id = $1`, tenantID,
	).Scan(
		&p.TenantID, &p.MinLength, &p.RequireUppercase, &p.RequireLowercase,
		&p.RequireDigit, &p.RequireSpecial, &p.MaxAgeDays, &p.HistoryCount,
		&p.MaxFailedAttempts, &p.LockoutDurationMinutes,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DefaultPasswordPolicy(tenantID), nil
	}
	if err != nil {
		return nil, fmt.Errorf("get password policy: %w", err)
	}
	return p, nil
}

func (r *PasswordPolicyRepo) Upsert(ctx context.Context, p *domain.PasswordPolicy) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO identity.password_policies
 (tenant_id, min_length, require_uppercase, require_lowercase, require_digit,
  require_special, max_age_days, history_count, max_failed_attempts, lockout_duration_minutes)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
 ON CONFLICT (tenant_id) DO UPDATE SET
  min_length=$2, require_uppercase=$3, require_lowercase=$4, require_digit=$5,
  require_special=$6, max_age_days=$7, history_count=$8, max_failed_attempts=$9,
  lockout_duration_minutes=$10`,
		p.TenantID, p.MinLength, p.RequireUppercase, p.RequireLowercase,
		p.RequireDigit, p.RequireSpecial, p.MaxAgeDays, p.HistoryCount,
		p.MaxFailedAttempts, p.LockoutDurationMinutes,
	)
	return err
}

type SessionPolicyRepo struct {
	pool *pgxpool.Pool
}

func NewSessionPolicyRepo(pool *pgxpool.Pool) *SessionPolicyRepo {
	return &SessionPolicyRepo{pool: pool}
}

// Get devuelve la politica por defecto cuando la empresa no la configuro: la ausencia
// de fila no es un error, es "todavia opera con lo de siempre".
func (r *SessionPolicyRepo) Get(ctx context.Context, tenantID uuid.UUID) (*domain.SessionPolicy, error) {
	p := &domain.SessionPolicy{}
	err := r.pool.QueryRow(ctx,
		`SELECT tenant_id, refresh_ttl_hours, max_concurrent_sessions, idle_timeout_minutes,
		        updated_at, updated_by
		   FROM identity.session_policies WHERE tenant_id = $1`, tenantID,
	).Scan(&p.TenantID, &p.RefreshTTLHours, &p.MaxConcurrentSessions, &p.IdleTimeoutMinutes,
		&p.UpdatedAt, &p.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DefaultSessionPolicy(tenantID), nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session policy: %w", err)
	}
	return p, nil
}

func (r *SessionPolicyRepo) Upsert(ctx context.Context, p *domain.SessionPolicy) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO identity.session_policies
		 (tenant_id, refresh_ttl_hours, max_concurrent_sessions, idle_timeout_minutes, updated_at, updated_by)
		 VALUES ($1,$2,$3,$4,NOW(),$5)
		 ON CONFLICT (tenant_id) DO UPDATE SET
		  refresh_ttl_hours=$2, max_concurrent_sessions=$3, idle_timeout_minutes=$4,
		  updated_at=NOW(), updated_by=$5`,
		p.TenantID, p.RefreshTTLHours, p.MaxConcurrentSessions, p.IdleTimeoutMinutes, p.UpdatedBy,
	)
	return err
}

type PasswordHistoryRepo struct {
	pool *pgxpool.Pool
}

func NewPasswordHistoryRepo(pool *pgxpool.Pool) *PasswordHistoryRepo {
	return &PasswordHistoryRepo{pool: pool}
}

func (r *PasswordHistoryRepo) Add(ctx context.Context, userID uuid.UUID, hash string) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO identity.password_history (id, user_id, password_hash) VALUES ($1, $2, $3)`,
		uuid.New(), userID, hash,
	)
	return err
}

func (r *PasswordHistoryRepo) GetRecent(ctx context.Context, userID uuid.UUID, count int) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT password_hash FROM identity.password_history
 WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2`, userID, count,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hashes []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		hashes = append(hashes, h)
	}
	return hashes, rows.Err()
}

type AuditRepo struct {
	pool *pgxpool.Pool
}

func NewAuditRepo(pool *pgxpool.Pool) *AuditRepo {
	return &AuditRepo{pool: pool}
}

func (r *AuditRepo) Log(ctx context.Context, e *domain.AuditEntry) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO identity.audit_log (id, tenant_id, user_id, action, resource, resource_id, ip_address, user_agent, details)
 VALUES ($1, $2, $3, $4, $5, $6, $7::inet, $8, $9)`,
		e.ID, e.TenantID, e.UserID, e.Action, e.Resource, e.ResourceID, e.IPAddress, e.UserAgent, e.Details,
	)
	return err
}

// TenantRepo resuelve la empresa del login por la vista publicada organization.v_tenants.
type TenantRepo struct {
	pool *pgxpool.Pool
}

func NewTenantRepo(pool *pgxpool.Pool) *TenantRepo {
	return &TenantRepo{pool: pool}
}

func (r *TenantRepo) GetIDBySlug(ctx context.Context, slug string) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.pool.QueryRow(ctx,
		`SELECT tenant_id FROM organization.v_tenants WHERE slug = $1 AND status = 'active'`, slug,
	).Scan(&id)
	if err != nil {
		return uuid.Nil, domain.ErrTenantNotFound
	}
	return id, nil
}

func (r *TenantRepo) GetIDByEmail(ctx context.Context, email string) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.pool.QueryRow(ctx,
		`SELECT tenant_id FROM identity.users WHERE email = $1 AND status != 'inactive' LIMIT 1`, email,
	).Scan(&id)
	if err != nil {
		return uuid.Nil, domain.ErrTenantNotFound
	}
	return id, nil
}

type PasswordResetRepo struct {
	pool *pgxpool.Pool
}

func NewPasswordResetRepo(pool *pgxpool.Pool) *PasswordResetRepo {
	return &PasswordResetRepo{pool: pool}
}

func (r *PasswordResetRepo) Create(ctx context.Context, t *domain.PasswordResetToken) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO identity.password_reset_tokens (id, user_id, tenant_id, token_hash, expires_at, created_at)
 VALUES ($1, $2, $3, $4, $5, $6)`,
		t.ID, t.UserID, t.TenantID, t.TokenHash, t.ExpiresAt, t.CreatedAt,
	)
	return err
}

func (r *PasswordResetRepo) GetByTokenHash(ctx context.Context, tokenHash string) (*domain.PasswordResetToken, error) {
	var t domain.PasswordResetToken
	err := r.pool.QueryRow(ctx,
		`SELECT id, user_id, tenant_id, token_hash, expires_at, used_at, created_at
 FROM identity.password_reset_tokens WHERE token_hash = $1`,
		tokenHash,
	).Scan(&t.ID, &t.UserID, &t.TenantID, &t.TokenHash, &t.ExpiresAt, &t.UsedAt, &t.CreatedAt)
	if err != nil {
		return nil, domain.ErrResetTokenInvalid
	}
	return &t, nil
}

func (r *PasswordResetRepo) MarkUsed(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE identity.password_reset_tokens SET used_at = NOW() WHERE id = $1 AND used_at IS NULL`, id,
	)
	return err
}

func (r *PasswordResetRepo) InvalidateForUser(ctx context.Context, userID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE identity.password_reset_tokens SET used_at = NOW()
 WHERE user_id = $1 AND used_at IS NULL AND expires_at > NOW()`,
		userID,
	)
	return err
}

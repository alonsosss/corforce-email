package domain

import (
	"time"

	"github.com/google/uuid"
)

type UserStatus string

const (
	UserStatusActive   UserStatus = "active"
	UserStatusInactive UserStatus = "inactive"
	UserStatusLocked   UserStatus = "locked"
	UserStatusPending  UserStatus = "pending"
)

type User struct {
	ID                  uuid.UUID
	TenantID            uuid.UUID
	Email               string
	PasswordHash        string
	FirstName           string
	LastName            string
	AvatarURL           string
	Status              UserStatus
	MFAEnabled          bool
	MFASecret           string
	PasswordChangedAt   *time.Time
	FailedLoginAttempts int
	LockedUntil         *time.Time
	LastLoginAt         *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// LockActive dice si el bloqueo por intentos fallidos sigue vigente en now. El bloqueo es
// siempre temporal (LockUser fija locked_until): sin fecha, o con la fecha ya pasada, la
// cuenta no esta bloqueada aunque su fila siga en locked hasta el siguiente inicio de
// sesion. Cerrar una cuenta sin plazo es inactive, no locked.
func (u *User) LockActive(now time.Time) bool {
	return u.Status == UserStatusLocked && u.LockedUntil != nil && now.Before(*u.LockedUntil)
}

// EffectiveStatus es el estado con el que se decide si la cuenta puede tener sesion: un
// bloqueo caducado cuenta como active. Es la misma regla que publica la columna
// effective_status de identity.v_user_status (registry 027), que lee access-control.
func (u *User) EffectiveStatus(now time.Time) UserStatus {
	if u.Status == UserStatusLocked && !u.LockActive(now) {
		return UserStatusActive
	}
	return u.Status
}

// SessionAllowed es la unica regla de inicio de sesion, segundo factor y renovacion: solo
// una cuenta efectivamente activa abre o conserva sesion. Un bloqueo vigente es
// ErrAccountLocked; inactive, pending o un estado desconocido, ErrAccountInactive.
func (u *User) SessionAllowed(now time.Time) error {
	switch u.EffectiveStatus(now) {
	case UserStatusActive:
		return nil
	case UserStatusLocked:
		return ErrAccountLocked
	default:
		return ErrAccountInactive
	}
}

// UserDeletion es la baja de una cuenta suelta tal como se anuncia al resto de la
// plataforma (identity.user.deleted). ActorID es quien la borro; uuid.Nil si no hubo persona.
type UserDeletion struct {
	UserID    uuid.UUID
	TenantID  uuid.UUID
	ActorID   uuid.UUID
	DeletedAt time.Time
}

type Session struct {
	ID               uuid.UUID
	UserID           uuid.UUID
	RefreshTokenHash string
	IPAddress        string
	UserAgent        string
	ExpiresAt        time.Time
	Revoked          bool
	RevokedAt        *time.Time
	// RevokedReason distingue la rotacion normal del refresh ("rotated", con gracia
	// anti-carrera) de un cierre deliberado ("revoked": logout-all o revocacion de
	// admin, sin gracia). Vacio en sesiones vivas.
	RevokedReason string
	CreatedAt     time.Time
	// LoginAt es la fecha del login original. Las sesiones rotan en cada refresh
	// (fila nueva por renovacion), asi que CreatedAt es la ultima renovacion; LoginAt
	// se propaga por la cadena para conservar cuando inicio realmente el dispositivo.
	LoginAt time.Time
}

// SessionInfo es una sesion enriquecida para la vista de dispositivos: incluye a
// quien pertenece y, en la vista de plataforma, de que empresa es.
type SessionInfo struct {
	ID         uuid.UUID `json:"id"`
	UserID     uuid.UUID `json:"user_id"`
	UserName   string    `json:"user_name"`
	UserEmail  string    `json:"user_email"`
	TenantID   uuid.UUID `json:"tenant_id"`
	TenantName string    `json:"tenant_name,omitempty"`
	IPAddress  string    `json:"ip_address"`
	UserAgent  string    `json:"user_agent"`
	LoginAt    time.Time `json:"login_at"`
	// LastSeenAt es la ultima renovacion del refresh token (~ultima actividad).
	LastSeenAt time.Time  `json:"last_seen_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	Revoked    bool       `json:"revoked"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// SessionFilter acota el listado de sesiones. TenantID nil = todas las empresas
// (solo la vista de plataforma); UserID nil = todos los usuarios del alcance.
type SessionFilter struct {
	TenantID   *uuid.UUID
	UserID     *uuid.UUID
	ActiveOnly bool
	Offset     int
	Limit      int
}

// PasswordResetToken respalda el flujo "olvide mi contrasena": se persiste solo
// el hash del token, es de un solo uso y expira a los pocos minutos.
type PasswordResetToken struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	TenantID  uuid.UUID
	TokenHash string
	ExpiresAt time.Time
	UsedAt    *time.Time
	CreatedAt time.Time
}

// SessionPolicy son los controles de sesion que cada empresa decide. Los tres son
// opt-in: 0 en los limites significa "sin limite", y los valores por defecto
// reproducen el comportamiento historico (refresh de 7 dias, sin tope de sesiones,
// sin cierre por inactividad).
type SessionPolicy struct {
	TenantID              uuid.UUID  `json:"tenant_id"`
	RefreshTTLHours       int        `json:"refresh_ttl_hours"`
	MaxConcurrentSessions int        `json:"max_concurrent_sessions"`
	IdleTimeoutMinutes    int        `json:"idle_timeout_minutes"`
	UpdatedAt             time.Time  `json:"updated_at"`
	UpdatedBy             *uuid.UUID `json:"updated_by,omitempty"`
}

// DefaultSessionPolicy es la politica de una empresa que nunca la configuro.
//
// Defaults endurecidos para acotar el radio de una sesion robada: refresh de 48h
// (antes 7 dias, una semana entera de vida para un refresh robado) y cierre por
// inactividad a los 60 min. El tope de sesiones concurrentes se deja en 0 (sin
// limite) para no expulsar dispositivos legitimos; cada empresa puede ajustar las
// tres via PUT /sessions/policy.
func DefaultSessionPolicy(tenantID uuid.UUID) *SessionPolicy {
	return &SessionPolicy{
		TenantID:              tenantID,
		RefreshTTLHours:       48,
		MaxConcurrentSessions: 0,
		IdleTimeoutMinutes:    60,
	}
}

// RefreshTTL traduce la politica a la duracion efectiva del refresh token.
func (p *SessionPolicy) RefreshTTL() time.Duration {
	if p == nil || p.RefreshTTLHours <= 0 {
		return 168 * time.Hour
	}
	return time.Duration(p.RefreshTTLHours) * time.Hour
}

// IdleExceeded indica si una sesion cuya ultima actividad fue lastSeen ya supero el
// limite de inactividad de la empresa. Sin limite configurado nunca vence.
func (p *SessionPolicy) IdleExceeded(lastSeen time.Time, now time.Time) bool {
	if p == nil || p.IdleTimeoutMinutes <= 0 {
		return false
	}
	return now.Sub(lastSeen) > time.Duration(p.IdleTimeoutMinutes)*time.Minute
}

// Validate protege las invariantes tambien en el servicio, no solo en la base: una
// politica imposible dejaria a la empresa sin poder entrar.
func (p *SessionPolicy) Validate() error {
	if p.RefreshTTLHours < 1 || p.RefreshTTLHours > 8760 {
		return ErrInvalidSessionPolicy
	}
	if p.MaxConcurrentSessions < 0 || p.MaxConcurrentSessions > 100 {
		return ErrInvalidSessionPolicy
	}
	if p.IdleTimeoutMinutes < 0 || p.IdleTimeoutMinutes > 43200 {
		return ErrInvalidSessionPolicy
	}
	return nil
}

type PasswordPolicy struct {
	TenantID               uuid.UUID
	MinLength              int
	RequireUppercase       bool
	RequireLowercase       bool
	RequireDigit           bool
	RequireSpecial         bool
	MaxAgeDays             int
	HistoryCount           int
	MaxFailedAttempts      int
	LockoutDurationMinutes int
}

// DefaultPasswordPolicy es la politica de una empresa que nunca la configuro. Coincide con
// los DEFAULT de identity.password_policies (registry 002_identity): guardar el formulario
// sin tocarlo y no tener fila dan el mismo resultado.
//
// Hasta el 2026-09-05, sin fila no se validaba NINGUNA contrasena ni se bloqueaba la
// cuenta por intentos fallidos, y dar de alta una empresa no crea la fila: cada empresa
// nueva nacia sin las dos protecciones hasta que alguien entrara a configurarlas.
func DefaultPasswordPolicy(tenantID uuid.UUID) *PasswordPolicy {
	return &PasswordPolicy{
		TenantID:               tenantID,
		MinLength:              8,
		RequireUppercase:       true,
		RequireLowercase:       true,
		RequireDigit:           true,
		RequireSpecial:         true,
		MaxAgeDays:             90,
		HistoryCount:           5,
		MaxFailedAttempts:      5,
		LockoutDurationMinutes: 30,
	}
}

// PasswordRules es lo que una pantalla necesita para explicar la contrasena ANTES de que
// el usuario choque con el rechazo: la forma que exige la empresa y si ademas se
// contrasta con filtraciones publicas. No lleva bloqueo ni caducidad: eso no se elige
// al escribirla.
type PasswordRules struct {
	MinLength        int  `json:"min_length"`
	RequireUppercase bool `json:"require_uppercase"`
	RequireLowercase bool `json:"require_lowercase"`
	RequireDigit     bool `json:"require_digit"`
	RequireSpecial   bool `json:"require_special"`
	BreachCheck      bool `json:"breach_check"`
}

func (p *PasswordPolicy) Rules(breachCheck bool) PasswordRules {
	return PasswordRules{
		MinLength:        p.MinLength,
		RequireUppercase: p.RequireUppercase,
		RequireLowercase: p.RequireLowercase,
		RequireDigit:     p.RequireDigit,
		RequireSpecial:   p.RequireSpecial,
		BreachCheck:      breachCheck,
	}
}

type AuditEntry struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	UserID     uuid.UUID
	Action     string
	Resource   string
	ResourceID string
	IPAddress  string
	UserAgent  string
	Details    map[string]interface{}
	CreatedAt  time.Time
}

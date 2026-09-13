package ports

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/google/uuid"
)

type UserRepository interface {
	Create(ctx context.Context, user *domain.User) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error)
	GetByEmail(ctx context.Context, tenantID uuid.UUID, email string) (*domain.User, error)
	List(ctx context.Context, tenantID uuid.UUID, offset, limit int, search string) ([]*domain.User, int64, error)
	Update(ctx context.Context, user *domain.User) error
	Delete(ctx context.Context, id uuid.UUID) error
	IncrementFailedAttempts(ctx context.Context, id uuid.UUID) error
	ResetFailedAttempts(ctx context.Context, id uuid.UUID) error
	LockUser(ctx context.Context, id uuid.UUID, until *time.Time) error
	UpdateLastLogin(ctx context.Context, id uuid.UUID) error
	UpdatePassword(ctx context.Context, id uuid.UUID, hash string) error
	EnableMFA(ctx context.Context, id uuid.UUID, secret string) error
	DisableMFA(ctx context.Context, id uuid.UUID) error
	// BumpTokenEpoch adelanta tokens_valid_from a ahora: invalida al instante todos los
	// access token del usuario emitidos antes (revocacion instantanea via el gateway).
	BumpTokenEpoch(ctx context.Context, id uuid.UUID) error
}

// RoleLookup resuelve los nombres de los roles vigentes de un usuario para sellarlos en
// el access token. Que roles tiene cada usuario lo administra access-control; identity
// solo los lee al emitir tokens.
type RoleLookup interface {
	RoleNames(ctx context.Context, userID uuid.UUID) ([]string, error)
}

type SessionRepository interface {
	Create(ctx context.Context, session *domain.Session) error
	GetByRefreshTokenHash(ctx context.Context, hash string) (*domain.Session, error)
	ListByUser(ctx context.Context, userID uuid.UUID) ([]*domain.Session, error)
	// ListInfo lista sesiones enriquecidas (usuario y empresa) para la vista de
	// dispositivos; el filtro define el alcance (tenant, usuario, solo activas).
	ListInfo(ctx context.Context, filter domain.SessionFilter) ([]*domain.SessionInfo, int64, error)
	// GetInfo carga una sesion con su tenant para validar el alcance antes de revocarla.
	GetInfo(ctx context.Context, id uuid.UUID) (*domain.SessionInfo, error)
	// CountActiveByUser y RevokeOldestByUser sostienen el limite de sesiones
	// simultaneas: al superarlo se cierra la mas antigua, no la que acaba de entrar.
	CountActiveByUser(ctx context.Context, userID uuid.UUID) (int, error)
	RevokeOldestByUser(ctx context.Context, userID uuid.UUID, keep int) (int, error)
	// Revoke es el cierre deliberado (sin gracia de reuso); RevokeForRotation marca la
	// rotacion normal del refresh, cuya gracia anti-carrera sigue vigente.
	Revoke(ctx context.Context, id uuid.UUID) error
	RevokeForRotation(ctx context.Context, id uuid.UUID) error
	RevokeAllByUser(ctx context.Context, userID uuid.UUID) error
	DeleteExpired(ctx context.Context) error
}

type TokenBlocklistRepository interface {
	Add(ctx context.Context, tokenHash string, expiresAt time.Time) error
	Exists(ctx context.Context, tokenHash string) (bool, error)
	DeleteExpired(ctx context.Context) error
}

type PasswordResetRepository interface {
	Create(ctx context.Context, token *domain.PasswordResetToken) error
	GetByTokenHash(ctx context.Context, tokenHash string) (*domain.PasswordResetToken, error)
	MarkUsed(ctx context.Context, id uuid.UUID) error
	// InvalidateForUser marca como usados los tokens vigentes del usuario para que
	// solo el enlace mas reciente sea valido.
	InvalidateForUser(ctx context.Context, userID uuid.UUID) error
}

// PasswordPolicyRepository guarda la politica de contrasenas por empresa. Get nunca
// falla por falta de fila: devuelve la politica por defecto del dominio.
type PasswordPolicyRepository interface {
	Get(ctx context.Context, tenantID uuid.UUID) (*domain.PasswordPolicy, error)
	Upsert(ctx context.Context, policy *domain.PasswordPolicy) error
}

// PasswordBreachChecker dice si una contrasena aparece en filtraciones publicas. Un error
// significa "no se pudo comprobar", nunca "es segura".
type PasswordBreachChecker interface {
	IsBreached(ctx context.Context, password string) (bool, error)
	// Enabled dice si la comprobacion esta activa: la pantalla lo anuncia al usuario
	// solo cuando es verdad.
	Enabled() bool
}

// SessionPolicyRepository guarda los controles de sesion por empresa. Get nunca falla
// por ausencia: una empresa que no la configuro recibe la politica por defecto.
type SessionPolicyRepository interface {
	Get(ctx context.Context, tenantID uuid.UUID) (*domain.SessionPolicy, error)
	Upsert(ctx context.Context, policy *domain.SessionPolicy) error
}

type PasswordHistoryRepository interface {
	Add(ctx context.Context, userID uuid.UUID, hash string) error
	GetRecent(ctx context.Context, userID uuid.UUID, count int) ([]string, error)
}

type AuditRepository interface {
	Log(ctx context.Context, entry *domain.AuditEntry) error
}

// TenantRepository resuelve la empresa de un login: por slug cuando el cliente lo
// indica, o por el correo cuando no.
type TenantRepository interface {
	GetIDBySlug(ctx context.Context, slug string) (uuid.UUID, error)
	GetIDByEmail(ctx context.Context, email string) (uuid.UUID, error)
	// IsActive responde si la empresa esta activa; false si no esta en el registro.
	IsActive(ctx context.Context, tenantID uuid.UUID) (bool, error)
}

// TenantUserRepository es el ciclo de vida de las cuentas de una empresa entera, que
// organization orquesta al darla de alta y de baja.
type TenantUserRepository interface {
	// CreateFirst da de alta la cuenta solo si la empresa aun no tiene ninguna;
	// ErrFirstUserConflict si ya la tiene.
	CreateFirst(ctx context.Context, user *domain.User) error
	// DeleteByTenant borra todas las cuentas de la empresa y devuelve cuantas borro. Sus
	// sesiones, historial y enlaces de reinicio caen con ellas.
	DeleteByTenant(ctx context.Context, tenantID uuid.UUID) (int64, error)
}

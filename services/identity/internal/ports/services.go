package ports

import (
	"context"

	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/google/uuid"
)

type AuthService interface {
	Login(ctx context.Context, req LoginRequest) (*LoginResponse, error)
	RefreshToken(ctx context.Context, refreshToken string) (*LoginResponse, error)
	Logout(ctx context.Context, tenantID, userID uuid.UUID, accessToken, refreshToken string) error
	LogoutAll(ctx context.Context, userID uuid.UUID) error
}

type UserService interface {
	Create(ctx context.Context, req CreateUserRequest) (*domain.User, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error)
	List(ctx context.Context, tenantID uuid.UUID, page, pageSize int, search string) ([]*domain.User, int64, error)
	Update(ctx context.Context, id uuid.UUID, req UpdateUserRequest) (*domain.User, error)
	ChangePassword(ctx context.Context, userID uuid.UUID, req ChangePasswordRequest) error
	ResetPassword(ctx context.Context, userID uuid.UUID, newPassword string) error
	Deactivate(ctx context.Context, id uuid.UUID) error
}

type LoginRequest struct {
	TenantSlug string
	Email      string
	Password   string
	IPAddress  string
	UserAgent  string
}

type LoginResponse struct {
	AccessToken  string   `json:"access_token,omitempty"`
	RefreshToken string   `json:"refresh_token,omitempty"`
	ExpiresIn    int64    `json:"expires_in,omitempty"`
	TokenType    string   `json:"token_type,omitempty"`
	UserID       string   `json:"user_id,omitempty"`
	TenantID     string   `json:"tenant_id,omitempty"`
	Roles        []string `json:"roles,omitempty"`
	MFARequired  bool     `json:"mfa_required,omitempty"`
	MFAToken     string   `json:"mfa_token,omitempty"`
}

// CreateUserRequest es el alta de una cuenta. Nace sin roles: concederlos es una decision
// explicita del administrador a traves de access-control, nunca un efecto del alta.
type CreateUserRequest struct {
	TenantID  uuid.UUID
	Email     string
	Password  string
	FirstName string
	LastName  string
	// CreatedBy es el administrador que origina el alta; queda en la bitacora.
	CreatedBy uuid.UUID
}

// PasswordHasher calcula y compara los hashes de contrasena. Todo hash que escribe identity
// sale del mismo hasher, con el mismo coste.
type PasswordHasher interface {
	Hash(password string) (string, error)
	// Compare devuelve nil solo si password corresponde a hash.
	Compare(hash, password string) error
}

// TransactionalMailer envia los correos transaccionales del flujo de identidad
// (recuperacion de contrasena) a traves del servicio de correo transaccional.
type TransactionalMailer interface {
	Send(ctx context.Context, tenantID uuid.UUID, to, subject, htmlBody string) error
}

type UpdateUserRequest struct {
	FirstName *string
	LastName  *string
	Status    *string
	AvatarURL *string
}

type ChangePasswordRequest struct {
	CurrentPassword string
	NewPassword     string
}

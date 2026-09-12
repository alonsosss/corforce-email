package app

import (
	"context"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/alonsosss/corforce-email/services/identity/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

type UserUseCase struct {
	users    ports.UserRepository
	policies ports.PasswordPolicyRepository
	history  ports.PasswordHistoryRepository
	audit    ports.AuditRepository
	events   ports.EventPublisher
	breach   ports.PasswordBreachChecker
	logger   *zap.Logger
}

func NewUserUseCase(
	users ports.UserRepository,
	policies ports.PasswordPolicyRepository,
	history ports.PasswordHistoryRepository,
	audit ports.AuditRepository,
	events ports.EventPublisher,
	breach ports.PasswordBreachChecker,
	logger *zap.Logger,
) *UserUseCase {
	return &UserUseCase{
		users:    users,
		policies: policies,
		history:  history,
		audit:    audit,
		events:   events,
		breach:   breach,
		logger:   logger,
	}
}

// PasswordRules devuelve lo que una pantalla de la empresa debe explicar al pedir una
// contrasena nueva.
func (uc *UserUseCase) PasswordRules(ctx context.Context, tenantID uuid.UUID) (domain.PasswordRules, error) {
	policy, err := uc.policies.Get(ctx, tenantID)
	if err != nil {
		return domain.PasswordRules{}, fmt.Errorf("password policy: %w", err)
	}
	return policy.Rules(breachEnabled(uc.breach)), nil
}

// Create da de alta la cuenta. Nace sin ningun rol: los concede despues un
// administrador desde access-control, de forma explicita. Asi entre el alta y la
// concesion no existe ninguna ventana con permisos que nadie decidio.
func (uc *UserUseCase) Create(ctx context.Context, req ports.CreateUserRequest) (*domain.User, error) {
	existing, _ := uc.users.GetByEmail(ctx, req.TenantID, req.Email)
	if existing != nil {
		return nil, domain.ErrUserAlreadyExists
	}

	policy, err := uc.policies.Get(ctx, req.TenantID)
	if err != nil {
		return nil, fmt.Errorf("password policy: %w", err)
	}
	if err := checkNewPassword(ctx, req.Password, policy, uc.breach, uc.logger); err != nil {
		return nil, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	now := time.Now()
	user := &domain.User{
		ID:           uuid.New(),
		TenantID:     req.TenantID,
		Email:        req.Email,
		PasswordHash: string(hash),
		FirstName:    req.FirstName,
		LastName:     req.LastName,
		Status:       domain.UserStatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := uc.users.Create(ctx, user); err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}

	uc.events.PublishUserCreated(req.TenantID.String(), user.ID.String(), user.Email)
	uc.audit.Log(ctx, &domain.AuditEntry{
		ID:         uuid.New(),
		TenantID:   req.TenantID,
		UserID:     req.CreatedBy,
		Action:     "create_user",
		Resource:   "user",
		ResourceID: user.ID.String(),
		Details:    map[string]interface{}{"email": user.Email},
		CreatedAt:  now,
	})

	return user, nil
}

func (uc *UserUseCase) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	user, err := uc.users.GetByID(ctx, id)
	if err != nil {
		return nil, domain.ErrUserNotFound
	}
	return user, nil
}

func (uc *UserUseCase) List(ctx context.Context, tenantID uuid.UUID, page, pageSize int, search string) ([]*domain.User, int64, error) {
	offset := (page - 1) * pageSize
	return uc.users.List(ctx, tenantID, offset, pageSize, search)
}

func (uc *UserUseCase) Update(ctx context.Context, id uuid.UUID, req ports.UpdateUserRequest) (*domain.User, error) {
	user, err := uc.users.GetByID(ctx, id)
	if err != nil {
		return nil, domain.ErrUserNotFound
	}

	if req.FirstName != nil {
		user.FirstName = *req.FirstName
	}
	if req.LastName != nil {
		user.LastName = *req.LastName
	}
	if req.Status != nil {
		user.Status = domain.UserStatus(*req.Status)
	}
	if req.AvatarURL != nil {
		user.AvatarURL = *req.AvatarURL
	}
	user.UpdatedAt = time.Now()

	if err := uc.users.Update(ctx, user); err != nil {
		return nil, fmt.Errorf("update user: %w", err)
	}

	return user, nil
}

func (uc *UserUseCase) Deactivate(ctx context.Context, id uuid.UUID) error {
	user, err := uc.users.GetByID(ctx, id)
	if err != nil {
		return domain.ErrUserNotFound
	}
	user.Status = domain.UserStatusInactive
	user.UpdatedAt = time.Now()
	return uc.users.Update(ctx, user)
}

func (uc *UserUseCase) Delete(ctx context.Context, id uuid.UUID) error {
	if _, err := uc.users.GetByID(ctx, id); err != nil {
		return domain.ErrUserNotFound
	}
	return uc.users.Delete(ctx, id)
}

func (uc *UserUseCase) ChangePassword(ctx context.Context, userID uuid.UUID, req ports.ChangePasswordRequest) error {
	user, err := uc.users.GetByID(ctx, userID)
	if err != nil {
		return domain.ErrUserNotFound
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.CurrentPassword)); err != nil {
		return domain.ErrInvalidCredentials
	}

	policy, err := uc.policies.Get(ctx, user.TenantID)
	if err != nil {
		return fmt.Errorf("password policy: %w", err)
	}
	if err := checkNewPassword(ctx, req.NewPassword, policy, uc.breach, uc.logger); err != nil {
		return err
	}
	if err := uc.checkNotReused(ctx, user.ID, req.NewPassword, policy.HistoryCount); err != nil {
		return err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	if err := uc.users.UpdatePassword(ctx, user.ID, string(hash)); err != nil {
		return fmt.Errorf("update password: %w", err)
	}

	uc.history.Add(ctx, user.ID, string(hash))
	uc.events.PublishPasswordChanged(user.TenantID.String(), user.ID.String())

	return nil
}

// checkNotReused aplica el historial de contrasenas de la politica: las ultimas N no
// se pueden repetir. Con historial 0 no se comprueba nada. Si el historial no se puede
// leer se admite la contrasena y queda constancia: es la misma decision que con las
// filtraciones, no bloquear un cambio de contrasena por un fallo ajeno al usuario.
func (uc *UserUseCase) checkNotReused(ctx context.Context, userID uuid.UUID, password string, historyCount int) error {
	if historyCount <= 0 {
		return nil
	}
	recent, err := uc.history.GetRecent(ctx, userID, historyCount)
	if err != nil {
		uc.logger.Warn("historial de contrasenas no disponible; no se comprueba la reutilizacion",
			zap.String("user_id", userID.String()), zap.Error(err))
		return nil
	}
	for _, h := range recent {
		if bcrypt.CompareHashAndPassword([]byte(h), []byte(password)) == nil {
			return domain.ErrPasswordReused
		}
	}
	return nil
}

// ResetPassword fija una contrasena nueva para un usuario sin requerir la actual.
// Es la operacion administrativa para usuarios que olvidaron su clave; el llamador
// debe tener permisos de administracion (validado en el handler). Ademas desbloquea
// la cuenta reiniciando los intentos fallidos para que el usuario pueda ingresar.
func (uc *UserUseCase) ResetPassword(ctx context.Context, userID uuid.UUID, newPassword string) error {
	user, err := uc.users.GetByID(ctx, userID)
	if err != nil {
		return domain.ErrUserNotFound
	}

	policy, err := uc.policies.Get(ctx, user.TenantID)
	if err != nil {
		return fmt.Errorf("password policy: %w", err)
	}
	if err := checkNewPassword(ctx, newPassword, policy, uc.breach, uc.logger); err != nil {
		return err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	if err := uc.users.UpdatePassword(ctx, user.ID, string(hash)); err != nil {
		return fmt.Errorf("update password: %w", err)
	}

	if err := uc.users.ResetFailedAttempts(ctx, user.ID); err != nil {
		uc.logger.Warn("no se pudieron reiniciar los intentos fallidos tras el reset",
			zap.String("user_id", user.ID.String()), zap.Error(err))
	}

	uc.history.Add(ctx, user.ID, string(hash))
	uc.events.PublishPasswordChanged(user.TenantID.String(), user.ID.String())
	uc.audit.Log(ctx, &domain.AuditEntry{
		ID:         uuid.New(),
		TenantID:   user.TenantID,
		UserID:     user.ID,
		Action:     "reset_password",
		Resource:   "user",
		ResourceID: user.ID.String(),
		CreatedAt:  time.Now(),
	})

	return nil
}

var _ ports.UserService = (*UserUseCase)(nil)

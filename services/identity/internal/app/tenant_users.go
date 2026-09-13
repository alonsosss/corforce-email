package app

import (
	"context"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/alonsosss/corforce-email/services/identity/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// TenantUsersDeps agrupa los puertos de TenantUsersUseCase.
type TenantUsersDeps struct {
	Users       ports.UserRepository
	TenantUsers ports.TenantUserRepository
	Tenants     ports.TenantRepository
	Policies    ports.PasswordPolicyRepository
	Breach      ports.PasswordBreachChecker
	Hasher      ports.PasswordHasher
	Audit       ports.AuditRepository
	Events      ports.EventPublisher
	Logger      *zap.Logger
}

// TenantUsersUseCase son las operaciones sobre las cuentas de una empresa entera que
// organization orquesta al darla de alta y de baja. Solo existen en la API interna.
type TenantUsersUseCase struct {
	users       ports.UserRepository
	tenantUsers ports.TenantUserRepository
	tenants     ports.TenantRepository
	policies    ports.PasswordPolicyRepository
	breach      ports.PasswordBreachChecker
	hasher      ports.PasswordHasher
	audit       ports.AuditRepository
	events      ports.EventPublisher
	logger      *zap.Logger
}

func NewTenantUsersUseCase(d TenantUsersDeps) *TenantUsersUseCase {
	logger := d.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &TenantUsersUseCase{
		users:       d.Users,
		tenantUsers: d.TenantUsers,
		tenants:     d.Tenants,
		policies:    d.Policies,
		breach:      d.Breach,
		hasher:      d.Hasher,
		audit:       d.Audit,
		events:      d.Events,
		logger:      logger,
	}
}

// FirstUserRequest es el primer administrador de una empresa. El id lo elige quien orquesta
// el alta: repetir la peticion con el mismo id es reintentarla, no pedir otra cuenta.
type FirstUserRequest struct {
	TenantID  uuid.UUID
	UserID    uuid.UUID
	Email     string
	Password  string
	FirstName string
	LastName  string
}

// CreateFirstUser da de alta la primera cuenta de una empresa por la misma puerta que
// cualquier contrasena nueva (politica de la empresa y filtraciones) y con el mismo hash.
// Nace sin roles, como toda cuenta: el rol lo asigna access-control en el paso siguiente
// del alta. Repetirla con el mismo id, correo y contrasena devuelve la cuenta ya creada
// (created = false); con otros datos, o si la empresa ya tiene otra cuenta,
// ErrFirstUserConflict.
func (uc *TenantUsersUseCase) CreateFirstUser(ctx context.Context, req FirstUserRequest) (*domain.User, bool, error) {
	if existing, err := uc.users.GetByID(ctx, req.UserID); err == nil {
		if existing.TenantID != req.TenantID || existing.Email != req.Email ||
			uc.hasher.Compare(existing.PasswordHash, req.Password) != nil {
			return nil, false, domain.ErrFirstUserConflict
		}
		return existing, false, nil
	}

	policy, err := uc.policies.Get(ctx, req.TenantID)
	if err != nil {
		return nil, false, fmt.Errorf("password policy: %w", err)
	}
	if err := checkNewPassword(ctx, req.Password, policy, uc.breach, uc.logger); err != nil {
		return nil, false, err
	}
	hash, err := uc.hasher.Hash(req.Password)
	if err != nil {
		return nil, false, fmt.Errorf("hash password: %w", err)
	}

	now := time.Now()
	user := &domain.User{
		ID:           req.UserID,
		TenantID:     req.TenantID,
		Email:        req.Email,
		PasswordHash: hash,
		FirstName:    req.FirstName,
		LastName:     req.LastName,
		Status:       domain.UserStatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := uc.tenantUsers.CreateFirst(ctx, user); err != nil {
		return nil, false, err
	}

	if err := uc.events.PublishUserCreated(req.TenantID.String(), user.ID.String(), user.Email); err != nil {
		uc.logger.Warn("no se pudo publicar el alta del primer usuario",
			zap.String("user_id", user.ID.String()), zap.Error(err))
	}
	uc.record(ctx, &domain.AuditEntry{
		ID:         uuid.New(),
		TenantID:   req.TenantID,
		Action:     "create_first_user",
		Resource:   "user",
		ResourceID: user.ID.String(),
		Details:    map[string]interface{}{"email": user.Email},
		CreatedAt:  now,
	})
	return user, true, nil
}

// RemoveTenantUsers borra las cuentas de una empresa que no esta activa. Sus sesiones caen
// con ellas: ningun refresh de esas cuentas vuelve a servir. Una empresa activa se rechaza:
// es la defensa ante una llamada interna equivocada, que dejaria a una empresa en servicio
// sin nadie que la administre.
func (uc *TenantUsersUseCase) RemoveTenantUsers(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	active, err := uc.tenants.IsActive(ctx, tenantID)
	if err != nil {
		return 0, fmt.Errorf("estado de la empresa: %w", err)
	}
	if active {
		return 0, domain.ErrTenantActive
	}
	removed, err := uc.tenantUsers.DeleteByTenant(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	if removed > 0 {
		uc.record(ctx, &domain.AuditEntry{
			ID:         uuid.New(),
			TenantID:   tenantID,
			Action:     "remove_tenant_users",
			Resource:   "tenant",
			ResourceID: tenantID.String(),
			Details:    map[string]interface{}{"users": removed},
			CreatedAt:  time.Now(),
		})
	}
	return removed, nil
}

// record deja el apunte en la bitacora propia de identity. Un fallo no deshace la
// operacion, pero queda constancia.
func (uc *TenantUsersUseCase) record(ctx context.Context, e *domain.AuditEntry) {
	if err := uc.audit.Log(ctx, e); err != nil {
		uc.logger.Warn("apunte de la bitacora de identity sin registrar",
			zap.String("action", e.Action), zap.Error(err))
	}
}

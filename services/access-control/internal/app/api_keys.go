package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/access-control/internal/domain"
	"github.com/alonsosss/corforce-email/services/access-control/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// usageTouchInterval acota las escrituras del ultimo uso: una clave que envia cada segundo no
// escribe cada segundo. Las resoluciones ya llegan espaciadas por la cache de quien pregunta.
const usageTouchInterval = time.Minute

// APIKeyValidationError es un dato de la peticion que no se admite; el mensaje se puede mostrar.
type APIKeyValidationError struct{ msg string }

func (e *APIKeyValidationError) Error() string { return e.msg }

func invalidAPIKey(format string, args ...any) error {
	return &APIKeyValidationError{msg: fmt.Sprintf(format, args...)}
}

// APIKeysDeps son las piezas del caso de uso de las claves de API.
type APIKeysDeps struct {
	Keys        ports.APIKeyRepository
	Hasher      ports.APIKeyHasher
	Users       ports.UserRoleRepository
	Modules     ports.TenantModuleGate
	Tenants     ports.TenantDirectory
	Revocations ports.APIKeyRevocations
	Metrics     ports.APIKeyMetrics
	SystemRoles SystemRoles
	Random      io.Reader
	Now         func() time.Time
	Logger      *zap.Logger
}

// APIKeysUseCase crea, lista, revoca y resuelve las claves de API de las empresas.
type APIKeysUseCase struct {
	d APIKeysDeps
}

func NewAPIKeysUseCase(d APIKeysDeps) *APIKeysUseCase {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Logger == nil {
		d.Logger = zap.NewNop()
	}
	return &APIKeysUseCase{d: d}
}

// ScopeRef nombra un permiso del alcance pedido.
type ScopeRef struct {
	Module   string
	Resource string
	Action   string
}

// CreateAPIKeyCommand es el alta de una clave por una persona de la empresa.
type CreateAPIKeyCommand struct {
	TenantID uuid.UUID
	Actor    Actor
	Name     string
	// Kind es la familia (domain.APIKeyKind*); vacia, la de envio. La de aprovisionamiento no la
	// crea una persona por la web: la emite quien opera la plataforma (docs/adr/0017).
	Kind      string
	Scopes    []ScopeRef
	ExpiresAt *time.Time
}

// CreatedAPIKey lleva el token completo: es la unica vez que existe fuera de quien lo recibe.
type CreatedAPIKey struct {
	Key   *domain.APIKey
	Token string
}

// Create da de alta la clave con permisos del catalogo que se pueden dar a una clave y que quien
// la crea tiene. El secreto solo se guarda como hash.
func (uc *APIKeysUseCase) Create(ctx context.Context, cmd CreateAPIKeyCommand) (*CreatedAPIKey, error) {
	now := uc.d.Now().UTC()
	kind := cmd.Kind
	if kind == "" {
		kind = domain.APIKeyKindSending
	}
	if domain.APIKeyTokenPrefixFor(kind) == "" {
		return nil, invalidAPIKey("familia de clave desconocida: %q", kind)
	}
	name, err := domain.NormalizeAPIKeyName(cmd.Name)
	if err != nil {
		return nil, invalidAPIKey("%s", err.Error())
	}
	if err := domain.ValidateAPIKeyExpiry(cmd.ExpiresAt, now); err != nil {
		return nil, invalidAPIKey("%s", err.Error())
	}
	scopes, err := uc.grantedScopes(ctx, cmd.Actor, cmd.TenantID, cmd.Scopes)
	if err != nil {
		return nil, err
	}
	active, err := uc.d.Keys.CountActive(ctx, cmd.TenantID, now)
	if err != nil {
		return nil, fmt.Errorf("contar las claves vigentes: %w", err)
	}
	if active >= domain.MaxAPIKeysPerTenant {
		return nil, domain.ErrAPIKeyLimit
	}

	prefix, secret, err := domain.NewAPIKeyToken(uc.d.Random)
	if err != nil {
		return nil, err
	}
	hash, keyID, err := uc.d.Hasher.Hash(domain.APIKeyHashInput(prefix, secret))
	if err != nil {
		return nil, fmt.Errorf("calcular el hash de la clave: %w", err)
	}
	var expires *time.Time
	if cmd.ExpiresAt != nil {
		e := cmd.ExpiresAt.UTC()
		expires = &e
	}
	key := &domain.APIKey{
		ID:         uuid.New(),
		TenantID:   cmd.TenantID,
		Name:       name,
		Kind:       kind,
		Prefix:     prefix,
		SecretHash: hash,
		HashKeyID:  keyID,
		CreatedBy:  cmd.Actor.UserID,
		Scopes:     scopes,
		ExpiresAt:  expires,
		CreatedAt:  now,
	}
	event := domain.APIKeyEvent{
		Type: domain.APIKeyEventCreated, TenantID: key.TenantID, ActorID: cmd.Actor.UserID, KeyID: key.ID,
		Name: key.Name, Prefix: key.Prefix, Scopes: key.Scopes, ExpiresAt: key.ExpiresAt, At: now,
	}
	if err := uc.d.Keys.Create(ctx, key, event); err != nil {
		return nil, fmt.Errorf("guardar la clave: %w", err)
	}
	return &CreatedAPIKey{Key: key, Token: domain.FormatAPIKeyToken(kind, prefix, secret)}, nil
}

// grantedScopes valida el alcance pedido: sin repetir, del catalogo que admite una clave, de
// alcance de empresa y que el actor tiene.
func (uc *APIKeysUseCase) grantedScopes(ctx context.Context, actor Actor, tenantID uuid.UUID, refs []ScopeRef) ([]domain.Permission, error) {
	if len(refs) == 0 {
		return nil, invalidAPIKey("la clave necesita al menos un permiso")
	}
	grantable, err := uc.d.Keys.GrantablePermissions(ctx)
	if err != nil {
		return nil, fmt.Errorf("leer los permisos que admite una clave: %w", err)
	}
	byRef := make(map[ScopeRef]domain.Permission, len(grantable))
	for _, p := range grantable {
		if p.AssignableToTenantRole() {
			byRef[ScopeRef{p.Module, p.Resource, p.Action}] = *p
		}
	}
	seen := make(map[ScopeRef]bool, len(refs))
	out := make([]domain.Permission, 0, len(refs))
	for _, ref := range refs {
		ref = ScopeRef{strings.TrimSpace(ref.Module), strings.TrimSpace(ref.Resource), strings.TrimSpace(ref.Action)}
		p, ok := byRef[ref]
		if !ok {
			return nil, domain.ErrAPIKeyScopeNotGrantable
		}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, p)
	}
	if !actor.Privileged {
		policy, err := uc.d.Users.GetAccessPolicy(ctx, actor.UserID, tenantID)
		if err != nil {
			return nil, fmt.Errorf("leer la politica de quien crea la clave: %w", err)
		}
		for _, p := range out {
			if !policy.HasPermission(p.Module, p.Resource, p.Action) {
				return nil, domain.ErrPermissionNotHeld
			}
		}
	}
	sortPermissions(out)
	return out, nil
}

// GrantableFor devuelve los permisos que el actor puede dar a una clave: los del catalogo de
// claves que el mismo tiene.
func (uc *APIKeysUseCase) GrantableFor(ctx context.Context, actor Actor, tenantID uuid.UUID) ([]domain.Permission, error) {
	grantable, err := uc.d.Keys.GrantablePermissions(ctx)
	if err != nil {
		return nil, err
	}
	var policy *domain.AccessPolicy
	if !actor.Privileged {
		if policy, err = uc.d.Users.GetAccessPolicy(ctx, actor.UserID, tenantID); err != nil {
			return nil, err
		}
	}
	out := make([]domain.Permission, 0, len(grantable))
	for _, p := range grantable {
		if !p.AssignableToTenantRole() {
			continue
		}
		if policy != nil && !policy.HasPermission(p.Module, p.Resource, p.Action) {
			continue
		}
		out = append(out, *p)
	}
	sortPermissions(out)
	return out, nil
}

// List devuelve las claves de la empresa, vigentes y no, sin su hash.
func (uc *APIKeysUseCase) List(ctx context.Context, tenantID uuid.UUID) ([]*domain.APIKey, error) {
	return uc.d.Keys.List(ctx, tenantID)
}

// Revoke deja la clave sin valor desde ya y avisa a las caches de gateway y smtp-relay.
func (uc *APIKeysUseCase) Revoke(ctx context.Context, tenantID uuid.UUID, actor Actor, id uuid.UUID) (*domain.APIKey, error) {
	now := uc.d.Now().UTC()
	key, err := uc.d.Keys.Revoke(ctx, tenantID, id, actor.UserID, now, func(k *domain.APIKey) domain.APIKeyEvent {
		return domain.APIKeyEvent{
			Type: domain.APIKeyEventRevoked, TenantID: k.TenantID, ActorID: actor.UserID, KeyID: k.ID,
			Name: k.Name, Prefix: k.Prefix, Scopes: k.Scopes, ExpiresAt: k.ExpiresAt, At: now,
		}
	})
	if err != nil {
		return nil, err
	}
	if uc.d.Revocations != nil {
		uc.d.Revocations.Revoked(ctx, key.ID)
	}
	return key, nil
}

// Resolve autentica un token y devuelve su empresa y su alcance efectivo: el de la clave acotado
// a lo que su creador tiene hoy y a los modulos que la empresa tiene habilitados. Cualquier
// motivo de rechazo es domain.ErrAPIKeyInvalid; un fallo de la base es otro error (quien
// pregunta responde que no pudo comprobarlo, no que la clave no vale).
func (uc *APIKeysUseCase) Resolve(ctx context.Context, token, clientIP string) (*domain.ResolvedAPIKey, error) {
	res, reason, err := uc.resolve(ctx, token)
	switch {
	case err != nil:
		uc.metric("error")
		uc.d.Logger.Warn("access-control: no se pudo resolver una clave de API", zap.Error(err))
		return nil, err
	case reason != "":
		uc.metric(reason)
		return nil, domain.ErrAPIKeyInvalid
	}
	uc.metric("ok")
	if err := uc.d.Keys.TouchUsage(ctx, res.ID, uc.d.Now().UTC(), clientIP, usageTouchInterval); err != nil {
		uc.d.Logger.Warn("access-control: no se anoto el ultimo uso de la clave", zap.String("api_key_id", res.ID.String()), zap.Error(err))
	}
	return res, nil
}

func (uc *APIKeysUseCase) metric(result string) {
	if uc.d.Metrics != nil {
		uc.d.Metrics.Resolved(result)
	}
}

func (uc *APIKeysUseCase) resolve(ctx context.Context, token string) (*domain.ResolvedAPIKey, string, error) {
	kind, prefix, secret, err := domain.ParseAPIKeyToken(token)
	if err != nil {
		return nil, domain.APIKeyRejectMalformed, nil
	}
	key, err := uc.d.Keys.GetByPrefix(ctx, prefix)
	if errors.Is(err, domain.ErrAPIKeyNotFound) {
		return nil, domain.APIKeyRejectUnknown, nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("leer la clave: %w", err)
	}
	// El prefijo del token anuncia la familia: si no es la de la clave guardada, el token no es de
	// esta clave aunque acierte el secreto. Sin esto, una credencial de aprovisionamiento presentada
	// como cfm_ pasaria por clave de envio.
	if key.Kind != kind {
		return nil, domain.APIKeyRejectMalformed, nil
	}
	input := domain.APIKeyHashInput(prefix, secret)
	ok, current, err := uc.d.Hasher.Verify(input, key.SecretHash, key.HashKeyID)
	if err != nil {
		// La llave con la que se firmo ya no esta en el anillo: se retiro antes de tiempo.
		uc.d.Logger.Error("access-control: la llave del hash de la clave no esta en el anillo",
			zap.String("api_key_id", key.ID.String()), zap.String("hash_key_id", key.HashKeyID), zap.Error(err))
		return nil, domain.APIKeyRejectSecret, nil
	}
	if !ok {
		return nil, domain.APIKeyRejectSecret, nil
	}
	now := uc.d.Now()
	switch key.Status(now) {
	case domain.APIKeyRevoked:
		return nil, domain.APIKeyRejectRevoked, nil
	case domain.APIKeyExpired:
		return nil, domain.APIKeyRejectExpired, nil
	}

	active, err := uc.d.Tenants.IsActive(ctx, key.TenantID)
	if err != nil {
		return nil, "", fmt.Errorf("leer el estado de la empresa: %w", err)
	}
	if !active {
		return nil, domain.APIKeyRejectTenant, nil
	}
	scopes, reason, err := uc.effectiveScopes(ctx, key)
	if err != nil || reason != "" {
		return nil, reason, err
	}
	if !current {
		if hash, keyID, err := uc.d.Hasher.Hash(input); err == nil {
			if err := uc.d.Keys.Rehash(ctx, key.ID, hash, keyID); err != nil {
				uc.d.Logger.Warn("access-control: no se rehizo el hash con la llave activa", zap.String("api_key_id", key.ID.String()), zap.Error(err))
			}
		}
	}
	return &domain.ResolvedAPIKey{ID: key.ID, TenantID: key.TenantID, Kind: key.Kind, Prefix: key.Prefix, Scopes: scopes, ExpiresAt: key.ExpiresAt}, "", nil
}

// effectiveScopes acota el alcance guardado a lo que el creador tiene ahora. La cuenta cerrada
// o borrada invalida la clave: nadie responde ya de ella.
func (uc *APIKeysUseCase) effectiveScopes(ctx context.Context, key *domain.APIKey) ([]domain.Permission, string, error) {
	account, err := uc.d.Users.UserAccount(ctx, key.CreatedBy, key.TenantID)
	if errors.Is(err, domain.ErrUserNotFound) {
		return nil, domain.APIKeyRejectOwner, nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("leer la cuenta del creador: %w", err)
	}
	if !account.Active() {
		return nil, domain.APIKeyRejectOwner, nil
	}
	roles, err := uc.d.Users.ListRoles(ctx, key.CreatedBy, key.TenantID)
	if err != nil {
		return nil, "", fmt.Errorf("leer los roles del creador: %w", err)
	}
	privileged := false
	for _, r := range roles {
		if r.Name == uc.d.SystemRoles.Superadmin || r.Name == uc.d.SystemRoles.TenantAdmin {
			privileged = true
		}
	}
	var policy *domain.AccessPolicy
	if !privileged {
		if policy, err = uc.d.Users.GetAccessPolicy(ctx, key.CreatedBy, key.TenantID); err != nil {
			return nil, "", fmt.Errorf("leer la politica del creador: %w", err)
		}
	}
	// Mismo criterio que el menu y el gateway: el catalogo contratado no es una frontera de
	// seguridad y un fallo al leerlo no filtra.
	var availability domain.ModuleAvailability
	if uc.d.Modules != nil {
		if a, gateErr := uc.d.Modules.EffectiveModules(ctx, key.TenantID); gateErr != nil {
			uc.d.Logger.Warn("access-control: sin modulos contratados de la empresa; la clave no se filtra por ellos",
				zap.String("tenant_id", key.TenantID.String()), zap.Error(gateErr))
		} else {
			availability = a
		}
	}
	out := make([]domain.Permission, 0, len(key.Scopes))
	for _, p := range key.Scopes {
		if policy != nil && !policy.HasPermission(p.Module, p.Resource, p.Action) {
			continue
		}
		if !availability.Allows(p.Module) {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, domain.APIKeyRejectNoScope, nil
	}
	return out, "", nil
}

func sortPermissions(ps []domain.Permission) {
	sort.Slice(ps, func(i, j int) bool {
		a, b := ps[i], ps[j]
		if a.Module != b.Module {
			return a.Module < b.Module
		}
		if a.Resource != b.Resource {
			return a.Resource < b.Resource
		}
		return a.Action < b.Action
	})
}

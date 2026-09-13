package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/services/access-control/internal/domain"
	"github.com/alonsosss/corforce-email/services/access-control/internal/ports"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	policyKeyPrefix = "ac:policy:"
	policyTTL       = 5 * time.Minute
)

// CachedUserRoleRepo envuelve un UserRoleRepository y cachea en Redis el resultado de
// GetAccessPolicy. El resto de metodos delega directamente e invalida la politica del
// usuario cuando cambian sus roles.
type CachedUserRoleRepo struct {
	inner ports.UserRoleRepository
	rdb   *redis.Client
}

func NewCachedUserRoleRepo(inner ports.UserRoleRepository, rdb *redis.Client) *CachedUserRoleRepo {
	return &CachedUserRoleRepo{inner: inner, rdb: rdb}
}

func policyKey(userID, tenantID uuid.UUID) string {
	return fmt.Sprintf("%s%s:%s", policyKeyPrefix, userID, tenantID)
}

func (r *CachedUserRoleRepo) GetAccessPolicy(ctx context.Context, userID, tenantID uuid.UUID) (*domain.AccessPolicy, error) {
	key := policyKey(userID, tenantID)

	data, err := r.rdb.Get(ctx, key).Bytes()
	if err == nil {
		var policy domain.AccessPolicy
		if json.Unmarshal(data, &policy) == nil {
			return &policy, nil
		}
	}

	policy, err := r.inner.GetAccessPolicy(ctx, userID, tenantID)
	if err != nil {
		return nil, err
	}

	if encoded, err := json.Marshal(policy); err == nil {
		_ = r.rdb.Set(ctx, key, encoded, policyTTL).Err()
	}

	return policy, nil
}

func (r *CachedUserRoleRepo) Assign(ctx context.Context, userID, roleID, assignedBy uuid.UUID) error {
	err := r.inner.Assign(ctx, userID, roleID, assignedBy)
	if err == nil {
		r.invalidateUser(ctx, userID)
	}
	return err
}

func (r *CachedUserRoleRepo) Revoke(ctx context.Context, userID, roleID uuid.UUID) error {
	err := r.inner.Revoke(ctx, userID, roleID)
	if err == nil {
		r.invalidateUser(ctx, userID)
	}
	return err
}

func (r *CachedUserRoleRepo) ListRoles(ctx context.Context, userID, tenantID uuid.UUID) ([]*domain.Role, error) {
	return r.inner.ListRoles(ctx, userID, tenantID)
}

// TokensValidFrom NO se cachea: la revocacion instantanea necesita el valor fresco. Se
// delega directo a la base (el gateway ya lo cachea unos segundos por su lado).
func (r *CachedUserRoleRepo) TokensValidFrom(ctx context.Context, userID uuid.UUID) (time.Time, error) {
	return r.inner.TokensValidFrom(ctx, userID)
}

func (r *CachedUserRoleRepo) ListPermissions(ctx context.Context, userID, tenantID uuid.UUID) ([]*domain.Permission, error) {
	return r.inner.ListPermissions(ctx, userID, tenantID)
}

func (r *CachedUserRoleRepo) ListAccessibleModules(ctx context.Context, userID, tenantID uuid.UUID) ([]string, error) {
	return r.inner.ListAccessibleModules(ctx, userID, tenantID)
}

func (r *CachedUserRoleRepo) ListWriteActionsByModule(ctx context.Context, userID, tenantID uuid.UUID) (map[string][]string, error) {
	return r.inner.ListWriteActionsByModule(ctx, userID, tenantID)
}

// ListUsersWithPermission no se cachea: se consulta al avisar de algo (rara vez) y una
// lista de destinatarios obsoleta significa avisar a quien ya no le corresponde.
func (r *CachedUserRoleRepo) ListUsersWithPermission(ctx context.Context, tenantID uuid.UUID, module, action string) ([]uuid.UUID, error) {
	return r.inner.ListUsersWithPermission(ctx, tenantID, module, action)
}

// InvalidateUsers descarta la politica cacheada de cada usuario, en todas sus empresas.
func (r *CachedUserRoleRepo) InvalidateUsers(ctx context.Context, userIDs []uuid.UUID) {
	for _, id := range userIDs {
		r.invalidateUser(ctx, id)
	}
}

// invalidateUser borra las politicas cacheadas del usuario en todos los tenants. Recorre
// con SCAN y no con KEYS para no bloquear a Redis con un barrido completo del espacio de
// claves; la invalidacion es best-effort porque la entrada expira sola en policyTTL.
func (r *CachedUserRoleRepo) invalidateUser(ctx context.Context, userID uuid.UUID) {
	pattern := fmt.Sprintf("%s%s:*", policyKeyPrefix, userID)
	iter := r.rdb.Scan(ctx, 0, pattern, 100).Iterator()
	keys := make([]string, 0, 4)
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	if iter.Err() != nil || len(keys) == 0 {
		return
	}
	_ = r.rdb.Del(ctx, keys...).Err()
}

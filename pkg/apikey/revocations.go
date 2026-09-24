package apikey

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// La marca de revocacion vive en el Redis de la plataforma (REDIS_*), el mismo que usan el
// gateway, access-control y smtp-relay. Dura mas que cualquier cache (MaxCacheTTL): pasado ese
// tiempo ya nadie tiene la clave cacheada y access-control la rechaza por su fila.
const (
	revokedKeyPrefix = "apikey:revoked:"
	RevocationTTL    = 2 * MaxCacheTTL
)

// RedisRevocations lee y escribe la marca.
type RedisRevocations struct {
	rdb redis.UniversalClient
}

func NewRedisRevocations(rdb redis.UniversalClient) *RedisRevocations {
	return &RedisRevocations{rdb: rdb}
}

func (r *RedisRevocations) IsRevoked(ctx context.Context, keyID string) (bool, error) {
	n, err := r.rdb.Exists(ctx, revokedKeyPrefix+keyID).Result()
	return n > 0, err
}

// MarkRevoked deja la marca. Un error solo retrasa la revocacion hasta que venza la cache.
func (r *RedisRevocations) MarkRevoked(ctx context.Context, keyID string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return r.rdb.Set(ctx, revokedKeyPrefix+keyID, "1", RevocationTTL).Err()
}

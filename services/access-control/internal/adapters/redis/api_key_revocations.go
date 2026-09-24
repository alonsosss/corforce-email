package redis

import (
	"context"

	"github.com/alonsosss/corforce-email/pkg/apikey"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// APIKeyRevocations implementa ports.APIKeyRevocations con la marca de pkg/apikey, que leen el
// gateway y smtp-relay en cada uso de una clave cacheada.
type APIKeyRevocations struct {
	marks  *apikey.RedisRevocations
	logger *zap.Logger
}

func NewAPIKeyRevocations(rdb *redis.Client, logger *zap.Logger) *APIKeyRevocations {
	return &APIKeyRevocations{marks: apikey.NewRedisRevocations(rdb), logger: logger}
}

func (r *APIKeyRevocations) Revoked(ctx context.Context, keyID uuid.UUID) {
	if err := r.marks.MarkRevoked(context.WithoutCancel(ctx), keyID.String()); err != nil {
		r.logger.Warn("access-control: sin marca de revocacion; la clave deja de valer al vencer las caches",
			zap.String("api_key_id", keyID.String()), zap.Error(err))
	}
}

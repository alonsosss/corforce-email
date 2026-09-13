package middleware

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// hitScript suma la peticion y abre la ventana en una sola operacion atomica: con INCR y
// EXPIRE por separado, un corte entre los dos dejaria una clave sin caducidad que
// bloquearia a esa identidad para siempre. El TTL solo se fija si la clave no lo tiene,
// asi que las peticiones siguientes no prolongan la ventana; y si una clave quedo sin
// caducidad por cualquier otro camino, la siguiente peticion se la pone.
//
// KEYS: 1 clave de la identidad. ARGV: 1 ventana en milisegundos.
// Devuelve {peticiones en la ventana, milisegundos hasta que se cierra}.
var hitScript = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
local ttl = redis.call('PTTL', KEYS[1])
if ttl < 0 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
  ttl = tonumber(ARGV[1])
end
return {n, ttl}
`)

// RedisRateLimitStore es el RateLimitStore sobre el Redis de la plataforma. Cada identidad
// ocupa una sola clave que caduca con su ventana: no hay nada que limpiar y la operacion es
// valida en Redis Cluster porque el script solo toca la clave que declara.
type RedisRateLimitStore struct {
	rdb redis.UniversalClient
}

func NewRedisRateLimitStore(rdb redis.UniversalClient) *RedisRateLimitStore {
	return &RedisRateLimitStore{rdb: rdb}
}

func (s *RedisRateLimitStore) Hit(ctx context.Context, key string, window time.Duration) (int64, time.Duration, error) {
	res, err := hitScript.Run(ctx, s.rdb, []string{key}, window.Milliseconds()).Int64Slice()
	if err != nil {
		return 0, 0, err
	}
	if len(res) != 2 {
		return 0, 0, fmt.Errorf("respuesta inesperada del script del limitador: %v", res)
	}
	return res[0], time.Duration(res[1]) * time.Millisecond, nil
}

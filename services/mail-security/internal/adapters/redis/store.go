// Package redis es el adaptador del bus de configuracion en caliente de los motores
// (redis-mail). Solo expone las operaciones que el servicio necesita.
package redis

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	goredis "github.com/redis/go-redis/v9"
)

type Store struct {
	client *goredis.Client
}

// Config es la conexion al Redis de los motores (MAIL_REDIS_*), distinto del Redis de la
// plataforma: los motores solo ven la red de la celda.
type Config struct {
	Host     string
	Port     int
	Password string
}

func New(cfg Config) *Store {
	return &Store{client: goredis.NewClient(&goredis.Options{
		Addr:         fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Password:     cfg.Password,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	})}
}

func (s *Store) Close() error { return s.client.Close() }

func (s *Store) Ping(ctx context.Context) error {
	if err := s.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("%w: %v", domain.ErrRedisUnavailable, err)
	}
	return nil
}

func (s *Store) HSet(ctx context.Context, key, field, value string) error {
	return s.client.HSet(ctx, key, field, value).Err()
}

func (s *Store) HDel(ctx context.Context, key string, fields ...string) error {
	if len(fields) == 0 {
		return nil
	}
	return s.client.HDel(ctx, key, fields...).Err()
}

func (s *Store) HGet(ctx context.Context, key, field string) (string, bool, error) {
	v, err := s.client.HGet(ctx, key, field).Result()
	if errors.Is(err, goredis.Nil) {
		return "", false, nil
	}
	return v, err == nil, err
}

func (s *Store) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	return s.client.HGetAll(ctx, key).Result()
}

// HKeys recorre el hash con HSCAN para no traer los valores (las claves DKIM pesan) ni
// bloquear Redis con un HGETALL grande.
func (s *Store) HKeys(ctx context.Context, key, pattern string) ([]string, error) {
	var out []string
	var cursor uint64
	for {
		fields, next, err := s.client.HScanNoValues(ctx, key, cursor, pattern, 200).Result()
		if err != nil {
			return nil, err
		}
		out = append(out, fields...)
		if next == 0 {
			return out, nil
		}
		cursor = next
	}
}

func (s *Store) Set(ctx context.Context, key, value string) error {
	return s.client.Set(ctx, key, value, 0).Err()
}

// LPushTrim apila y recorta en la misma transaccion: la lista nunca crece sin tope.
func (s *Store) LPushTrim(ctx context.Context, key, value string, maxLen int64) error {
	pipe := s.client.TxPipeline()
	pipe.LPush(ctx, key, value)
	pipe.LTrim(ctx, key, 0, maxLen-1)
	_, err := pipe.Exec(ctx)
	return err
}

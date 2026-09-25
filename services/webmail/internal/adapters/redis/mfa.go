package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	goredis "github.com/redis/go-redis/v9"
)

// MFAStore implementa ports.MFAChallengeStore con dos claves por celda (prefijo webmail:<celda>:):
//   - mfa:c:<sha256 del token>  el desafio del segundo paso (hash con la identidad en d y los
//     intentos en a), con la vida del desafio como TTL.
//   - mfa:s:<buzon>             el SHA-256 del secreto TOTP preparado, a la espera de activarse.
type MFAStore struct {
	rdb    *goredis.Client
	prefix string
}

func NewMFAStore(rdb *goredis.Client, cellCode string) *MFAStore {
	return &MFAStore{rdb: rdb, prefix: "webmail:" + cellCode + ":mfa:"}
}

// countAttempt suma un intento solo si el desafio sigue vivo: un HINCRBY sobre una clave caducada la
// recrearia sin TTL.
var countAttempt = goredis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then
  return -1
end
return redis.call('HINCRBY', KEYS[1], 'a', 1)`)

type challengeRecord struct {
	Username    string `json:"u"`
	DisplayName string `json:"n"`
	TenantID    string `json:"t,omitempty"`
	MailboxID   string `json:"m,omitempty"`
	StartedAt   int64  `json:"c"`
}

func (s *MFAStore) challengeKey(key string) string  { return s.prefix + "c:" + key }
func (s *MFAStore) setupKey(username string) string { return s.prefix + "s:" + username }

func (s *MFAStore) CreateChallenge(ctx context.Context, key string, c domain.MFAChallenge, ttl time.Duration) error {
	data, err := json.Marshal(challengeRecord{
		Username: c.Identity.Username, DisplayName: c.Identity.DisplayName, TenantID: c.Identity.TenantID,
		MailboxID: c.Identity.MailboxID, StartedAt: c.StartedAt.UnixMicro(),
	})
	if err != nil {
		return err
	}
	pipe := s.rdb.TxPipeline()
	pipe.HSet(ctx, s.challengeKey(key), "d", data, "a", 0)
	pipe.PExpire(ctx, s.challengeKey(key), ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("guardar desafio: %w", err)
	}
	return nil
}

func (s *MFAStore) GetChallenge(ctx context.Context, key string) (domain.MFAChallenge, error) {
	raw, err := s.rdb.HGet(ctx, s.challengeKey(key), "d").Bytes()
	if errors.Is(err, goredis.Nil) {
		return domain.MFAChallenge{}, domain.ErrMFAChallengeExpired
	}
	if err != nil {
		return domain.MFAChallenge{}, fmt.Errorf("leer desafio: %w", err)
	}
	var r challengeRecord
	if err := json.Unmarshal(raw, &r); err != nil || r.Username == "" {
		return domain.MFAChallenge{}, domain.ErrMFAChallengeExpired
	}
	return domain.MFAChallenge{
		Identity: domain.Identity{
			Username: r.Username, DisplayName: r.DisplayName, TenantID: r.TenantID, MailboxID: r.MailboxID, MFARequired: true,
		},
		StartedAt: time.UnixMicro(r.StartedAt).UTC(),
	}, nil
}

func (s *MFAStore) CountAttempt(ctx context.Context, key string) (int, error) {
	n, err := countAttempt.Run(ctx, s.rdb, []string{s.challengeKey(key)}).Int()
	if err != nil {
		return 0, fmt.Errorf("contar intento: %w", err)
	}
	if n < 0 {
		return 0, domain.ErrMFAChallengeExpired
	}
	return n, nil
}

func (s *MFAStore) DeleteChallenge(ctx context.Context, key string) error {
	if err := s.rdb.Del(ctx, s.challengeKey(key)).Err(); err != nil {
		return fmt.Errorf("borrar desafio: %w", err)
	}
	return nil
}

func (s *MFAStore) SaveSetup(ctx context.Context, username, secretHash string, ttl time.Duration) error {
	if err := s.rdb.Set(ctx, s.setupKey(username), secretHash, ttl).Err(); err != nil {
		return fmt.Errorf("guardar preparacion: %w", err)
	}
	return nil
}

func (s *MFAStore) SetupHash(ctx context.Context, username string) (string, error) {
	v, err := s.rdb.Get(ctx, s.setupKey(username)).Result()
	if errors.Is(err, goredis.Nil) {
		return "", domain.ErrMFASetupExpired
	}
	if err != nil {
		return "", fmt.Errorf("leer preparacion: %w", err)
	}
	return v, nil
}

func (s *MFAStore) DeleteSetup(ctx context.Context, username string) error {
	if err := s.rdb.Del(ctx, s.setupKey(username)).Err(); err != nil {
		return fmt.Errorf("borrar preparacion: %w", err)
	}
	return nil
}

// Package redis guarda las sesiones del webmail en el Redis de la plataforma.
//
// Tres claves por celda (prefijo webmail:<celda>:):
//   - s:<sha256 del token>  la sesion, con la inactividad como TTL.
//   - u:<buzon>             el conjunto de sesiones del buzon, para borrarlas todas.
//   - r:<buzon>             la marca de revocacion: toda sesion abierta antes queda
//     invalida aunque su clave siga en Redis (la borra la siguiente peticion).
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

// deleteBatch acota cada DEL de una revocacion masiva.
const deleteBatch = 500

// markRevocation fija la marca solo si es posterior a la vigente: una reentrega tardia
// de un evento viejo no debe mover la marca hacia atras ni dejar vivas sesiones nuevas.
var markRevocation = goredis.NewScript(`
local current = redis.call('GET', KEYS[1])
if (not current) or (tonumber(current) < tonumber(ARGV[1])) then
  redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
end
return 1`)

// SessionStore implementa ports.SessionStore.
type SessionStore struct {
	rdb    *goredis.Client
	prefix string
	// retention es cuanto vive el indice y la marca: la vida maxima de una sesion. Pasado
	// ese plazo ninguna sesion anterior puede seguir viva.
	retention time.Duration
}

func NewSessionStore(rdb *goredis.Client, cellCode string, maxLifetime time.Duration) *SessionStore {
	return &SessionStore{rdb: rdb, prefix: "webmail:" + cellCode + ":", retention: maxLifetime + time.Minute}
}

type record struct {
	Username    string `json:"u"`
	DisplayName string `json:"n"`
	CreatedAt   int64  `json:"c"`
	ExpiresAt   int64  `json:"e"`
}

func (s *SessionStore) sessionKey(key string) string     { return s.prefix + "s:" + key }
func (s *SessionStore) userKey(username string) string    { return s.prefix + "u:" + username }
func (s *SessionStore) revokedKey(username string) string { return s.prefix + "r:" + username }

func (s *SessionStore) Create(ctx context.Context, key string, sess domain.Session, ttl time.Duration) error {
	data, err := json.Marshal(record{
		Username: sess.Username, DisplayName: sess.DisplayName,
		CreatedAt: sess.CreatedAt.UnixMicro(), ExpiresAt: sess.ExpiresAt.UnixMicro(),
	})
	if err != nil {
		return err
	}
	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, s.sessionKey(key), data, ttl)
	pipe.SAdd(ctx, s.userKey(sess.Username), key)
	pipe.Expire(ctx, s.userKey(sess.Username), s.retention)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("guardar sesion: %w", err)
	}
	return nil
}

func (s *SessionStore) Get(ctx context.Context, key string) (domain.Session, error) {
	raw, err := s.rdb.Get(ctx, s.sessionKey(key)).Bytes()
	if errors.Is(err, goredis.Nil) {
		return domain.Session{}, domain.ErrSessionInvalid
	}
	if err != nil {
		return domain.Session{}, fmt.Errorf("leer sesion: %w", err)
	}
	var r record
	if err := json.Unmarshal(raw, &r); err != nil || r.Username == "" {
		return domain.Session{}, domain.ErrSessionInvalid
	}
	return domain.Session{
		Username: r.Username, DisplayName: r.DisplayName,
		CreatedAt: time.UnixMicro(r.CreatedAt).UTC(), ExpiresAt: time.UnixMicro(r.ExpiresAt).UTC(),
	}, nil
}

func (s *SessionStore) Touch(ctx context.Context, key string, ttl time.Duration) error {
	return s.rdb.PExpire(ctx, s.sessionKey(key), ttl).Err()
}

func (s *SessionStore) Delete(ctx context.Context, key, username string) error {
	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, s.sessionKey(key))
	if username != "" {
		pipe.SRem(ctx, s.userKey(username), key)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("borrar sesion: %w", err)
	}
	return nil
}

// Revoke fija la marca ANTES de borrar: desde ese instante ninguna peticion con una
// sesion anterior pasa, aunque el borrado falle a medias. Solo se borran las sesiones
// abiertas hasta at: una reentrega tardia del evento no cierra sesiones posteriores.
func (s *SessionStore) Revoke(ctx context.Context, username string, at time.Time) error {
	if err := markRevocation.Run(ctx, s.rdb, []string{s.revokedKey(username)},
		at.UnixMicro(), s.retention.Milliseconds()).Err(); err != nil {
		return fmt.Errorf("marcar revocacion: %w", err)
	}
	members, err := s.rdb.SMembers(ctx, s.userKey(username)).Result()
	if err != nil {
		return fmt.Errorf("listar sesiones del buzon: %w", err)
	}
	for len(members) > 0 {
		n := min(deleteBatch, len(members))
		if err := s.dropRevoked(ctx, username, members[:n], at.UnixMicro()); err != nil {
			return err
		}
		members = members[n:]
	}
	return nil
}

// dropRevoked borra, de un lote de sesiones del buzon, las abiertas hasta revokedAt y
// las que ya caducaron (su clave ya no existe).
func (s *SessionStore) dropRevoked(ctx context.Context, username string, members []string, revokedAt int64) error {
	keys := make([]string, len(members))
	for i, m := range members {
		keys[i] = s.sessionKey(m)
	}
	values, err := s.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return fmt.Errorf("leer sesiones del buzon: %w", err)
	}
	var drop, forget []string
	for i, v := range values {
		raw, ok := v.(string)
		var r record
		if ok && json.Unmarshal([]byte(raw), &r) == nil && r.CreatedAt > revokedAt {
			continue
		}
		drop = append(drop, keys[i])
		forget = append(forget, members[i])
	}
	if len(drop) == 0 {
		return nil
	}
	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, drop...)
	pipe.SRem(ctx, s.userKey(username), forget)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("borrar sesiones del buzon: %w", err)
	}
	return nil
}

func (s *SessionStore) RevokedAt(ctx context.Context, username string) (time.Time, error) {
	v, err := s.rdb.Get(ctx, s.revokedKey(username)).Int64()
	if errors.Is(err, goredis.Nil) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("leer revocacion: %w", err)
	}
	return time.UnixMicro(v).UTC(), nil
}

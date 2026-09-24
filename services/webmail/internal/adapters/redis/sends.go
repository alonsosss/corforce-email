package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	goredis "github.com/redis/go-redis/v9"
)

// Registro de envios por clave de idempotencia: un hash por clave en
// webmail:<celda>:send:<sha256 de buzon y clave>. Reservar, actualizar y liberar son scripts
// atomicos; actualizar y liberar solo actuan si el registro sigue siendo de quien lo
// reservo (campo t), de modo que un envio lento cuya reserva caduco no pisa a otro.
var (
	reserveSend = goredis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then
  return redis.call('HGETALL', KEYS[1])
end
redis.call('HSET', KEYS[1], unpack(ARGV, 2))
redis.call('PEXPIRE', KEYS[1], ARGV[1])
return false`)

	updateSend = goredis.NewScript(`
if redis.call('HGET', KEYS[1], 't') ~= ARGV[1] then
  return 0
end
redis.call('HSET', KEYS[1], unpack(ARGV, 3))
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1`)

	releaseSend = goredis.NewScript(`
if redis.call('HGET', KEYS[1], 't') == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0`)
)

// SendLedger implementa ports.SendLedger.
type SendLedger struct {
	rdb    *goredis.Client
	prefix string
}

func NewSendLedger(rdb *goredis.Client, cellCode string) *SendLedger {
	return &SendLedger{rdb: rdb, prefix: "webmail:" + cellCode + ":send:"}
}

func (l *SendLedger) Reserve(ctx context.Context, key string, rec domain.SendRecord, ttl time.Duration) (domain.SendRecord, bool, error) {
	token, err := newRecordToken()
	if err != nil {
		return domain.SendRecord{}, false, err
	}
	rec.Token = token
	args := append([]any{ttlMillis(ttl)}, recordFields(rec)...)
	current, err := reserveSend.Run(ctx, l.rdb, []string{l.prefix + key}, args...).Slice()
	if errors.Is(err, goredis.Nil) {
		return rec, true, nil
	}
	if err != nil {
		return domain.SendRecord{}, false, fmt.Errorf("reservar envio: %w", err)
	}
	return parseRecord(current), false, nil
}

func (l *SendLedger) Update(ctx context.Context, key string, rec domain.SendRecord, ttl time.Duration) (bool, error) {
	args := append([]any{rec.Token, ttlMillis(ttl)}, recordFields(rec)...)
	n, err := updateSend.Run(ctx, l.rdb, []string{l.prefix + key}, args...).Int64()
	if err != nil {
		return false, fmt.Errorf("guardar envio: %w", err)
	}
	return n == 1, nil
}

func (l *SendLedger) Release(ctx context.Context, key, token string) error {
	if err := releaseSend.Run(ctx, l.rdb, []string{l.prefix + key}, token).Err(); err != nil {
		return fmt.Errorf("liberar envio: %w", err)
	}
	return nil
}

func recordFields(rec domain.SendRecord) []any {
	return []any{
		"t", rec.Token, "s", string(rec.State), "f", rec.Fingerprint, "m", rec.MessageID,
		"c", boolField(rec.SavedToSent), "d", boolField(rec.DraftRemoved), "i", rec.ScheduledID,
	}
}

func parseRecord(values []any) domain.SendRecord {
	fields := make(map[string]string, len(values)/2)
	for i := 0; i+1 < len(values); i += 2 {
		k, _ := values[i].(string)
		v, _ := values[i+1].(string)
		fields[k] = v
	}
	return domain.SendRecord{
		State: domain.SendState(fields["s"]), Fingerprint: fields["f"], MessageID: fields["m"],
		SavedToSent: fields["c"] == "1", DraftRemoved: fields["d"] == "1", ScheduledID: fields["i"], Token: fields["t"],
	}
}

func boolField(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func ttlMillis(ttl time.Duration) int64 {
	return max(ttl.Milliseconds(), 1)
}

func newRecordToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("token del registro de envios: %w", err)
	}
	return hex.EncodeToString(b), nil
}

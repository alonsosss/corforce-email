package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	goredis "github.com/redis/go-redis/v9"
)

// Cupo diario del asistente: dos contadores por dia UTC, uno por buzon y otro por empresa, en
// webmail:<celda>:assistant:<AAAAMMDD>:mb:<buzon> y ...:tenant:<empresa>. Comprobar y anotar es un
// solo script: dos replicas no pueden pasar a la vez del tope. Las claves caducan a los dos dias.
var consumeAssistant = goredis.NewScript(`
local mb = tonumber(redis.call('GET', KEYS[1]) or '0')
local tn = tonumber(redis.call('GET', KEYS[2]) or '0')
if mb >= tonumber(ARGV[1]) then
  return {-1, mb, tn}
end
if tn >= tonumber(ARGV[2]) then
  return {-2, mb, tn}
end
mb = redis.call('INCR', KEYS[1])
tn = redis.call('INCR', KEYS[2])
redis.call('PEXPIRE', KEYS[1], ARGV[3])
redis.call('PEXPIRE', KEYS[2], ARGV[3])
return {0, mb, tn}`)

const assistantQuotaTTL = 48 * time.Hour

// AssistantQuota implementa ports.AssistantQuota.
type AssistantQuota struct {
	rdb    *goredis.Client
	prefix string
}

func NewAssistantQuota(rdb *goredis.Client, cellCode string) *AssistantQuota {
	return &AssistantQuota{rdb: rdb, prefix: "webmail:" + cellCode + ":assistant:"}
}

func (q *AssistantQuota) keys(tenantID, username string, at time.Time) []string {
	day := q.prefix + at.UTC().Format("20060102")
	return []string{day + ":mb:" + username, day + ":tenant:" + tenantID}
}

func (q *AssistantQuota) Consume(ctx context.Context, tenantID, username string, at time.Time, limits domain.AssistantLimits) (domain.AssistantUsage, error) {
	res, err := consumeAssistant.Run(ctx, q.rdb, q.keys(tenantID, username, at),
		limits.MailboxDaily, limits.TenantDaily, assistantQuotaTTL.Milliseconds()).Int64Slice()
	if err != nil {
		return domain.AssistantUsage{}, fmt.Errorf("cupo del asistente: %w", err)
	}
	if len(res) != 3 {
		return domain.AssistantUsage{}, fmt.Errorf("cupo del asistente: respuesta inesperada %v", res)
	}
	usage := domain.AssistantUsage{Mailbox: int(res[1]), Tenant: int(res[2])}
	switch res[0] {
	case -1:
		return usage, &domain.AssistantQuotaError{Scope: domain.QuotaMailbox, Limit: limits.MailboxDaily}
	case -2:
		return usage, &domain.AssistantQuotaError{Scope: domain.QuotaTenant, Limit: limits.TenantDaily}
	}
	return usage, nil
}

func (q *AssistantQuota) Usage(ctx context.Context, tenantID, username string, at time.Time) (domain.AssistantUsage, error) {
	vals, err := q.rdb.MGet(ctx, q.keys(tenantID, username, at)...).Result()
	if err != nil {
		return domain.AssistantUsage{}, fmt.Errorf("cupo del asistente: %w", err)
	}
	return domain.AssistantUsage{Mailbox: counter(vals[0]), Tenant: counter(vals[1])}, nil
}

func counter(v any) int {
	s, ok := v.(string)
	if !ok {
		return 0
	}
	var n int
	if _, err := fmt.Sscan(s, &n); err != nil {
		return 0
	}
	return n
}

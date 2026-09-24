//go:build integration

package redis

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// Cupo del asistente contra un Redis real (WEBMAIL_TEST_REDIS_ADDR): topes por buzon y por empresa,
// atomicos entre peticiones concurrentes, por dia UTC y con caducidad.
func TestCupoDelAsistenteContraRedis(t *testing.T) {
	addr := integrationEnv(t, "WEBMAIL_TEST_REDIS_ADDR")
	rdb := goredis.NewClient(&goredis.Options{Addr: addr, Password: os.Getenv("WEBMAIL_TEST_REDIS_PASSWORD")})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("redis: %v", err)
	}
	cell := "it" + uuid.NewString()[:8]
	q := NewAssistantQuota(rdb, cell)
	t.Cleanup(func() {
		keys, _ := rdb.Keys(context.Background(), "webmail:"+cell+":assistant:*").Result()
		if len(keys) > 0 {
			_ = rdb.Del(context.Background(), keys...).Err()
		}
	})
	tenant := uuid.NewString()
	day := time.Date(2026, 9, 24, 23, 0, 0, 0, time.UTC)
	limits := domain.AssistantLimits{MaxInputChars: 1, MaxThreadMessages: 1, MaxInstructionChars: 1, MailboxDaily: 3, TenantDaily: 4}

	var wg sync.WaitGroup
	var mu sync.Mutex
	granted, rejected := 0, 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := q.Consume(ctx, tenant, "ana@empresa.pe", day, limits)
			mu.Lock()
			defer mu.Unlock()
			var qerr *domain.AssistantQuotaError
			switch {
			case err == nil:
				granted++
			case errors.As(err, &qerr) && qerr.Scope == domain.QuotaMailbox:
				rejected++
			default:
				t.Errorf("consumo: %v", err)
			}
		}()
	}
	wg.Wait()
	if granted != 3 || rejected != 7 {
		t.Fatalf("concurrencia: %d concedidas, %d rechazadas", granted, rejected)
	}

	if _, err := q.Consume(ctx, tenant, "luis@empresa.pe", day, limits); err != nil {
		t.Fatal(err)
	}
	var qerr *domain.AssistantQuotaError
	if _, err := q.Consume(ctx, tenant, "eva@empresa.pe", day, limits); !errors.As(err, &qerr) || qerr.Scope != domain.QuotaTenant {
		t.Fatalf("tope de la empresa: %v", err)
	}
	usage, err := q.Usage(ctx, tenant, "ana@empresa.pe", day)
	if err != nil || usage.Mailbox != 3 || usage.Tenant != 4 {
		t.Fatalf("uso: %+v %v", usage, err)
	}
	if usage, _ := q.Usage(ctx, tenant, "eva@empresa.pe", day); usage.Mailbox != 0 {
		t.Fatalf("un rechazo no anota nada: %+v", usage)
	}

	next := day.Add(2 * time.Hour)
	if _, err := q.Consume(ctx, tenant, "ana@empresa.pe", next, limits); err != nil {
		t.Fatalf("el cupo vuelve al dia siguiente (UTC): %v", err)
	}
	key := "webmail:" + cell + ":assistant:20260924:mb:ana@empresa.pe"
	if ttl := rdb.PTTL(ctx, key).Val(); ttl <= 24*time.Hour || ttl > assistantQuotaTTL {
		t.Fatalf("las claves caducan solas: %v", ttl)
	}
}

//go:build integration

package redis

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// integrationEnv devuelve la variable de entorno que apunta a la infraestructura de la
// prueba. Sin ella la prueba se salta, salvo con INTEGRATION_REQUIRED=1 (make
// test-integration y CI): ahi es un fallo, porque un salto esconderia que no llego.
func integrationEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		if os.Getenv("INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("%s no definida con INTEGRATION_REQUIRED=1", name)
		}
		t.Skipf("%s no definida", name)
	}
	return v
}

// REPUTATION_TEST_REDIS_ADDR apunta a un Redis desechable (docker run -d redis:7.4.10-alpine;
// REPUTATION_TEST_REDIS_PASSWORD si pide contrasena).
func testClient(t *testing.T) *goredis.Client {
	t.Helper()
	addr := integrationEnv(t, "REPUTATION_TEST_REDIS_ADDR")
	rdb := goredis.NewClient(&goredis.Options{Addr: addr, Password: os.Getenv("REPUTATION_TEST_REDIS_PASSWORD")})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func TestReservaCompruebaHoraYDiaAntesDeSumar(t *testing.T) {
	rdb := testClient(t)
	ctx := context.Background()
	l := NewRateLimiter(rdb)
	tenant := uuid.New()
	class := domain.ClassMarketing
	limits := domain.Limits{Hourly: 10, Daily: 15}
	at1030 := time.Date(2026, 9, 12, 10, 30, 0, 0, time.UTC)
	at1105 := at1030.Add(35 * time.Minute)
	t.Cleanup(func() {
		rdb.Del(ctx, HourKey(tenant, class, at1030), HourKey(tenant, class, at1105), DayKey(tenant, class, at1030))
	})

	steps := []struct {
		now      time.Time
		count    int64
		allowed  bool
		hour     int64
		day      int64
		exceeded domain.Window
	}{
		{at1030, 4, true, 4, 4, domain.WindowNone},
		{at1030, 7, false, 4, 4, domain.WindowHour},
		{at1030, 6, true, 10, 10, domain.WindowNone},
		{at1105, 6, false, 0, 10, domain.WindowDay},
		{at1105, 5, true, 5, 15, domain.WindowNone},
		{at1105, 1, false, 5, 15, domain.WindowDay},
	}
	for i, s := range steps {
		out, err := l.Reserve(ctx, tenant, class, s.now, s.count, limits)
		if err != nil {
			t.Fatalf("paso %d: %v", i, err)
		}
		if out.Allowed != s.allowed || out.HourUsed != s.hour || out.DayUsed != s.day || out.Exceeded != s.exceeded {
			t.Fatalf("paso %d: %+v; se esperaba allowed=%v hora=%d dia=%d agotada=%d", i, out, s.allowed, s.hour, s.day, s.exceeded)
		}
	}

	hour, day, err := l.Usage(ctx, tenant, class, at1105)
	if err != nil || hour != 5 || day != 15 {
		t.Fatalf("uso: hora=%d dia=%d err=%v", hour, day, err)
	}
	hour, day, err = l.Usage(ctx, uuid.New(), class, at1105)
	if err != nil || hour != 0 || day != 0 {
		t.Fatalf("sin claves el uso es cero: %d %d %v", hour, day, err)
	}

	hourTTLNow := rdb.TTL(ctx, HourKey(tenant, class, at1105)).Val()
	dayTTLNow := rdb.TTL(ctx, DayKey(tenant, class, at1105)).Val()
	if hourTTLNow <= 0 || hourTTLNow > hourTTL || dayTTLNow <= 0 || dayTTLNow > dayTTL {
		t.Fatalf("caducidad: hora=%s dia=%s", hourTTLNow, dayTTLNow)
	}
}

func TestReservaNoRenuevaLaCaducidad(t *testing.T) {
	rdb := testClient(t)
	ctx := context.Background()
	l := NewRateLimiter(rdb)
	tenant := uuid.New()
	now := time.Now().UTC()
	hk := HourKey(tenant, domain.ClassTransactional, now)
	t.Cleanup(func() { rdb.Del(ctx, hk, DayKey(tenant, domain.ClassTransactional, now)) })

	limits := domain.Limits{Hourly: 100, Daily: 1000}
	if _, err := l.Reserve(ctx, tenant, domain.ClassTransactional, now, 1, limits); err != nil {
		t.Fatal(err)
	}
	if err := rdb.Expire(ctx, hk, 100*time.Second).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Reserve(ctx, tenant, domain.ClassTransactional, now, 1, limits); err != nil {
		t.Fatal(err)
	}
	if ttl := rdb.TTL(ctx, hk).Val(); ttl > 100*time.Second {
		t.Fatalf("un envio no debe alargar la vida de la ventana: %s", ttl)
	}
}

func TestReservaConcurrenteNoSobrepasaElLimite(t *testing.T) {
	rdb := testClient(t)
	ctx := context.Background()
	l := NewRateLimiter(rdb)
	tenant := uuid.New()
	now := time.Now().UTC()
	t.Cleanup(func() {
		rdb.Del(ctx, HourKey(tenant, domain.ClassMarketing, now), DayKey(tenant, domain.ClassMarketing, now))
	})

	limits := domain.Limits{Hourly: 25, Daily: 1000}
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := l.Reserve(ctx, tenant, domain.ClassMarketing, now, 1, limits)
			if err != nil {
				t.Error(err)
				return
			}
			if out.Allowed {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	hour, day, err := l.Usage(ctx, tenant, domain.ClassMarketing, now)
	if err != nil || allowed.Load() != 25 || hour != 25 || day != 25 {
		t.Fatalf("autorizados=%d hora=%d dia=%d err=%v", allowed.Load(), hour, day, err)
	}
}

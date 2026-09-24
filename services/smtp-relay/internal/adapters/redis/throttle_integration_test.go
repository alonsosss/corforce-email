//go:build integration

// Pruebas del freno y de los cupos del relay contra un Redis real (REDIS_TEST_ADDR,
// REDIS_TEST_PASSWORD; make test-integration los levanta).
package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func redisForTest(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		if os.Getenv("INTEGRATION_REQUIRED") == "1" {
			t.Fatal("REDIS_TEST_ADDR no definida con INTEGRATION_REQUIRED=1")
		}
		t.Skip("REDIS_TEST_ADDR no definida")
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr, Password: os.Getenv("REDIS_TEST_PASSWORD")})
	t.Cleanup(func() { _ = rdb.Close() })
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("redis de prueba: %v", err)
	}
	return rdb
}

func unique(t *testing.T) string {
	t.Helper()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func TestFrenoBloqueaTrasLosFallos(t *testing.T) {
	ctx := context.Background()
	th := NewThrottle(redisForTest(t), ThrottleConfig{MaxFailures: 3, MaxFailuresPerIP: 5, Window: time.Minute, LockTTL: time.Minute}, zap.NewNop())
	user, ip := "u"+unique(t), "198.51.100."+unique(t)
	for i := 0; i < 2; i++ {
		th.Failure(ctx, user, ip)
	}
	if th.Blocked(ctx, user, ip) {
		t.Fatal("por debajo del umbral no se bloquea")
	}
	th.Success(ctx, user, ip)
	for i := 0; i < 2; i++ {
		th.Failure(ctx, user, ip)
	}
	if th.Blocked(ctx, user, ip) {
		t.Fatal("un acierto reinicia la pareja")
	}
	th.Failure(ctx, user, ip)
	if !th.Blocked(ctx, user, ip) {
		t.Fatal("al tercer fallo seguido la pareja queda bloqueada")
	}

	// Un barrido desde una IP contra muchos usuarios bloquea la IP entera.
	sweep := "203.0.113." + unique(t)
	for i := 0; i < 5; i++ {
		th.Failure(ctx, "u"+unique(t), sweep)
	}
	if !th.Blocked(ctx, "otro"+unique(t), sweep) {
		t.Fatal("la IP del barrido queda bloqueada para cualquier usuario")
	}
}

func TestCuposCompartidos(t *testing.T) {
	ctx := context.Background()
	store := middleware.NewRedisRateLimitStore(redisForTest(t))
	// Nombres propios de esta ejecucion: el cupo vive en Redis y no se comparte con otra.
	id := unique(t)
	a := NewLimits(store, LimitsConfig{ConnectionsPerIP: 2, MessagesPerKey: 1, MessagesPerIP: 10}, zap.NewNop())
	b := NewLimits(store, LimitsConfig{ConnectionsPerIP: 2, MessagesPerKey: 1, MessagesPerIP: 10}, zap.NewNop())
	ip := "192.0.2." + id
	if !a.AllowConnection(ctx, ip) || !b.AllowConnection(ctx, ip) || a.AllowConnection(ctx, ip) {
		t.Fatal("el cupo de conexiones es comun a todas las replicas")
	}
	if !a.AllowMessage(ctx, "k"+id, ip) || b.AllowMessage(ctx, "k"+id, ip) {
		t.Fatal("el cupo de mensajes por clave es comun a todas las replicas")
	}
}

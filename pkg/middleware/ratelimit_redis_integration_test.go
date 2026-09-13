//go:build integration

// Pruebas del limitador compartido contra un Redis real. REDIS_TEST_ADDR apunta a un Redis
// desechable (REDIS_TEST_PASSWORD si hace falta):
//
//	docker run -d --name rl-test -p 127.0.0.1:26379:6379 redis:7.4.10-alpine
//	REDIS_TEST_ADDR=127.0.0.1:26379 go test -tags integration -race ./pkg/middleware/
package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func redisForTest(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR no definido")
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr, Password: os.Getenv("REDIS_TEST_PASSWORD")})
	t.Cleanup(func() { _ = rdb.Close() })
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("redis de prueba: %v", err)
	}
	return rdb
}

func uniqueLimiterName(t *testing.T) string {
	t.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "test:" + hex.EncodeToString(b)
}

func TestRedisDosReplicasCompartenCupo(t *testing.T) {
	rdb := redisForTest(t)
	name := uniqueLimiterName(t)
	key := "rl:" + name + ":ip:203.0.113.7"
	t.Cleanup(func() { rdb.Del(context.Background(), key) })

	store := NewRedisRateLimitStore(rdb)
	a := NewSharedRateLimiter(store, name, 3, time.Minute, zap.NewNop()).Limit(okHandler)
	b := NewSharedRateLimiter(store, name, 3, time.Minute, zap.NewNop()).Limit(okHandler)

	for i, h := range []http.Handler{a, b, a} {
		if code := serve(h, peticionDe("", "203.0.113.7")).Code; code != http.StatusOK {
			t.Fatalf("peticion %d dentro del cupo: %d", i+1, code)
		}
	}
	w := serve(b, peticionDe("", "203.0.113.7"))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("la segunda replica concedio un cupo propio: %d", w.Code)
	}
	secs, err := strconv.Atoi(w.Header().Get("Retry-After"))
	if err != nil || secs < 1 || secs > 60 {
		t.Fatalf("Retry-After %q fuera de la ventana", w.Header().Get("Retry-After"))
	}

	ctx := context.Background()
	if n, err := rdb.Get(ctx, key).Int64(); err != nil || n != 4 {
		t.Fatalf("contador en Redis: %d, %v", n, err)
	}
	ttl, err := rdb.PTTL(ctx, key).Result()
	if err != nil || ttl <= 0 || ttl > time.Minute {
		t.Fatalf("la clave debe caducar con la ventana: %v, %v", ttl, err)
	}
}

// Una clave que quedo sin caducidad la recibe en la siguiente peticion, y las peticiones
// no prolongan la ventana de una clave que ya la tiene.
func TestRedisClaveSinCaducidadLaRecibe(t *testing.T) {
	rdb := redisForTest(t)
	ctx := context.Background()
	key := "rl:" + uniqueLimiterName(t) + ":ip:198.51.100.4"
	t.Cleanup(func() { rdb.Del(ctx, key) })
	if err := rdb.Set(ctx, key, 5, 0).Err(); err != nil {
		t.Fatal(err)
	}

	store := NewRedisRateLimitStore(rdb)
	n, reset, err := store.Hit(ctx, key, time.Minute)
	if err != nil || n != 6 || reset <= 0 || reset > time.Minute {
		t.Fatalf("Hit = %d, %v, %v", n, reset, err)
	}
	if err := rdb.PExpire(ctx, key, 10*time.Second).Err(); err != nil {
		t.Fatal(err)
	}
	if _, reset, err := store.Hit(ctx, key, time.Minute); err != nil || reset > 10*time.Second {
		t.Fatalf("la peticion prolongo la ventana: %v, %v", reset, err)
	}
}

// Con el Redis inalcanzable se decide en memoria sin que cada peticion espere al almacen.
func TestRedisInalcanzableDegradaSinAtascar(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 200 * time.Millisecond})
	t.Cleanup(func() { _ = rdb.Close() })
	name := uniqueLimiterName(t)
	before := degradedCount(t, name)
	h := NewSharedRateLimiter(NewRedisRateLimitStore(rdb), name, 2, time.Minute, zap.NewNop()).Limit(okHandler)

	start := time.Now()
	codes := []int{}
	for i := 0; i < 3; i++ {
		codes = append(codes, serve(h, peticionDe("", "203.0.113.7")).Code)
	}
	if codes[0] != http.StatusOK || codes[1] != http.StatusOK || codes[2] != http.StatusTooManyRequests {
		t.Fatalf("con Redis caido: %v; se esperaba 200, 200, 429", codes)
	}
	if elapsed := time.Since(start); elapsed > sharedTimeout+time.Second {
		t.Fatalf("tres peticiones con Redis caido tardaron %v", elapsed)
	}
	if got := degradedCount(t, name) - before; got != 3 {
		t.Fatalf("rate_limit_degraded_total subio %v; se esperaba 3", got)
	}
}

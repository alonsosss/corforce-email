//go:build integration

// Prueba del adaptador contra un Redis real de la misma version que redis-mail (HSCAN
// NOVALUES exige 7.4). Usa claves con prefijo propio y las borra al terminar.
//
//	docker run -d --name ms-redis -p 127.0.0.1:56379:6379 redis:7.4.10-alpine redis-server --requirepass t
//	MAIL_SECURITY_TEST_REDIS='127.0.0.1:56379' MAIL_SECURITY_TEST_REDIS_PASSWORD=t go test -tags integration ./services/mail-security/internal/adapters/redis/
package redis

import (
	"context"
	"net"
	"os"
	"sort"
	"strconv"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"go.uber.org/zap"
)

func TestIntegracionRedis(t *testing.T) {
	addr := os.Getenv("MAIL_SECURITY_TEST_REDIS")
	if addr == "" {
		t.Skip("MAIL_SECURITY_TEST_REDIS no definido")
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	store := New(Config{Host: host, Port: port, Password: os.Getenv("MAIL_SECURITY_TEST_REDIS_PASSWORD")})
	defer store.Close()
	ctx := context.Background()
	if err := store.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		store.client.Del(ctx, domain.RedisDKIMPrivKeys, domain.RedisDKIMSelectors, domain.RedisRateLimitLog, domain.RedisDomainMap)
	}
	cleanup()
	defer cleanup()

	// Rotacion y bajas de DKIM a traves del sincronizador, con HSCAN real.
	sync := app.NewRedisSync(store, apptest.NewDirectory(), apptest.NewPolicyReader(), zap.NewNop())
	pem := "-----BEGIN PRIVATE KEY-----\nx\n-----END PRIVATE KEY-----"
	for _, k := range []domain.DKIMKey{
		{Domain: "acme.com", Selector: "s1", PrivateKeyPEM: pem},
		{Domain: "acme.com", Selector: "s2", PrivateKeyPEM: pem},
		{Domain: "sub.acme.com", Selector: "s1", PrivateKeyPEM: pem},
	} {
		if err := sync.SyncDKIM(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := store.HKeys(ctx, domain.RedisDKIMPrivKeys, "*.acme.com")
	sort.Strings(keys)
	if err != nil || len(keys) != 3 {
		t.Fatalf("HSCAN MATCH: %v %v", keys, err)
	}
	if err := sync.RemoveDKIMDomain(ctx, "acme.com"); err != nil {
		t.Fatal(err)
	}
	left, _ := store.HGetAll(ctx, domain.RedisDKIMPrivKeys)
	if len(left) != 1 || left["s1.sub.acme.com"] != pem {
		t.Fatalf("la baja de acme.com solo debe quitar sus claves: %v", left)
	}
	if sel, ok, _ := store.HGet(ctx, domain.RedisDKIMSelectors, "acme.com"); ok {
		t.Fatalf("selector de acme.com debia retirarse: %q", sel)
	}
	if sel, ok, _ := store.HGet(ctx, domain.RedisDKIMSelectors, "sub.acme.com"); !ok || sel != "s1" {
		t.Fatalf("selector de sub.acme.com intacto: %q %v", sel, ok)
	}

	// LPUSH + LTRIM en una transaccion.
	for i := 0; i < 5; i++ {
		if err := store.LPushTrim(ctx, domain.RedisRateLimitLog, strconv.Itoa(i), 3); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := store.client.LRange(ctx, domain.RedisRateLimitLog, 0, -1).Result()
	if len(got) != 3 || got[0] != "4" || got[2] != "2" {
		t.Fatalf("RL_LOG recortado: %v", got)
	}

	// HDEL sin campos no debe fallar (reconciliacion sin sobrantes).
	if err := store.HDel(ctx, domain.RedisDomainMap); err != nil {
		t.Fatal(err)
	}
}

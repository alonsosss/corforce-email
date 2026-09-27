//go:build integration

package redis

import (
	"context"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
)

// La lista del registro de entregas: orden de llegada, lista de trabajo y contexto por id de cola.
func TestIntegracionRegistroDeEntregasRedis(t *testing.T) {
	addr := os.Getenv("MAIL_SECURITY_TEST_REDIS")
	if addr == "" {
		t.Skip("MAIL_SECURITY_TEST_REDIS no definido")
	}
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	store := New(Config{Host: host, Port: port, Password: os.Getenv("MAIL_SECURITY_TEST_REDIS_PASSWORD")})
	defer store.Close()
	ctx := context.Background()
	cleanup := func() {
		store.client.Del(ctx, domain.RedisDeliveryLog, domain.RedisDeliveryLogWork, domain.RedisDeliveryQIDPrefix+"ABC123")
	}
	cleanup()
	defer cleanup()
	log := NewDeliveryLog(store)

	// syslog-ng hace LPUSH: la primera linea escrita es la primera que se lee.
	store.client.LPush(ctx, domain.RedisDeliveryLog, "uno", "dos", "tres")
	if n, err := log.Backlog(ctx); err != nil || n != 3 {
		t.Fatalf("backlog: %d %v", n, err)
	}
	first, ok, err := log.Next(ctx, time.Second)
	if err != nil || !ok || first != "uno" {
		t.Fatalf("primera: %q %v %v", first, ok, err)
	}
	second, _, _ := log.Next(ctx, time.Second)
	pending, err := log.Pending(ctx)
	if err != nil || len(pending) != 2 || pending[0] != "uno" || pending[1] != "dos" || second != "dos" {
		t.Fatalf("la lista de trabajo conserva lo leido y sin confirmar, en orden: %v %v", pending, err)
	}
	if err := log.Ack(ctx, "uno"); err != nil {
		t.Fatal(err)
	}
	if pending, _ := log.Pending(ctx); len(pending) != 1 || pending[0] != "dos" {
		t.Fatalf("tras confirmar: %v", pending)
	}
	store.client.Del(ctx, domain.RedisDeliveryLog)
	start := time.Now()
	if _, ok, err := log.Next(ctx, time.Second); ok || err != nil || time.Since(start) < 900*time.Millisecond {
		t.Fatalf("sin lineas espera y devuelve false: %v %v", ok, err)
	}

	qc := domain.QueueContext{SASLUsername: "ana@acme.com", From: "ana@acme.com", MessageID: "m@x"}
	if err := log.SaveContext(ctx, "ABC123", qc, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := log.LoadContext(ctx, "ABC123")
	if err != nil || got != qc {
		t.Fatalf("contexto: %+v %v", got, err)
	}
	if ttl := store.client.TTL(ctx, domain.RedisDeliveryQIDPrefix+"ABC123").Val(); ttl <= 0 || ttl > time.Minute {
		t.Errorf("el contexto caduca: %v", ttl)
	}
	if empty, err := log.LoadContext(ctx, "NOEXISTE"); err != nil || empty != (domain.QueueContext{}) {
		t.Errorf("sin contexto: %+v %v", empty, err)
	}
}

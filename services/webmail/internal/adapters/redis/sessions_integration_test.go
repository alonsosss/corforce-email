//go:build integration

// Prueba del almacen de sesiones contra un Redis real (WEBMAIL_TEST_REDIS_ADDR, por
// ejemplo 127.0.0.1:6379; WEBMAIL_TEST_REDIS_PASSWORD si hace falta). Usa una celda
// aleatoria para no pisar datos.
package redis

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

func testStore(t *testing.T) (*SessionStore, *goredis.Client) {
	t.Helper()
	addr := os.Getenv("WEBMAIL_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("WEBMAIL_TEST_REDIS_ADDR no definido")
	}
	rdb := goredis.NewClient(&goredis.Options{Addr: addr, Password: os.Getenv("WEBMAIL_TEST_REDIS_PASSWORD")})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("redis: %v", err)
	}
	cell := "test-" + uuid.NewString()
	t.Cleanup(func() {
		keys, _ := rdb.Keys(context.Background(), "webmail:"+cell+":*").Result()
		if len(keys) > 0 {
			rdb.Del(context.Background(), keys...)
		}
		_ = rdb.Close()
	})
	return NewSessionStore(rdb, cell, 12*time.Hour), rdb
}

func TestIntegracionSesionesEnRedis(t *testing.T) {
	ctx := context.Background()
	store, rdb := testStore(t)
	t0 := time.Now().UTC().Truncate(time.Microsecond)
	old := domain.Session{Username: "ana@empresa.pe", DisplayName: "Ana", CreatedAt: t0, ExpiresAt: t0.Add(12 * time.Hour)}

	if err := store.Create(ctx, "k-old", old, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, "k-old")
	if err != nil || got.Username != old.Username || got.DisplayName != old.DisplayName ||
		!got.CreatedAt.Equal(old.CreatedAt) || !got.ExpiresAt.Equal(old.ExpiresAt) {
		t.Fatalf("got %+v %v", got, err)
	}
	if ttl := rdb.PTTL(ctx, store.sessionKey("k-old")).Val(); ttl <= 0 || ttl > time.Minute {
		t.Fatalf("la inactividad es el TTL de la clave: %v", ttl)
	}
	if err := store.Touch(ctx, "k-old", 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	if ttl := rdb.PTTL(ctx, store.sessionKey("k-old")).Val(); ttl <= time.Minute {
		t.Fatalf("Touch renueva el TTL: %v", ttl)
	}

	newer := old
	newer.CreatedAt = t0.Add(time.Minute)
	if err := store.Create(ctx, "k-new", newer, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(ctx, "ana@empresa.pe", t0.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "k-old"); err != domain.ErrSessionInvalid {
		t.Fatalf("la sesion anterior a la revocacion se borra: %v", err)
	}
	if _, err := store.Get(ctx, "k-new"); err != nil {
		t.Fatalf("una sesion posterior sobrevive: %v", err)
	}
	if at, err := store.RevokedAt(ctx, "ana@empresa.pe"); err != nil || !at.Equal(t0.Add(30*time.Second)) {
		t.Fatalf("marca: %v %v", at, err)
	}
	// Una reentrega vieja no mueve la marca hacia atras.
	if err := store.Revoke(ctx, "ana@empresa.pe", t0); err != nil {
		t.Fatal(err)
	}
	if at, _ := store.RevokedAt(ctx, "ana@empresa.pe"); !at.Equal(t0.Add(30 * time.Second)) {
		t.Fatalf("la marca no retrocede: %v", at)
	}
	if members := rdb.SMembers(ctx, store.userKey("ana@empresa.pe")).Val(); len(members) != 1 || members[0] != "k-new" {
		t.Fatalf("el indice solo conserva la sesion viva: %v", members)
	}
	if err := store.Delete(ctx, "k-new", "ana@empresa.pe"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "k-new"); err != domain.ErrSessionInvalid {
		t.Fatalf("borrada: %v", err)
	}
	if at, _ := store.RevokedAt(ctx, "otro@empresa.pe"); !at.IsZero() {
		t.Fatal("sin revocacion la marca es cero")
	}
}

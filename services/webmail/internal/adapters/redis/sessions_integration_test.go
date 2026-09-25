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

func testStore(t *testing.T) (*SessionStore, *goredis.Client) {
	t.Helper()
	addr := integrationEnv(t, "WEBMAIL_TEST_REDIS_ADDR")
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
	old := domain.Session{Username: "ana@empresa.pe", DisplayName: "Ana", CreatedAt: t0, ExpiresAt: t0.Add(12 * time.Hour),
		TenantID: "11111111-1111-4111-8111-111111111111", MailboxID: "22222222-2222-4222-8222-222222222222"}

	if err := store.Create(ctx, "k-old", old, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, "k-old")
	if err != nil || got.Username != old.Username || got.DisplayName != old.DisplayName || got.TenantID != old.TenantID || got.MailboxID != old.MailboxID ||
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

// Al activar la verificacion en dos pasos se revoca en "at" y quien activo sigue con una sesion que
// nace un microsegundo despues: la resolucion con la que Redis guarda la marca y el inicio.
func TestIntegracionSesionReabiertaUnMicrosegundoDespuesDeLaMarca(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	at := time.Now().UTC().Truncate(time.Microsecond)
	sess := domain.Session{Username: "ana@empresa.pe", CreatedAt: at.Add(-time.Minute), ExpiresAt: at.Add(time.Hour)}
	if err := store.Create(ctx, "k-antes", sess, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(ctx, sess.Username, at); err != nil {
		t.Fatal(err)
	}
	fresh := sess
	fresh.CreatedAt = at.Add(time.Microsecond)
	if err := store.Create(ctx, "k-nueva", fresh, time.Minute); err != nil {
		t.Fatal(err)
	}
	mark, err := store.RevokedAt(ctx, sess.Username)
	if err != nil || !mark.Equal(at) {
		t.Fatalf("marca: %v %v", mark, err)
	}
	got, err := store.Get(ctx, "k-nueva")
	if err != nil || got.RevokedBy(mark) {
		t.Fatalf("la sesion reabierta no cae con la marca: %+v %v", got, err)
	}
	if _, err := store.Get(ctx, "k-antes"); err != domain.ErrSessionInvalid {
		t.Fatalf("la anterior se borra: %v", err)
	}
}

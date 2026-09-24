//go:build integration

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

// Prueba del registro de envios contra un Redis real (WEBMAIL_TEST_REDIS_ADDR): que la
// reserva es atomica y que solo quien reservo actualiza o libera.
func TestRegistroDeEnviosContraRedis(t *testing.T) {
	addr := integrationEnv(t, "WEBMAIL_TEST_REDIS_ADDR")
	rdb := goredis.NewClient(&goredis.Options{Addr: addr, Password: os.Getenv("WEBMAIL_TEST_REDIS_PASSWORD")})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("redis: %v", err)
	}
	cell := "it" + uuid.NewString()[:8]
	ledger := NewSendLedger(rdb, cell)
	key := "clave-" + uuid.NewString()
	redisKey := "webmail:" + cell + ":send:" + key
	t.Cleanup(func() { _ = rdb.Del(context.Background(), redisKey).Err() })

	pending := domain.SendRecord{State: domain.SendPending, Fingerprint: "huella"}
	mine, reserved, err := ledger.Reserve(ctx, key, pending, time.Minute)
	if err != nil || !reserved || mine.Token == "" || mine.State != domain.SendPending {
		t.Fatalf("reserva: %+v %v %v", mine, reserved, err)
	}
	if ttl := rdb.PTTL(ctx, redisKey).Val(); ttl <= 0 || ttl > time.Minute {
		t.Fatalf("la reserva caduca sola: %v", ttl)
	}

	current, reserved, err := ledger.Reserve(ctx, key, pending, time.Minute)
	if err != nil || reserved || current.Token != mine.Token || current.Fingerprint != "huella" {
		t.Fatalf("segunda reserva: %+v %v %v", current, reserved, err)
	}

	stolen := mine
	stolen.Token = "otra-marca"
	stolen.State = domain.SendSent
	if ok, err := ledger.Update(ctx, key, stolen, time.Hour); err != nil || ok {
		t.Fatalf("otra marca no actualiza: %v %v", ok, err)
	}
	sent := mine
	sent.State, sent.MessageID, sent.SavedToSent, sent.ScheduledID = domain.SendSent, "abc@empresa.pe", true, "00000000-0000-4000-8000-000000000001"
	if ok, err := ledger.Update(ctx, key, sent, time.Hour); err != nil || !ok {
		t.Fatalf("actualizar: %v %v", ok, err)
	}
	current, _, _ = ledger.Reserve(ctx, key, pending, time.Minute)
	if current.State != domain.SendSent || current.MessageID != "abc@empresa.pe" || !current.SavedToSent || current.DraftRemoved ||
		current.ScheduledID != "00000000-0000-4000-8000-000000000001" {
		t.Fatalf("registro guardado: %+v", current)
	}
	if ttl := rdb.PTTL(ctx, redisKey).Val(); ttl <= time.Minute {
		t.Fatalf("el registro enviado vive lo que una sesion: %v", ttl)
	}

	if err := ledger.Release(ctx, key, "otra-marca"); err != nil || rdb.Exists(ctx, redisKey).Val() != 1 {
		t.Fatalf("otra marca no libera: %v", err)
	}
	if err := ledger.Release(ctx, key, mine.Token); err != nil || rdb.Exists(ctx, redisKey).Val() != 0 {
		t.Fatalf("liberar: %v", err)
	}
	if _, reserved, err := ledger.Reserve(ctx, key, pending, time.Minute); err != nil || !reserved {
		t.Fatalf("liberada, la clave se vuelve a reservar: %v %v", reserved, err)
	}
}

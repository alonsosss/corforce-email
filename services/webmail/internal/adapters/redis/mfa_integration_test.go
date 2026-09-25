//go:build integration

package redis

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/google/uuid"
)

func testMFAStore(t *testing.T) *MFAStore {
	t.Helper()
	_, rdb := testStore(t)
	cell := "test-" + uuid.NewString()
	// Corre antes que la limpieza de testStore, que cierra el cliente.
	t.Cleanup(func() {
		keys, _ := rdb.Keys(context.Background(), "webmail:"+cell+":*").Result()
		if len(keys) > 0 {
			rdb.Del(context.Background(), keys...)
		}
	})
	return NewMFAStore(rdb, cell)
}

func TestDesafioDelSegundoPaso(t *testing.T) {
	s := testMFAStore(t)
	ctx := context.Background()
	started := time.Date(2026, 9, 24, 8, 0, 0, 123000, time.UTC)
	id := domain.Identity{Username: "ana@empresa.pe", DisplayName: "Ana", TenantID: "11111111-1111-4111-8111-111111111111", MailboxID: "22222222-2222-4222-8222-222222222222"}
	if err := s.CreateChallenge(ctx, "k1", domain.MFAChallenge{Identity: id, StartedAt: started}, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetChallenge(ctx, "k1")
	if err != nil || got.Identity.Username != id.Username || got.Identity.MailboxID != id.MailboxID || !got.StartedAt.Equal(started) {
		t.Fatalf("%+v %v", got, err)
	}
	for want := 1; want <= 3; want++ {
		if n, err := s.CountAttempt(ctx, "k1"); err != nil || n != want {
			t.Fatalf("intento %d: %d %v", want, n, err)
		}
	}
	ttl, _ := s.rdb.PTTL(ctx, s.challengeKey("k1")).Result()
	if ttl <= 0 || ttl > time.Minute {
		t.Fatalf("contar no quita ni alarga la vida del desafio: %v", ttl)
	}
	if err := s.DeleteChallenge(ctx, "k1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetChallenge(ctx, "k1"); !errors.Is(err, domain.ErrMFAChallengeExpired) {
		t.Fatalf("%v", err)
	}
	if _, err := s.CountAttempt(ctx, "k1"); !errors.Is(err, domain.ErrMFAChallengeExpired) {
		t.Fatalf("contar sobre un desafio borrado: %v", err)
	}
	if n, _ := s.rdb.Exists(ctx, s.challengeKey("k1")).Result(); n != 0 {
		t.Fatal("contar no recrea el desafio")
	}
}

func TestDesafioCaduca(t *testing.T) {
	s := testMFAStore(t)
	ctx := context.Background()
	if err := s.CreateChallenge(ctx, "k2", domain.MFAChallenge{Identity: domain.Identity{Username: "ana@empresa.pe"}}, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Millisecond)
	if _, err := s.GetChallenge(ctx, "k2"); !errors.Is(err, domain.ErrMFAChallengeExpired) {
		t.Fatalf("%v", err)
	}
}

func TestPreparacionDelSecreto(t *testing.T) {
	s := testMFAStore(t)
	ctx := context.Background()
	if _, err := s.SetupHash(ctx, "ana@empresa.pe"); !errors.Is(err, domain.ErrMFASetupExpired) {
		t.Fatalf("%v", err)
	}
	if err := s.SaveSetup(ctx, "ana@empresa.pe", "h1", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSetup(ctx, "ana@empresa.pe", "h2", time.Minute); err != nil {
		t.Fatal(err)
	}
	if h, err := s.SetupHash(ctx, "ana@empresa.pe"); err != nil || h != "h2" {
		t.Fatalf("la ultima preparacion sustituye a la anterior: %q %v", h, err)
	}
	if err := s.DeleteSetup(ctx, "ana@empresa.pe"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetupHash(ctx, "ana@empresa.pe"); !errors.Is(err, domain.ErrMFASetupExpired) {
		t.Fatalf("%v", err)
	}
}

package nats

import (
	"context"
	"testing"
	"time"
)

func TestTokenBucketRate(t *testing.T) {
	b := NewTokenBucket(10, 2)
	clock := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return clock }
	b.last = clock

	if b.take() != 0 || b.take() != 0 {
		t.Fatal("la rafaga inicial debe permitir 2 envios")
	}
	wait := b.take()
	if wait < 90*time.Millisecond || wait > 110*time.Millisecond {
		t.Fatalf("a 10/s el siguiente token tarda ~100ms, se pidio esperar %v", wait)
	}
	clock = clock.Add(100 * time.Millisecond)
	if b.take() != 0 {
		t.Fatal("pasados 100ms debe haber un token")
	}
	clock = clock.Add(time.Hour)
	b.take()
	b.take()
	if b.take() == 0 {
		t.Fatal("tras una pausa larga la rafaga no supera burst")
	}
}

func TestTokenBucketWaitHonoursContext(t *testing.T) {
	b := NewTokenBucket(0.001, 1)
	if err := b.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := b.Wait(ctx); err == nil {
		t.Fatal("sin tokens, Wait debe terminar con el contexto")
	}
}

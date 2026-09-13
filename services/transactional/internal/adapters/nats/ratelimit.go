package nats

import (
	"context"
	"sync"
	"time"
)

// TokenBucket acota los envios al proveedor a rate por segundo con una rafaga de burst.
// Lo comparten todos los workers del proceso: SES_MAX_SEND_RATE es un tope por cuenta,
// no por goroutine.
type TokenBucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
	now    func() time.Time
}

func NewTokenBucket(ratePerSecond float64, burst int) *TokenBucket {
	if ratePerSecond <= 0 {
		ratePerSecond = 1
	}
	if burst < 1 {
		burst = 1
	}
	now := time.Now()
	return &TokenBucket{rate: ratePerSecond, burst: float64(burst), tokens: float64(burst), last: now, now: time.Now}
}

// Wait bloquea hasta disponer de un token o hasta que el contexto termine.
func (b *TokenBucket) Wait(ctx context.Context) error {
	for {
		wait := b.take()
		if wait <= 0 {
			return nil
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// take consume un token si lo hay; si no, devuelve cuanto falta para el siguiente.
func (b *TokenBucket) take() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * b.rate
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return 0
	}
	return time.Duration((1 - b.tokens) / b.rate * float64(time.Second))
}

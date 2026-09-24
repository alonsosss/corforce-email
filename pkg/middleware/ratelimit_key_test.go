package middleware

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// AllowKey y AllowIP cuentan dentro del handler con el mismo almacen y la misma ventana que
// los middlewares; la clave llega resumida al almacen y una IPv6 cuenta por su /64.
func TestAllowKeyYAllowIP(t *testing.T) {
	clock := newFakeClock()
	store := newFakeStore(clock)
	rl := newSharedRateLimiter(store, "test:claves", 1, time.Hour, zap.NewNop(), clock.Now)
	ctx := context.Background()
	if ok, _ := rl.AllowKey(ctx, "nonce-secreto"); !ok {
		t.Fatal("el primer uso cabe")
	}
	if ok, retry := rl.AllowKey(ctx, "nonce-secreto"); ok || retry <= 0 {
		t.Fatal("el segundo uso de la misma clave no cabe")
	}
	if ok, _ := rl.AllowKey(ctx, "otro"); !ok {
		t.Fatal("otra clave tiene su propio cupo")
	}
	_, keys := store.snapshot()
	for _, k := range keys {
		if strings.Contains(k, "nonce-secreto") {
			t.Fatalf("la clave llega en claro al almacen: %s", k)
		}
	}
	if ok, _ := rl.AllowIP(ctx, "2001:db8::1"); !ok {
		t.Fatal("primera IPv6")
	}
	if ok, _ := rl.AllowIP(ctx, "2001:db8::2"); ok {
		t.Fatal("otra direccion del mismo /64 no tiene cupo propio")
	}
	clock.Advance(time.Hour + time.Second)
	if ok, _ := rl.AllowKey(ctx, "nonce-secreto"); !ok {
		t.Fatal("al cerrarse la ventana la clave vuelve a caber")
	}
}

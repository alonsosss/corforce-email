package formguard

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// memStore es un almacen compartido en memoria: dos Guard sobre el son dos replicas.
type memStore struct {
	mu     sync.Mutex
	counts map[string]int64
}

func (s *memStore) Hit(_ context.Context, key string, window time.Duration) (int64, time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts[key]++
	return s.counts[key], window, nil
}

func newGuards(n int) ([]*Guard, *memStore) {
	store := &memStore{counts: map[string]int64{}}
	cfg := Config{PerIP: 2, IPWindow: time.Minute, PerFormHour: 3, TokenTTL: time.Hour}
	out := make([]*Guard, n)
	for i := range out {
		out[i] = New(store, cfg, zap.NewNop())
	}
	return out, store
}

func TestCuposCompartidosEntreReplicas(t *testing.T) {
	g, store := newGuards(2)
	ctx := context.Background()
	if ok, _ := g[0].AllowIP(ctx, "203.0.113.5"); !ok {
		t.Fatal("primer envio de la IP")
	}
	if ok, _ := g[1].AllowIP(ctx, "203.0.113.5"); !ok {
		t.Fatal("segundo envio de la IP en otra replica")
	}
	if ok, retry := g[0].AllowIP(ctx, "203.0.113.5"); ok || retry <= 0 {
		t.Fatal("el tercero supera el cupo comun")
	}
	if ok, _ := g[1].AllowIP(ctx, "198.51.100.5"); !ok {
		t.Fatal("otra IP tiene su cupo")
	}

	tenant, form := uuid.New(), uuid.New()
	for i := 0; i < 3; i++ {
		if ok, _ := g[i%2].AllowForm(ctx, tenant, form); !ok {
			t.Fatalf("envio %d del formulario", i+1)
		}
	}
	if ok, _ := g[0].AllowForm(ctx, tenant, form); ok {
		t.Fatal("el formulario supera su cupo por hora")
	}
	if ok, _ := g[0].AllowForm(ctx, tenant, uuid.New()); !ok {
		t.Fatal("otro formulario tiene su cupo")
	}

	if !g[0].FirstUse(ctx, "nonce-1") {
		t.Fatal("primer uso del nonce")
	}
	if g[1].FirstUse(ctx, "nonce-1") {
		t.Fatal("el nonce se reutiliza en otra replica")
	}
	for key := range store.counts {
		if strings.Contains(key, "nonce-1") || strings.Contains(key, form.String()) {
			t.Fatalf("una clave llega en claro al almacen: %s", key)
		}
		if !strings.HasPrefix(key, "rl:contacts:form") {
			t.Fatalf("clave fuera del espacio de los formularios: %s", key)
		}
	}
}

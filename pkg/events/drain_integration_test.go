//go:build integration

// Pruebas de DrainSubscriptions contra un NATS real:
//
//	NATS_TEST_URL=nats://... go test -tags integration ./pkg/events/
package events

import (
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

// inFlight suscribe handler y le entrega un mensaje; vuelve cuando el manejador ya lo esta procesando.
func inFlight(t *testing.T, handler func()) *nats.Subscription {
	t.Helper()
	url := os.Getenv("NATS_TEST_URL")
	if url == "" {
		if os.Getenv("INTEGRATION_REQUIRED") == "1" {
			t.Fatal("NATS_TEST_URL no definida con INTEGRATION_REQUIRED=1")
		}
		t.Skip("NATS_TEST_URL no definida")
	}
	bus, err := NewBus(url, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bus.Close)
	subject := "drain-it." + uuid.NewString()
	started := make(chan struct{}, 1)
	sub, err := bus.Subscribe(subject, func(Event) {
		started <- struct{}{}
		handler()
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.Publish(subject, Event{Type: "drain.prueba"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("el manejador no recibio el mensaje")
	}
	return sub
}

func TestDrainSubscriptionsEsperaAlMensajeEnVuelo(t *testing.T) {
	var finished atomic.Bool
	sub := inFlight(t, func() {
		time.Sleep(300 * time.Millisecond)
		finished.Store(true)
	})
	if pending := drainSubscriptions(zap.NewNop(), 2*time.Second, sub); pending != 0 {
		t.Fatalf("pendientes = %d", pending)
	}
	if !finished.Load() {
		t.Fatal("volvio antes de que terminara el manejador en vuelo")
	}
	if sub.IsValid() {
		t.Fatal("la suscripcion sigue activa")
	}
}

func TestDrainSubscriptionsNoEsperaMasDelTope(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	sub := inFlight(t, func() { <-release })
	t0 := time.Now()
	if pending := drainSubscriptions(zap.NewNop(), 200*time.Millisecond, sub, nil); pending != 1 {
		t.Fatalf("pendientes = %d, se esperaba 1", pending)
	}
	if d := time.Since(t0); d > 2*time.Second {
		t.Fatalf("espero %s con un tope de 200ms", d)
	}
}

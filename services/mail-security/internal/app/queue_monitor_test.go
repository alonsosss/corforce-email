package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"go.uber.org/zap"
)

type recordingQueueMetrics struct {
	mu      sync.Mutex
	counts  map[string]int
	oldest  time.Time
	ok, bad int
}

func (r *recordingQueueMetrics) QueueObserved(counts map[string]int, oldest time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts, r.oldest, r.ok = counts, oldest, r.ok+1
}

func (r *recordingQueueMetrics) QueuePollFailed() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bad++
}

func TestElMonitorAnotaElConteoYElMasAntiguoDeLaColaEntera(t *testing.T) {
	engine := apptest.NewQueue()
	engine.Listing = domain.QueueListing{Total: 40, Truncated: true, Counts: map[string]int{"deferred": 38, "hold": 2}, OldestArrival: 1_700_000_000}
	metrics := &recordingQueueMetrics{}
	NewQueueMonitor(engine, metrics, time.Minute, zap.NewNop()).poll(context.Background())

	if engine.ListLimit != 1 {
		t.Fatalf("el monitor pide un solo mensaje, pidio %d", engine.ListLimit)
	}
	if metrics.ok != 1 || metrics.counts["deferred"] != 38 || metrics.counts["hold"] != 2 || metrics.oldest.Unix() != 1_700_000_000 {
		t.Fatalf("anotado: %+v", metrics)
	}
}

func TestUnaColaVaciaAnotaUnMasAntiguoNulo(t *testing.T) {
	engine := apptest.NewQueue()
	engine.Listing = domain.QueueListing{Counts: map[string]int{}}
	metrics := &recordingQueueMetrics{}
	NewQueueMonitor(engine, metrics, time.Minute, zap.NewNop()).poll(context.Background())
	if metrics.ok != 1 || !metrics.oldest.IsZero() {
		t.Fatalf("cola vacia: %+v", metrics)
	}
}

func TestUnaConsultaFallidaSeCuentaYNoSeConfundeConUnaColaVacia(t *testing.T) {
	engine := apptest.NewQueue()
	engine.Err = domain.ErrEngineUnreachable
	metrics := &recordingQueueMetrics{}
	NewQueueMonitor(engine, metrics, time.Minute, zap.NewNop()).poll(context.Background())
	if metrics.bad != 1 || metrics.ok != 0 {
		t.Fatalf("fallo: %+v", metrics)
	}
}

// Con la cola enorme o el agente sobrecargado, consultar cada intervalo suma carga al contenedor de Postfix
// justo cuando peor le viene: tras fallos seguidos el monitor espera mas, y vuelve al ritmo normal al
// primer exito.
func TestElMonitorEspaceLasConsultasTrasFallosSeguidos(t *testing.T) {
	m := NewQueueMonitor(apptest.NewQueue(), &recordingQueueMetrics{}, time.Minute, zap.NewNop())
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 4 * time.Minute, 4 * time.Minute}
	for failures, d := range want {
		if got := m.delay(failures); got != d {
			t.Errorf("tras %d fallos seguidos espera %s, quiero %s", failures, got, d)
		}
	}
}

func TestElMonitorNoRepiteLaConsultaAlRitmoNormalMientrasFalla(t *testing.T) {
	engine := apptest.NewQueue()
	engine.Err = domain.ErrEngineUnreachable
	metrics := &recordingQueueMetrics{}
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	NewQueueMonitor(engine, metrics, 50*time.Millisecond, zap.NewNop()).Run(ctx)
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	if metrics.bad > 4 {
		t.Fatalf("en 350 ms con intervalo de 50 ms y espera creciente hubo %d consultas fallidas, a ritmo fijo serian 7", metrics.bad)
	}
}

func TestElMonitorSeDetieneConElContexto(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		NewQueueMonitor(apptest.NewQueue(), &recordingQueueMetrics{}, 10*time.Millisecond, zap.NewNop()).Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run no termina al cancelar el contexto")
	}
}

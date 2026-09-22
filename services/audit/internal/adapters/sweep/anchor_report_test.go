package sweep

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
)

// Un apagado antes del siguiente multiplo del intervalo no envia nada ni se queda esperando.
func TestElBarridoSeDetieneSinEnviarSiElContextoTerminaAntesDeLaHora(t *testing.T) {
	r := NewAnchorReport(nil, nil, nil, nil, zap.NewNop(), 24*time.Hour)
	r.now = func() time.Time { return time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run no termino al cancelar el contexto")
	}
}

func TestElInformeSeAlineaAlRelojYNoAlArranque(t *testing.T) {
	day := 24 * time.Hour
	casos := []struct {
		now   time.Time
		every time.Duration
		want  time.Time
	}{
		{time.Date(2026, 9, 21, 23, 59, 0, 0, time.UTC), day, time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)},
		{time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC), day, time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)},
		{time.Date(2026, 9, 22, 0, 0, 1, 0, time.UTC), day, time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)},
		{time.Date(2026, 9, 22, 10, 17, 0, 0, time.FixedZone("madrid", 2*3600)), day, time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)},
		{time.Date(2026, 9, 22, 10, 17, 0, 0, time.UTC), time.Hour, time.Date(2026, 9, 22, 11, 0, 0, 0, time.UTC)},
		{time.Date(2026, 9, 22, 10, 17, 0, 0, time.UTC), 6 * time.Hour, time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)},
	}
	for _, c := range casos {
		if got := NextReportAt(c.now, c.every); !got.Equal(c.want) {
			t.Fatalf("desde %s cada %s: %s, esperado %s", c.now, c.every, got, c.want)
		}
	}
	// Dos procesos arrancados a horas distintas del mismo dia apuntan al mismo instante.
	a := NextReportAt(time.Date(2026, 9, 22, 1, 0, 0, 0, time.UTC), day)
	b := NextReportAt(time.Date(2026, 9, 22, 22, 0, 0, 0, time.UTC), day)
	if !a.Equal(b) {
		t.Fatalf("%s != %s", a, b)
	}
}

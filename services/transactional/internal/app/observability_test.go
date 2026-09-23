package app

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
)

// Cada intento contra SES se cuenta con su resultado, tambien los que se reintentan: la alerta
// de throttling mira los intentos, no los mensajes.
func TestCadaIntentoDeEnvioSeCuenta(t *testing.T) {
	fastRetries(t)
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	id := createQueued(t, f, rawCommand(f, "ana@example.com"))
	throttled := &domain.SendError{Kind: domain.ErrorTransient, Code: "TooManyRequestsException"}
	f.sender.errs = []error{throttled, nil}

	if _, err := f.uc.SendQueued(ctx, f.tenant, id); err != nil {
		t.Fatal(err)
	}
	want := []string{"transactional/throttled", "transactional/sent"}
	if len(f.metrics.attempts) != 2 || f.metrics.attempts[0] != want[0] || f.metrics.attempts[1] != want[1] {
		t.Fatalf("intentos contados: %v, se esperaba %v", f.metrics.attempts, want)
	}
}

type fakeAccountReader struct {
	statuses []domain.SESAccountStatus
	errs     []error
	calls    int
}

func (r *fakeAccountReader) AccountStatus(context.Context) (domain.SESAccountStatus, error) {
	i := r.calls
	r.calls++
	if i < len(r.errs) && r.errs[i] != nil {
		return domain.SESAccountStatus{}, r.errs[i]
	}
	if i < len(r.statuses) {
		return r.statuses[i], nil
	}
	return domain.SESAccountStatus{SendingEnabled: true}, nil
}

// El vigilante publica lo valido, cuenta los fallos y descarta un NaN que llegara del
// proveedor en vez de publicarlo.
func TestVigilanteDeLaCuentaDeSES(t *testing.T) {
	f := newFixture(t, Config{})
	nan := math.NaN()
	rate := 0.012
	reader := &fakeAccountReader{
		statuses: []domain.SESAccountStatus{{SendingEnabled: true, Max24HourSend: 200, BounceRate: &rate}, {}, {BounceRate: &nan}},
		errs:     []error{nil, errors.New("AccessDenied"), nil},
	}
	cctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.uc.RunSESAccountMonitor(cctx, reader, 5*time.Millisecond); close(done) }()
	deadline := time.After(2 * time.Second)
	for {
		f.metrics.mu.Lock()
		n := len(f.metrics.accounts) + f.metrics.failures
		f.metrics.mu.Unlock()
		if n >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("el vigilante no leyo tres veces")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done

	f.metrics.mu.Lock()
	defer f.metrics.mu.Unlock()
	if len(f.metrics.accounts) < 1 || f.metrics.accounts[0].Max24HourSend != 200 || *f.metrics.accounts[0].BounceRate != rate {
		t.Fatalf("estado publicado: %+v", f.metrics.accounts)
	}
	for _, s := range f.metrics.accounts {
		if s.BounceRate != nil && math.IsNaN(*s.BounceRate) {
			t.Fatal("un NaN no se publica")
		}
	}
	if f.metrics.failures < 2 {
		t.Fatalf("el permiso denegado y el NaN cuentan como fallo: %d", f.metrics.failures)
	}
}

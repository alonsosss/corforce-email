package mailauth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
)

type countingAuth struct {
	calls   atomic.Int32
	release chan struct{}
	err     error
	who     domain.Principal
}

func (a *countingAuth) Authenticate(ctx context.Context, username, password, _ string) (domain.Principal, error) {
	a.calls.Add(1)
	if a.release != nil {
		select {
		case <-a.release:
		case <-ctx.Done():
			return domain.Principal{}, ctx.Err()
		}
	}
	if a.err != nil {
		return domain.Principal{}, a.err
	}
	if password != "buena" {
		return domain.Principal{}, domain.ErrInvalidCredentials
	}
	return a.who, nil
}

func newGuard(t *testing.T, inner *countingAuth, mutate func(*GuardConfig)) (*Guard, *time.Time) {
	t.Helper()
	cfg := GuardConfig{CacheTTL: 10 * time.Second, MaxCached: 4, MaxConcurrent: 2, Wait: 50 * time.Millisecond}
	if mutate != nil {
		mutate(&cfg)
	}
	g, err := NewGuard(inner, cfg)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	g.now = func() time.Time { return now }
	return g, &now
}

func who() domain.Principal {
	return domain.Principal{TenantID: uuid.New(), MailboxID: uuid.New(), Username: "ana@acme.test"}
}

// Cada peticion de un cliente que sincroniza volvia a pagar un bcrypt en mail-auth: un acierto se recuerda
// unos segundos, y solo el acierto.
func TestUnAciertoSeRecuerdaPeroUnRechazoNunca(t *testing.T) {
	inner := &countingAuth{who: who()}
	g, now := newGuard(t, inner, nil)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		p, err := g.Authenticate(ctx, "Ana@Acme.test", "buena", "203.0.113.1")
		if err != nil || p != inner.who {
			t.Fatalf("intento %d: %+v %v", i, p, err)
		}
	}
	if inner.calls.Load() != 1 {
		t.Fatalf("mail-auth se consulto %d veces para 5 peticiones iguales", inner.calls.Load())
	}
	if _, err := g.Authenticate(ctx, "ana@acme.test", "mala", "203.0.113.1"); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("otra contrasena no hereda el acierto: %v", err)
	}
	for i := 0; i < 3; i++ {
		g.Authenticate(ctx, "ana@acme.test", "mala", "203.0.113.1")
	}
	if inner.calls.Load() != 5 {
		t.Fatalf("un rechazo no se recuerda: %d consultas", inner.calls.Load())
	}
	if _, err := g.Authenticate(ctx, "bea@acme.test", "buena", "203.0.113.1"); err != nil || inner.calls.Load() != 6 {
		t.Fatalf("otro buzon no hereda el acierto de ana: %v %d", err, inner.calls.Load())
	}
	*now = now.Add(11 * time.Second)
	if _, err := g.Authenticate(ctx, "ana@acme.test", "buena", "203.0.113.1"); err != nil || inner.calls.Load() != 7 {
		t.Fatalf("pasado el TTL se vuelve a verificar: %v %d", err, inner.calls.Load())
	}
}

func TestSinTTLNoSeRecuerdaNada(t *testing.T) {
	inner := &countingAuth{who: who()}
	g, _ := newGuard(t, inner, func(c *GuardConfig) { c.CacheTTL = 0 })
	for i := 0; i < 3; i++ {
		if _, err := g.Authenticate(context.Background(), "ana@acme.test", "buena", "203.0.113.1"); err != nil {
			t.Fatal(err)
		}
	}
	if inner.calls.Load() != 3 {
		t.Fatalf("sin cache cada peticion verifica: %d", inner.calls.Load())
	}
}

func TestLaCacheEstaAcotada(t *testing.T) {
	inner := &countingAuth{who: who()}
	g, now := newGuard(t, inner, nil)
	for i := 0; i < 20; i++ {
		g.Authenticate(context.Background(), "ana@acme.test", "buena", "203.0.113.1")
		g.Authenticate(context.Background(), string(rune('a'+i))+"@acme.test", "buena", "203.0.113.1")
	}
	g.mu.Lock()
	n := len(g.cached)
	g.mu.Unlock()
	if n > 4 {
		t.Fatalf("la cache tiene %d entradas, tope 4", n)
	}
	*now = now.Add(time.Minute)
	g.Authenticate(context.Background(), "nuevo@acme.test", "buena", "203.0.113.1")
	g.mu.Lock()
	n = len(g.cached)
	g.mu.Unlock()
	if n != 1 {
		t.Fatalf("lo vencido se purga al llenarse: %d entradas", n)
	}
}

// El DAV no puede dejar sin CPU los inicios de IMAP que atiende el mismo mail-auth: pasado el tope de
// verificaciones simultaneas la peticion espera un turno y, si no llega, responde que no esta disponible.
func TestLasVerificacionesSimultaneasEstanAcotadas(t *testing.T) {
	inner := &countingAuth{who: who(), release: make(chan struct{})}
	g, _ := newGuard(t, inner, func(c *GuardConfig) { c.CacheTTL = 0 })
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.Authenticate(context.Background(), "ana@acme.test", "buena", "203.0.113.1")
		}()
	}
	for inner.calls.Load() < 2 {
		time.Sleep(time.Millisecond)
	}
	_, err := g.Authenticate(context.Background(), "ana@acme.test", "buena", "203.0.113.1")
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("la tercera verificacion debia esperar y rendirse: %v", err)
	}
	if inner.calls.Load() != 2 {
		t.Fatalf("la tercera llego a mail-auth: %d", inner.calls.Load())
	}
	close(inner.release)
	wg.Wait()
	if _, err := g.Authenticate(context.Background(), "ana@acme.test", "buena", "203.0.113.1"); err != nil {
		t.Fatalf("liberados los turnos vuelve a verificar: %v", err)
	}
}

func TestUnFalloDeMailAuthNoSeRecuerdaYSeCancelaLaEspera(t *testing.T) {
	inner := &countingAuth{who: who(), err: domain.ErrUnavailable}
	g, _ := newGuard(t, inner, nil)
	for i := 0; i < 3; i++ {
		if _, err := g.Authenticate(context.Background(), "ana@acme.test", "buena", "203.0.113.1"); !errors.Is(err, domain.ErrUnavailable) {
			t.Fatal(err)
		}
	}
	if inner.calls.Load() != 3 {
		t.Fatalf("un fallo no se recuerda: %d", inner.calls.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	blocked := &countingAuth{who: who(), release: make(chan struct{})}
	g2, _ := newGuard(t, blocked, func(c *GuardConfig) { c.MaxConcurrent = 1; c.Wait = time.Minute })
	go g2.Authenticate(context.Background(), "ana@acme.test", "buena", "203.0.113.1")
	for blocked.calls.Load() < 1 {
		time.Sleep(time.Millisecond)
	}
	if _, err := g2.Authenticate(ctx, "ana@acme.test", "buena", "203.0.113.1"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("una peticion cancelada deja de esperar: %v", err)
	}
	close(blocked.release)
}

func TestNewGuardValidaSuConfiguracion(t *testing.T) {
	inner := &countingAuth{}
	for name, cfg := range map[string]GuardConfig{
		"sin turnos":       {MaxConcurrent: 0, Wait: time.Second},
		"sin espera":       {MaxConcurrent: 1},
		"cache sin tamano": {MaxConcurrent: 1, Wait: time.Second, CacheTTL: time.Second},
		"TTL negativo":     {MaxConcurrent: 1, Wait: time.Second, CacheTTL: -time.Second, MaxCached: 1},
	} {
		if _, err := NewGuard(inner, cfg); err == nil {
			t.Errorf("%s: debia rechazarse", name)
		}
	}
	if _, err := NewGuard(nil, GuardConfig{MaxConcurrent: 1, Wait: time.Second}); err == nil {
		t.Error("sin autenticador")
	}
}

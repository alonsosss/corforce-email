package app

import (
	"context"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-auth/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

func harnessWithClock(mb *domain.Mailbox, now *time.Time) *harness {
	h := &harness{
		repo:     &fakeRepo{mailbox: mb},
		verifier: &fakeVerifier{},
		throttle: &fakeThrottle{},
		metrics:  &fakeMetrics{},
	}
	h.uc = New(Deps{Repo: h.repo, Passwords: h.verifier, Throttle: h.throttle, Metrics: h.metrics, Logger: zap.NewNop(),
		Now: func() time.Time { return *now }})
	return h
}

// mail-dav verifica cada peticion HTTP: un cliente que sincroniza escribiria una fila en sasl_logins (y una
// actualizacion de la contrasena de aplicacion) por peticion, en una tabla que solo crece. Un mismo cliente
// (buzon, servicio, IP y credencial) deja un registro por ventana; los protocolos con sesion (IMAP) siguen
// dejando uno por inicio.
func TestUnClienteDAVNoEscribeUnRegistroPorPeticion(t *testing.T) {
	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	h := harnessWithClock(activeMailbox(), &now)
	appID := uuid.New()
	h.repo.appPasswords = []domain.AppPassword{{ID: appID, Name: "movil", PasswordHash: hashApp}}
	h.repo.appProtocol = domain.ProtocolDAV
	ctx := context.Background()
	dav := func(password, ip string) {
		t.Helper()
		r := request(password, "dav")
		r.RemoteIP = ip
		if got := h.uc.Verify(ctx, r); got != domain.ResultOK {
			t.Fatalf("dav %s: %s", password, got)
		}
	}
	for i := 0; i < 20; i++ {
		dav("principal", "203.0.113.7")
	}
	if len(h.repo.logins) != 1 {
		t.Fatalf("20 peticiones DAV del mismo cliente dejaron %d registros", len(h.repo.logins))
	}
	for i := 0; i < 20; i++ {
		dav("movil", "203.0.113.7")
	}
	if len(h.repo.logins) != 2 || len(h.repo.touched) != 1 {
		t.Fatalf("la contrasena de aplicacion es otro cliente: %d registros, %d usos anotados", len(h.repo.logins), len(h.repo.touched))
	}
	dav("principal", "198.51.100.9")
	if len(h.repo.logins) != 3 {
		t.Fatalf("otra IP es otro cliente: %d registros", len(h.repo.logins))
	}
	now = now.Add(loginDedupeWindow + time.Second)
	dav("principal", "203.0.113.7")
	if len(h.repo.logins) != 4 {
		t.Fatalf("pasada la ventana se vuelve a registrar: %d registros", len(h.repo.logins))
	}
	for i := 0; i < 3; i++ {
		if got := h.uc.Verify(ctx, request("principal", "imap")); got != domain.ResultOK {
			t.Fatal(got)
		}
	}
	if len(h.repo.logins) != 7 {
		t.Fatalf("cada inicio de IMAP se registra: %d registros", len(h.repo.logins))
	}
}

func TestLaMemoriaDeRegistrosRecientesEstaAcotada(t *testing.T) {
	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	d := newLoginDedupe(4, time.Minute)
	for i := 0; i < 20; i++ {
		d.firstInWindow(string(rune('a'+i)), now)
	}
	if n := d.size(); n > 4 {
		t.Fatalf("la memoria crecio a %d entradas, tope 4", n)
	}
	now = now.Add(2 * time.Minute)
	if !d.firstInWindow("nuevo", now) || d.size() != 1 {
		t.Fatalf("lo vencido se purga al llenarse: %d entradas", d.size())
	}
	// Llena y sin nada vencido, se registra (no se deduplica) en vez de perder un inicio.
	full := newLoginDedupe(2, time.Hour)
	full.firstInWindow("a", now)
	full.firstInWindow("b", now)
	if !full.firstInWindow("c", now) || !full.firstInWindow("c", now) {
		t.Fatal("con la memoria llena un inicio no se puede dar por repetido")
	}
}

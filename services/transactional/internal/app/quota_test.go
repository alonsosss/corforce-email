package app

import (
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
)

func account(sent float64) domain.SESAccountStatus {
	return domain.SESAccountStatus{SendingEnabled: true, ProductionAccess: true, Max24HourSend: 1000, SentLast24Hours: sent, MaxSendRate: 14}
}

func TestMarketingSeAplazaEnLaReservaYElTransaccionalSigue(t *testing.T) {
	f := newMarketingFixture(t)
	res, err := f.uc.CreateMarketingBatch(ctx, batchCommand(f, "ana@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	f.uc.quota.observe(account(800), f.now, 15*time.Minute)

	out, err := f.uc.SendQueued(ctx, f.tenant, res.MessageIDs[0])
	if err != nil || !out.Ack || out.Status != domain.StatusAccepted {
		t.Fatalf("en la reserva el marketing se aplaza y se confirma en la cola: %+v, %v", out, err)
	}
	m := f.repo.messages[res.MessageIDs[0]]
	if f.mSender.calls != 0 || m.Status != domain.StatusAccepted || m.ScheduledAt == nil ||
		!m.ScheduledAt.Equal(f.now.Add(marketingQuotaDeferral)) || m.Attempts != 0 || f.metrics.deferred != 1 {
		t.Fatalf("aplazado sin enviar ni gastar intentos: llamadas=%d mensaje=%+v aplazados=%d", f.mSender.calls, m, f.metrics.deferred)
	}

	id := createQueued(t, f, rawCommand(f, "luis@example.com"))
	if out, err := f.uc.SendQueued(ctx, f.tenant, id); err != nil || out.Status != domain.StatusSent {
		t.Fatalf("el transaccional usa la reserva: %+v, %v", out, err)
	}

	f.now = f.now.Add(marketingQuotaDeferral)
	f.uc.quota.observe(account(100), f.now, 15*time.Minute)
	if n, err := f.uc.ReleaseDue(ctx, f.tenant); err != nil || n != 1 {
		t.Fatalf("el aplazado vuelve a la cola a su hora: %d, %v", n, err)
	}
	if out, err := f.uc.SendQueued(ctx, f.tenant, res.MessageIDs[0]); err != nil || out.Status != domain.StatusSent || f.mSender.calls != 1 {
		t.Fatalf("fuera de la reserva el marketing sale: %+v, %v", out, err)
	}
}

func TestReservaCuentaLoEnviadoDesdeLaLectura(t *testing.T) {
	f := newMarketingFixture(t)
	f.uc.quota.observe(account(799), f.now, 15*time.Minute)
	if !f.uc.marketingQuotaOpen() {
		t.Fatal("por debajo de la reserva el marketing sale")
	}
	f.uc.quota.noteSent()
	if f.uc.marketingQuotaOpen() {
		t.Fatal("lo enviado desde la lectura cuenta para la reserva")
	}
}

func TestReservaNoFrenaSinLecturaOConLecturaVieja(t *testing.T) {
	f := newMarketingFixture(t)
	if !f.uc.marketingQuotaOpen() {
		t.Fatal("sin lectura de la cuenta no se frena")
	}
	f.uc.quota.observe(account(1000), f.now, 15*time.Minute)
	if f.uc.marketingQuotaOpen() {
		t.Fatal("con la cuota agotada el marketing se frena")
	}
	f.now = f.now.Add(16 * time.Minute)
	if !f.uc.marketingQuotaOpen() {
		t.Fatal("con el vigilante sin datos recientes no se frena: SES aplica su cuota")
	}
}

func TestMarketingQuotaOpen(t *testing.T) {
	cases := []struct {
		status domain.SESAccountStatus
		since  int64
		want   bool
	}{
		{account(0), 0, true},
		{account(799), 0, true},
		{account(800), 0, false},
		{account(700), 100, false},
		{domain.SESAccountStatus{Max24HourSend: 0, SentLast24Hours: 5000}, 0, true},
	}
	for i, c := range cases {
		if got := c.status.MarketingQuotaOpen(c.since, 0.2); got != c.want {
			t.Errorf("caso %d: %v, se esperaba %v", i, got, c.want)
		}
	}
}

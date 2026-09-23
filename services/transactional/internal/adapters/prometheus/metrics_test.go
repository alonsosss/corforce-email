package prometheus

import (
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMetricasDeTransactional(t *testing.T) {
	m := New()
	m.now = func() time.Time { return time.Unix(1790000000, 0) }

	m.SendAttempt("", domain.SendResultSent)
	m.SendAttempt("marketing", domain.SendResultThrottled)
	if v := testutil.ToFloat64(m.sendAttempts.WithLabelValues("transactional", "sent")); v != 1 {
		t.Fatalf("una clase vacia cuenta como transaccional: %v", v)
	}
	m.SESEvent(domain.EventBounce)
	m.SESEventRejected(domain.EventRejectSignature)
	if testutil.ToFloat64(m.sesEvents.WithLabelValues("bounce")) != 1 || testutil.ToFloat64(m.sesRejected.WithLabelValues("signature")) != 1 {
		t.Fatal("eventos y rechazos")
	}

	rate := 0.03
	m.SESAccount(domain.SESAccountStatus{SendingEnabled: true, Max24HourSend: 200, SentLast24Hours: 50, MaxSendRate: 1, BounceRate: &rate})
	if testutil.ToFloat64(m.sendingEnabled) != 1 || testutil.ToFloat64(m.productionAccess) != 0 || testutil.ToFloat64(m.sent24h) != 50 {
		t.Fatal("estado de la cuenta")
	}
	if testutil.ToFloat64(m.bounceRate) != 0.03 || testutil.ToFloat64(m.reputationKnown.WithLabelValues("bounce")) != 1 {
		t.Fatal("tasa de rebotes con dato")
	}
	if testutil.ToFloat64(m.reputationKnown.WithLabelValues("complaint")) != 0 {
		t.Fatal("sin dato de quejas se marca como desconocida")
	}
	if testutil.ToFloat64(m.lastSuccess) != 1790000000 {
		t.Fatal("marca de la ultima lectura")
	}
	m.SESAccountCheckFailed()
	if testutil.ToFloat64(m.checkFailures) != 1 {
		t.Fatal("fallos de lectura")
	}
}

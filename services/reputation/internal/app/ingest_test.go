package app

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/google/uuid"
)

func bounce(tenant uuid.UUID, class domain.Class, id, bounceType string) DeliveryEvent {
	return DeliveryEvent{EventID: id, TenantID: tenant, Kind: KindBounced, Class: class, BounceType: bounceType}
}

func TestRecordDeliveryEsIdempotente(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	ev := DeliveryEvent{EventID: "evt-1", TenantID: tenant, Kind: KindSent, Class: domain.ClassMarketing, Recipients: 3}

	res, err := h.uc.RecordDelivery(ctx, ev)
	if err != nil || res.Duplicate || res.Ignored {
		t.Fatalf("primera entrega: %+v %v", res, err)
	}
	res, err = h.uc.RecordDelivery(ctx, ev)
	if err != nil || !res.Duplicate {
		t.Fatalf("reentrega: %+v %v", res, err)
	}
	if got := h.stats.day(tenant, domain.ClassMarketing, h.today()); got.Sent != 3 {
		t.Fatalf("la reentrega no debe sumar: %+v", got)
	}
}

func TestRecordDeliveryRebotesRestringenMarketing(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	if err := h.stats.AddDaily(ctx, tenant, domain.ClassMarketing, h.today(), domain.Counts{Sent: 1000}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 40; i++ {
		if _, err := h.uc.RecordDelivery(ctx, bounce(tenant, domain.ClassMarketing, fmt.Sprintf("b-%d", i), BounceTypePermanent)); err != nil {
			t.Fatal(err)
		}
		if i == 19 && h.states.get(tenant, domain.ClassMarketing).State != domain.StateOK {
			t.Fatal("con 1,9 % de rebotes la clase sigue en ok")
		}
		if i == 20 && h.states.get(tenant, domain.ClassMarketing).State != domain.StateWarning {
			t.Fatal("con 2 % de rebotes la clase pasa a warning")
		}
	}
	rec := h.states.get(tenant, domain.ClassMarketing)
	if rec.State != domain.StateRestricted || rec.Reason != domain.ReasonBounceBlock || rec.Manual {
		t.Fatalf("con 4 %% de rebotes la clase queda restringida: %+v", rec)
	}
	if !rec.BounceRate.Equal(dec("0.04")) {
		t.Fatalf("tasa guardada: %s", rec.BounceRate)
	}
	if len(h.states.history) != 2 || len(h.events.changes) != 2 {
		t.Fatalf("dos transiciones (ok->warning->restricted): historial=%d eventos=%d", len(h.states.history), len(h.events.changes))
	}
	last := h.events.changes[1]
	if last.From != domain.StateWarning || last.To != domain.StateRestricted || last.Manual || last.ChangedBy != nil {
		t.Fatalf("evento: %+v", last)
	}
	if h.metrics.changes["marketing/warning"] != 1 || h.metrics.changes["marketing/restricted"] != 1 {
		t.Fatalf("metricas: %+v", h.metrics.changes)
	}
	if st := h.states.get(tenant, domain.ClassTransactional); st.State != "" {
		t.Fatalf("la otra clase no se toca: %+v", st)
	}

	dec, err := h.uc.Authorize(ctx, AuthorizeInput{TenantID: tenant, Class: domain.ClassMarketing, Count: 1})
	if err != nil || dec.Allowed || dec.Reason != domain.ReasonReputationRestricted {
		t.Fatalf("una clase restringida no envia: %+v %v", dec, err)
	}
	dec, err = h.uc.Authorize(ctx, AuthorizeInput{TenantID: tenant, Class: domain.ClassTransactional, Count: 1})
	if err != nil || !dec.Allowed {
		t.Fatalf("la transaccional de la misma empresa sigue enviando: %+v %v", dec, err)
	}
}

func TestRecordDeliveryTransaccionalSoloAvisaEntreUnaYDosVeces(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	if err := h.stats.AddDaily(ctx, tenant, domain.ClassTransactional, h.today(), domain.Counts{Sent: 1000, Bounced: 49}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.uc.RecordDelivery(ctx, bounce(tenant, domain.ClassTransactional, "b-50", BounceTypePermanent)); err != nil {
		t.Fatal(err)
	}
	if st := h.states.get(tenant, domain.ClassTransactional).State; st != domain.StateWarning {
		t.Fatalf("5 %% de rebotes en transaccional es warning, no restriccion: %s", st)
	}
}

func TestRecordDeliveryReentregaTerminaLaReevaluacion(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	if err := h.stats.AddDaily(ctx, tenant, domain.ClassMarketing, h.today(), domain.Counts{Sent: 1000, Bounced: 39}); err != nil {
		t.Fatal(err)
	}
	h.states.failForUpdate = 1
	if _, err := h.uc.RecordDelivery(ctx, bounce(tenant, domain.ClassMarketing, "b-40", BounceTypePermanent)); !errors.Is(err, errBoom) {
		t.Fatalf("la reevaluacion fallida debe devolver error para que no se acke: %v", err)
	}
	res, err := h.uc.RecordDelivery(ctx, bounce(tenant, domain.ClassMarketing, "b-40", BounceTypePermanent))
	if err != nil || !res.Duplicate {
		t.Fatalf("reentrega: %+v %v", res, err)
	}
	if got := h.stats.day(tenant, domain.ClassMarketing, h.today()); got.Bounced != 40 {
		t.Fatalf("el rebote se cuenta una sola vez: %+v", got)
	}
	if st := h.states.get(tenant, domain.ClassMarketing).State; st != domain.StateRestricted {
		t.Fatalf("la reentrega completa la reevaluacion: %s", st)
	}
}

func TestRecordDeliveryReboteTransitorioNoCuenta(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	res, err := h.uc.RecordDelivery(ctx, bounce(tenant, domain.ClassMarketing, "t-1", "transient"))
	if err != nil || !res.Ignored {
		t.Fatalf("un rebote transitorio se ignora: %+v %v", res, err)
	}
	res, err = h.uc.RecordDelivery(ctx, bounce(tenant, domain.ClassMarketing, "t-2", ""))
	if err != nil || !res.Ignored {
		t.Fatalf("un rebote sin tipo se ignora: %+v %v", res, err)
	}
	if len(h.stats.processed) != 0 || len(h.states.records) != 0 {
		t.Fatal("un hecho ignorado no toca la base")
	}
}

func TestRecordDeliveryQuejaCuenta(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	ev := DeliveryEvent{EventID: "c-1", TenantID: tenant, Kind: KindComplained, Class: domain.ClassMarketing}
	if _, err := h.uc.RecordDelivery(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if got := h.stats.day(tenant, domain.ClassMarketing, h.today()); got.Complained != 1 {
		t.Fatalf("queja: %+v", got)
	}
	if rec := h.states.get(tenant, domain.ClassMarketing); rec.State != domain.StateOK || rec.Reason != domain.ReasonInitial {
		t.Fatalf("una queja con poco volumen deja la clase en ok: %+v", rec)
	}
}

func TestRecordDeliveryDestinatariosYFecha(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	yesterday := h.now.Add(-24 * time.Hour)
	events := []DeliveryEvent{
		{EventID: "s-1", TenantID: tenant, Kind: KindSent, Class: domain.ClassTransactional, Recipients: 0},
		{EventID: "s-2", TenantID: tenant, Kind: KindSent, Class: domain.ClassTransactional, Recipients: 4, OccurredAt: yesterday},
		{EventID: "s-3", TenantID: tenant, Kind: KindSent, Class: domain.ClassTransactional, Recipients: 2, OccurredAt: h.now.Add(48 * time.Hour)},
	}
	for _, ev := range events {
		if _, err := h.uc.RecordDelivery(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.stats.day(tenant, domain.ClassTransactional, h.today()); got.Sent != 3 {
		t.Fatalf("hoy: sin destinatarios cuenta 1 y la fecha futura cae hoy: %+v", got)
	}
	if got := h.stats.day(tenant, domain.ClassTransactional, yesterday); got.Sent != 4 {
		t.Fatalf("ayer: %+v", got)
	}
}

func TestRecordDeliveryEventosInvalidos(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	cases := map[string]DeliveryEvent{
		"sin id":            {TenantID: tenant, Kind: KindSent, Class: domain.ClassMarketing},
		"id enorme":         {EventID: string(make([]byte, maxEventIDLength+1)), TenantID: tenant, Kind: KindSent, Class: domain.ClassMarketing},
		"sin empresa":       {EventID: "x", Kind: KindSent, Class: domain.ClassMarketing},
		"clase invalida":    {EventID: "x", TenantID: tenant, Kind: KindSent, Class: "newsletter"},
		"hecho desconocido": {EventID: "x", TenantID: tenant, Kind: "opened", Class: domain.ClassMarketing},
	}
	for name, ev := range cases {
		if _, err := h.uc.RecordDelivery(ctx, ev); !IsInputError(err) {
			t.Errorf("%s: se esperaba un error de entrada, hubo %v", name, err)
		}
	}
	if IsInputError(errBoom) {
		t.Fatal("un fallo de infraestructura no es de entrada")
	}
}

func TestKindForSubject(t *testing.T) {
	for subject, want := range map[string]DeliveryKind{
		SubjectEmailSent: KindSent, SubjectEmailBounced: KindBounced, SubjectEmailComplained: KindComplained,
	} {
		if got, ok := KindForSubject(subject); !ok || got != want {
			t.Errorf("%s: %s %v", subject, got, ok)
		}
	}
	if _, ok := KindForSubject("transactional.email.opened"); ok {
		t.Fatal("subject ajeno")
	}
}

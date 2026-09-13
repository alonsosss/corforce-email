package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/billing/internal/domain"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

var (
	ctx   = context.Background()
	start = time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC)
)

func date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

// draftPlan: buzones segun el caso, 5 dominios duros, 1000 mensajes blandos, resto ilimitado.
func draftPlan(code string, mailboxes int64, hard bool) domain.Plan {
	var limits []domain.PlanLimit
	for _, r := range domain.Resources() {
		l := domain.PlanLimit{Resource: r, Included: domain.Unlimited, HardLimit: true}
		switch r {
		case domain.ResourceMailboxes:
			l.Included, l.HardLimit = mailboxes, hard
		case domain.ResourceDomains:
			l.Included = 5
		case domain.ResourceTransactionalMessages:
			l.Included, l.HardLimit = 1000, false
		}
		limits = append(limits, l)
	}
	return domain.Plan{
		Code: code, Name: "Plan " + code, Currency: "USD",
		BasePrice: decimal.RequireFromString("49.00"), BillingPeriod: domain.PeriodMonthly, Limits: limits,
	}
}

func mustPlan(t *testing.T, uc *UseCase, draft domain.Plan) *domain.Plan {
	t.Helper()
	p, err := uc.CreatePlan(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func subscribe(t *testing.T, uc *UseCase, tenant uuid.UUID, code string) *domain.Subscription {
	t.Helper()
	s, _, err := uc.PutSubscription(ctx, tenant, PutSubscriptionInput{PlanCode: code})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func change(id, subject string, tenant uuid.UUID, r domain.Resource, delta int64) domain.UsageChange {
	return domain.UsageChange{EventID: id, Subject: subject, TenantID: tenant, Resource: r, Delta: delta}
}

func TestConsumoIdempotentePorIdDeEvento(t *testing.T) {
	uc, db, _ := newTestUseCase(start, Config{})
	mustPlan(t, uc, draftPlan("business", 10, true))
	tenant := uuid.New()
	subscribe(t, uc, tenant, "business")

	ev := change("evt-1", "mail.mailbox.created", tenant, domain.ResourceMailboxes, 1)
	first, err := uc.RecordUsage(ctx, ev)
	if err != nil || first.Duplicate || first.Quantity != 1 {
		t.Fatalf("primera entrega: %+v %v", first, err)
	}
	again, err := uc.RecordUsage(ctx, ev)
	if err != nil || !again.Duplicate {
		t.Fatalf("la reentrega debe detectarse: %+v %v", again, err)
	}
	if q := db.quantity(tenant, domain.ResourceMailboxes, domain.StockPeriodStart); q != 1 {
		t.Fatalf("el mismo evento conto %d veces", q)
	}
}

func TestStockNoBajaDeCero(t *testing.T) {
	uc, db, _ := newTestUseCase(start, Config{})
	tenant := uuid.New()
	// Baja de un buzon que existia antes de billing, y sin suscripcion: el stock se cuenta igual.
	if _, err := uc.RecordUsage(ctx, change("d-1", "mail.mailbox.deleted", tenant, domain.ResourceMailboxes, -1)); err != nil {
		t.Fatal(err)
	}
	if q := db.quantity(tenant, domain.ResourceMailboxes, domain.StockPeriodStart); q != 0 {
		t.Fatalf("stock negativo: %d", q)
	}
	if _, err := uc.RecordUsage(ctx, change("c-1", "mail.mailbox.created", tenant, domain.ResourceMailboxes, 1)); err != nil {
		t.Fatal(err)
	}
	if q := db.quantity(tenant, domain.ResourceMailboxes, domain.StockPeriodStart); q != 1 {
		t.Fatalf("tras el alta: %d", q)
	}
}

func TestDominioInformadoPorDosFuentesCuentaUnaVez(t *testing.T) {
	uc, db, _ := newTestUseCase(start, Config{})
	tenant := uuid.New()
	domainEvent := func(id, subject, source string, delta int64) {
		t.Helper()
		ch := change(id, subject, tenant, domain.ResourceDomains, delta)
		ch.ItemKey, ch.Source = "empresa.test", source
		if _, err := uc.RecordUsage(ctx, ch); err != nil {
			t.Fatal(err)
		}
	}
	want := func(n int64) {
		t.Helper()
		if q := db.quantity(tenant, domain.ResourceDomains, domain.StockPeriodStart); q != n {
			t.Fatalf("dominios = %d; se esperaba %d", q, n)
		}
	}
	domainEvent("1", "domains.domain.created", "domain-service", 1)
	want(1)
	domainEvent("2", "mail.domain.created", "mail-directory", 1)
	want(1)
	domainEvent("3", "mail.domain.deleted", "mail-directory", -1)
	want(1)
	domainEvent("4", "domains.domain.deleted", "domain-service", -1)
	want(0)
}

func TestAvisoDeLimiteDuroUnaVezPorPeriodo(t *testing.T) {
	uc, db, _ := newTestUseCase(start, Config{})
	mustPlan(t, uc, draftPlan("small", 2, true))
	tenant := uuid.New()
	subscribe(t, uc, tenant, "small")

	steps := []struct {
		id    string
		delta int64
	}{{"a", 1}, {"b", 1}, {"c", -1}, {"d", 1}}
	for _, s := range steps {
		subject := "mail.mailbox.created"
		if s.delta < 0 {
			subject = "mail.mailbox.deleted"
		}
		if _, err := uc.RecordUsage(ctx, change(s.id, subject, tenant, domain.ResourceMailboxes, s.delta)); err != nil {
			t.Fatal(err)
		}
	}
	reached := db.eventsOf("billing.limit.reached")
	if len(reached) != 1 || !reached[0].start.Equal(date(2026, 1, 31)) {
		t.Fatalf("billing.limit.reached debe salir una vez en el periodo: %+v", reached)
	}
}

func TestFlujoSinSuscripcionNoSeCuentaYConSuscripcionVaAlPeriodoVigente(t *testing.T) {
	uc, db, clock := newTestUseCase(start, Config{})
	tenant := uuid.New()
	res, err := uc.RecordUsage(ctx, change("s-1", "transactional.email.sent", tenant, domain.ResourceTransactionalMessages, 1))
	if err != nil || !res.Skipped {
		t.Fatalf("sin suscripcion el flujo no tiene periodo: %+v %v", res, err)
	}

	mustPlan(t, uc, draftPlan("business", 10, true))
	subscribe(t, uc, tenant, "business") // 31 ene - 28 feb
	*clock = time.Date(2026, 3, 5, 9, 0, 0, 0, time.UTC)
	if _, err := uc.RecordUsage(ctx, change("s-2", "transactional.email.sent", tenant, domain.ResourceTransactionalMessages, 1)); err != nil {
		t.Fatal(err)
	}
	if q := db.quantity(tenant, domain.ResourceTransactionalMessages, date(2026, 2, 28)); q != 1 {
		t.Fatalf("el envio del 5 de marzo pertenece al periodo del 28 de febrero aunque el barrido no corrio: %d", q)
	}
}

func TestEmpresaNuevaRecibeElPlanPorDefecto(t *testing.T) {
	uc, db, _ := newTestUseCase(start, Config{DefaultPlanCode: "starter", TrialDays: 14})
	mustPlan(t, uc, draftPlan("starter", 3, true))
	tenant := uuid.New()

	for i := 0; i < 2; i++ {
		if err := uc.OnTenantCreated(ctx, "org-1", "organization.tenant.created", tenant); err != nil {
			t.Fatal(err)
		}
	}
	sub := db.subs[tenant]
	if sub == nil || sub.Status != domain.StatusTrialing || sub.TrialEndsAt == nil ||
		!sub.TrialEndsAt.Equal(start.Add(14*24*time.Hour)) {
		t.Fatalf("suscripcion en prueba de 14 dias: %+v", sub)
	}
	if n := len(db.eventsOf("billing.subscription.created")); n != 1 {
		t.Fatalf("billing.subscription.created una sola vez, hubo %d", n)
	}
}

func TestSinPlanPorDefectoUtilizableNoSeInventaNinguno(t *testing.T) {
	cases := map[string]func(t *testing.T, uc *UseCase){
		"":        func(*testing.T, *UseCase) {},
		"missing": func(*testing.T, *UseCase) {},
		"retired": func(t *testing.T, uc *UseCase) {
			p := mustPlan(t, uc, draftPlan("retired", 3, true))
			if _, err := uc.RetirePlan(ctx, p.ID); err != nil {
				t.Fatal(err)
			}
		},
	}
	for code, setup := range cases {
		uc, db, _ := newTestUseCase(start, Config{DefaultPlanCode: code})
		setup(t, uc)
		tenant := uuid.New()
		if err := uc.OnTenantCreated(ctx, "org-"+code, "organization.tenant.created", tenant); err != nil {
			t.Fatalf("%q: %v", code, err)
		}
		if db.subs[tenant] != nil {
			t.Fatalf("plan por defecto %q: no debe crearse suscripcion", code)
		}
	}
}

func TestSuspensionDeLaEmpresaSuspendeLaSuscripcion(t *testing.T) {
	uc, db, _ := newTestUseCase(start, Config{})
	mustPlan(t, uc, draftPlan("business", 10, true))
	tenant := uuid.New()
	subscribe(t, uc, tenant, "business")

	if err := uc.OnTenantStatusChanged(ctx, "st-1", "organization.tenant.status_changed", tenant, "active"); err != nil {
		t.Fatal(err)
	}
	if db.subs[tenant].Status != domain.StatusActive {
		t.Fatal("un cambio a active no toca la suscripcion")
	}
	if err := uc.OnTenantStatusChanged(ctx, "st-2", "organization.tenant.status_changed", tenant, TenantStatusSuspended); err != nil {
		t.Fatal(err)
	}
	if db.subs[tenant].Status != domain.StatusSuspended || len(db.eventsOf("billing.subscription.suspended")) != 1 {
		t.Fatalf("suspension: %s", db.subs[tenant].Status)
	}
	ent, err := uc.CheckEntitlement(ctx, tenant, domain.ResourceMailboxes, 1)
	if err != nil || ent.Allowed || ent.Reason != domain.ReasonSubscriptionInactive {
		t.Fatalf("una empresa suspendida no consume: %+v %v", ent, err)
	}
}

func TestDerechosContraLosContadores(t *testing.T) {
	uc, db, _ := newTestUseCase(start, Config{})
	mustPlan(t, uc, draftPlan("small", 2, true))
	tenant := uuid.New()
	subscribe(t, uc, tenant, "small")
	for _, id := range []string{"m1", "m2"} {
		if _, err := uc.RecordUsage(ctx, change(id, "mail.mailbox.created", tenant, domain.ResourceMailboxes, 1)); err != nil {
			t.Fatal(err)
		}
	}
	ent, err := uc.CheckEntitlement(ctx, tenant, domain.ResourceMailboxes, 1)
	if err != nil || ent.Allowed || ent.Reason != domain.ReasonLimitReached || ent.Used != 2 || *ent.Remaining != 0 {
		t.Fatalf("en el tope se deniega: %+v %v", ent, err)
	}
	if q := db.quantity(tenant, domain.ResourceMailboxes, domain.StockPeriodStart); q != 2 {
		t.Fatalf("la consulta no debe incrementar: %d", q)
	}
	ent, err = uc.CheckEntitlement(ctx, tenant, domain.ResourceTransactionalMessages, 5000)
	if err != nil || !ent.Allowed {
		t.Fatalf("un limite blando permite el excedente: %+v %v", ent, err)
	}
	if _, err := uc.CheckEntitlement(ctx, tenant, domain.ResourceMailboxes, 0); !errors.Is(err, domain.ErrInvalidQuantity) {
		t.Fatalf("cantidad cero: %v", err)
	}

	loose, _, _ := newTestUseCase(start, Config{Enforce: false})
	if ent, _ := loose.CheckEntitlement(ctx, uuid.New(), domain.ResourceDomains, 1); !ent.Allowed {
		t.Fatalf("sin enforce se permite: %+v", ent)
	}
	strict, _, _ := newTestUseCase(start, Config{Enforce: true})
	if ent, _ := strict.CheckEntitlement(ctx, uuid.New(), domain.ResourceDomains, 1); ent.Allowed || ent.Reason != domain.ReasonNoSubscription {
		t.Fatalf("con enforce se deniega: %+v", ent)
	}
}

func TestCambioDePlanNoReiniciaElPeriodo(t *testing.T) {
	uc, db, clock := newTestUseCase(start, Config{})
	mustPlan(t, uc, draftPlan("small", 2, true))
	mustPlan(t, uc, draftPlan("large", 50, true))
	legacy := mustPlan(t, uc, draftPlan("legacy", 1, true))
	if _, err := uc.RetirePlan(ctx, legacy.ID); err != nil {
		t.Fatal(err)
	}
	tenant := uuid.New()

	sub, created, err := uc.PutSubscription(ctx, tenant, PutSubscriptionInput{PlanCode: "small"})
	if err != nil || !created || len(db.eventsOf("billing.subscription.created")) != 1 {
		t.Fatalf("alta por la plataforma: %v %v", created, err)
	}
	*clock = start.Add(10 * 24 * time.Hour)
	changed, created, err := uc.PutSubscription(ctx, tenant, PutSubscriptionInput{PlanCode: "large"})
	if err != nil || created || changed.PlanCode != "large" ||
		!changed.CurrentPeriodStart.Equal(sub.CurrentPeriodStart) || !changed.CurrentPeriodEnd.Equal(sub.CurrentPeriodEnd) {
		t.Fatalf("cambio de plan: %+v %v", changed, err)
	}
	if len(db.eventsOf("billing.subscription.changed")) != 1 {
		t.Fatal("el cambio de plan publica billing.subscription.changed")
	}
	if _, _, err := uc.PutSubscription(ctx, tenant, PutSubscriptionInput{PlanCode: "large"}); err != nil || len(db.eventsOf("billing.subscription.changed")) != 1 {
		t.Fatalf("repetir el mismo PUT no cambia nada ni publica: %v", err)
	}
	if _, _, err := uc.PutSubscription(ctx, tenant, PutSubscriptionInput{PlanCode: "legacy"}); !errors.Is(err, domain.ErrPlanRetired) {
		t.Fatalf("plan retirado: %v", err)
	}
	if _, _, err := uc.PutSubscription(ctx, tenant, PutSubscriptionInput{PlanCode: "nope"}); !errors.Is(err, domain.ErrUnknownPlanCode) {
		t.Fatalf("plan inexistente: %v", err)
	}
	suspended := domain.StatusSuspended
	if _, _, err := uc.PutSubscription(ctx, tenant, PutSubscriptionInput{PlanCode: "large", Update: domain.SubscriptionUpdate{Status: &suspended}}); err != nil ||
		len(db.eventsOf("billing.subscription.suspended")) != 1 {
		t.Fatalf("suspender por la plataforma: %v", err)
	}
}

func TestPlanConSuscripcionesNoCambiaSusCondiciones(t *testing.T) {
	uc, _, _ := newTestUseCase(start, Config{})
	p := mustPlan(t, uc, draftPlan("business", 10, true))

	price := decimal.RequireFromString("59.00")
	if got, err := uc.UpdatePlan(ctx, p.ID, domain.PlanPatch{BasePrice: &price}); err != nil || !got.BasePrice.Equal(price) {
		t.Fatalf("sin suscripciones el precio se edita: %v", err)
	}
	subscribe(t, uc, uuid.New(), "business")
	price = decimal.RequireFromString("69.00")
	if _, err := uc.UpdatePlan(ctx, p.ID, domain.PlanPatch{BasePrice: &price}); !errors.Is(err, domain.ErrPlanInUse) {
		t.Fatalf("con suscripciones: %v", err)
	}
	name := "Business 2026"
	if got, err := uc.UpdatePlan(ctx, p.ID, domain.PlanPatch{Name: &name}); err != nil || got.Name != name {
		t.Fatalf("el nombre se edita siempre: %v", err)
	}
	if _, err := uc.CreatePlan(ctx, draftPlan("business", 1, true)); !errors.Is(err, domain.ErrPlanCodeTaken) {
		t.Fatalf("codigo repetido: %v", err)
	}
}

func TestCierreDePeriodosYCicloDeVida(t *testing.T) {
	uc, db, clock := newTestUseCase(start, Config{})
	mustPlan(t, uc, draftPlan("business", 10, true))
	tenant := uuid.New()
	subscribe(t, uc, tenant, "business") // 31 ene - 28 feb, ancla 31
	if _, err := uc.RecordUsage(ctx, change("mb", "mail.mailbox.created", tenant, domain.ResourceMailboxes, 1)); err != nil {
		t.Fatal(err)
	}
	*clock = date(2026, 2, 10)
	if _, err := uc.RecordUsage(ctx, change("e1", "transactional.email.sent", tenant, domain.ResourceTransactionalMessages, 1)); err != nil {
		t.Fatal(err)
	}
	*clock = date(2026, 3, 5)
	for _, id := range []string{"e2", "e3"} {
		if _, err := uc.RecordUsage(ctx, change(id, "transactional.email.sent", tenant, domain.ResourceTransactionalMessages, 1)); err != nil {
			t.Fatal(err)
		}
	}

	trialTenant := uuid.New()
	trialEnd := date(2026, 3, 20)
	trialing := domain.StatusTrialing
	*clock = date(2026, 3, 10)
	if _, _, err := uc.PutSubscription(ctx, trialTenant, PutSubscriptionInput{PlanCode: "business",
		Update: domain.SubscriptionUpdate{Status: &trialing, TrialEndsAt: domain.OptionalTime{Set: true, Value: &trialEnd}}}); err != nil {
		t.Fatal(err)
	}

	*clock = time.Date(2026, 4, 2, 6, 0, 0, 0, time.UTC)
	rep, err := uc.SweepPeriods(ctx)
	if err != nil || rep.Closed != 2 || rep.Transitions != 1 || rep.Failed != 0 {
		t.Fatalf("barrido: %+v %v", rep, err)
	}
	closed := db.eventsOf("billing.period.closed")
	if len(closed) != 2 || !closed[0].start.Equal(date(2026, 1, 31)) || !closed[1].start.Equal(date(2026, 2, 28)) {
		t.Fatalf("un cierre por periodo vencido: %+v", closed)
	}
	if closed[0].usage[domain.ResourceTransactionalMessages] != 1 || closed[1].usage[domain.ResourceTransactionalMessages] != 2 ||
		closed[1].usage[domain.ResourceMailboxes] != 1 || len(closed[1].usage) != len(domain.Resources()) {
		t.Fatalf("consumo de cada periodo: %+v / %+v", closed[0].usage, closed[1].usage)
	}
	sub := db.subs[tenant]
	if !sub.CurrentPeriodStart.Equal(date(2026, 3, 31)) || !sub.CurrentPeriodEnd.Equal(date(2026, 4, 30)) {
		t.Fatalf("periodo vigente tras el cierre: %s - %s", sub.CurrentPeriodStart, sub.CurrentPeriodEnd)
	}
	if db.subs[trialTenant].Status != domain.StatusActive || len(db.eventsOf("billing.subscription.changed")) != 1 {
		t.Fatalf("la prueba terminada pasa a activa: %s", db.subs[trialTenant].Status)
	}
	usage, err := uc.TenantUsage(ctx, tenant)
	if err != nil || usage.Lines[0].Resource != domain.ResourceUsers || !usage.PeriodStart.Equal(date(2026, 3, 31)) {
		t.Fatalf("consumo del periodo nuevo: %+v %v", usage, err)
	}
	for _, l := range usage.Lines {
		if l.Resource == domain.ResourceTransactionalMessages && l.Used != 0 {
			t.Fatalf("el flujo empieza en cero en el periodo nuevo: %d", l.Used)
		}
	}

	if rep, err := uc.SweepPeriods(ctx); err != nil || rep.Closed != 0 || rep.Transitions != 0 {
		t.Fatalf("un segundo barrido no hace nada: %+v %v", rep, err)
	}
}

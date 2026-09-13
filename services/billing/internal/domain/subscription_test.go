package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestMesesAnclados(t *testing.T) {
	cases := []struct {
		from           time.Time
		months, anchor int
		want           time.Time
	}{
		{day(2026, 1, 31), 1, 31, day(2026, 2, 28)},
		{day(2028, 1, 31), 1, 31, day(2028, 2, 29)},
		{day(2026, 2, 28), 1, 31, day(2026, 3, 31)},
		{day(2026, 3, 31), 1, 31, day(2026, 4, 30)},
		{day(2026, 12, 15), 1, 15, day(2027, 1, 15)},
		{day(2026, 3, 15), 12, 15, day(2027, 3, 15)},
		{day(2028, 2, 29), 12, 29, day(2029, 2, 28)},
		{day(2031, 2, 28), 12, 29, day(2032, 2, 29)},
	}
	for _, c := range cases {
		if got := AddMonthsAnchored(c.from, c.months, c.anchor); !got.Equal(c.want) {
			t.Errorf("%s + %d meses (ancla %d) = %s; se esperaba %s",
				c.from.Format("2006-01-02"), c.months, c.anchor, got.Format("2006-01-02"), c.want.Format("2006-01-02"))
		}
	}
}

func TestSuscripcionNuevaMensualYAnual(t *testing.T) {
	now := time.Date(2026, 1, 31, 15, 30, 0, 0, time.UTC)
	plan := validPlan()
	plan.ID = uuid.New()

	s, err := NewSubscription(uuid.New(), plan, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != StatusActive || !s.CurrentPeriodStart.Equal(day(2026, 1, 31)) ||
		!s.CurrentPeriodEnd.Equal(day(2026, 2, 28)) || s.AnchorDay != 31 {
		t.Fatalf("mensual: %+v", s)
	}

	plan.BillingPeriod = PeriodYearly
	s, err = NewSubscription(uuid.New(), plan, now, nil)
	if err != nil || !s.CurrentPeriodEnd.Equal(day(2027, 1, 31)) {
		t.Fatalf("anual: %+v %v", s, err)
	}

	trial := now.Add(14 * 24 * time.Hour)
	s, err = NewSubscription(uuid.New(), plan, now, &trial)
	if err != nil || s.Status != StatusTrialing {
		t.Fatalf("con prueba: %+v %v", s, err)
	}
	past := now.Add(-time.Hour)
	if _, err := NewSubscription(uuid.New(), plan, now, &past); !errors.Is(err, ErrInvalidSubscription) {
		t.Fatalf("prueba en el pasado: %v", err)
	}
	plan.Status = PlanRetired
	if _, err := NewSubscription(uuid.New(), plan, now, nil); !errors.Is(err, ErrPlanRetired) {
		t.Fatalf("plan retirado: %v", err)
	}
}

func TestAvanceDePeriodoMensualFinDeMes(t *testing.T) {
	s := &Subscription{CurrentPeriodStart: day(2026, 1, 31), CurrentPeriodEnd: day(2026, 2, 28), AnchorDay: 31}
	start, end := s.AdvancePeriod(PeriodMonthly)
	if !start.Equal(day(2026, 1, 31)) || !end.Equal(day(2026, 2, 28)) {
		t.Fatalf("periodo cerrado: %s - %s", start, end)
	}
	if !s.CurrentPeriodStart.Equal(day(2026, 2, 28)) || !s.CurrentPeriodEnd.Equal(day(2026, 3, 31)) {
		t.Fatalf("periodo nuevo: %s - %s", s.CurrentPeriodStart, s.CurrentPeriodEnd)
	}
	s.AdvancePeriod(PeriodMonthly)
	if !s.CurrentPeriodStart.Equal(day(2026, 3, 31)) || !s.CurrentPeriodEnd.Equal(day(2026, 4, 30)) {
		t.Fatalf("sin deriva del ancla: %s - %s", s.CurrentPeriodStart, s.CurrentPeriodEnd)
	}
}

func TestAvanceDePeriodoAnualBisiesto(t *testing.T) {
	s := &Subscription{CurrentPeriodStart: day(2028, 2, 29), CurrentPeriodEnd: day(2029, 2, 28), AnchorDay: 29}
	start, end := s.AdvancePeriod(PeriodYearly)
	if !start.Equal(day(2028, 2, 29)) || !end.Equal(day(2029, 2, 28)) {
		t.Fatalf("periodo cerrado: %s - %s", start, end)
	}
	if !s.CurrentPeriodEnd.Equal(day(2030, 2, 28)) {
		t.Fatalf("periodo nuevo: %s", s.CurrentPeriodEnd)
	}
}

func TestPeriodoVigenteSinTocarLaSuscripcion(t *testing.T) {
	s := &Subscription{CurrentPeriodStart: day(2026, 1, 31), CurrentPeriodEnd: day(2026, 2, 28), AnchorDay: 31}
	start, end := s.PeriodAt(time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC), PeriodMonthly)
	if !start.Equal(day(2026, 2, 28)) || !end.Equal(day(2026, 3, 31)) {
		t.Fatalf("periodo vigente: %s - %s", start, end)
	}
	if !s.CurrentPeriodStart.Equal(day(2026, 1, 31)) {
		t.Fatal("PeriodAt no debe modificar la suscripcion")
	}
	if s.PeriodExpired(time.Date(2026, 2, 27, 23, 59, 0, 0, time.UTC)) {
		t.Fatal("el 27 de febrero el periodo sigue vigente")
	}
	if !s.PeriodExpired(day(2026, 2, 28)) {
		t.Fatal("el fin del periodo es exclusivo")
	}
}

func TestCicloDeVida(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	ended := now.Add(-time.Minute)
	future := now.Add(time.Hour)

	s := &Subscription{Status: StatusTrialing, TrialEndsAt: &ended}
	if !s.ApplyLifecycle(now) || s.Status != StatusActive {
		t.Fatalf("prueba terminada debe pasar a activa: %s", s.Status)
	}
	s = &Subscription{Status: StatusTrialing, TrialEndsAt: &future, CancelAt: &ended}
	if !s.ApplyLifecycle(now) || s.Status != StatusCancelled {
		t.Fatalf("la cancelacion programada manda: %s", s.Status)
	}
	s = &Subscription{Status: StatusActive, CancelAt: &future}
	if s.ApplyLifecycle(now) || s.Status != StatusActive {
		t.Fatalf("sin nada vencido no cambia: %s", s.Status)
	}
}

func TestCambiosDeLaPlataforma(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	trialing := StatusTrialing

	s := &Subscription{Status: StatusActive}
	if err := s.ApplyUpdate(SubscriptionUpdate{Status: &trialing}, now); !errors.Is(err, ErrInvalidSubscription) {
		t.Fatalf("prueba sin fin de prueba: %v", err)
	}
	if err := s.ApplyUpdate(SubscriptionUpdate{Status: &trialing, TrialEndsAt: OptionalTime{Set: true, Value: &future}}, now); err != nil || s.Status != StatusTrialing {
		t.Fatalf("prueba con fin futuro: %v %s", err, s.Status)
	}
	if err := s.ApplyUpdate(SubscriptionUpdate{CancelAt: OptionalTime{Set: true, Value: &past}}, now); !errors.Is(err, ErrInvalidSubscription) {
		t.Fatalf("cancelacion en el pasado: %v", err)
	}
	s.CancelAt = &future
	if err := s.ApplyUpdate(SubscriptionUpdate{CancelAt: OptionalTime{Set: true}}, now); err != nil || s.CancelAt != nil {
		t.Fatalf("vaciar la cancelacion: %v %v", err, s.CancelAt)
	}

	current := validPlan()
	current.ID = uuid.New()
	s = &Subscription{PlanID: current.ID, PlanCode: current.Code, Status: StatusActive}
	current.Status = PlanRetired
	if err := s.ChangePlan(current); err != nil {
		t.Fatalf("seguir en el mismo plan retirado es valido: %v", err)
	}
	other := validPlan()
	other.ID, other.Code, other.Status = uuid.New(), "legacy", PlanRetired
	if err := s.ChangePlan(other); !errors.Is(err, ErrPlanRetired) {
		t.Fatalf("pasar a un plan retirado: %v", err)
	}
	other.Status = PlanActive
	if err := s.ChangePlan(other); err != nil || s.PlanID != other.ID || s.PlanCode != "legacy" {
		t.Fatalf("cambio de plan: %v %+v", err, s)
	}
}

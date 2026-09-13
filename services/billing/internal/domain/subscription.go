package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type SubscriptionStatus string

const (
	StatusTrialing  SubscriptionStatus = "trialing"
	StatusActive    SubscriptionStatus = "active"
	StatusPastDue   SubscriptionStatus = "past_due"
	StatusSuspended SubscriptionStatus = "suspended"
	StatusCancelled SubscriptionStatus = "cancelled"
)

var subscriptionStatuses = []SubscriptionStatus{StatusTrialing, StatusActive, StatusPastDue, StatusSuspended, StatusCancelled}

func SubscriptionStatuses() []SubscriptionStatus {
	return append([]SubscriptionStatus(nil), subscriptionStatuses...)
}

func ParseSubscriptionStatus(s string) (SubscriptionStatus, error) {
	for _, st := range subscriptionStatuses {
		if string(st) == s {
			return st, nil
		}
	}
	return "", fmt.Errorf("%w: estado %q desconocido", ErrInvalidSubscription, s)
}

// AllowsUsage indica si la suscripcion da derecho a consumir. past_due sigue consumiendo:
// es la gracia de cobro hasta que la plataforma decida suspender.
func (s SubscriptionStatus) AllowsUsage() bool {
	return s == StatusTrialing || s == StatusActive || s == StatusPastDue
}

type Subscription struct {
	ID                 uuid.UUID
	TenantID           uuid.UUID
	PlanID             uuid.UUID
	PlanCode           string
	Status             SubscriptionStatus
	CurrentPeriodStart time.Time
	CurrentPeriodEnd   time.Time
	AnchorDay          int
	TrialEndsAt        *time.Time
	CancelAt           *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// NewSubscription abre la suscripcion de una empresa con periodo desde hoy (UTC). Con
// trialEndsAt empieza en prueba.
func NewSubscription(tenantID uuid.UUID, plan *Plan, now time.Time, trialEndsAt *time.Time) (*Subscription, error) {
	if !plan.Assignable() {
		return nil, ErrPlanRetired
	}
	status := StatusActive
	if trialEndsAt != nil {
		if !trialEndsAt.After(now) {
			return nil, fmt.Errorf("%w: trial_ends_at debe ser futura", ErrInvalidSubscription)
		}
		status = StatusTrialing
	}
	start := Date(now)
	return &Subscription{
		TenantID:           tenantID,
		PlanID:             plan.ID,
		PlanCode:           plan.Code,
		Status:             status,
		CurrentPeriodStart: start,
		CurrentPeriodEnd:   AddMonthsAnchored(start, plan.BillingPeriod.Months(), start.Day()),
		AnchorDay:          start.Day(),
		TrialEndsAt:        trialEndsAt,
	}, nil
}

// OptionalTime distingue un campo ausente (Set=false) de uno que se vacia (Value=nil).
type OptionalTime struct {
	Set   bool
	Value *time.Time
}

// SubscriptionUpdate es lo que la plataforma cambia de una suscripcion, ademas del plan.
type SubscriptionUpdate struct {
	Status      *SubscriptionStatus
	TrialEndsAt OptionalTime
	CancelAt    OptionalTime
}

// ApplyUpdate aplica el cambio pedido por la plataforma. Una fecha que se fija debe ser
// futura; una suscripcion en prueba necesita su fin de prueba.
func (s *Subscription) ApplyUpdate(u SubscriptionUpdate, now time.Time) error {
	if u.TrialEndsAt.Set {
		if u.TrialEndsAt.Value != nil && !u.TrialEndsAt.Value.After(now) {
			return fmt.Errorf("%w: trial_ends_at debe ser futura", ErrInvalidSubscription)
		}
		s.TrialEndsAt = u.TrialEndsAt.Value
	}
	if u.CancelAt.Set {
		if u.CancelAt.Value != nil && !u.CancelAt.Value.After(now) {
			return fmt.Errorf("%w: cancel_at debe ser futura", ErrInvalidSubscription)
		}
		s.CancelAt = u.CancelAt.Value
	}
	if u.Status != nil {
		if *u.Status == StatusTrialing && (s.TrialEndsAt == nil || !s.TrialEndsAt.After(now)) {
			return fmt.Errorf("%w: una suscripcion en prueba necesita trial_ends_at futura", ErrInvalidSubscription)
		}
		s.Status = *u.Status
	}
	if s.Status == StatusTrialing && s.TrialEndsAt == nil {
		return fmt.Errorf("%w: una suscripcion en prueba necesita trial_ends_at", ErrInvalidSubscription)
	}
	return nil
}

// ChangePlan cambia el plan desde ya sin reiniciar el periodo: el siguiente periodo dura
// lo que diga el plan nuevo.
func (s *Subscription) ChangePlan(plan *Plan) error {
	if plan.ID == s.PlanID {
		return nil
	}
	if !plan.Assignable() {
		return ErrPlanRetired
	}
	s.PlanID, s.PlanCode = plan.ID, plan.Code
	return nil
}

// PeriodAt devuelve el periodo vigente en el dia dado sin tocar la suscripcion: si el
// barrido de cierre aun no avanzo un periodo vencido, el consumo ya cae en el nuevo.
func (s *Subscription) PeriodAt(now time.Time, bp BillingPeriod) (start, end time.Time) {
	day := Date(now)
	start, end = s.CurrentPeriodStart, s.CurrentPeriodEnd
	for !day.Before(end) {
		start, end = end, AddMonthsAnchored(end, bp.Months(), s.AnchorDay)
	}
	return start, end
}

// PeriodExpired indica si el periodo vigente ya termino en el dia dado.
func (s *Subscription) PeriodExpired(now time.Time) bool {
	return !Date(now).Before(s.CurrentPeriodEnd)
}

// AdvancePeriod cierra el periodo vigente, abre el siguiente y devuelve el cerrado.
func (s *Subscription) AdvancePeriod(bp BillingPeriod) (closedStart, closedEnd time.Time) {
	closedStart, closedEnd = s.CurrentPeriodStart, s.CurrentPeriodEnd
	s.CurrentPeriodStart = closedEnd
	s.CurrentPeriodEnd = AddMonthsAnchored(closedEnd, bp.Months(), s.AnchorDay)
	return closedStart, closedEnd
}

// ApplyLifecycle aplica las transiciones que dependen del reloj: una cancelacion
// programada que llego y una prueba que termino. Sin pasarela de pago, la prueba pasa a
// activa; con ella, esta es la transicion que decidira cobrar o pasar a past_due.
func (s *Subscription) ApplyLifecycle(now time.Time) bool {
	if s.CancelAt != nil && !now.Before(*s.CancelAt) && s.Status != StatusCancelled {
		s.Status = StatusCancelled
		return true
	}
	if s.Status == StatusTrialing && s.TrialEndsAt != nil && !now.Before(*s.TrialEndsAt) {
		s.Status = StatusActive
		return true
	}
	return false
}

// Date trunca al dia UTC: los periodos son dias de calendario UTC, [inicio, fin).
func Date(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// AddMonthsAnchored suma meses a una fecha fijando el dia al ancla, recortado al ultimo
// dia del mes destino. Anclar evita la deriva: 31 ene -> 28 feb -> 31 mar, y no 28 mar.
func AddMonthsAnchored(from time.Time, months, anchorDay int) time.Time {
	from = Date(from)
	first := time.Date(from.Year(), from.Month()+time.Month(months), 1, 0, 0, 0, 0, time.UTC)
	day := anchorDay
	if last := daysIn(first.Year(), first.Month()); day > last {
		day = last
	}
	return time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, time.UTC)
}

func daysIn(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

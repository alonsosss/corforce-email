package app

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/alonsosss/corforce-email/services/reputation/internal/ports"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

// ClassSummary es el estado de una clase junto con los numeros de su ventana actual.
type ClassSummary struct {
	Record        domain.Record
	Counts        domain.Counts
	BounceRate    decimal.Decimal
	ComplaintRate decimal.Decimal
}

// ClassStatus es la reputacion de una clase tal como la ve la empresa: ademas del estado,
// los umbrales que se le aplican y los limites de tasa con su uso actual.
type ClassStatus struct {
	ClassSummary
	Thresholds domain.Thresholds
	Hourly     domain.Usage
	Daily      domain.Usage
}

// Status es la reputacion de la empresa en todas sus clases.
type Status struct {
	WindowDays  int
	WindowStart time.Time
	MinVolume   int64
	Classes     []ClassStatus
}

// Status arma la reputacion de la empresa. Las tasas son las de la ventana en este momento,
// no las guardadas en el ultimo cambio. Si Redis no responde, el uso de la hora y el dia
// queda sin valor y el resto se informa igual.
func (uc *UseCase) Status(ctx context.Context, tenantID uuid.UUID) (Status, error) {
	now := uc.now()
	from := uc.policy.WindowStart(now)
	records, err := uc.records(ctx, tenantID)
	if err != nil {
		return Status{}, err
	}
	counts, err := uc.stats.WindowCountsByClass(ctx, tenantID, from)
	if err != nil {
		return Status{}, err
	}
	overrides, err := uc.limits.List(ctx, tenantID)
	if err != nil {
		return Status{}, err
	}
	byClass := make(map[domain.Class]*domain.LimitOverride, len(overrides))
	for i := range overrides {
		byClass[overrides[i].Class] = &overrides[i]
	}

	st := Status{WindowDays: uc.policy.WindowDays, WindowStart: from, MinVolume: uc.policy.Thresholds.MinVolume}
	for _, class := range domain.Classes() {
		limits := uc.policy.LimitsFor(class, byClass[class])
		cs := ClassStatus{
			ClassSummary: summarize(records[class], counts[class]),
			Thresholds:   uc.policy.Thresholds.For(class),
			Hourly:       domain.Usage{Limit: limits.Hourly},
			Daily:        domain.Usage{Limit: limits.Daily},
		}
		if hour, day, err := uc.rate.Usage(ctx, tenantID, class, now); err != nil {
			uc.logger.Warn("reputation: uso de tasa no disponible en el estado de la empresa",
				zap.String("tenant_id", tenantID.String()), zap.String("class", string(class)), zap.Error(err))
		} else {
			cs.Hourly.Used, cs.Daily.Used = &hour, &day
		}
		st.Classes = append(st.Classes, cs)
	}
	return st, nil
}

// History lista los cambios de estado de la empresa, del mas reciente al mas antiguo.
func (uc *UseCase) History(ctx context.Context, tenantID uuid.UUID, f ports.HistoryFilter) ([]domain.Change, int64, error) {
	if f.Class != "" {
		if _, err := domain.ParseClass(string(f.Class)); err != nil {
			return nil, 0, err
		}
	}
	return uc.states.ListHistory(ctx, tenantID, f)
}

// records devuelve el estado de cada clase; las que aun no tienen fila, en su valor inicial.
func (uc *UseCase) records(ctx context.Context, tenantID uuid.UUID) (map[domain.Class]domain.Record, error) {
	list, err := uc.states.List(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := make(map[domain.Class]domain.Record, len(domain.Classes()))
	for _, c := range domain.Classes() {
		out[c] = domain.DefaultRecord(tenantID, c)
	}
	for _, r := range list {
		out[r.Class] = r
	}
	return out, nil
}

func summarize(rec domain.Record, c domain.Counts) ClassSummary {
	return ClassSummary{
		Record:        rec,
		Counts:        c,
		BounceRate:    domain.Rate(c.Bounced, c.Sent),
		ComplaintRate: domain.Rate(c.Complained, c.Sent),
	}
}

package app

import (
	"context"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// sweepTenantTimeout acota el barrido de una empresa.
const sweepTenantTimeout = time.Minute

// SweepReport resume una pasada del barrido.
type SweepReport struct {
	Tenants      int
	Changed      int
	Failed       int
	EventsPruned int64
	DaysPruned   int64
}

func (r *SweepReport) add(o SweepReport) {
	r.Tenants += o.Tenants
	r.Changed += o.Changed
	r.Failed += o.Failed
	r.EventsPruned += o.EventsPruned
	r.DaysPruned += o.DaysPruned
}

// Sweep reevalua todas las clases de todas las empresas activas y poda lo que ya no
// cuenta. La ventana se desplaza cada dia aunque no lleguen eventos: una empresa
// restringida que deja de enviar ve caer sus tasas y vuelve sola a ok.
func (uc *UseCase) Sweep(ctx context.Context) (SweepReport, error) {
	var (
		mu    sync.Mutex
		total SweepReport
	)
	err := uc.tenants.ForEachActive(ctx, sweepTenantTimeout, func(tctx context.Context, tenantID uuid.UUID) {
		rep := uc.SweepTenant(tctx, tenantID)
		mu.Lock()
		total.add(rep)
		mu.Unlock()
	})
	return total, err
}

// SweepTenant reevalua las clases de una empresa y poda sus eventos anotados y sus
// contadores antiguos. Un fallo en una clase no impide las demas.
func (uc *UseCase) SweepTenant(ctx context.Context, tenantID uuid.UUID) SweepReport {
	rep := SweepReport{Tenants: 1}
	for _, class := range domain.Classes() {
		_, changed, err := uc.Reevaluate(ctx, tenantID, class)
		if err != nil {
			rep.Failed++
			uc.logger.Warn("reputation: barrido: no se pudo reevaluar",
				zap.String("tenant_id", tenantID.String()), zap.String("class", string(class)), zap.Error(err))
			continue
		}
		if changed {
			rep.Changed++
		}
	}
	now := uc.now()
	events, days, err := uc.stats.Prune(ctx, now.Add(-ProcessedRetention), dayOf(now).AddDate(0, 0, -StatsRetentionDays))
	if err != nil {
		rep.Failed++
		uc.logger.Warn("reputation: barrido: no se pudo podar", zap.String("tenant_id", tenantID.String()), zap.Error(err))
		return rep
	}
	rep.EventsPruned, rep.DaysPruned = events, days
	return rep
}

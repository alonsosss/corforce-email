package app

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// listTenantTimeout acota lo que el listado de plataforma espera a la base de cada empresa.
const listTenantTimeout = 5 * time.Second

// TenantReputation es la reputacion de una empresa en el listado de plataforma.
type TenantReputation struct {
	TenantID uuid.UUID
	// Unavailable: la base de la empresa no respondio. Se lista igual para que el listado
	// no parezca completo cuando no lo es.
	Unavailable bool
	Classes     []ClassSummary
}

// HasState indica si alguna clase de la empresa esta en el estado dado.
func (t TenantReputation) HasState(s domain.State) bool {
	for _, c := range t.Classes {
		if c.Record.State == s {
			return true
		}
	}
	return false
}

// ListTenants recorre las empresas activas y devuelve su reputacion por clase, ordenadas
// por empresa. filter vacio = todas; con filtro, las que tengan alguna clase en ese estado
// (y las que no respondieron, que podrian tenerlo).
func (uc *UseCase) ListTenants(ctx context.Context, filter domain.State) ([]TenantReputation, error) {
	if filter != "" {
		if _, err := domain.ParseState(string(filter)); err != nil {
			return nil, err
		}
	}
	from := uc.policy.WindowStart(uc.now())
	var (
		mu  sync.Mutex
		out = []TenantReputation{}
	)
	err := uc.tenants.ForEachActive(ctx, listTenantTimeout, func(tctx context.Context, tenantID uuid.UUID) {
		item, err := uc.tenantSummary(tctx, tenantID, from)
		if err != nil {
			uc.logger.Warn("reputation: base de empresa no disponible en el listado de plataforma",
				zap.String("tenant_id", tenantID.String()), zap.Error(err))
			item = TenantReputation{TenantID: tenantID, Unavailable: true}
		} else if filter != "" && !item.HasState(filter) {
			return
		}
		mu.Lock()
		out = append(out, item)
		mu.Unlock()
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TenantID.String() < out[j].TenantID.String() })
	return out, nil
}

func (uc *UseCase) tenantSummary(ctx context.Context, tenantID uuid.UUID, from time.Time) (TenantReputation, error) {
	records, err := uc.records(ctx, tenantID)
	if err != nil {
		return TenantReputation{}, err
	}
	counts, err := uc.stats.WindowCountsByClass(ctx, tenantID, from)
	if err != nil {
		return TenantReputation{}, err
	}
	t := TenantReputation{TenantID: tenantID}
	for _, class := range domain.Classes() {
		t.Classes = append(t.Classes, summarize(records[class], counts[class]))
	}
	return t, nil
}

// SetLimitsInput son los limites que el superadmin fija a una clase de una empresa. Un
// valor nil vuelve al de la configuracion; los dos nil retiran los limites propios.
type SetLimitsInput struct {
	TenantID  uuid.UUID
	Class     domain.Class
	Hourly    *int64
	Daily     *int64
	UpdatedBy uuid.UUID
}

// LimitsResult son los limites efectivos tras el cambio y los propios, si quedan.
type LimitsResult struct {
	Effective domain.Limits
	Override  *domain.LimitOverride
}

// SetLimits fija los limites de tasa de una clase de OTRA empresa: la base se resuelve por
// el id de la ruta, nunca por la empresa del token del superadmin.
func (uc *UseCase) SetLimits(ctx context.Context, in SetLimitsInput) (LimitsResult, error) {
	if _, err := domain.ParseClass(string(in.Class)); err != nil {
		return LimitsResult{}, err
	}
	o := domain.LimitOverride{TenantID: in.TenantID, Class: in.Class, Hourly: in.Hourly, Daily: in.Daily, UpdatedBy: in.UpdatedBy}
	if err := uc.policy.ValidateOverride(o); err != nil {
		return LimitsResult{}, err
	}
	tctx, err := uc.tenants.Scope(ctx, in.TenantID)
	if err != nil {
		return LimitsResult{}, err
	}
	if o.Hourly == nil && o.Daily == nil {
		if err := uc.limits.Delete(tctx, in.TenantID, in.Class); err != nil {
			return LimitsResult{}, err
		}
		uc.logger.Info("reputation: limites propios retirados",
			zap.String("tenant_id", in.TenantID.String()), zap.String("class", string(in.Class)),
			zap.String("by", in.UpdatedBy.String()))
		return LimitsResult{Effective: uc.policy.LimitsFor(in.Class, nil)}, nil
	}
	if err := uc.limits.Upsert(tctx, &o); err != nil {
		return LimitsResult{}, err
	}
	effective := uc.policy.LimitsFor(in.Class, &o)
	uc.logger.Info("reputation: limites fijados",
		zap.String("tenant_id", in.TenantID.String()), zap.String("class", string(in.Class)),
		zap.Int64("hourly", effective.Hourly), zap.Int64("daily", effective.Daily),
		zap.String("by", in.UpdatedBy.String()))
	return LimitsResult{Effective: effective, Override: &o}, nil
}

// Suspend suspende a mano una clase de otra empresa. La suspension no la levanta ninguna
// evaluacion: solo Release.
func (uc *UseCase) Suspend(ctx context.Context, tenantID uuid.UUID, class domain.Class, reason string, by uuid.UUID) (domain.Record, error) {
	if _, err := domain.ParseClass(string(class)); err != nil {
		return domain.Record{}, err
	}
	reason, err := domain.NormalizeReason(reason)
	if err != nil {
		return domain.Record{}, err
	}
	return uc.manualChange(ctx, tenantID, class, func(cur domain.Record, v domain.Verdict, now time.Time) (domain.Record, bool) {
		return domain.Suspend(cur, reason, v, by, now)
	})
}

// Release retira la marca manual de una clase de otra empresa y la deja en lo que diga la
// evaluacion de su ventana actual.
func (uc *UseCase) Release(ctx context.Context, tenantID uuid.UUID, class domain.Class, by uuid.UUID) (domain.Record, error) {
	if _, err := domain.ParseClass(string(class)); err != nil {
		return domain.Record{}, err
	}
	return uc.manualChange(ctx, tenantID, class, func(cur domain.Record, v domain.Verdict, now time.Time) (domain.Record, bool) {
		return domain.Release(cur, v, by, now)
	})
}

// manualChange resuelve la base de la empresa indicada y aplica, con la fila bloqueada, la
// decision manual sobre el estado vigente y el veredicto de la ventana.
func (uc *UseCase) manualChange(ctx context.Context, tenantID uuid.UUID, class domain.Class,
	decide func(domain.Record, domain.Verdict, time.Time) (domain.Record, bool)) (domain.Record, error) {
	tctx, err := uc.tenants.Scope(ctx, tenantID)
	if err != nil {
		return domain.Record{}, err
	}
	var (
		out    domain.Record
		change *domain.Change
	)
	err = uc.tx.Transact(tctx, func(ctx context.Context) error {
		cur, verdict, err := uc.lockAndEvaluate(ctx, tenantID, class)
		if err != nil {
			return err
		}
		next, changed := decide(cur, verdict, uc.now())
		out = next
		if !changed {
			return nil
		}
		change, err = uc.applyChange(ctx, cur, next, true)
		return err
	})
	if err != nil {
		return domain.Record{}, err
	}
	uc.recordChange(change)
	return out, nil
}

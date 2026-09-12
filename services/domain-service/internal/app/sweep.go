package app

import (
	"context"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// SweepReport resume una pasada del barrido sobre una empresa.
type SweepReport struct {
	Rechecked int
	Failed    int
	Retired   int
	Pruned    int64
}

// SweepTenant reverifica los dominios de una empresa: los verificados (que pueden
// perder sus registros) y los pendientes recientes (que pueden haberlos publicado sin
// pulsar verificar). Despues retira las claves DKIM fuera de gracia y poda el historial.
// El contexto lleva ya el pool de la empresa (ForEachActiveTenantConcurrent).
func (uc *UseCase) SweepTenant(ctx context.Context, tenantID uuid.UUID) SweepReport {
	var report SweepReport
	now := uc.now()
	log := uc.logger.With(zap.String("tenant_id", tenantID.String()))

	domains, err := uc.repo.ListForRecheck(ctx, tenantID, now.Add(-uc.pendingWindow))
	if err != nil {
		log.Error("barrido: listar dominios", zap.Error(err))
		return report
	}
	for _, d := range domains {
		if ctx.Err() != nil {
			return report
		}
		res, err := uc.verify(ctx, d, true)
		if err != nil {
			log.Error("barrido: verificar dominio", zap.String("domain", d.Domain), zap.Error(err))
			continue
		}
		report.Rechecked++
		if res.Domain.Status == domain.StatusFailed {
			report.Failed++
		}
	}

	expired, err := uc.repo.ListWithExpiredPreviousDKIM(ctx, tenantID, now.Add(-uc.rotationGrace))
	if err != nil {
		log.Error("barrido: listar claves DKIM en gracia", zap.Error(err))
	}
	for _, d := range expired {
		if ctx.Err() != nil {
			return report
		}
		if !uc.canRetirePreviousDKIM(ctx, d) {
			log.Warn("clave DKIM anterior fuera de gracia pero el selector nuevo sigue sin publicarse; se conserva",
				zap.String("domain", d.Domain), zap.String("selector", d.DKIMSelector))
			continue
		}
		if err := uc.retirePreviousDKIM(ctx, d); err != nil {
			log.Warn("barrido: retirar clave DKIM anterior", zap.String("domain", d.Domain), zap.Error(err))
			continue
		}
		report.Retired++
	}

	pruned, err := uc.repo.PruneChecks(ctx, tenantID, now.Add(-uc.retention))
	if err != nil {
		log.Error("barrido: podar comprobaciones", zap.Error(err))
	}
	report.Pruned = pruned
	return report
}

// canRetirePreviousDKIM: un dominio verificado solo suelta la clave anterior cuando la
// ultima comprobacion vio publicado el TXT del selector nuevo; retirarla antes dejaria
// el correo saliente firmado con una clave que ningun receptor puede comprobar. Un
// dominio que no esta verificado no firma, asi que su clave anterior se retira sin mas.
func (uc *UseCase) canRetirePreviousDKIM(ctx context.Context, d *domain.Domain) bool {
	if d.Status != domain.StatusVerified {
		return true
	}
	checks, err := uc.repo.LatestChecks(ctx, d.TenantID, d.ID)
	if err != nil {
		return false
	}
	for _, c := range checks {
		if c.Record == domain.RecordDKIM {
			return c.OK
		}
	}
	return false
}

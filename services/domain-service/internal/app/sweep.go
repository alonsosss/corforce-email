package app

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// SweepReport resume una pasada del barrido sobre una empresa.
type SweepReport struct {
	Rechecked   int
	Failed      int
	Deactivated int
	Retired     int
	// Revoked son las revocaciones por clave comprometida que la celda confirmo en esta pasada.
	Revoked int
	Pruned  int64
}

// SweepTenant termina primero las revocaciones de claves comprometidas que la celda no confirmo
// (una clave revocada que siga firmando es lo mas urgente) y las desactivaciones que quedaron sin
// confirmar en el directorio de la celda. Despues reverifica los dominios de una empresa: los
// verificados (que pueden perder sus registros) y los pendientes recientes (que pueden haberlos
// publicado sin pulsar verificar). Por ultimo retira las claves DKIM fuera de gracia y poda el
// historial. El contexto lleva ya el pool de la empresa (ForEachActiveTenantConcurrent).
func (uc *UseCase) SweepTenant(ctx context.Context, tenantID uuid.UUID) SweepReport {
	var report SweepReport
	now := uc.now()
	log := uc.logger.With(zap.String("tenant_id", tenantID.String()))

	revocations, err := uc.repo.ListPendingDKIMRevocation(ctx, tenantID)
	if errors.Is(err, domain.ErrTenantSchemaNotReady) {
		log.Warn("barrido: la base de la empresa aun no tiene todas las migraciones; se salta esta pasada", zap.Error(err))
		return report
	}
	if err != nil {
		log.Error("barrido: listar revocaciones DKIM pendientes", zap.Error(err))
	}
	for _, d := range revocations {
		if ctx.Err() != nil {
			return report
		}
		if err := uc.finishRevocation(ctx, d.TenantID, d.ID); err != nil {
			log.Error("barrido: la celda sigue sin confirmar la revocacion DKIM; la clave revocada puede seguir firmando",
				zap.String("domain", d.Domain), zap.Error(err))
			continue
		}
		report.Revoked++
	}

	pending, err := uc.repo.ListPendingDeactivation(ctx, tenantID)
	if err != nil {
		log.Error("barrido: listar desactivaciones pendientes", zap.Error(err))
	}
	for _, d := range pending {
		if ctx.Err() != nil {
			return report
		}
		if err := uc.completeDeactivation(ctx, d); err != nil {
			log.Warn("barrido: el dominio ya no recibe y sigue sin desactivarse en mail-directory; se reintenta en el siguiente",
				zap.String("domain", d.Domain), zap.Error(err))
			continue
		}
		report.Deactivated++
	}

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
		// En modo automatico su TXT sale tambien de la zona del proveedor; un fallo lo deja alli,
		// como en modo manual, y lo registra.
		if d.DNSAutomatic() {
			uc.publishDKIMAutomatically(ctx, d.TenantID, d.ID, []string{d.DKIMPreviousSelector})
		}
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

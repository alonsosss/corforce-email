package app

import (
	"context"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// SweepReport resume una pasada del barrido.
type SweepReport struct {
	Tenants        int
	Expired        int
	Failed         int
	ObjectsDeleted int
	HistoryPurged  int
	Errors         int
}

// RunSweeper barre cada SweepInterval hasta que ctx se cancele. Solo barre la replica que tiene el
// cerrojo de lider; las demas esperan a la siguiente pasada.
func (uc *UseCase) RunSweeper(ctx context.Context) {
	if uc.cfg.SweepInterval <= 0 || uc.tenants == nil {
		uc.logger.Info("mail-files: barrido desactivado")
		return
	}
	ticker := time.NewTicker(uc.cfg.SweepInterval)
	defer ticker.Stop()
	for {
		uc.sweepAsLeader(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (uc *UseCase) sweepAsLeader(ctx context.Context) {
	if uc.leader != nil {
		release, ok := uc.leader(ctx)
		if !ok {
			return
		}
		defer release()
	}
	report := uc.Sweep(ctx)
	if report.Expired+report.Failed+report.ObjectsDeleted+report.HistoryPurged+report.Errors > 0 {
		uc.logger.Info("mail-files: barrido",
			zap.Int("tenants", report.Tenants), zap.Int("expired", report.Expired), zap.Int("failed", report.Failed),
			zap.Int("objects_deleted", report.ObjectsDeleted), zap.Int("history_purged", report.HistoryPurged),
			zap.Int("errors", report.Errors))
	}
}

// Sweep hace una pasada por todas las empresas: cierra lo caducado y las subidas abandonadas, borra
// los objetos que ya no se sirven y poda el historial. Un fallo en una empresa no detiene a las demas.
func (uc *UseCase) Sweep(ctx context.Context) SweepReport {
	var report SweepReport
	if uc.tenants == nil {
		return report
	}
	err := uc.tenants.ForEach(ctx, func(tctx context.Context, tenantID uuid.UUID) {
		report.Tenants++
		uc.sweepTenant(tctx, tenantID, &report)
	})
	if err != nil {
		report.Errors++
		uc.logger.Warn("mail-files: barrido sin lista de empresas", zap.Error(err))
	}
	return report
}

func (uc *UseCase) sweepTenant(ctx context.Context, tenantID uuid.UUID, report *SweepReport) {
	now := uc.now()
	fail := func(step string, err error) {
		report.Errors++
		uc.logger.Warn("mail-files: barrido de una empresa", zap.String("tenant_id", tenantID.String()), zap.String("step", step), zap.Error(err))
	}
	if n, err := uc.repo.ExpireDue(ctx, tenantID, now); err != nil {
		fail("expire", err)
	} else {
		report.Expired += n
	}
	if n, err := uc.repo.FailStalePending(ctx, tenantID, now.Add(-uc.cfg.PendingGrace)); err != nil {
		fail("pending", err)
	} else {
		report.Failed += n
	}
	if uc.store != nil {
		due, err := uc.repo.DueForDeletion(ctx, tenantID, now, uc.cfg.DeleteGrace, uc.cfg.SweepBatch)
		if err != nil {
			fail("due", err)
		}
		for _, f := range due {
			if err := uc.store.Delete(ctx, f.ObjectKey); err != nil {
				fail("delete", err)
				continue
			}
			if err := uc.repo.MarkObjectDeleted(ctx, tenantID, f.ID); err != nil {
				fail("mark", err)
				continue
			}
			report.ObjectsDeleted++
		}
	}
	if n, err := uc.repo.PurgeHistory(ctx, tenantID, now.Add(-uc.cfg.HistoryRetention)); err != nil {
		fail("history", err)
	} else {
		report.HistoryPurged += n
	}
}

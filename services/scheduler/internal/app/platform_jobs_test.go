package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/google/uuid"
)

func TestLosTrabajosDePlataformaNoSeCambianDesdeUnaEmpresa(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	platform := f.addJob(func(j *domain.JobDefinition) { j.TenantID = nil; j.Handler = platformHandler })
	exec := domain.JobExecution{ID: uuid.New(), JobID: platform.ID, Status: domain.StatusFailed}
	f.store.execs[exec.ID] = exec

	if err := f.uc.EnableJob(ctx, platform.ID, f.tenantID); !errors.Is(err, domain.ErrPlatformJob) {
		t.Errorf("enable: %v", err)
	}
	if err := f.uc.DisableJob(ctx, platform.ID, f.tenantID); !errors.Is(err, domain.ErrPlatformJob) {
		t.Errorf("disable: %v", err)
	}
	if _, err := f.uc.RunJob(ctx, f.tenantID, platform.ID); !errors.Is(err, domain.ErrPlatformJob) {
		t.Errorf("run: %v", err)
	}
	if _, err := f.uc.UpdateJob(ctx, &platform); !errors.Is(err, domain.ErrPlatformJob) {
		t.Errorf("update: %v", err)
	}
	if err := f.uc.CancelExecution(ctx, exec.ID, f.tenantID); !errors.Is(err, domain.ErrPlatformJob) {
		t.Errorf("cancel: %v", err)
	}
	if _, err := f.uc.RetryFailedExecution(ctx, exec.ID, f.tenantID); !errors.Is(err, domain.ErrPlatformJob) {
		t.Errorf("retry: %v", err)
	}
	if f.store.jobUpdates+f.store.deactivated+len(f.store.writes)+len(f.store.events) != 0 {
		t.Fatalf("hubo efectos: updated=%d deactivated=%d writes=%d events=%d",
			f.store.jobUpdates, f.store.deactivated, len(f.store.writes), len(f.store.events))
	}
}

func TestLosTrabajosDeLaEmpresaSiguenFuncionando(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	own := f.addJob(nil)
	if err := f.uc.EnableJob(ctx, own.ID, f.tenantID); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := f.uc.DisableJob(ctx, own.ID, f.tenantID); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := f.uc.RunJob(ctx, f.tenantID, own.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	if f.store.jobUpdates != 1 || f.store.deactivated != 1 || len(f.store.writes) != 1 || len(f.eventsOf("scheduler.job.started")) != 1 {
		t.Fatalf("efectos: updated=%d deactivated=%d writes=%d started=%d, se esperaba 1 de cada",
			f.store.jobUpdates, f.store.deactivated, len(f.store.writes), len(f.eventsOf("scheduler.job.started")))
	}
	if err := f.uc.DisableJob(ctx, uuid.New(), f.tenantID); !errors.Is(err, domain.ErrJobNotFound) {
		t.Fatalf("un trabajo ajeno o inexistente no se desactiva por id: %v", err)
	}
}

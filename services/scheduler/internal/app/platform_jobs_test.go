package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type fakeJobs struct {
	ports.JobDefinitionRepository
	byID        map[uuid.UUID]*domain.JobDefinition
	updated     int
	deactivated int
}

func (f *fakeJobs) GetByID(_ context.Context, id, _ uuid.UUID) (*domain.JobDefinition, error) {
	if j, ok := f.byID[id]; ok {
		return j, nil
	}
	return nil, domain.ErrJobNotFound
}
func (f *fakeJobs) Update(context.Context, *domain.JobDefinition) error { f.updated++; return nil }
func (f *fakeJobs) Deactivate(context.Context, uuid.UUID) error         { f.deactivated++; return nil }

type fakeExecutions struct {
	ports.JobExecutionRepository
	created int
}

func (f *fakeExecutions) Create(context.Context, *domain.JobExecution) error { f.created++; return nil }

type fakeEvents struct {
	ports.EventPublisher
	started int
}

func (f *fakeEvents) PublishJobStarted(string, string, string) error { f.started++; return nil }

type jobsFixture struct {
	uc                    *SchedulerUseCase
	jobs                  *fakeJobs
	execs                 *fakeExecutions
	events                *fakeEvents
	tenantID              uuid.UUID
	platformJob, ownJobID uuid.UUID
}

func newJobsFixture() jobsFixture {
	tenantID := uuid.New()
	platformID, ownID := uuid.New(), uuid.New()
	jobs := &fakeJobs{byID: map[uuid.UUID]*domain.JobDefinition{
		platformID: {ID: platformID},
		ownID:      {ID: ownID, TenantID: &tenantID},
	}}
	execs, events := &fakeExecutions{}, &fakeEvents{}
	uc := NewSchedulerUseCase(SchedulerDeps{Jobs: jobs, Executions: execs, Events: events, Logger: zap.NewNop()})
	return jobsFixture{uc: uc, jobs: jobs, execs: execs, events: events, tenantID: tenantID, platformJob: platformID, ownJobID: ownID}
}

func TestLosTrabajosDePlataformaNoSeCambianDesdeUnaEmpresa(t *testing.T) {
	f := newJobsFixture()
	ctx := context.Background()
	if err := f.uc.EnableJob(ctx, f.platformJob, f.tenantID); !errors.Is(err, domain.ErrPlatformJob) {
		t.Errorf("enable: %v", err)
	}
	if err := f.uc.DisableJob(ctx, f.platformJob, f.tenantID); !errors.Is(err, domain.ErrPlatformJob) {
		t.Errorf("disable: %v", err)
	}
	if _, err := f.uc.RunJob(ctx, f.tenantID, f.platformJob); !errors.Is(err, domain.ErrPlatformJob) {
		t.Errorf("run: %v", err)
	}
	if err := f.uc.UpdateJob(ctx, f.jobs.byID[f.platformJob]); !errors.Is(err, domain.ErrPlatformJob) {
		t.Errorf("update: %v", err)
	}
	if f.jobs.updated+f.jobs.deactivated+f.execs.created+f.events.started != 0 {
		t.Fatalf("hubo efectos: updated=%d deactivated=%d created=%d started=%d",
			f.jobs.updated, f.jobs.deactivated, f.execs.created, f.events.started)
	}
}

func TestLosTrabajosDeLaEmpresaSiguenFuncionando(t *testing.T) {
	f := newJobsFixture()
	ctx := context.Background()
	if err := f.uc.EnableJob(ctx, f.ownJobID, f.tenantID); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := f.uc.DisableJob(ctx, f.ownJobID, f.tenantID); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := f.uc.RunJob(ctx, f.tenantID, f.ownJobID); err != nil {
		t.Fatalf("run: %v", err)
	}
	if f.jobs.updated != 1 || f.jobs.deactivated != 1 || f.execs.created != 1 || f.events.started != 1 {
		t.Fatalf("efectos: updated=%d deactivated=%d created=%d started=%d, se esperaba 1 de cada",
			f.jobs.updated, f.jobs.deactivated, f.execs.created, f.events.started)
	}
	if err := f.uc.DisableJob(ctx, uuid.New(), f.tenantID); !errors.Is(err, domain.ErrJobNotFound) {
		t.Fatalf("un trabajo ajeno o inexistente no se desactiva por id: %v", err)
	}
}

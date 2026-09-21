package app

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/ports"
	"github.com/google/uuid"
)

// Meta es lo que la interfaz necesita para ofrecer el formulario sin fijar valores propios.
type Meta struct {
	Configured        bool
	SourcePorts       []int
	SourceTLSModes    []domain.TLSMode
	DefaultTLSForPort map[int]domain.TLSMode
	MaxActiveJobs     int
	ActiveJobs        int
	Phases            []domain.Phase
}

func (uc *UseCase) Meta(ctx context.Context, tenantID uuid.UUID) (Meta, error) {
	active, err := uc.repo.CountActive(ctx, tenantID)
	if err != nil {
		return Meta{}, err
	}
	defaults := make(map[int]domain.TLSMode, len(uc.cfg.Source.Ports))
	for _, p := range uc.cfg.Source.Ports {
		defaults[p] = domain.DefaultTLS(p)
	}
	return Meta{
		Configured:        uc.cfg.RunnerConfigured,
		SourcePorts:       append([]int(nil), uc.cfg.Source.Ports...),
		SourceTLSModes:    uc.cfg.Source.TLSModes(),
		DefaultTLSForPort: defaults,
		MaxActiveJobs:     uc.cfg.MaxActivePerTenant,
		ActiveJobs:        active,
		Phases:            domain.Phases(),
	}, nil
}

type CreateInput struct {
	MailboxID uuid.UUID
	Source    domain.Source
}

// Create valida el origen, comprueba que el buzon destino es de la empresa y esta activo, cifra la
// contrasena de origen y deja el trabajo pendiente junto con su evento de auditoria.
func (uc *UseCase) Create(ctx context.Context, tenantID, actorID uuid.UUID, in CreateInput) (*domain.Job, error) {
	if !uc.cfg.RunnerConfigured {
		return nil, domain.ErrNotConfigured
	}
	src := in.Source
	if err := uc.cfg.Source.Normalize(&src); err != nil {
		return nil, err
	}
	if err := uc.checkResolved(ctx, src.Host); err != nil {
		return nil, err
	}
	mbx, err := uc.mailboxes.Lookup(ctx, tenantID, in.MailboxID)
	if err != nil {
		return nil, err
	}
	if !mbx.Active {
		return nil, domain.ErrMailboxInactive
	}
	jobID := uuid.New()
	enc, err := uc.cipher.EncryptWithAAD([]byte(src.Password), domain.SourcePasswordAAD(tenantID, jobID))
	if err != nil {
		return nil, err
	}
	now := uc.now()
	job := &domain.Job{
		ID: jobID, TenantID: tenantID, MailboxID: mbx.ID, MailboxUsername: mbx.Username,
		SourceHost: src.Host, SourcePort: src.Port, SourceTLS: src.TLS, SourceUsername: src.Username,
		SourcePasswordEnc: enc, Status: domain.StatusPending, Progress: emptyProgress(),
		RequestedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	err = uc.tx.Transact(ctx, func(ctx context.Context) error {
		if err := uc.repo.Insert(ctx, job, uc.insertLimits(now)); err != nil {
			return err
		}
		return uc.events.Created(ctx, job)
	})
	if err != nil {
		return nil, err
	}
	uc.hint(tenantID)
	job.SourcePasswordEnc = nil
	return job, nil
}

func (uc *UseCase) insertLimits(now time.Time) ports.InsertLimits {
	return ports.InsertLimits{
		MaxActive: uc.cfg.MaxActivePerTenant,
		MaxRecent: uc.cfg.MaxJobsPerDay, RecentSince: now.Add(-24 * time.Hour),
		MaxAuthFailures: uc.cfg.MaxAuthFailuresPerHour, AuthFailuresSince: now.Add(-time.Hour),
	}
}

// checkResolved resuelve el servidor de origen y rechaza el que apunte a una direccion no publica.
// Es la primera de dos comprobaciones: el ejecutor vuelve a resolver y a comprobar justo antes de
// conectar, porque el DNS puede cambiar entre las dos.
func (uc *UseCase) checkResolved(ctx context.Context, host string) error {
	addrs, err := uc.resolver.LookupAddrs(ctx, host)
	if err != nil {
		return &domain.FieldError{Field: "source_host", Err: domain.ErrHostUnresolvable}
	}
	return uc.cfg.Source.CheckAddresses(addrs)
}

func (uc *UseCase) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Job, error) {
	j, err := uc.repo.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	j.SourcePasswordEnc = nil
	return j, nil
}

func (uc *UseCase) List(ctx context.Context, tenantID uuid.UUID, f ports.ListFilter, p ports.Page) ([]domain.Job, ports.Total, error) {
	jobs, total, err := uc.repo.List(ctx, tenantID, f, p)
	for i := range jobs {
		jobs[i].SourcePasswordEnc = nil
	}
	return jobs, total, err
}

// Cancel cancela al instante un trabajo pendiente y pide al ejecutor que detenga uno en curso; el
// ejecutor lo cierra en su siguiente latido.
func (uc *UseCase) Cancel(ctx context.Context, tenantID, actorID, id uuid.UUID) (*domain.Job, error) {
	now := uc.now()
	var job *domain.Job
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		j, err := uc.repo.RequestCancel(ctx, tenantID, id, now)
		if err != nil {
			return err
		}
		job = j
		if j.Status == domain.StatusCancelled {
			return uc.events.Finished(ctx, j, actorID)
		}
		if j.CancelRequestedAt != nil && j.CancelRequestedAt.Equal(now) {
			return uc.events.CancelRequested(ctx, j, actorID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	job.SourcePasswordEnc = nil
	return job, nil
}

func emptyProgress() domain.Progress { return domain.Progress{Folders: []domain.FolderProgress{}} }

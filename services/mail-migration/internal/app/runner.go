package app

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const maxRunnerIDRunes = 100

// ClaimedJob es lo que recibe el ejecutor al tomar un trabajo. Password va en claro y solo aqui.
type ClaimedJob struct {
	JobID               uuid.UUID
	TenantID            uuid.UUID
	LeaseID             uuid.UUID
	Attempt             int
	SourceHost          string
	SourcePort          int
	SourceTLS           domain.TLSMode
	SourceUsername      string
	SourcePassword      string
	DestinationUsername string
	// DestinationPassword es la credencial de destino del trabajo: abre solo el buzon destino, solo
	// mientras el trabajo esta en curso. Vacia cuando el servicio no las emite y el ejecutor usa el
	// maestro compartido.
	DestinationPassword string
	LeaseSeconds        int
}

// Claim entrega al ejecutor el siguiente trabajo, o nil si no hay ninguno. Prueba primero las
// empresas con trabajo conocido y, si no hay nada, recorre todas las activas como mucho cada
// SweepInterval.
//
// Un ejecutor tiene un trabajo a la vez y esta instancia no reparte mas de MaxRunningJobs entre todos:
// quien lo pida antes de tiempo recibe "nada que hacer". Es lo que acota lo que se lleva un ejecutor
// comprometido, que necesita la contrasena de cada trabajo que reclama.
func (uc *UseCase) Claim(ctx context.Context, runnerID string) (*ClaimedJob, error) {
	if !uc.cfg.RunnerConfigured {
		return nil, domain.ErrNotConfigured
	}
	runnerID = strings.TrimSpace(runnerID)
	if runnerID == "" || utf8.RuneCountInString(runnerID) > maxRunnerIDRunes {
		return nil, domain.ErrInvalidRunner
	}
	slot, ok := uc.reserveSlot(runnerID)
	if !ok {
		return nil, nil
	}
	claimed, err := uc.claimNext(ctx, runnerID)
	if claimed == nil {
		uc.releaseSlot(slot)
		return nil, err
	}
	uc.bindSlot(slot, claimed.JobID)
	return claimed, err
}

func (uc *UseCase) claimNext(ctx context.Context, runnerID string) (*ClaimedJob, error) {
	for _, tenantID := range uc.hints() {
		claimed, err := uc.claimFromTenant(ctx, tenantID, runnerID)
		if err != nil {
			uc.logger.Warn("mail-migration: no se pudo reclamar de la empresa", zap.String("tenant_id", tenantID.String()), zap.Error(err))
			continue
		}
		if claimed != nil {
			return claimed, nil
		}
		uc.unhint(tenantID)
	}
	if !uc.sweepDue() {
		return nil, nil
	}
	var claimed *ClaimedJob
	err := uc.tenants.ForEach(ctx, func(ctx context.Context, tenantID uuid.UUID) bool {
		c, err := uc.claimInTenantContext(ctx, tenantID, runnerID)
		if err != nil {
			uc.logger.Warn("mail-migration: no se pudo reclamar de la empresa", zap.String("tenant_id", tenantID.String()), zap.Error(err))
			return false
		}
		if c == nil {
			return false
		}
		claimed = c
		uc.hint(tenantID)
		return true
	})
	return claimed, err
}

func (uc *UseCase) claimFromTenant(ctx context.Context, tenantID uuid.UUID, runnerID string) (*ClaimedJob, error) {
	tctx, err := uc.tenants.For(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return uc.claimInTenantContext(tctx, tenantID, runnerID)
}

func (uc *UseCase) claimInTenantContext(ctx context.Context, tenantID uuid.UUID, runnerID string) (*ClaimedJob, error) {
	now := uc.now()
	params := ports.ClaimParams{
		RunnerID: runnerID, LeaseID: uuid.New(), Now: now, LeaseUntil: now.Add(uc.cfg.Lease), MaxAttempts: uc.cfg.MaxAttempts,
	}
	var credential domain.DestinationCredential
	if uc.cfg.JobCredentials {
		c, err := domain.NewDestinationCredential(uc.random)
		if err != nil {
			return nil, err
		}
		credential = c
		params.DestinationCredentialHash = c.Hash
	}
	var job *domain.Job
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		expired, err := uc.repo.ExpireLost(ctx, tenantID, now, uc.cfg.MaxAttempts)
		if err != nil {
			return err
		}
		for i := range expired {
			if err := uc.events.Finished(ctx, &expired[i], uuid.Nil); err != nil {
				return err
			}
		}
		j, err := uc.repo.Claim(ctx, tenantID, params)
		if err != nil || j == nil {
			return err
		}
		if err := uc.events.Started(ctx, j); err != nil {
			return err
		}
		job = j
		return nil
	})
	if err != nil || job == nil {
		return nil, err
	}
	password, err := uc.cipher.DecryptWithAAD(job.SourcePasswordEnc, domain.SourcePasswordAAD(tenantID, job.ID))
	if err != nil {
		uc.logger.Error("mail-migration: no se pudo descifrar la credencial de origen; el trabajo falla",
			zap.String("tenant_id", tenantID.String()), zap.String("job_id", job.ID.String()))
		if ferr := uc.finish(ctx, tenantID, job.ID, ports.FinishParams{
			LeaseID: *job.LeaseID, Status: domain.StatusFailed, Progress: job.Progress, Error: domain.CredentialUnreadableError(), Now: uc.now(),
		}); ferr != nil {
			return nil, ferr
		}
		return nil, nil
	}
	claimed := &ClaimedJob{
		JobID: job.ID, TenantID: tenantID, LeaseID: *job.LeaseID, Attempt: job.Attempt,
		SourceHost: job.SourceHost, SourcePort: job.SourcePort, SourceTLS: job.SourceTLS,
		SourceUsername: job.SourceUsername, SourcePassword: string(password),
		DestinationUsername: job.MailboxUsername, LeaseSeconds: int(uc.cfg.Lease / time.Second),
	}
	if uc.cfg.JobCredentials {
		claimed.DestinationPassword = credential.Token(tenantID, job.ID)
	}
	return claimed, nil
}

type HeartbeatInput struct {
	LeaseID  uuid.UUID
	Phase    string
	Progress domain.Progress
}

type HeartbeatResult struct {
	Cancel       bool
	LeaseSeconds int
}

// Heartbeat extiende el lease, guarda el progreso y avisa al ejecutor si el usuario pidio cancelar.
func (uc *UseCase) Heartbeat(ctx context.Context, tenantID, jobID uuid.UUID, in HeartbeatInput) (HeartbeatResult, error) {
	phase, ok := domain.ParsePhase(in.Phase)
	if !ok {
		return HeartbeatResult{}, domain.ErrInvalidPhase
	}
	if err := in.Progress.Normalize(); err != nil {
		return HeartbeatResult{}, err
	}
	tctx, err := uc.tenants.For(ctx, tenantID)
	if err != nil {
		return HeartbeatResult{}, err
	}
	now := uc.now()
	cancel, err := uc.repo.Heartbeat(tctx, tenantID, jobID, ports.HeartbeatParams{
		LeaseID: in.LeaseID, Phase: phase, Progress: in.Progress, Now: now, LeaseUntil: now.Add(uc.cfg.Lease),
	})
	if err != nil {
		return HeartbeatResult{}, err
	}
	uc.extendHold(jobID, now.Add(uc.cfg.Lease))
	return HeartbeatResult{Cancel: cancel, LeaseSeconds: int(uc.cfg.Lease / time.Second)}, nil
}

type CompleteInput struct {
	LeaseID      uuid.UUID
	Outcome      string
	Progress     domain.Progress
	ErrorCode    string
	ErrorMessage string
}

// Complete cierra el trabajo, borra la credencial de origen y anuncia el resultado. El mensaje de
// error del ejecutor se sanea con la contrasena de origen como secreto a retirar, por si un
// servidor ajeno la repitio en su respuesta.
func (uc *UseCase) Complete(ctx context.Context, tenantID, jobID uuid.UUID, in CompleteInput) (*domain.Job, error) {
	outcome, err := domain.ParseOutcome(in.Outcome)
	if err != nil {
		return nil, err
	}
	if err := in.Progress.Normalize(); err != nil {
		return nil, err
	}
	tctx, err := uc.tenants.For(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	var jobErr *domain.JobError
	switch outcome {
	case domain.OutcomeFailed:
		jobErr = domain.NewJobError(domain.ErrorCode(in.ErrorCode), in.ErrorMessage, uc.sourceSecrets(tctx, tenantID, jobID)...)
	case domain.OutcomeCancelled:
		jobErr = &domain.JobError{Code: domain.CodeCancelled}
	}
	params := ports.FinishParams{LeaseID: in.LeaseID, Status: outcome.Status(), Progress: in.Progress, Error: jobErr, Now: uc.now()}
	var job *domain.Job
	err = uc.tx.Transact(tctx, func(ctx context.Context) error {
		j, err := uc.repo.Finish(ctx, tenantID, jobID, params)
		if err != nil {
			return err
		}
		job = j
		return uc.events.Finished(ctx, j, uuid.Nil)
	})
	if err == nil || errors.Is(err, domain.ErrLeaseLost) {
		uc.releaseSlot(jobID)
	}
	if err != nil {
		return nil, err
	}
	job.SourcePasswordEnc = nil
	return job, nil
}

func (uc *UseCase) finish(ctx context.Context, tenantID, jobID uuid.UUID, p ports.FinishParams) error {
	return uc.tx.Transact(ctx, func(ctx context.Context) error {
		j, err := uc.repo.Finish(ctx, tenantID, jobID, p)
		if err != nil {
			if errors.Is(err, domain.ErrLeaseLost) {
				return nil
			}
			return err
		}
		return uc.events.Finished(ctx, j, uuid.Nil)
	})
}

// sourceSecrets devuelve la contrasena de origen descifrada, para retirarla del mensaje de error. Si
// el trabajo ya no la tiene o no se puede descifrar, no hay nada que retirar.
func (uc *UseCase) sourceSecrets(ctx context.Context, tenantID, jobID uuid.UUID) []string {
	j, err := uc.repo.Get(ctx, tenantID, jobID)
	if err != nil || len(j.SourcePasswordEnc) == 0 {
		return nil
	}
	plain, err := uc.cipher.DecryptWithAAD(j.SourcePasswordEnc, domain.SourcePasswordAAD(tenantID, jobID))
	if err != nil || len(plain) == 0 {
		return nil
	}
	return []string{string(plain)}
}

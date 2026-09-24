package app

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

// maxScheduledListed acota el listado de un buzon: las pendientes tienen su propio tope y las fallidas
// se purgan con la retencion.
const maxScheduledListed = 500

// CreateScheduledSendRequest es la fila que deja el webmail tras guardar el mensaje en Scheduled.
type CreateScheduledSendRequest struct {
	Username    string
	MessageID   string
	Folder      string
	UIDValidity uint32
	UID         uint32
	SendAt      time.Time
	Subject     string
	Recipients  []string
}

// CreateScheduledSend guarda el indice de un envio programado del buzon, con el tope de pendientes.
func (uc *UseCase) CreateScheduledSend(ctx context.Context, req CreateScheduledSendRequest) (*domain.ScheduledSend, error) {
	s := &domain.ScheduledSend{
		ID: uuid.New(), MessageID: req.MessageID, Folder: req.Folder, UIDValidity: req.UIDValidity, UID: req.UID,
		SendAt: req.SendAt, Subject: req.Subject, Recipients: append([]string(nil), req.Recipients...),
	}
	if err := s.Normalize(uc.now()); err != nil {
		return nil, err
	}
	tenantID, mailboxID, err := uc.locate(ctx, req.Username)
	if err != nil {
		return nil, err
	}
	s.TenantID = tenantID
	err = uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		s.Username = m.Username
		pending, err := uc.scheduled.CountPending(ctx, tenantID, m.Username)
		if err != nil {
			return err
		}
		if pending >= domain.MaxScheduledPerMailbox {
			return domain.ErrScheduledSendLimit
		}
		return uc.scheduled.Create(ctx, s)
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// ListScheduledSends devuelve las pendientes, en curso y fallidas del buzon.
func (uc *UseCase) ListScheduledSends(ctx context.Context, username string) ([]domain.ScheduledSend, error) {
	tenantID, mailboxID, err := uc.locate(ctx, username)
	if err != nil {
		return nil, err
	}
	var out []domain.ScheduledSend
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		out, err = uc.scheduled.ListByUsername(ctx, tenantID, m.Username, maxScheduledListed)
		return err
	})
	if out == nil {
		out = []domain.ScheduledSend{}
	}
	return out, err
}

// RescheduleSend cambia la hora de un envio aun pendiente del buzon.
func (uc *UseCase) RescheduleSend(ctx context.Context, username string, id uuid.UUID, sendAt time.Time) (*domain.ScheduledSend, error) {
	if err := domain.ValidateSendAt(sendAt, uc.now()); err != nil {
		return nil, err
	}
	var out *domain.ScheduledSend
	err := uc.ownScheduled(ctx, username, id, func(ctx context.Context, tenantID uuid.UUID, s *domain.ScheduledSend) error {
		if s.Status != domain.ScheduledPending {
			return domain.ErrScheduledSendNotPending
		}
		updated, err := uc.scheduled.Reschedule(ctx, tenantID, id, sendAt.UTC())
		out = updated
		return err
	})
	return out, err
}

// CancelScheduledSend cancela un envio del buzon. Es idempotente sobre una fila ya cancelada y admite
// retirar una fallida de la lista; la que se esta enviando o ya salio no se cancela.
func (uc *UseCase) CancelScheduledSend(ctx context.Context, username string, id uuid.UUID) error {
	return uc.ownScheduled(ctx, username, id, func(ctx context.Context, tenantID uuid.UUID, s *domain.ScheduledSend) error {
		switch s.Status {
		case domain.ScheduledCanceled:
			return nil
		case domain.ScheduledPending, domain.ScheduledFailed:
			return uc.scheduled.Cancel(ctx, tenantID, id)
		default:
			return domain.ErrScheduledSendNotPending
		}
	})
}

// ownScheduled localiza el buzon y bloquea la fila, que tiene que ser suya.
func (uc *UseCase) ownScheduled(ctx context.Context, username string, id uuid.UUID, fn func(ctx context.Context, tenantID uuid.UUID, s *domain.ScheduledSend) error) error {
	tenantID, mailboxID, err := uc.locate(ctx, username)
	if err != nil {
		return err
	}
	return uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		s, err := uc.scheduled.GetForUpdate(ctx, tenantID, m.Username, id)
		if err != nil {
			return err
		}
		return fn(ctx, tenantID, s)
	})
}

// ClaimScheduledSends reclama las filas vencidas de toda la celda para el trabajador del webmail. No
// pasa por la empresa: el trabajador no la conoce, y la reclamacion es la unica forma de que dos
// replicas no envien la misma fila.
func (uc *UseCase) ClaimScheduledSends(ctx context.Context, limit, leaseSeconds int) ([]domain.ScheduledSend, error) {
	p, err := domain.NormalizeClaim(limit, leaseSeconds)
	if err != nil {
		return nil, err
	}
	var out []domain.ScheduledSend
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		out, err = uc.scheduled.Claim(ctx, p, domain.MaxScheduledAttempts, domain.ScheduledRetention)
		return err
	})
	if out == nil {
		out = []domain.ScheduledSend{}
	}
	return out, err
}

// FinishScheduledSend cierra una fila reclamada con el resultado del trabajador.
func (uc *UseCase) FinishScheduledSend(ctx context.Context, id uuid.UUID, outcome domain.ScheduledOutcome) (*domain.ScheduledSend, error) {
	o, err := domain.NormalizeOutcome(outcome)
	if err != nil {
		return nil, err
	}
	var out *domain.ScheduledSend
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		s, err := uc.scheduled.ClaimedForUpdate(ctx, id)
		if err != nil {
			return err
		}
		t, err := s.Close(o)
		if err != nil {
			return err
		}
		out, err = uc.scheduled.Close(ctx, id, t)
		return err
	})
	return out, err
}

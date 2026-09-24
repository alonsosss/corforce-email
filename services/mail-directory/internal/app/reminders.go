package app

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

// maxRemindersListed acota el listado de un buzon por tipo: los activos tienen su propio tope y los
// fallidos se purgan con la retencion.
const maxRemindersListed = 1000

// CreateReminderRequest es la fila que deja el webmail al posponer un mensaje o al pedir seguimiento
// de uno enviado.
type CreateReminderRequest struct {
	Username     string
	Kind         string
	MessageID    string
	Folder       string
	UIDValidity  uint32
	UID          uint32
	ReturnFolder string
	DueAt        time.Time
	Subject      string
	Addresses    []string
}

// CreateReminder guarda un recordatorio del buzon, con el tope de activos.
func (uc *UseCase) CreateReminder(ctx context.Context, req CreateReminderRequest) (*domain.Reminder, error) {
	r := &domain.Reminder{
		ID: uuid.New(), Kind: req.Kind, MessageID: req.MessageID, Folder: req.Folder, UIDValidity: req.UIDValidity, UID: req.UID,
		ReturnFolder: req.ReturnFolder, DueAt: req.DueAt, Subject: req.Subject, Addresses: append([]string(nil), req.Addresses...),
	}
	if err := r.Normalize(uc.now()); err != nil {
		return nil, err
	}
	tenantID, mailboxID, err := uc.locate(ctx, req.Username)
	if err != nil {
		return nil, err
	}
	r.TenantID = tenantID
	err = uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		r.Username = m.Username
		active, err := uc.reminders.CountActive(ctx, tenantID, m.Username)
		if err != nil {
			return err
		}
		if active >= domain.MaxRemindersPerMailbox {
			return domain.ErrReminderLimit
		}
		return uc.reminders.Create(ctx, r)
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}

// ListReminders devuelve los pendientes, en curso y fallidos del buzon de ese tipo.
func (uc *UseCase) ListReminders(ctx context.Context, username, kind string) ([]domain.Reminder, error) {
	if !domain.ValidReminderKind(kind) {
		return nil, &domain.FieldError{Field: "kind", Reason: "debe ser snooze o follow_up"}
	}
	tenantID, mailboxID, err := uc.locate(ctx, username)
	if err != nil {
		return nil, err
	}
	var out []domain.Reminder
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		out, err = uc.reminders.ListByUsername(ctx, tenantID, m.Username, kind, maxRemindersListed)
		return err
	})
	if out == nil {
		out = []domain.Reminder{}
	}
	return out, err
}

// RescheduleReminder cambia la hora de un recordatorio aun pendiente del buzon.
func (uc *UseCase) RescheduleReminder(ctx context.Context, username string, id uuid.UUID, due time.Time) (*domain.Reminder, error) {
	if err := domain.ValidateReminderDue(due, uc.now()); err != nil {
		return nil, err
	}
	var out *domain.Reminder
	err := uc.ownReminder(ctx, username, id, func(ctx context.Context, tenantID uuid.UUID, r *domain.Reminder) error {
		if r.Status != domain.ReminderPending {
			return domain.ErrReminderNotPending
		}
		updated, err := uc.reminders.Reschedule(ctx, tenantID, id, due.UTC())
		out = updated
		return err
	})
	return out, err
}

// CancelReminder cancela un recordatorio del buzon. Es idempotente sobre uno ya cancelado y admite
// retirar uno fallido de la lista; el que se esta procesando o ya termino no se cancela.
func (uc *UseCase) CancelReminder(ctx context.Context, username string, id uuid.UUID) error {
	return uc.ownReminder(ctx, username, id, func(ctx context.Context, tenantID uuid.UUID, r *domain.Reminder) error {
		switch r.Status {
		case domain.ReminderCanceled:
			return nil
		case domain.ReminderPending, domain.ReminderFailed:
			return uc.reminders.Cancel(ctx, tenantID, id)
		default:
			return domain.ErrReminderNotPending
		}
	})
}

// ownReminder localiza el buzon y bloquea la fila, que tiene que ser suya.
func (uc *UseCase) ownReminder(ctx context.Context, username string, id uuid.UUID, fn func(ctx context.Context, tenantID uuid.UUID, r *domain.Reminder) error) error {
	tenantID, mailboxID, err := uc.locate(ctx, username)
	if err != nil {
		return err
	}
	return uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		r, err := uc.reminders.GetForUpdate(ctx, tenantID, m.Username, id)
		if err != nil {
			return err
		}
		return fn(ctx, tenantID, r)
	})
}

// ClaimReminders reclama los recordatorios vencidos de toda la celda para el trabajador del webmail,
// como ClaimScheduledSends.
func (uc *UseCase) ClaimReminders(ctx context.Context, limit, leaseSeconds int) ([]domain.Reminder, error) {
	p, err := domain.NormalizeClaim(limit, leaseSeconds)
	if err != nil {
		return nil, err
	}
	var out []domain.Reminder
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		out, err = uc.reminders.Claim(ctx, p, domain.MaxReminderAttempts, domain.ReminderRetention)
		return err
	})
	if out == nil {
		out = []domain.Reminder{}
	}
	return out, err
}

// FinishReminder cierra una fila reclamada con el resultado del trabajador.
func (uc *UseCase) FinishReminder(ctx context.Context, id uuid.UUID, outcome domain.ReminderOutcome) (*domain.Reminder, error) {
	o, err := domain.NormalizeReminderOutcome(outcome)
	if err != nil {
		return nil, err
	}
	var out *domain.Reminder
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		r, err := uc.reminders.ClaimedForUpdate(ctx, id)
		if err != nil {
			return err
		}
		t, err := r.Close(o)
		if err != nil {
			return err
		}
		out, err = uc.reminders.Close(ctx, id, t)
		return err
	})
	return out, err
}

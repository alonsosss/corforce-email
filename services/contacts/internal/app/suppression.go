package app

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

// Subjects que este servicio consume. Los publica suppression (stream SUPPRESSION) por
// su outbox con Data {tenant_id, email, reason, source}.
const (
	SubjectSuppressionAdded   = "suppression.entry.added"
	SubjectSuppressionRemoved = "suppression.entry.removed"
)

// Causas de suppression (su contrato; ver services/suppression/internal/domain).
const (
	reasonUnsubscribe = "unsubscribe"
	reasonHardBounce  = "hard_bounce"
	reasonComplaint   = "complaint"
)

// SuppressionEvent es un evento de suppression tal como lo lee este servicio.
type SuppressionEvent struct {
	Subject  string
	TenantID uuid.UUID
	Email    string
	Reason   string
	Source   string
}

// SuppressionResult dice que hizo el evento, para el log del consumidor.
type SuppressionResult struct {
	// Ignored: la causa no cambia el contacto (manual, invalid) o no hay contacto con
	// esa direccion en la empresa.
	Ignored bool
	Changed bool
}

// IsInputError distingue el evento que nunca se va a poder procesar (direccion no
// valida) del fallo transitorio de la base: el consumidor descarta el primero.
func IsInputError(err error) bool {
	return errors.Is(err, domain.ErrInvalidEmail)
}

// statusForReason es el estado que implica cada causa de exclusion. manual e invalid
// no dicen nada de la persona: excluyen del envio (lo hace suppression) sin cambiarla.
func statusForReason(reason string) (domain.Status, bool) {
	switch reason {
	case reasonUnsubscribe:
		return domain.StatusUnsubscribed, true
	case reasonHardBounce:
		return domain.StatusBounced, true
	case reasonComplaint:
		return domain.StatusComplained, true
	}
	return "", false
}

// ApplySuppression refleja en el contacto un alta o una baja de la lista de exclusiones.
// Idempotente: reentregar el mismo evento no cambia nada la segunda vez.
func (uc *UseCase) ApplySuppression(ctx context.Context, ev SuppressionEvent) (SuppressionResult, error) {
	email, err := domain.NormalizeEmail(ev.Email)
	if err != nil {
		return SuppressionResult{}, err
	}
	target, ok := statusForReason(ev.Reason)
	if !ok || (ev.Subject != SubjectSuppressionAdded && ev.Subject != SubjectSuppressionRemoved) {
		return SuppressionResult{Ignored: true}, nil
	}
	// Retirar una baja no reactiva por aqui: eso lo hace el nuevo consentimiento, que es
	// quien la pide (contacts.contact.resubscribed).
	if ev.Subject == SubjectSuppressionRemoved && ev.Reason == reasonUnsubscribe {
		return SuppressionResult{Ignored: true}, nil
	}

	var res SuppressionResult
	err = uc.tx.Transact(ctx, func(ctx context.Context) error {
		c, err := uc.contacts.GetByEmailForUpdate(ctx, ev.TenantID, email)
		if errors.Is(err, domain.ErrContactNotFound) {
			res.Ignored = true
			return nil
		}
		if err != nil {
			return err
		}
		if ev.Subject == SubjectSuppressionRemoved {
			if !c.LiftSuppression(target) {
				return nil
			}
			res.Changed = true
			if err := uc.contacts.Update(ctx, c); err != nil {
				return err
			}
			return uc.events.ContactUpdated(ctx, c, []string{"status"})
		}

		if ev.Reason == reasonUnsubscribe && c.ConsentStatus != domain.ConsentRevoked {
			source := "suppression"
			if ev.Source != "" {
				source += ":" + ev.Source
			}
			revoked := &domain.Consent{
				TenantID: ev.TenantID, ContactID: c.ID, Purpose: domain.PurposeMarketing,
				Status: domain.ConsentRevoked, Method: domain.MethodSuppression,
				Source: truncateString(source, domain.MaxConsentSource), Evidence: map[string]any{"reason": ev.Reason},
			}
			if err := uc.consents.Append(ctx, revoked); err != nil {
				return err
			}
			c.ConsentStatus = domain.ConsentRevoked
			res.Changed = true
			if err := uc.events.ConsentRevoked(ctx, revoked); err != nil {
				return err
			}
		}
		if c.ApplySuppression(target) {
			res.Changed = true
			if err := uc.contacts.Update(ctx, c); err != nil {
				return err
			}
			return uc.events.ContactUpdated(ctx, c, []string{"status"})
		}
		return nil
	})
	return res, err
}

func truncateString(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

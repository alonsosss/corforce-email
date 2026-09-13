package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

// Subjects que este servicio consume. Los publica suppression (stream SUPPRESSION) por
// su outbox con Data {tenant_id, email, reason, source, reasons}: reason es la causa que
// entro o salio y reasons las vigentes que le quedan a la direccion.
const (
	SubjectSuppressionAdded   = "suppression.entry.added"
	SubjectSuppressionRemoved = "suppression.entry.removed"
)

// SuppressionEvent es un evento de suppression tal como lo lee este servicio.
type SuppressionEvent struct {
	Subject  string
	TenantID uuid.UUID
	Email    string
	Reason   string
	Source   string
	// HasReasons: el evento trae reasons, luego lo publico el suppression de una fila por
	// causa. Sin ellas viene de un productor anterior, de una fila por direccion.
	HasReasons bool
}

// SuppressionResult dice que hizo el evento, para el log del consumidor.
type SuppressionResult struct {
	// Ignored: el evento no aplica a ningun contacto (subject ajeno, ninguna direccion
	// igual en la empresa) o, en un evento sin reasons, su causa no cambia al contacto.
	Ignored bool
	Changed bool
}

// IsInputError distingue el evento que nunca se va a poder procesar (direccion no
// valida) del fallo transitorio de la base o de suppression: el consumidor descarta el
// primero.
func IsInputError(err error) bool {
	return errors.Is(err, domain.ErrInvalidEmail)
}

// ApplySuppression refleja en el contacto un alta o una baja de la lista de exclusiones.
// Idempotente: reentregar el mismo evento no cambia nada la segunda vez.
//
// Un evento con reasons no se aplica por su causa ni por su foto de causas: los dos
// subjects llegan por durables distintos y una reentrega puede adelantar a un evento
// anterior, asi que el ultimo en aplicarse podria traer una foto vieja. Se decide con las
// causas vigentes que devuelve suppression, leidas con la fila del contacto bloqueada:
// dos eventos de la misma direccion se serializan en ese bloqueo y el que escribe despues
// leyo despues, de modo que el estado final es el que suppression tenia al aplicarse el
// ultimo.
func (uc *UseCase) ApplySuppression(ctx context.Context, ev SuppressionEvent) (SuppressionResult, error) {
	email, err := domain.NormalizeEmail(ev.Email)
	if err != nil {
		return SuppressionResult{}, err
	}
	if ev.Subject != SubjectSuppressionAdded && ev.Subject != SubjectSuppressionRemoved {
		return SuppressionResult{Ignored: true}, nil
	}
	if !ev.HasReasons {
		return uc.applySuppressionByReason(ctx, ev, email)
	}
	if uc.suppression == nil {
		return SuppressionResult{}, errors.New("contacts: sin lector del estado de suppression")
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
		active, err := uc.suppression.ActiveCauses(ctx, ev.TenantID, email)
		if err != nil {
			return fmt.Errorf("consultar el estado vigente en suppression: %w", err)
		}
		registered, err := uc.unsubscribeRegistered(ctx, c, ev, active)
		if err != nil {
			return err
		}
		causes := domain.CausesOf(active)
		if registered {
			revoked, err := uc.revokeByUnsubscribe(ctx, c, ev)
			if err != nil {
				return err
			}
			res.Changed = res.Changed || revoked
		}
		if c.ReconcileSuppression(causes, registered) {
			res.Changed = true
			return uc.saveStatus(ctx, c)
		}
		return nil
	})
	return res, err
}

// unsubscribeRegistered dice si el evento registra una baja que revoca el consentimiento
// del contacto ya bloqueado: es el alta de una baja, la baja sigue vigente (la que
// suppression ya retiro porque la persona reconsintio no deshace ese consentimiento) y no
// es anterior al ultimo reconsentimiento del contacto (domain.UnsubscribeRevokes), que es
// lo que distingue la baja atrasada o reentregada, aun vigente mientras suppression no
// procesa contacts.contact.resubscribed, de una baja nueva.
func (uc *UseCase) unsubscribeRegistered(ctx context.Context, c *domain.Contact, ev SuppressionEvent, active []domain.ActiveCause) (bool, error) {
	if ev.Subject != SubjectSuppressionAdded || domain.SuppressionCause(ev.Reason) != domain.CauseUnsubscribe {
		return false, nil
	}
	at, ok := domain.CauseRegisteredAt(active, domain.CauseUnsubscribe)
	if !ok {
		return false, nil
	}
	consents, err := uc.consents.ListByContact(ctx, ev.TenantID, c.ID)
	if err != nil {
		return false, fmt.Errorf("leer el historial de consentimiento: %w", err)
	}
	return domain.UnsubscribeRevokes(at, consents), nil
}

// applySuppressionByReason es la regla de un productor sin reasons, que guardaba una
// sola fila por direccion: el estado sale de la causa del evento, sin degradar uno mas
// grave, y retirar un rebote, una queja, una direccion no valida o una exclusion manual
// reactiva a quien estaba en ese estado. Solo la baja revoca el consentimiento.
func (uc *UseCase) applySuppressionByReason(ctx context.Context, ev SuppressionEvent, email string) (SuppressionResult, error) {
	cause := domain.SuppressionCause(ev.Reason)
	target, _ := domain.StatusForCause(cause)
	if target == "" {
		return SuppressionResult{Ignored: true}, nil
	}
	// Retirar una baja no reactiva por aqui: eso lo hace el nuevo consentimiento, que es
	// quien la pide (contacts.contact.resubscribed).
	if ev.Subject == SubjectSuppressionRemoved && cause == domain.CauseUnsubscribe {
		return SuppressionResult{Ignored: true}, nil
	}

	var res SuppressionResult
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
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
			return uc.saveStatus(ctx, c)
		}
		if cause == domain.CauseUnsubscribe {
			revoked, err := uc.revokeByUnsubscribe(ctx, c, ev)
			if err != nil {
				return err
			}
			res.Changed = revoked
		}
		if c.ApplySuppression(target) {
			res.Changed = true
			return uc.saveStatus(ctx, c)
		}
		return nil
	})
	return res, err
}

// revokeByUnsubscribe deja la evidencia de la baja sobre un contacto ya bloqueado, salvo
// que su consentimiento vigente ya este revocado. Devuelve si la anadio.
func (uc *UseCase) revokeByUnsubscribe(ctx context.Context, c *domain.Contact, ev SuppressionEvent) (bool, error) {
	if c.ConsentStatus == domain.ConsentRevoked {
		return false, nil
	}
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
		return false, err
	}
	c.ConsentStatus = domain.ConsentRevoked
	return true, uc.events.ConsentRevoked(ctx, revoked)
}

// saveStatus guarda el estado nuevo del contacto y publica el cambio.
func (uc *UseCase) saveStatus(ctx context.Context, c *domain.Contact) error {
	if err := uc.contacts.Update(ctx, c); err != nil {
		return err
	}
	return uc.events.ContactUpdated(ctx, c, []string{"status"})
}

func truncateString(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

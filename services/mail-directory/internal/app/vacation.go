package app

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

// PutVacationRequest reemplaza la respuesta automatica del buzon entera: lo que no llega se borra.
type PutVacationRequest struct {
	Enabled      bool
	Subject      string
	Message      string
	IntervalDays int
	StartsOn     *time.Time
	EndsOn       *time.Time
}

// GetMailboxVacation devuelve la respuesta automatica del buzon; si nunca la configuro, una
// desactivada y vacia (no es un error: el formulario la muestra en blanco).
func (uc *UseCase) GetMailboxVacation(ctx context.Context, tenantID, mailboxID uuid.UUID) (*domain.VacationReply, error) {
	var out *domain.VacationReply
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		v, err := uc.vacation.ByUsername(ctx, tenantID, m.Username)
		if errors.Is(err, domain.ErrNotFound) {
			out = domain.NewVacationReply(tenantID, m.Username)
			return nil
		}
		out = v
		return err
	})
	return out, err
}

// PutMailboxVacation valida, genera el script Sieve y guarda. Se valida antes de abrir la
// transaccion: un texto invalido no toma el candado de la empresa.
func (uc *UseCase) PutMailboxVacation(ctx context.Context, tenantID, mailboxID uuid.UUID, req PutVacationRequest) (*domain.VacationReply, error) {
	v := &domain.VacationReply{
		ID: uuid.New(), TenantID: tenantID, Enabled: req.Enabled, Subject: req.Subject, Message: req.Message,
		IntervalDays: req.IntervalDays, StartsOn: req.StartsOn, EndsOn: req.EndsOn,
	}
	if err := v.Normalize(); err != nil {
		return nil, err
	}
	err := uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		v.Username = m.Username
		return uc.vacation.Upsert(ctx, v)
	})
	if err != nil {
		return nil, err
	}
	return v, nil
}

// VacationByUsername y PutVacationByUsername sirven al webmail: el buzon se resuelve en toda la
// celda porque quien llama se autentico como el y no conoce su empresa. La lectura y la escritura
// siguen despues el mismo camino que las de administracion, ya bajo la empresa del buzon.
func (uc *UseCase) VacationByUsername(ctx context.Context, username string) (*domain.VacationReply, error) {
	tenantID, mailboxID, err := uc.locate(ctx, username)
	if err != nil {
		return nil, err
	}
	return uc.GetMailboxVacation(ctx, tenantID, mailboxID)
}

func (uc *UseCase) PutVacationByUsername(ctx context.Context, username string, req PutVacationRequest) (*domain.VacationReply, error) {
	tenantID, mailboxID, err := uc.locate(ctx, username)
	if err != nil {
		return nil, err
	}
	return uc.PutMailboxVacation(ctx, tenantID, mailboxID, req)
}

func (uc *UseCase) locate(ctx context.Context, username string) (uuid.UUID, uuid.UUID, error) {
	if uc.locator == nil {
		return uuid.Nil, uuid.Nil, errors.New("mail-directory: localizador de buzones sin cablear")
	}
	login, _, err := domain.NormalizeEmail(username)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return uc.locator.Locate(ctx, login)
}

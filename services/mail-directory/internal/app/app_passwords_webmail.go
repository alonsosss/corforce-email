package app

import (
	"context"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

// Contrasenas de aplicacion del buzon con el que el webmail inicio sesion. Siguen el mismo camino que
// las del administrador (mismo tope, misma generacion en el servidor, mismos avisos al revocar); el
// webmail comprueba antes la contrasena del buzon, y el codigo si tiene verificacion en dos pasos.

func (uc *UseCase) ListAppPasswordsByUsername(ctx context.Context, username string) ([]domain.AppPassword, error) {
	tenantID, mailboxID, err := uc.locate(ctx, username)
	if err != nil {
		return nil, err
	}
	return uc.ListAppPasswords(ctx, tenantID, mailboxID)
}

func (uc *UseCase) CreateAppPasswordByUsername(ctx context.Context, username string, req CreateAppPasswordRequest) (*domain.AppPassword, string, error) {
	tenantID, mailboxID, err := uc.locate(ctx, username)
	if err != nil {
		return nil, "", err
	}
	return uc.CreateAppPassword(ctx, tenantID, mailboxID, req)
}

func (uc *UseCase) DeleteAppPasswordByUsername(ctx context.Context, username string, id uuid.UUID) error {
	tenantID, mailboxID, err := uc.locate(ctx, username)
	if err != nil {
		return err
	}
	return uc.DeleteAppPassword(ctx, tenantID, mailboxID, id)
}

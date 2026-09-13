package app

import (
	"context"
	"fmt"

	"github.com/alonsosss/corforce-email/services/access-control/internal/ports"
	"github.com/google/uuid"
)

// DeletedAccountsUseCase retira lo que access-control guarda de una cuenta que identity borro
// (identity.user.deleted): sus asignaciones de roles y su politica cacheada. No conceden nada
// mientras la cuenta no existe, porque el gateway rechaza su token antes, pero una cuenta
// restaurada o un id reutilizado las heredaria.
type DeletedAccountsUseCase struct {
	roles ports.DeletedAccountRoles
	// cache es nil sin Redis.
	cache ports.PolicyCache
}

func NewDeletedAccountsUseCase(roles ports.DeletedAccountRoles, cache ports.PolicyCache) *DeletedAccountsUseCase {
	return &DeletedAccountsUseCase{roles: roles, cache: cache}
}

// ForgetDeletedUser es idempotente: repetirlo, o recibirlo despues de que la baja de la
// empresa ya retirara sus roles, no borra nada mas. La politica cacheada se descarta siempre:
// es barato y una entrada anterior a la baja no debe sobrevivirla.
func (uc *DeletedAccountsUseCase) ForgetDeletedUser(ctx context.Context, tenantID, userID uuid.UUID) (int64, error) {
	removed, err := uc.roles.RemoveRolesOfDeletedUser(ctx, tenantID, userID)
	if err != nil {
		return 0, fmt.Errorf("retirar los roles de la cuenta borrada: %w", err)
	}
	if uc.cache != nil {
		uc.cache.InvalidateUsers(ctx, []uuid.UUID{userID})
	}
	return removed, nil
}

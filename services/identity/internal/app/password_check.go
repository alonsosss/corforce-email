package app

import (
	"context"

	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/alonsosss/corforce-email/services/identity/internal/ports"
	"go.uber.org/zap"
)

func breachEnabled(b ports.PasswordBreachChecker) bool { return b != nil && b.Enabled() }

// checkNewPassword es la unica puerta por la que entra una contrasena nueva: alta de
// usuario, cambio propio, reinicio por administrador y reinicio por enlace. Primero la
// politica de la empresa (forma), despues las filtraciones publicas (si ya esta en la
// lista de cualquier ataque por diccionario).
//
// Si el servicio de filtraciones no responde se admite la contrasena y se deja
// constancia: bloquear altas y cambios de contrasena por una caida ajena seria un
// segundo incidente. Un resultado positivo, en cambio, siempre rechaza.
func checkNewPassword(ctx context.Context, password string, policy *domain.PasswordPolicy, breach ports.PasswordBreachChecker, logger *zap.Logger) error {
	if err := ValidatePasswordPolicy(password, policy); err != nil {
		return err
	}
	if breach == nil {
		return nil
	}
	found, err := breach.IsBreached(ctx, password)
	if err != nil {
		if logger != nil {
			logger.Warn("comprobacion de contrasenas filtradas no disponible; se admite la contrasena", zap.Error(err))
		}
		return nil
	}
	if found {
		return domain.ErrPasswordBreached
	}
	return nil
}

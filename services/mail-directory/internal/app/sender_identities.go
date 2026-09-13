package app

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"go.uber.org/zap"
)

// MaxSenderIdentities acota la respuesta. Un buzon con mas direcciones concretas propias
// no es un caso real; si llegara, las que quedan fuera no se ofrecen en el webmail (que
// tampoco las admite), pero Postfix las sigue aceptando por SMTP.
const MaxSenderIdentities = 1000

// SenderIdentities devuelve las direcciones concretas con las que el buzon puede enviar,
// segun la regla de smtpd_sender_login_maps. La lista es de la celda, no de una empresa:
// la pide el webmail, que se autentico como ese buzon y no conoce su empresa.
func (uc *UseCase) SenderIdentities(ctx context.Context, username string) ([]string, error) {
	if uc.senders == nil {
		return nil, errors.New("mail-directory: repositorio de remitentes sin cablear")
	}
	login, _, err := domain.NormalizeEmail(username)
	if err != nil {
		return nil, err
	}
	addresses, err := uc.senders.ForLogin(ctx, login, MaxSenderIdentities)
	if err != nil {
		return nil, err
	}
	if len(addresses) == MaxSenderIdentities {
		uc.logger.Warn("mail-directory: el buzon alcanza el tope de remitentes que se enumeran",
			zap.String("username", login), zap.Int("max", MaxSenderIdentities))
	}
	return addresses, nil
}

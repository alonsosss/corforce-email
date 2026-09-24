package smtp

import (
	gosmtp "github.com/emersion/go-smtp"

	"github.com/alonsosss/corforce-email/services/smtp-relay/internal/domain"
)

// replies traduce cada rechazo a su respuesta SMTP (RFC 5321 y codigos mejorados de RFC 3463).
// Los textos no dicen nada interno: ni que servicio fallo, ni la empresa, ni el motivo exacto por
// el que una credencial no vale.
var replies = map[string]*gosmtp.SMTPError{
	domain.ErrAuthRequired.Reason:      {Code: 530, EnhancedCode: gosmtp.EnhancedCode{5, 7, 0}, Message: "Authentication required"},
	domain.ErrAuthFailed.Reason:        {Code: 535, EnhancedCode: gosmtp.EnhancedCode{5, 7, 8}, Message: "Authentication credentials invalid"},
	domain.ErrAuthUnavailable.Reason:   {Code: 454, EnhancedCode: gosmtp.EnhancedCode{4, 7, 0}, Message: "Temporary authentication failure"},
	domain.ErrBlocked.Reason:           {Code: 454, EnhancedCode: gosmtp.EnhancedCode{4, 7, 0}, Message: "Too many failed attempts, try again later"},
	domain.ErrConnectionLimit.Reason:   {Code: 421, EnhancedCode: gosmtp.EnhancedCode{4, 7, 0}, Message: "Too many connections, try again later"},
	domain.ErrMessageRate.Reason:       {Code: 451, EnhancedCode: gosmtp.EnhancedCode{4, 7, 1}, Message: "Message rate exceeded, try again later"},
	domain.ErrTooManyRecipients.Reason: {Code: 452, EnhancedCode: gosmtp.EnhancedCode{4, 5, 3}, Message: "Too many recipients"},
	domain.ErrInvalidAddress.Reason:    {Code: 553, EnhancedCode: gosmtp.EnhancedCode{5, 1, 3}, Message: "Invalid address"},
	domain.ErrTooLarge.Reason:          {Code: 552, EnhancedCode: gosmtp.EnhancedCode{5, 3, 4}, Message: "Message too big"},
	domain.ErrMalformed.Reason:         {Code: 554, EnhancedCode: gosmtp.EnhancedCode{5, 6, 0}, Message: "Malformed message"},
	domain.ErrInfected.Reason:          {Code: 554, EnhancedCode: gosmtp.EnhancedCode{5, 7, 1}, Message: "Message rejected: malware detected"},
	domain.ErrScanUnavailable.Reason:   {Code: 451, EnhancedCode: gosmtp.EnhancedCode{4, 3, 0}, Message: "Temporary failure, try again later"},
	domain.ErrKeyRevoked.Reason:        {Code: 535, EnhancedCode: gosmtp.EnhancedCode{5, 7, 8}, Message: "Authentication credentials no longer valid"},
	domain.ErrSenderNotVerified.Reason: {Code: 550, EnhancedCode: gosmtp.EnhancedCode{5, 7, 1}, Message: "Sender domain not verified for sending"},
	domain.ErrSendingDenied.Reason:     {Code: 554, EnhancedCode: gosmtp.EnhancedCode{5, 7, 1}, Message: "Sending not allowed for this account"},
	domain.ErrSendingThrottled.Reason:  {Code: 451, EnhancedCode: gosmtp.EnhancedCode{4, 7, 1}, Message: "Sending rate exceeded, try again later"},
	domain.ErrRejected.Reason:          {Code: 554, EnhancedCode: gosmtp.EnhancedCode{5, 6, 0}, Message: "Message rejected"},
	domain.ErrUpstream.Reason:          {Code: 451, EnhancedCode: gosmtp.EnhancedCode{4, 3, 0}, Message: "Temporary failure, try again later"},
}

// reply devuelve la respuesta del error; un error que no es un rechazo del dominio (un fallo al
// leer el DATA, el tope de tamano del propio servidor) se devuelve tal cual si ya es SMTP y como
// fallo temporal si no.
func reply(err error) error {
	if err == nil {
		return nil
	}
	if smtpErr, ok := err.(*gosmtp.SMTPError); ok {
		return smtpErr
	}
	if r, ok := replies[domain.AsRejection(err).Reason]; ok {
		return r
	}
	return replies[domain.ErrUpstream.Reason]
}

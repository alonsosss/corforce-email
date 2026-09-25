package ports

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

// MFARepository guarda la verificacion en dos pasos de los buzones (mail.mailbox_mfa). Una fila por
// buzon, solo mientras esta activa; sin fila, domain.ErrNotFound.
type MFARepository interface {
	Get(ctx context.Context, tenantID, mailboxID uuid.UUID) (*domain.MailboxMFA, error)
	// Create da de alta la fila; domain.ErrAlreadyExists si el buzon ya tenia una.
	Create(ctx context.Context, m *domain.MailboxMFA) error
	// AdvanceStep guarda step como ultimo paso TOTP aceptado solo si es mayor que el guardado, en una
	// sola sentencia: false es un codigo ya usado (o de un paso anterior), aunque lleguen dos a la vez.
	AdvanceStep(ctx context.Context, tenantID, mailboxID uuid.UUID, step int64) (bool, error)
	// ConsumeRecoveryCode retira el hash de los codigos que quedan, en una sola sentencia: false si no
	// estaba. remaining son los que quedan despues.
	ConsumeRecoveryCode(ctx context.Context, tenantID, mailboxID uuid.UUID, hash string) (remaining int, ok bool, err error)
	// ReplaceRecoveryCodes sustituye todos los codigos de recuperacion por los de hashes.
	ReplaceRecoveryCodes(ctx context.Context, tenantID, mailboxID uuid.UUID, hashes []string) error
	// Delete borra la fila y dice si habia una.
	Delete(ctx context.Context, tenantID, mailboxID uuid.UUID) (bool, error)
}

// MailPolicyRepository guarda la politica de correo por empresa (mail.mail_policy). Sin fila,
// domain.ErrNotFound.
type MailPolicyRepository interface {
	// Lock toma el cerrojo de la politica de la empresa hasta el final de la transaccion: compartido
	// para quien guarda filtros con la politica que lee, exclusivo para quien la cambia. Asi un
	// reenvio externo no se guarda con la politica vieja mientras otra transaccion la apaga y
	// retira los ya guardados.
	Lock(ctx context.Context, tenantID uuid.UUID, exclusive bool) error
	Get(ctx context.Context, tenantID uuid.UUID) (*domain.MailPolicy, error)
	// Upsert crea la fila de la empresa o la reemplaza entera.
	Upsert(ctx context.Context, p *domain.MailPolicy) error
}

// SecretSealer cifra lo que el directorio guarda y nunca devuelve (el secreto TOTP). aad ata el dato
// a su fila: copiado a otra no se abre.
type SecretSealer interface {
	Seal(plain, aad []byte) ([]byte, error)
	Open(sealed, aad []byte) ([]byte, error)
}

// TOTPVerifier comprueba un codigo TOTP (RFC 6238) a una hora y devuelve el paso de 30 s en que
// coincide, con el que se impide repetirlo.
type TOTPVerifier interface {
	ValidateStep(secret, code string, now time.Time) (step int64, ok bool)
}

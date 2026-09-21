package app

import (
	"context"
	"errors"
	"strings"

	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/google/uuid"
)

// VerifiedCredential es el buzon al que abre una credencial de destino y el trabajo al que pertenece.
type VerifiedCredential struct {
	TenantID  uuid.UUID
	JobID     uuid.UUID
	MailboxID uuid.UUID
	Username  string
}

// VerifyDestinationCredential decide si token abre el buzon username. Lo pregunta mail-auth cada vez que
// Dovecot recibe una contrasena con el prefijo de trabajo. Abre solo si el trabajo esta en curso, con el
// lease vigente y sin cancelacion pedida, el secreto coincide y el buzon del trabajo es username; cualquier
// otra cosa es domain.ErrCredentialInvalid, sin decir cual. Un fallo de infraestructura se devuelve tal
// cual para que quien pregunta no lo confunda con un rechazo.
func (uc *UseCase) VerifyDestinationCredential(ctx context.Context, token, username string) (VerifiedCredential, error) {
	if !uc.cfg.JobCredentials {
		return VerifiedCredential{}, domain.ErrCredentialInvalid
	}
	parsed, err := domain.ParseDestinationToken(token)
	if err != nil {
		return VerifiedCredential{}, err
	}
	tctx, err := uc.tenants.For(ctx, parsed.TenantID)
	if err != nil {
		if errors.Is(err, domain.ErrTenantUnknown) {
			return VerifiedCredential{}, domain.ErrCredentialInvalid
		}
		return VerifiedCredential{}, err
	}
	target, err := uc.repo.DestinationTarget(tctx, parsed.TenantID, parsed.JobID, uc.now())
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return VerifiedCredential{}, domain.ErrCredentialInvalid
		}
		return VerifiedCredential{}, err
	}
	if !parsed.Matches(target.Hash) || !strings.EqualFold(strings.TrimSpace(username), target.MailboxUsername) {
		return VerifiedCredential{}, domain.ErrCredentialInvalid
	}
	return VerifiedCredential{TenantID: parsed.TenantID, JobID: parsed.JobID, MailboxID: target.MailboxID, Username: target.MailboxUsername}, nil
}

// Package accesscontrol resuelve la credencial SMTP con access-control por pkg/apikey: la misma
// resolucion, cache y marca de revocacion que usa el gateway para el API de envio.
package accesscontrol

import (
	"context"
	"errors"
	"fmt"

	"github.com/alonsosss/corforce-email/pkg/apikey"
	"github.com/alonsosss/corforce-email/services/smtp-relay/internal/domain"
)

type resolver interface {
	Resolve(ctx context.Context, token, clientIP string) (*apikey.Principal, error)
}

// Authenticator implementa ports.Authenticator. Una clave valida sin el permiso de envio
// (transactional/messages/create) no sirve como credencial SMTP.
type Authenticator struct {
	resolver resolver
}

func New(r resolver) *Authenticator { return &Authenticator{resolver: r} }

func (a *Authenticator) Authenticate(ctx context.Context, token, clientIP string) (*domain.Credential, error) {
	p, err := a.resolver.Resolve(ctx, token, clientIP)
	switch {
	case errors.Is(err, apikey.ErrInvalid):
		return nil, domain.ErrAuthFailed
	case err != nil:
		return nil, fmt.Errorf("%w: %v", domain.ErrAuthUnavailable, err)
	}
	if !p.Allows(domain.SendModule, domain.SendResource, domain.SendAction) {
		return nil, domain.ErrAuthFailed
	}
	return &domain.Credential{KeyID: p.ID, TenantID: p.TenantID, Prefix: p.Prefix, Token: token}, nil
}

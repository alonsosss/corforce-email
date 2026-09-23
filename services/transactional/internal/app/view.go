package app

import (
	"context"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/alonsosss/corforce-email/services/transactional/internal/ports"
	"github.com/google/uuid"
)

// reservedFor son las variables reservadas de un mensaje renderizado para un destinatario:
// su enlace de baja, su enlace de ver en el navegador (con la caducidad de
// VIEW_IN_BROWSER_TTL) y su direccion.
func (uc *UseCase) reservedFor(tenantID, messageID uuid.UUID, email string) ports.ReservedVariables {
	return ports.ReservedVariables{
		UnsubscribeURL: uc.links.UnsubscribeURL(domain.UnsubscribeClaims{TenantID: tenantID, MessageID: messageID, Email: email}),
		ViewInBrowserURL: uc.links.ViewInBrowserURL(domain.ViewClaims{
			TenantID: tenantID, MessageID: messageID, ExpiresAt: uc.now().Add(uc.cfg.ViewInBrowserTTL),
		}),
		RecipientEmail: email,
	}
}

// VerifyViewLink comprueba la firma y la caducidad del enlace de ver en el navegador. La
// firma va primero: un enlace alterado nunca revela si habria caducado.
func (uc *UseCase) VerifyViewLink(claims domain.ViewClaims, signature string) error {
	if !uc.links.VerifyView(claims, signature) {
		return domain.ErrInvalidSignature
	}
	if !uc.now().Before(claims.ExpiresAt) {
		return domain.ErrLinkExpired
	}
	return nil
}

// ViewedMessage es el cuerpo de un mensaje tal como se guardo al renderizarlo para su
// destinatario.
type ViewedMessage struct {
	HTML string
	Text string
}

// ViewMessage devuelve el correo tal como se envio. Se sirve lo guardado al renderizar, no
// un render nuevo: la plantilla puede haber cambiado despues y el enlace promete el correo
// que se recibio.
func (uc *UseCase) ViewMessage(ctx context.Context, claims domain.ViewClaims, signature string) (*ViewedMessage, error) {
	if err := uc.VerifyViewLink(claims, signature); err != nil {
		return nil, err
	}
	msg, err := uc.repo.GetMessage(ctx, claims.TenantID, claims.MessageID)
	if err != nil {
		return nil, err
	}
	out := &ViewedMessage{}
	if msg.HTML != nil {
		out.HTML = *msg.HTML
	}
	if msg.Text != nil {
		out.Text = *msg.Text
	}
	if out.HTML == "" && out.Text == "" {
		return nil, domain.ErrNotFound
	}
	return out, nil
}

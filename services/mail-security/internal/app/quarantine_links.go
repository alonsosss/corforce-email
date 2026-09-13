package app

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// LinkRequest es lo que trae un enlace del aviso de cuarentena. La accion sale de la ruta,
// no de la URL: la firma la incluye, asi que un enlace de liberar no descarta.
type LinkRequest struct {
	TenantID  uuid.UUID
	QHash     string
	ExpiresAt int64
	Signature string
	Action    domain.QuarantineLinkAction
}

// LinkClient es quien pulsa el boton, para dejar constancia del uso.
type LinkClient struct {
	IP        string
	UserAgent string
}

// CheckLink comprueba, sin cambiar nada, que el enlace sigue sirviendo y devuelve el
// mensaje. Todo fallo del enlace es domain.ErrInvalidLink.
func (uc *QuarantineUseCase) CheckLink(ctx context.Context, req LinkRequest) (*domain.QuarantineItem, error) {
	var item *domain.QuarantineItem
	err := uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		var err error
		item, err = uc.findLinked(ctx, req)
		return err
	})
	return item, uc.linkError(err, req)
}

// ReleaseByLink libera el mensaje del enlace con el caso de uso de siempre (reinyeccion por
// el puerto interno, borrado y evento en una transaccion con la fila bloqueada) y registra
// el uso del enlace en esa misma transaccion.
func (uc *QuarantineUseCase) ReleaseByLink(ctx context.Context, req LinkRequest, client LinkClient) error {
	if req.Action != domain.LinkRelease {
		return domain.ErrInvalidLink
	}
	var id uuid.UUID
	err := uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		item, err := uc.findLinked(ctx, req)
		if err != nil {
			return err
		}
		id = item.ID
		return nil
	})
	if err != nil {
		return uc.linkError(err, req)
	}
	err = uc.release(ctx, req.TenantID, id, "", func(ctx context.Context, locked *domain.QuarantineItem) error {
		return uc.recordLinkUse(ctx, req, locked, client)
	})
	return uc.linkError(err, req)
}

// DiscardByLink borra el mensaje del enlace y registra el uso en la misma transaccion.
func (uc *QuarantineUseCase) DiscardByLink(ctx context.Context, req LinkRequest, client LinkClient) error {
	if req.Action != domain.LinkDiscard {
		return domain.ErrInvalidLink
	}
	err := uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		item, err := uc.findLinked(ctx, req)
		if err != nil {
			return err
		}
		locked, err := uc.notices.LockForLink(ctx, req.TenantID, item.ID)
		if err != nil {
			return err
		}
		if err := uc.recordLinkUse(ctx, req, locked, client); err != nil {
			return err
		}
		return uc.repo.Delete(ctx, req.TenantID, locked.ID)
	})
	return uc.linkError(err, req)
}

// findLinked encuentra la fila por qhash y comprueba la firma contra su id. La caducidad y
// la forma se comprueban antes de consultar la base.
func (uc *QuarantineUseCase) findLinked(ctx context.Context, req LinkRequest) (*domain.QuarantineItem, error) {
	if uc.links == nil || uc.notices == nil || req.Action.Path() == "" || req.ExpiresAt <= uc.now().Unix() ||
		!domain.IsHexToken(req.QHash) || !domain.IsHexToken(req.Signature) {
		return nil, domain.ErrInvalidLink
	}
	item, err := uc.notices.FindByQHash(ctx, req.TenantID, req.QHash)
	if err != nil {
		return nil, err
	}
	claims := domain.QuarantineLinkClaims{TenantID: req.TenantID, MessageID: item.ID, Action: req.Action, ExpiresAt: req.ExpiresAt}
	if !uc.links.Verify(claims, req.Signature, uc.now()) {
		return nil, domain.ErrInvalidLink
	}
	return item, nil
}

// recordLinkUse corre con la fila bloqueada: vuelve a exigir que sea la del enlace (el
// qhash no ha cambiado) y registra el uso. La unicidad del registro es la garantia de un
// solo uso aunque dos peticiones lleguen a la vez.
func (uc *QuarantineUseCase) recordLinkUse(ctx context.Context, req LinkRequest, locked *domain.QuarantineItem, client LinkClient) error {
	if locked.QHash != req.QHash {
		return domain.ErrInvalidLink
	}
	return uc.notices.InsertLinkUse(ctx, &domain.QuarantineLinkUse{
		TenantID: req.TenantID, QuarantineID: locked.ID, Action: req.Action, Rcpt: locked.Rcpt,
		ClientIP: client.IP, UserAgent: truncateRunes(client.UserAgent, maxUserAgentRunes), UsedAt: uc.now(),
	})
}

// maxUserAgentRunes acota el user agent que se guarda como constancia del uso.
const maxUserAgentRunes = 512

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// linkError reduce los fallos del enlace a domain.ErrInvalidLink (fila inexistente, ya
// liberada o descartada, uso repetido) y registra los demas, que son de infraestructura.
func (uc *QuarantineUseCase) linkError(err error, req LinkRequest) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrInvalidLink), errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrLinkUsed):
		return domain.ErrInvalidLink
	}
	uc.logger.Error("enlace de cuarentena no atendido", zap.String("tenant", req.TenantID.String()),
		zap.String("action", string(req.Action)), zap.Error(err))
	return err
}

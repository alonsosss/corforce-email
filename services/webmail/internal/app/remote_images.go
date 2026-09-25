package app

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

// remoteImageProxy devuelve con que se reescribe cada imagen remota permitida al leer un mensaje del
// buzon de la sesion: su enlace firmado del proxy. nil (ninguna imagen remota se muestra) si el
// proxy no esta configurado o la sesion no tiene el identificador del buzon, que paga el cupo; una
// sesion abierta antes de que mail-auth lo devolviera tiene que volver a entrar para verlas.
func (s *Service) remoteImageProxy(sess domain.Session) func(string) (string, bool) {
	if s.imageProxy == nil || !domain.ValidUUID(sess.MailboxID) {
		return nil
	}
	now := s.clock()
	return func(src string) (string, bool) {
		link, err := domain.NewRemoteImageLink(src, sess.MailboxID, now, s.imageProxy.TTL)
		if err != nil {
			return "", false
		}
		return s.imageProxy.URL(link.Sign(s.imageProxy.Key)), true
	}
}

// remoteImagesOf resume las imagenes remotas de un mensaje leido: se consideran bloqueadas si las
// habia y no se muestra ninguna, porque el lector no lo pidio o porque no se pueden servir por el
// proxy.
func remoteImagesOf(clean domain.SanitizedHTML, allowed bool) domain.RemoteImages {
	shown := allowed && clean.Proxied
	return domain.RemoteImages{Present: clean.RemoteImages, Blocked: clean.RemoteImages && !shown, Proxied: shown}
}

// VerifyRemoteImage comprueba el enlace firmado del proxy: domain.ErrRemoteImageLinkInvalid si no lo
// firmo este webmail (o se altero) y domain.ErrRemoteImageLinkExpired si ya vencio.
func (s *Service) VerifyRemoteImage(signed domain.SignedRemoteImage) (domain.RemoteImageLink, error) {
	if s.imageProxy == nil {
		return domain.RemoteImageLink{}, domain.ErrRemoteImageLinkInvalid
	}
	return signed.Verify(s.imageProxy.Key, s.clock())
}

// FetchRemoteImage descarga la imagen de un enlace ya verificado y confirma por su firma de fichero
// que es uno de los mapas de bits admitidos. Los fallos del servidor remoto no se detallan al cliente:
// quedan en el registro con el host, nunca con la URL entera (puede llevar un identificador del
// destinatario).
func (s *Service) FetchRemoteImage(ctx context.Context, link domain.RemoteImageLink) (domain.RemoteImage, error) {
	if s.imageProxy == nil {
		return domain.RemoteImage{}, domain.ErrRemoteImageLinkInvalid
	}
	data, err := s.imageProxy.Fetcher.Fetch(ctx, link.URL)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrRemoteImageRefused), errors.Is(err, domain.ErrRemoteImageTooLarge):
		default:
			s.logger.Info("webmail: imagen remota no disponible", zap.String("host", remoteImageHost(link.URL)),
				zap.String("mailbox_id", link.MailboxID), zap.Error(err))
		}
		return domain.RemoteImage{}, err
	}
	contentType, ok := domain.SniffRemoteImage(data)
	if !ok {
		return domain.RemoteImage{}, domain.ErrRemoteImageNotImage
	}
	return domain.RemoteImage{ContentType: contentType, Data: data}, nil
}

func remoteImageHost(raw string) string {
	u, err := domain.ValidateRemoteImageURL(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

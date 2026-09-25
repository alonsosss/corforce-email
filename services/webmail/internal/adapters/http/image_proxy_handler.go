package http

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// ImageProxyPath es la ruta del proxy de imagenes remotas bajo BasePath.
//
// GET ImageProxyPath?u=<URL en base64url>&x=<caducidad Unix>&m=<buzon>&s=<firma en base64url>. No
// usa la cookie de sesion: la pide un iframe aislado que no la envia, y la firma es la autorizacion.
const ImageProxyPath = "/image-proxy"

// Parametros de la URL del proxy.
const (
	imageProxyParamURL       = "u"
	imageProxyParamExpires   = "x"
	imageProxyParamMailbox   = "m"
	imageProxyParamSignature = "s"
)

// imageProxyRetryAfter es lo que espera el navegador cuando el proxy esta lleno.
const imageProxyRetryAfter = "2"

// ImageProxyMetrics cuenta las peticiones al proxy por resultado (domain.RemoteImageOutcome*).
type ImageProxyMetrics interface {
	ImageProxyRequest(outcome string)
}

type noImageProxyMetrics struct{}

func (noImageProxyMetrics) ImageProxyRequest(string) {}

// RemoteImageURL es la URL del proxy de un enlace firmado, relativa al origen de la aplicacion.
func RemoteImageURL(s domain.SignedRemoteImage) string {
	q := url.Values{}
	q.Set(imageProxyParamURL, s.EncodedURL)
	q.Set(imageProxyParamExpires, s.Expires)
	q.Set(imageProxyParamMailbox, s.MailboxID)
	q.Set(imageProxyParamSignature, s.Signature)
	return BasePath + ImageProxyPath + "?" + q.Encode()
}

// imageGate acota las descargas simultaneas del proxy en el proceso: cada una retiene en memoria hasta
// el tope de bytes de una imagen.
type imageGate struct{ slots chan struct{} }

func newImageGate(capacity int) *imageGate { return &imageGate{slots: make(chan struct{}, capacity)} }

func (g *imageGate) acquire() (func(), bool) {
	select {
	case g.slots <- struct{}{}:
		return func() { <-g.slots }, true
	default:
		return nil, false
	}
}

// ImageProxy sirve una imagen remota firmada. Orden: cupo por IP (middleware), firma y caducidad, cupo
// del buzon que firma el enlace, hueco en el proceso y descarga. Las respuestas de error son cortas y no
// dicen nada del servidor remoto.
func (h *Handler) ImageProxy(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	link, err := h.app.VerifyRemoteImage(domain.SignedRemoteImage{
		EncodedURL: q.Get(imageProxyParamURL), Expires: q.Get(imageProxyParamExpires),
		MailboxID: q.Get(imageProxyParamMailbox), Signature: q.Get(imageProxyParamSignature),
	})
	if err != nil {
		h.imageProxyFailed(w, err)
		return
	}
	if allowed, reset := h.cfg.ImageProxyRateLimiter.AllowKey(r.Context(), link.MailboxID); !allowed {
		h.imageMetrics.ImageProxyRequest(domain.RemoteImageOutcomeRateLimited)
		writeRateLimited(w, reset)
		return
	}
	release, ok := h.images.acquire()
	if !ok {
		h.imageMetrics.ImageProxyRequest(domain.RemoteImageOutcomeBusy)
		w.Header().Set("Retry-After", imageProxyRetryAfter)
		response.Err(w, http.StatusServiceUnavailable, "IMAGE_PROXY_BUSY", "el proxy de imágenes está ocupado")
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.OperationTimeout)
	defer cancel()
	img, err := h.app.FetchRemoteImage(ctx, link)
	if err != nil {
		h.imageProxyFailed(w, err)
		return
	}
	h.imageMetrics.ImageProxyRequest(domain.RemoteImageOutcomeOK)
	hd := w.Header()
	hd.Set("Content-Type", img.ContentType)
	hd.Set("Content-Length", strconv.Itoa(len(img.Data)))
	hd.Set("Content-Disposition", "inline")
	hd.Set("Cache-Control", "private, max-age="+strconv.FormatInt(cacheSeconds(link.Expires, time.Now()), 10))
	hd.Del("Pragma")
	// La pide un documento de origen opaco (el iframe aislado del mensaje): con same-site el navegador
	// la bloquearia. La URL firmada solo la conoce quien leyo el mensaje.
	hd.Set("Cross-Origin-Resource-Policy", "cross-origin")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(img.Data)
}

// cacheSeconds es lo que el navegador puede reutilizar la imagen: hasta que caduca el enlace.
func cacheSeconds(expires, now time.Time) int64 {
	return max(int64(expires.Sub(now)/time.Second), 0)
}

// imageProxyErrors traduce cada fallo del proxy a su codigo HTTP, su codigo de error y su resultado
// en las metricas.
var imageProxyErrors = []struct {
	err     error
	status  int
	code    string
	outcome string
}{
	{domain.ErrRemoteImageLinkInvalid, http.StatusForbidden, "IMAGE_LINK_INVALID", domain.RemoteImageOutcomeInvalid},
	{domain.ErrRemoteImageLinkExpired, http.StatusGone, "IMAGE_LINK_EXPIRED", domain.RemoteImageOutcomeExpired},
	{domain.ErrRemoteImageRefused, http.StatusBadGateway, "IMAGE_UNAVAILABLE", domain.RemoteImageOutcomeRefused},
	{domain.ErrRemoteImageUnavailable, http.StatusBadGateway, "IMAGE_UNAVAILABLE", domain.RemoteImageOutcomeUpstream},
	{domain.ErrRemoteImageTooLarge, http.StatusBadGateway, "IMAGE_TOO_LARGE", domain.RemoteImageOutcomeTooLarge},
	{domain.ErrRemoteImageNotImage, http.StatusBadGateway, "IMAGE_TYPE_NOT_ALLOWED", domain.RemoteImageOutcomeNotImage},
}

func (h *Handler) imageProxyFailed(w http.ResponseWriter, err error) {
	for _, e := range imageProxyErrors {
		if errors.Is(err, e.err) {
			h.imageMetrics.ImageProxyRequest(e.outcome)
			response.Err(w, e.status, e.code, e.err.Error())
			return
		}
	}
	h.imageMetrics.ImageProxyRequest(domain.RemoteImageOutcomeUpstream)
	response.Err(w, http.StatusBadGateway, "IMAGE_UNAVAILABLE", domain.ErrRemoteImageUnavailable.Error())
}

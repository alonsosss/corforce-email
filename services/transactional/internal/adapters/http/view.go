package http

import (
	"bytes"
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// viewCSP es la politica del correo servido en el navegador. El HTML lo escribio la empresa y
// no se confia en el: nada se ejecuta (sin script-src y con sandbox sin allow-scripts), ningun
// formulario se envia, la pagina no se enmarca y solo carga imagenes, estilos y tipografias.
// Los enlaces siguen funcionando (allow-popups para los target=_blank, navegacion de la propia
// pagina al pulsar).
const viewCSP = "default-src 'none'; img-src https: data:; style-src 'unsafe-inline' https:; font-src https: data:; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'none'; " +
	"sandbox allow-popups allow-popups-to-escape-sandbox allow-top-navigation-by-user-activation"

var textViewTemplate = template.Must(template.New("text").Parse(`<!DOCTYPE html>
<html lang="es">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
</head>
<body style="margin:0;padding:24px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif">
<pre style="white-space:pre-wrap;word-break:break-word;font:inherit">{{.}}</pre>
</body>
</html>
`))

var (
	pageViewInvalid  = pageData{Title: "Enlace no válido", Body: "Este enlace no es válido o ha sido alterado."}
	pageViewExpired  = pageData{Title: "Enlace caducado", Body: "Este enlace ya no está disponible. El correo sigue en tu bandeja de entrada."}
	pageViewNotFound = pageData{Title: "Correo no disponible", Body: "Este correo ya no está disponible."}

	pageUnavailableView = pageData{Title: "Servicio no disponible", Body: "No se pudo mostrar el correo en este momento. Vuelve a intentarlo en unos minutos."}
)

func viewClaims(r *http.Request) (domain.ViewClaims, string, bool) {
	q := r.URL.Query()
	tenantID, err := uuid.Parse(q.Get("t"))
	if err != nil {
		return domain.ViewClaims{}, "", false
	}
	messageID, err := uuid.Parse(q.Get("m"))
	if err != nil {
		return domain.ViewClaims{}, "", false
	}
	expires, err := strconv.ParseInt(q.Get("x"), 10, 64)
	if err != nil || expires <= 0 {
		return domain.ViewClaims{}, "", false
	}
	return domain.ViewClaims{TenantID: tenantID, MessageID: messageID, ExpiresAt: time.Unix(expires, 0).UTC()}, q.Get("sig"), true
}

// ViewInBrowser sirve el correo tal como se envio a su destinatario, por el enlace firmado
// {{.view_in_browser_url}}. Sin sesion: la firma y la caducidad son la proteccion.
func (h *Handler) ViewInBrowser(w http.ResponseWriter, r *http.Request) {
	claims, sig, ok := viewClaims(r)
	if !ok {
		writePage(w, http.StatusForbidden, pageViewInvalid)
		return
	}
	if err := h.uc.VerifyViewLink(claims, sig); err != nil {
		writeViewError(w, err)
		return
	}
	pool, err := h.tenantDB.ResolveForTenant(r.Context(), claims.TenantID.String())
	if err != nil {
		if db.IsUnknownTenant(err) {
			writePage(w, http.StatusNotFound, pageViewNotFound)
			return
		}
		h.logger.Error("transactional: base de la empresa no disponible para ver un correo", zap.Error(err))
		writePage(w, http.StatusServiceUnavailable, pageUnavailableView)
		return
	}
	ctx := db.WithTenant(r.Context(), pool, claims.TenantID.String())
	msg, err := h.uc.ViewMessage(ctx, claims, sig)
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) && !errors.Is(err, domain.ErrInvalidSignature) && !errors.Is(err, domain.ErrLinkExpired) {
			h.logger.Error("transactional: no se pudo leer el correo para verlo en el navegador", zap.Error(err))
			writePage(w, http.StatusServiceUnavailable, pageUnavailableView)
			return
		}
		writeViewError(w, err)
		return
	}
	body := []byte(msg.HTML)
	if msg.HTML == "" {
		var buf bytes.Buffer
		if err := textViewTemplate.Execute(&buf, msg.Text); err != nil {
			writePage(w, http.StatusInternalServerError, pageUnavailableView)
			return
		}
		body = buf.Bytes()
	}
	hdr := w.Header()
	hdr.Set("Content-Type", "text/html; charset=utf-8")
	hdr.Set("Content-Security-Policy", viewCSP)
	hdr.Set("X-Frame-Options", "DENY")
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	hdr.Set("Referrer-Policy", "no-referrer")
	hdr.Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func writeViewError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrLinkExpired):
		writePage(w, http.StatusGone, pageViewExpired)
	case errors.Is(err, domain.ErrNotFound):
		writePage(w, http.StatusNotFound, pageViewNotFound)
	default:
		writePage(w, http.StatusForbidden, pageViewInvalid)
	}
}

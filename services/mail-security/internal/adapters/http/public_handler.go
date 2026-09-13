package http

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// linkBodyLimit acota el POST de los enlaces (un formulario sin campos).
const linkBodyLimit = 4 << 10

// Texto de terceros en la pagina de confirmacion: el mismo recorte que en el aviso.
const (
	pageSubjectRunes = 200
	pageSenderRunes  = 254
)

// publicRoutes son los enlaces del aviso de cuarentena, sin sesion: GET muestra la
// confirmacion y POST ejecuta. El gateway los declara en routes.json (public).
func (h *Handler) publicRoutes(r chi.Router) {
	r.Use(h.publicLimiter.Limit)
	for _, action := range []domain.QuarantineLinkAction{domain.LinkRelease, domain.LinkDiscard} {
		r.Get("/"+string(action), h.linkPage(action))
		r.Post("/"+string(action), h.linkSubmit(action))
	}
}

// linkRequest lee t (empresa), q (qhash), e (caducidad) y sig de la URL o, en el POST, del
// formulario. Solo comprueba la forma; la firma la comprueba el caso de uso.
func linkRequest(r *http.Request, action domain.QuarantineLinkAction) (app.LinkRequest, bool) {
	get := func(name string) string {
		if v := r.URL.Query().Get(name); v != "" {
			return v
		}
		if r.Method == http.MethodPost {
			return r.PostFormValue(name)
		}
		return ""
	}
	tenantID, err := uuid.Parse(get("t"))
	if err != nil {
		return app.LinkRequest{}, false
	}
	expires, err := strconv.ParseInt(get("e"), 10, 64)
	if err != nil {
		return app.LinkRequest{}, false
	}
	return app.LinkRequest{TenantID: tenantID, QHash: get("q"), ExpiresAt: expires, Signature: get("sig"), Action: action}, true
}

// linkContext fija la empresa del enlace y ninguna persona: la ruta es publica y lo que
// diga una cabecera de identidad no cuenta. Sin usuario, la transaccion no cambia de rol y
// el SQL filtra por la empresa firmada en el enlace.
func linkContext(r *http.Request, tenantID uuid.UUID) *http.Request {
	return r.WithContext(middleware.WithIdentity(r.Context(), "", tenantID.String()))
}

func linkFailed(w http.ResponseWriter, err error) {
	if errors.Is(err, domain.ErrInvalidLink) {
		writePage(w, http.StatusForbidden, pageInvalidLink)
		return
	}
	writePage(w, http.StatusServiceUnavailable, pageUnavailable)
}

// linkPage (GET) no ejecuta nada: muestra el boton si el enlace sigue sirviendo.
func (h *Handler) linkPage(action domain.QuarantineLinkAction) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := linkRequest(r, action)
		if !ok {
			writePage(w, http.StatusForbidden, pageInvalidLink)
			return
		}
		r = linkContext(r, req.TenantID)
		item, err := h.quarantine.CheckLink(r.Context(), req)
		if err != nil {
			linkFailed(w, err)
			return
		}
		subject := domain.CleanThirdPartyText(item.Subject, pageSubjectRunes)
		sender := domain.CleanThirdPartyText(item.Sender, pageSenderRunes)
		if action == domain.LinkRelease {
			writePage(w, http.StatusOK, pageConfirmRelease(r.URL.RequestURI(), subject, sender))
			return
		}
		writePage(w, http.StatusOK, pageConfirmDiscard(r.URL.RequestURI(), subject, sender))
	}
}

// linkSubmit (POST) libera o descarta y deja constancia con la ip que el gateway puso en
// X-Real-IP (el cliente no puede escribirla: el gateway la reemite) y el user agent.
func (h *Handler) linkSubmit(action domain.QuarantineLinkAction) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, linkBodyLimit)
		req, ok := linkRequest(r, action)
		if !ok {
			writePage(w, http.StatusForbidden, pageInvalidLink)
			return
		}
		r = linkContext(r, req.TenantID)
		client := app.LinkClient{IP: r.Header.Get("X-Real-IP"), UserAgent: r.UserAgent()}
		if action == domain.LinkRelease {
			if err := h.quarantine.ReleaseByLink(r.Context(), req, client); err != nil {
				linkFailed(w, err)
				return
			}
			writePage(w, http.StatusOK, pageReleased)
			return
		}
		if err := h.quarantine.DiscardByLink(r.Context(), req, client); err != nil {
			linkFailed(w, err)
			return
		}
		writePage(w, http.StatusOK, pageDiscarded)
	}
}

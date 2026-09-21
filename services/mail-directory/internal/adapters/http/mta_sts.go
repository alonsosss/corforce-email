package http

import (
	"errors"
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/go-chi/chi/v5"
)

// codeDNSUnavailable: no se pudo consultar el DNS del dominio para comprobar enforce.
const codeDNSUnavailable = "DNS_UNAVAILABLE"

type mtaSTSRequest struct {
	Mode string `json:"mode"`
}

func (h *Handler) mtaSTSRoutes(r chi.Router) {
	const res = "mta_sts"
	r.With(h.require(moduleDomains, res, actionRead)).Get("/", h.ListMTASTS)
	r.With(h.require(moduleDomains, res, actionRead)).Get("/{domain}", h.GetMTASTS)
	r.With(h.require(moduleDomains, res, actionUpdate)).Put("/{domain}", h.SetMTASTS)
}

// ListMTASTS lista los dominios de la empresa con su modo MTA-STS.
func (h *Handler) ListMTASTS(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	page, perPage, pg := pageFrom(r)
	items, total, err := h.uc.ListMTASTS(r.Context(), tenantID, pg)
	if err != nil {
		writeMTASTSError(w, err)
		return
	}
	response.JSONWithMeta(w, http.StatusOK, items, response.PageMeta(total, page, perPage))
}

// GetMTASTS y SetMTASTS son el modo de UN dominio de la empresa; un dominio que el directorio no
// tiene responde 404. Cambiar a enforce es la unica accion que puede dejar sin correo a la empresa:
// el caso de uso la valida.
func (h *Handler) GetMTASTS(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	state, err := h.uc.GetMTASTS(r.Context(), tenantID, chi.URLParam(r, "domain"))
	if err != nil {
		writeMTASTSError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, state)
}

func (h *Handler) SetMTASTS(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req mtaSTSRequest
	if !decode(w, r, &req) {
		return
	}
	state, err := h.uc.SetMTASTSMode(r.Context(), tenantID, chi.URLParam(r, "domain"), req.Mode)
	if err != nil {
		writeMTASTSError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, state)
}

// PublicMTASTS sirve la politica que descargan otros servidores de correo (RFC 8461, 3.3), sin
// sesion. {cell} solo enruta en el gateway. Un dominio sin politica publicada, inactivo o que no es
// de la celda responde 404 igual: no revela cuales son de la plataforma.
func (h *Handler) PublicMTASTS(w http.ResponseWriter, r *http.Request) {
	body, err := h.uc.PublishedMTASTS(r.Context(), chi.URLParam(r, "domain"))
	if err != nil {
		w.Header().Set("Cache-Control", "no-store")
		writeMTASTSError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

func writeMTASTSError(w http.ResponseWriter, err error) {
	if errors.Is(err, domain.ErrMTASTSDNSUnavailable) {
		response.Err(w, http.StatusServiceUnavailable, codeDNSUnavailable, err.Error())
		return
	}
	writeError(w, err)
}

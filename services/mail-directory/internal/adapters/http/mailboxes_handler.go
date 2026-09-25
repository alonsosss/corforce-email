package http

import (
	"net/http"
	"strconv"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/go-chi/chi/v5"
)

// maxSieveBody cubre dos scripts de 64 KiB mas el envoltorio JSON.
const maxSieveBody = 3 * domain.MaxSieveScriptBytes

func (h *Handler) mailboxRoutes(r chi.Router) {
	const res, apRes, sieveRes = "mailboxes", "app_passwords", "sieve"
	r.With(h.require(moduleMailboxes, res, actionRead)).Get("/", h.ListMailboxes)
	r.With(h.require(moduleMailboxes, res, actionCreate)).Post("/", h.CreateMailbox)
	r.With(h.require(moduleMailboxes, res, actionRead)).Get("/{id}", getOf(h.uc.GetMailbox))
	r.With(h.require(moduleMailboxes, res, actionUpdate)).Patch("/{id}", h.UpdateMailbox)
	r.With(h.require(moduleMailboxes, res, actionDelete)).Delete("/{id}", deleteOf(h.uc.DeleteMailbox))
	r.With(h.require(moduleMailboxes, res, actionSetPassword)).Post("/{id}/password", h.SetMailboxPassword)
	r.With(h.require(moduleMailboxes, res, actionRead)).Get("/{id}/quota", getOf(h.uc.MailboxQuota))
	r.With(h.require(moduleMailboxes, res, actionRead)).Get("/{id}/logins", h.MailboxLogins)
	r.With(h.require(moduleMailboxes, resourceMailboxMFA, actionDelete)).Delete("/{id}/mfa", h.ResetMailboxMFA)

	r.With(h.require(moduleMailboxes, apRes, actionRead)).Get("/{id}/app-passwords", h.ListAppPasswords)
	r.With(h.require(moduleMailboxes, apRes, actionCreate)).Post("/{id}/app-passwords", h.CreateAppPassword)
	r.With(h.require(moduleMailboxes, apRes, actionUpdate)).Patch("/{id}/app-passwords/{apId}", h.UpdateAppPassword)
	r.With(h.require(moduleMailboxes, apRes, actionDelete)).Delete("/{id}/app-passwords/{apId}", h.DeleteAppPassword)

	r.With(h.require(moduleMailboxes, sieveRes, actionRead)).Get("/{id}/sieve", h.GetSieve)
	r.With(h.require(moduleMailboxes, sieveRes, actionUpdate)).Put("/{id}/sieve", h.PutSieve)
	// La respuesta automatica es un filtro mas del buzon: mismos permisos que sieve.
	r.With(h.require(moduleMailboxes, sieveRes, actionRead)).Get("/{id}/vacation", h.GetVacation)
	r.With(h.require(moduleMailboxes, sieveRes, actionUpdate)).Put("/{id}/vacation", h.PutVacation)
}

// ListMailboxes admite ?search= (subcadena de username o nombre visible, sin distinguir
// mayusculas) y ?domain= (exacto) ademas de la paginacion. El filtro va en el SQL, bajo
// RLS: la interfaz ya no trae la pagina maxima para filtrar en el navegador.
func (h *Handler) ListMailboxes(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	page, perPage, pg := pageFrom(r)
	q := r.URL.Query()
	items, total, err := h.uc.ListMailboxes(r.Context(), tenantID, ports.MailboxFilter{Search: q.Get("search"), Domain: q.Get("domain")}, pg)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSONWithMeta(w, http.StatusOK, items, response.PageMeta(total, page, perPage))
}

func (h *Handler) CreateMailbox(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req createMailboxRequest
	if !decode(w, r, &req) {
		return
	}
	v := validate.New()
	v.Required("local_part", req.LocalPart)
	v.Required("domain", req.Domain)
	v.Required("password", req.Password)
	v.MaxLength("display_name", req.DisplayName, domain.MaxDisplayNameLength)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	m, err := h.uc.CreateMailbox(r.Context(), tenantID, app.CreateMailboxRequest{
		LocalPart: req.LocalPart, Domain: req.Domain, Password: req.Password, DisplayName: req.DisplayName,
		QuotaBytes: req.QuotaBytes, Active: req.Active, Kind: req.Kind, TLSEnforceIn: req.TLSEnforceIn,
		TLSEnforceOut: req.TLSEnforceOut, IMAPAccess: req.IMAPAccess, POP3Access: req.POP3Access,
		SMTPAccess: req.SMTPAccess, SieveAccess: req.SieveAccess, DAVAccess: req.DAVAccess, ForcePwUpdate: req.ForcePwUpdate,
		RelayhostID: req.RelayhostID,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, m)
}

func (h *Handler) UpdateMailbox(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var req updateMailboxRequest
	if !decode(w, r, &req) {
		return
	}
	if req.DisplayName != nil {
		v := validate.New()
		v.MaxLength("display_name", *req.DisplayName, domain.MaxDisplayNameLength)
		if !v.Valid() {
			response.ErrValidation(w, v.Error())
			return
		}
	}
	m, err := h.uc.UpdateMailbox(r.Context(), tenantID, id, app.UpdateMailboxRequest{
		DisplayName: req.DisplayName, QuotaBytes: req.QuotaBytes, Active: req.Active, Kind: req.Kind,
		TLSEnforceIn: req.TLSEnforceIn, TLSEnforceOut: req.TLSEnforceOut, IMAPAccess: req.IMAPAccess,
		POP3Access: req.POP3Access, SMTPAccess: req.SMTPAccess, SieveAccess: req.SieveAccess, DAVAccess: req.DAVAccess,
		ForcePwUpdate: req.ForcePwUpdate, RelayhostID: req.RelayhostID.Value,
		ClearRelayhost: req.RelayhostID.Set && req.RelayhostID.Value == nil,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, m)
}

func (h *Handler) SetMailboxPassword(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var req setPasswordRequest
	if !decode(w, r, &req) {
		return
	}
	if err := h.uc.SetMailboxPassword(r.Context(), tenantID, id, req.Password); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) MailboxLogins(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	logins, err := h.uc.MailboxLogins(r.Context(), tenantID, id, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, logins)
}

// ── Contrasenas de aplicacion ─────────────────────────────────────────────────

func (h *Handler) ListAppPasswords(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	items, err := h.uc.ListAppPasswords(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, items)
}

// CreateAppPassword responde la contrasena generada una unica vez, junto al registro.
func (h *Handler) CreateAppPassword(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var req createAppPasswordRequest
	if !decode(w, r, &req) {
		return
	}
	v := validate.New()
	v.Required("name", req.Name)
	v.MaxLength("name", req.Name, 100)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	p, plain, err := h.uc.CreateAppPassword(r.Context(), tenantID, id, app.CreateAppPasswordRequest{
		Name: req.Name, IMAPAccess: req.IMAPAccess, POP3Access: req.POP3Access, SMTPAccess: req.SMTPAccess,
		SieveAccess: req.SieveAccess, DAVAccess: req.DAVAccess,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, map[string]interface{}{
		"app_password": p,
		"password":     plain,
	})
}

func (h *Handler) UpdateAppPassword(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	apID, ok := idParam(w, r, "apId")
	if !ok {
		return
	}
	var req updateAppPasswordRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Name != nil {
		v := validate.New()
		v.Required("name", *req.Name)
		v.MaxLength("name", *req.Name, 100)
		if !v.Valid() {
			response.ErrValidation(w, v.Error())
			return
		}
	}
	p, err := h.uc.UpdateAppPassword(r.Context(), tenantID, id, apID, app.UpdateAppPasswordRequest{
		Name: req.Name, IMAPAccess: req.IMAPAccess, POP3Access: req.POP3Access, SMTPAccess: req.SMTPAccess,
		SieveAccess: req.SieveAccess, DAVAccess: req.DAVAccess, Active: req.Active,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, p)
}

func (h *Handler) DeleteAppPassword(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	apID, ok := idParam(w, r, "apId")
	if !ok {
		return
	}
	if err := h.uc.DeleteAppPassword(r.Context(), tenantID, id, apID); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Sieve ─────────────────────────────────────────────────────────────────────

func (h *Handler) GetSieve(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	s, err := h.uc.GetMailboxSieve(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, s)
}

func (h *Handler) PutSieve(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var req putSieveRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxSieveBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	toScript := func(s *sieveScriptRequest) *app.SieveScript {
		if s == nil {
			return nil
		}
		return &app.SieveScript{ScriptDesc: s.ScriptDesc, ScriptData: s.ScriptData, Active: s.Active}
	}
	s, err := h.uc.PutMailboxSieve(r.Context(), tenantID, id, app.PutSieveRequest{
		Prefilter: toScript(req.Prefilter), Postfilter: toScript(req.Postfilter),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, s)
}

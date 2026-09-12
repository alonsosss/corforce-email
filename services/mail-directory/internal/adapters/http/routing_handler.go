package http

import (
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/go-chi/chi/v5"
)

func (h *Handler) routingRoutes(r chi.Router) {
	// Cada recurso de enrutado tiene su propio permiso (resource en snake_case).
	crud := func(path, resource string, list, get http.HandlerFunc, create, update http.HandlerFunc, del http.HandlerFunc) {
		r.Route(path, func(r chi.Router) {
			r.With(h.require(moduleRouting, resource, actionRead)).Get("/", list)
			r.With(h.require(moduleRouting, resource, actionCreate)).Post("/", create)
			r.With(h.require(moduleRouting, resource, actionRead)).Get("/{id}", get)
			r.With(h.require(moduleRouting, resource, actionUpdate)).Patch("/{id}", update)
			r.With(h.require(moduleRouting, resource, actionDelete)).Delete("/{id}", del)
		})
	}
	crud("/aliases", "aliases", listOf(h.uc.ListAliases), getOf(h.uc.GetAlias), h.CreateAlias, h.UpdateAlias, deleteOf(h.uc.DeleteAlias))
	crud("/spam-aliases", "spam_aliases", listOf(h.uc.ListSpamAliases), getOf(h.uc.GetSpamAlias), h.CreateSpamAlias, h.UpdateSpamAlias, deleteOf(h.uc.DeleteSpamAlias))
	crud("/sender-acl", "sender_acl", listOf(h.uc.ListSenderACL), getOf(h.uc.GetSenderACL), h.CreateSenderACL, h.UpdateSenderACL, deleteOf(h.uc.DeleteSenderACL))
	crud("/relayhosts", "relayhosts", listOf(h.uc.ListRelayhosts), getOf(h.uc.GetRelayhost), h.CreateRelayhost, h.UpdateRelayhost, deleteOf(h.uc.DeleteRelayhost))
	crud("/transports", "transports", h.ListTransports, h.GetTransport, h.CreateTransport, h.UpdateTransport, h.DeleteTransport)
	crud("/tls-policies", "tls_policies", listOf(h.uc.ListTLSPolicies), getOf(h.uc.GetTLSPolicy), h.CreateTLSPolicy, h.UpdateTLSPolicy, deleteOf(h.uc.DeleteTLSPolicy))
	crud("/recipient-maps", "recipient_maps", listOf(h.uc.ListRecipientMaps), getOf(h.uc.GetRecipientMap), h.CreateRecipientMap, h.UpdateRecipientMap, deleteOf(h.uc.DeleteRecipientMap))
	crud("/bcc-maps", "bcc_maps", listOf(h.uc.ListBCCMaps), getOf(h.uc.GetBCCMap), h.CreateBCCMap, h.UpdateBCCMap, deleteOf(h.uc.DeleteBCCMap))
}

// ── Aliases ───────────────────────────────────────────────────────────────────

func (h *Handler) CreateAlias(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req createAliasRequest
	if !decode(w, r, &req) {
		return
	}
	v := validate.New()
	v.Required("address", req.Address)
	v.Required("goto", req.Goto)
	v.MaxLength("private_comment", req.PrivateComment, 1000)
	v.MaxLength("public_comment", req.PublicComment, 1000)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	a, err := h.uc.CreateAlias(r.Context(), tenantID, app.CreateAliasRequest{
		Address: req.Address, Goto: req.Goto, SenderAllowed: req.SenderAllowed, Internal: req.Internal,
		Active: req.Active, PrivateComment: req.PrivateComment, PublicComment: req.PublicComment,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, a)
}

func (h *Handler) UpdateAlias(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var req updateAliasRequest
	if !decode(w, r, &req) {
		return
	}
	a, err := h.uc.UpdateAlias(r.Context(), tenantID, id, app.UpdateAliasRequest{
		Goto: req.Goto, SenderAllowed: req.SenderAllowed, Internal: req.Internal, Active: req.Active,
		PrivateComment: req.PrivateComment, PublicComment: req.PublicComment,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, a)
}

// ── Aliases temporales ────────────────────────────────────────────────────────

func (h *Handler) CreateSpamAlias(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req createSpamAliasRequest
	if !decode(w, r, &req) {
		return
	}
	v := validate.New()
	v.Required("address", req.Address)
	v.Required("goto", req.Goto)
	v.MaxLength("description", req.Description, 255)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	a, err := h.uc.CreateSpamAlias(r.Context(), tenantID, app.CreateSpamAliasRequest{
		Address: req.Address, Goto: req.Goto, Description: req.Description, ValidUntil: req.ValidUntil, Permanent: req.Permanent,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, a)
}

func (h *Handler) UpdateSpamAlias(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var req updateSpamAliasRequest
	if !decode(w, r, &req) {
		return
	}
	a, err := h.uc.UpdateSpamAlias(r.Context(), tenantID, id, app.UpdateSpamAliasRequest{
		Description: req.Description, ValidUntil: req.ValidUntil, Permanent: req.Permanent,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, a)
}

// ── Sender ACL ────────────────────────────────────────────────────────────────

func (h *Handler) CreateSenderACL(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req createSenderACLRequest
	if !decode(w, r, &req) {
		return
	}
	v := validate.New()
	v.Required("logged_in_as", req.LoggedInAs)
	v.Required("send_as", req.SendAs)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	a, err := h.uc.CreateSenderACL(r.Context(), tenantID, app.SenderACLRequest{
		LoggedInAs: req.LoggedInAs, SendAs: req.SendAs, External: req.External,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, a)
}

func (h *Handler) UpdateSenderACL(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var req updateSenderACLRequest
	if !decode(w, r, &req) {
		return
	}
	a, err := h.uc.UpdateSenderACL(r.Context(), tenantID, id, app.UpdateSenderACLRequest{SendAs: req.SendAs, External: req.External})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, a)
}

// ── Relayhosts ────────────────────────────────────────────────────────────────

func (h *Handler) CreateRelayhost(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req createRelayhostRequest
	if !decode(w, r, &req) {
		return
	}
	v := validate.New()
	v.Required("hostname", req.Hostname)
	v.MaxLength("username", req.Username, 255)
	v.MaxLength("password", req.Password, 255)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	rh, err := h.uc.CreateRelayhost(r.Context(), tenantID, app.CreateRelayhostRequest{
		Hostname: req.Hostname, Username: req.Username, Password: req.Password, Active: req.Active,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, rh)
}

func (h *Handler) UpdateRelayhost(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var req updateRelayhostRequest
	if !decode(w, r, &req) {
		return
	}
	rh, err := h.uc.UpdateRelayhost(r.Context(), tenantID, id, app.UpdateRelayhostRequest{
		Hostname: req.Hostname, Username: req.Username, Password: req.Password, Active: req.Active,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, rh)
}

// ── Transportes ───────────────────────────────────────────────────────────────

func (h *Handler) ListTransports(w http.ResponseWriter, r *http.Request) {
	scope, ok := scopeFrom(w, r)
	if !ok {
		return
	}
	page, perPage, pg := pageFrom(r)
	items, total, err := h.uc.ListTransports(r.Context(), scope, pg)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSONWithMeta(w, http.StatusOK, items, response.PageMeta(total, page, perPage))
}

func (h *Handler) GetTransport(w http.ResponseWriter, r *http.Request) {
	scope, ok := scopeFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	t, err := h.uc.GetTransport(r.Context(), scope, id)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, t)
}

func (h *Handler) CreateTransport(w http.ResponseWriter, r *http.Request) {
	scope, ok := scopeFrom(w, r)
	if !ok {
		return
	}
	var req createTransportRequest
	if !decode(w, r, &req) {
		return
	}
	v := validate.New()
	v.Required("destination", req.Destination)
	v.Required("nexthop", req.Nexthop)
	v.MaxLength("username", req.Username, 255)
	v.MaxLength("password", req.Password, 255)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	t, err := h.uc.CreateTransport(r.Context(), scope, app.CreateTransportRequest{
		Destination: req.Destination, Nexthop: req.Nexthop, Username: req.Username, Password: req.Password,
		IsMXBased: req.IsMXBased, Active: req.Active, Platform: req.Platform,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, t)
}

func (h *Handler) UpdateTransport(w http.ResponseWriter, r *http.Request) {
	scope, ok := scopeFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var req updateTransportRequest
	if !decode(w, r, &req) {
		return
	}
	t, err := h.uc.UpdateTransport(r.Context(), scope, id, app.UpdateTransportRequest{
		Destination: req.Destination, Nexthop: req.Nexthop, Username: req.Username, Password: req.Password,
		IsMXBased: req.IsMXBased, Active: req.Active,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, t)
}

func (h *Handler) DeleteTransport(w http.ResponseWriter, r *http.Request) {
	scope, ok := scopeFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.uc.DeleteTransport(r.Context(), scope, id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Politicas TLS ─────────────────────────────────────────────────────────────

func (h *Handler) CreateTLSPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req createTLSPolicyRequest
	if !decode(w, r, &req) {
		return
	}
	v := validate.New()
	v.Required("dest", req.Dest)
	v.Required("policy", req.Policy)
	v.MaxLength("parameters", req.Parameters, 255)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	p, err := h.uc.CreateTLSPolicy(r.Context(), tenantID, app.CreateTLSPolicyRequest{
		Dest: req.Dest, Policy: req.Policy, Parameters: req.Parameters, Active: req.Active,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, p)
}

func (h *Handler) UpdateTLSPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var req updateTLSPolicyRequest
	if !decode(w, r, &req) {
		return
	}
	p, err := h.uc.UpdateTLSPolicy(r.Context(), tenantID, id, app.UpdateTLSPolicyRequest{
		Policy: req.Policy, Parameters: req.Parameters, Active: req.Active,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, p)
}

// ── Mapas de destinatario ─────────────────────────────────────────────────────

func (h *Handler) CreateRecipientMap(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req createRecipientMapRequest
	if !decode(w, r, &req) {
		return
	}
	v := validate.New()
	v.Required("old_dest", req.OldDest)
	v.Required("new_dest", req.NewDest)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	m, err := h.uc.CreateRecipientMap(r.Context(), tenantID, app.CreateRecipientMapRequest{
		OldDest: req.OldDest, NewDest: req.NewDest, Active: req.Active,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, m)
}

func (h *Handler) UpdateRecipientMap(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var req updateRecipientMapRequest
	if !decode(w, r, &req) {
		return
	}
	m, err := h.uc.UpdateRecipientMap(r.Context(), tenantID, id, app.UpdateRecipientMapRequest{NewDest: req.NewDest, Active: req.Active})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, m)
}

// ── Mapas BCC ─────────────────────────────────────────────────────────────────

func (h *Handler) CreateBCCMap(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req createBCCMapRequest
	if !decode(w, r, &req) {
		return
	}
	v := validate.New()
	v.Required("local_dest", req.LocalDest)
	v.Required("bcc_dest", req.BCCDest)
	v.Required("type", req.Type)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	m, err := h.uc.CreateBCCMap(r.Context(), tenantID, app.CreateBCCMapRequest{
		LocalDest: req.LocalDest, BCCDest: req.BCCDest, Type: req.Type, Active: req.Active,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, m)
}

func (h *Handler) UpdateBCCMap(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var req updateBCCMapRequest
	if !decode(w, r, &req) {
		return
	}
	m, err := h.uc.UpdateBCCMap(r.Context(), tenantID, id, app.UpdateBCCMapRequest{BCCDest: req.BCCDest, Type: req.Type, Active: req.Active})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, m)
}

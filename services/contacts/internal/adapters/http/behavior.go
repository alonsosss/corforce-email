package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/contacts/internal/app"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

// Consultas internas de automations por contacto: si cumplen un segmento o una condicion
// (ramas de un flujo) y a quien le toca hoy el aniversario de un atributo de fecha
// (disparadores por fecha).

type matchRequest struct {
	SegmentID  *uuid.UUID      `json:"segment_id"`
	Definition json.RawMessage `json:"definition"`
	ContactIDs []uuid.UUID     `json:"contact_ids"`
}

// Match devuelve cuales de los contactos pedidos cumplen. Sin contact_ids solo valida el
// segmento o la definicion (422 si no vale, 404 si el segmento no existe).
func (h *Handler) Match(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req matchRequest
	if err := validate.DecodeJSONLimit(w, r, &req, segmentBodyLimit+membersBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	if len(req.ContactIDs) > app.MaxMatchIDs {
		response.ErrValidation(w, "contact_ids admite como maximo "+strconv.Itoa(app.MaxMatchIDs)+" contactos")
		return
	}
	definition := req.Definition
	if strings.TrimSpace(string(definition)) == "null" {
		definition = nil
	}
	ids, err := h.uc.MatchContacts(r.Context(), tenantID, app.MatchInput{
		SegmentID: req.SegmentID, Definition: definition, ContactIDs: req.ContactIDs,
	})
	if err != nil {
		writeBehaviorError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, membershipResponse{ContactIDs: ids})
}

type anniversaryRequest struct {
	Attribute        string     `json:"attribute"`
	Hour             *int       `json:"hour"`
	FallbackTimezone string     `json:"fallback_timezone"`
	ListID           *uuid.UUID `json:"list_id"`
	Cursor           string     `json:"cursor"`
	Limit            int        `json:"limit"`
}

type anniversaryResponse struct {
	Matches    []app.AnniversaryMatch `json:"matches"`
	NextCursor *string                `json:"next_cursor"`
}

// Anniversaries recorre por tandas los contactos cuyo aniversario del atributo cae hoy en
// su hora local (o la de respaldo). Solo devuelve ids y la fecha del aniversario: ni el
// valor del atributo ni la zona salen del servicio.
func (h *Handler) Anniversaries(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req anniversaryRequest
	if err := validate.DecodeJSONLimit(w, r, &req, defaultBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	if req.Hour == nil {
		v.Add("hour", "es obligatorio")
	}
	if req.Limit < 0 || req.Limit > app.MaxAnniversaryLimit {
		v.Add("limit", "debe estar entre 1 y "+strconv.Itoa(app.MaxAnniversaryLimit))
	}
	v.MaxLength("attribute", req.Attribute, 63)
	v.MaxLength("fallback_timezone", req.FallbackTimezone, 64)
	v.MaxLength("cursor", req.Cursor, 64)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	page, err := h.uc.Anniversaries(r.Context(), tenantID, app.AnniversaryInput{
		Attribute: req.Attribute, Hour: *req.Hour, FallbackTimezone: req.FallbackTimezone,
		ListID: req.ListID, Cursor: req.Cursor, Limit: req.Limit,
	})
	if err != nil {
		writeBehaviorError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, anniversaryResponse{Matches: page.Matches, NextCursor: page.NextCursor})
}

func writeBehaviorError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, app.ErrMatchSource), errors.Is(err, domain.ErrInvalidAnniversary):
		response.ErrValidation(w, err.Error())
	default:
		writeError(w, err)
	}
}

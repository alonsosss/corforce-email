package http

import (
	"net/http"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

// Rutas internas que el webmail llama para los ajustes de su propio buzon: tras RequireGatewayToken,
// solo servicios, sin empresa y con el buzon de la sesion en ?username=. El usuario nunca lo elige.

const (
	maxSignatureBody = domain.MaxSignatureHTMLBytes + domain.MaxSignatureTextBytes + 4096
	// maxFiltersBody cubre el peor caso de los topes (50 reglas de 10 condiciones de 256 caracteres de
	// hasta 4 bytes, escapados) con holgura.
	maxFiltersBody  = 2 << 20
	maxPasswordBody = 4096
)

type signatureRequest struct {
	Enabled   bool   `json:"enabled"`
	HTML      string `json:"html"`
	Text      string `json:"text"`
	OnReplies bool   `json:"on_replies"`
}

type signatureLimits struct {
	MaxHTMLBytes int `json:"max_html_bytes"`
	MaxTextBytes int `json:"max_text_bytes"`
}

type signatureResponse struct {
	Limits    signatureLimits `json:"limits"`
	Enabled   bool            `json:"enabled"`
	HTML      string          `json:"html"`
	Text      string          `json:"text"`
	OnReplies bool            `json:"on_replies"`
	UpdatedAt *time.Time      `json:"updated_at"`
}

func toSignatureResponse(s *domain.MailboxSignature) signatureResponse {
	return signatureResponse{
		Limits:  signatureLimits{MaxHTMLBytes: domain.MaxSignatureHTMLBytes, MaxTextBytes: domain.MaxSignatureTextBytes},
		Enabled: s.Enabled, HTML: s.HTML, Text: s.Text, OnReplies: s.OnReplies, UpdatedAt: updatedAt(s.UpdatedAt),
	}
}

func updatedAt(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	at := t.UTC()
	return &at
}

func (h *Handler) InternalGetSignature(w http.ResponseWriter, r *http.Request) {
	s, err := h.uc.SignatureByUsername(r.Context(), r.URL.Query().Get("username"))
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toSignatureResponse(s))
}

func (h *Handler) InternalPutSignature(w http.ResponseWriter, r *http.Request) {
	var req signatureRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxSignatureBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	s, err := h.uc.PutSignatureByUsername(r.Context(), r.URL.Query().Get("username"), app.PutSignatureRequest{
		Enabled: req.Enabled, HTML: req.HTML, Text: req.Text, OnReplies: req.OnReplies,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toSignatureResponse(s))
}

// filterRuleBody lleva el id como texto: vacio es una regla nueva, y uno mal escrito se senala en su
// campo en vez de romper la decodificacion entera.
type filterRuleBody struct {
	ID         string                   `json:"id"`
	Name       string                   `json:"name"`
	Enabled    bool                     `json:"enabled"`
	Match      string                   `json:"match"`
	Conditions []domain.FilterCondition `json:"conditions"`
	Actions    []domain.FilterAction    `json:"actions"`
	Stop       bool                     `json:"stop"`
}

type filtersRequest struct {
	Rules      []filterRuleBody  `json:"rules"`
	Forwarding domain.Forwarding `json:"forwarding"`
}

type filtersLimits struct {
	MaxRules            int `json:"max_rules"`
	MaxConditions       int `json:"max_conditions"`
	MaxActions          int `json:"max_actions"`
	MaxForwardAddresses int `json:"max_forward_addresses"`
	MaxValueLength      int `json:"max_value_length"`
	MaxNameLength       int `json:"max_name_length"`
	MaxFolderBytes      int `json:"max_folder_bytes"`
}

type filtersResponse struct {
	Limits     filtersLimits       `json:"limits"`
	Rules      []domain.FilterRule `json:"rules"`
	Forwarding domain.Forwarding   `json:"forwarding"`
	UpdatedAt  *time.Time          `json:"updated_at"`
}

func toFiltersResponse(f *domain.MailboxFilters) filtersResponse {
	return filtersResponse{
		Limits: filtersLimits{
			MaxRules: domain.MaxFilterRules, MaxConditions: domain.MaxFilterConditions, MaxActions: domain.MaxFilterActions,
			MaxForwardAddresses: domain.MaxForwardAddresses, MaxValueLength: domain.MaxFilterValueRunes,
			MaxNameLength: domain.MaxFilterNameRunes, MaxFolderBytes: domain.MaxFilterFolderBytes,
		},
		Rules: f.Rules, Forwarding: f.Forwarding, UpdatedAt: updatedAt(f.UpdatedAt),
	}
}

func (req filtersRequest) toApp() (app.PutFiltersRequest, error) {
	rules := make([]domain.FilterRule, 0, len(req.Rules))
	for i, r := range req.Rules {
		id := uuid.Nil
		if r.ID != "" {
			parsed, err := uuid.Parse(r.ID)
			if err != nil {
				return app.PutFiltersRequest{}, &domain.FieldError{Field: "rules[" + strconv.Itoa(i) + "].id", Reason: "no es un identificador"}
			}
			id = parsed
		}
		rules = append(rules, domain.FilterRule{
			ID: id, Name: r.Name, Enabled: r.Enabled, Match: r.Match, Conditions: r.Conditions, Actions: r.Actions, Stop: r.Stop,
		})
	}
	return app.PutFiltersRequest{Rules: rules, Forwarding: req.Forwarding}, nil
}

func (h *Handler) InternalGetFilters(w http.ResponseWriter, r *http.Request) {
	f, err := h.uc.FiltersByUsername(r.Context(), r.URL.Query().Get("username"))
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toFiltersResponse(f))
}

func (h *Handler) InternalPutFilters(w http.ResponseWriter, r *http.Request) {
	var req filtersRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxFiltersBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	in, err := req.toApp()
	if err != nil {
		writeError(w, err)
		return
	}
	f, err := h.uc.PutFiltersByUsername(r.Context(), r.URL.Query().Get("username"), in)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toFiltersResponse(f))
}

// InternalSetPassword cambia la contrasena del buzon del webmail, que ya comprobo la actual contra
// mail-auth. 204; el evento de credenciales revoca todas las sesiones del buzon.
func (h *Handler) InternalSetPassword(w http.ResponseWriter, r *http.Request) {
	var req setPasswordRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxPasswordBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	if err := h.uc.SetPasswordByUsername(r.Context(), r.URL.Query().Get("username"), req.Password); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

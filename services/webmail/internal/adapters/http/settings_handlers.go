package http

import (
	"net/http"
	"time"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

const (
	// maxSignatureBody y maxFiltersBody acotan el JSON antes de llegar al directorio, que aplica
	// sus propios topes (max_html_bytes, reglas, condiciones y acciones) y los sirve en limits.
	maxSignatureBody = 1 << 20
	maxFiltersBody   = 1 << 20
)

type signatureLimitsDTO struct {
	MaxHTMLBytes int `json:"max_html_bytes"`
	MaxTextBytes int `json:"max_text_bytes"`
}

type signatureDTO struct {
	Enabled   bool               `json:"enabled"`
	HTML      string             `json:"html"`
	Text      string             `json:"text"`
	OnReplies bool               `json:"on_replies"`
	UpdatedAt *time.Time         `json:"updated_at"`
	Limits    signatureLimitsDTO `json:"limits"`
}

func toSignatureDTO(s domain.Signature) signatureDTO {
	return signatureDTO{
		Enabled: s.Enabled, HTML: s.HTML, Text: s.Text, OnReplies: s.OnReplies, UpdatedAt: s.UpdatedAt,
		Limits: signatureLimitsDTO{MaxHTMLBytes: s.Limits.MaxHTMLBytes, MaxTextBytes: s.Limits.MaxTextBytes},
	}
}

type signatureRequest struct {
	Enabled   bool   `json:"enabled"`
	HTML      string `json:"html"`
	OnReplies bool   `json:"on_replies"`
}

func (h *Handler) Signature(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	sig, err := h.app.Signature(ctx, sessionFrom(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toSignatureDTO(sig))
}

// SetSignature guarda la firma del buzon de la sesion. El HTML lo sanea el servicio y de el sale el
// texto: lo que diga el cliente como texto no se usa.
func (h *Handler) SetSignature(w http.ResponseWriter, r *http.Request) {
	var req signatureRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxSignatureBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	sig, err := h.app.SetSignature(ctx, sessionFrom(r), req.Enabled, req.HTML, req.OnReplies)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toSignatureDTO(sig))
}

type filterConditionDTO struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

type filterActionDTO struct {
	Type     string `json:"type"`
	Folder   string `json:"folder,omitempty"`
	Address  string `json:"address,omitempty"`
	KeepCopy *bool  `json:"keep_copy,omitempty"`
}

type filterRuleDTO struct {
	ID         string               `json:"id"`
	Name       string               `json:"name"`
	Enabled    bool                 `json:"enabled"`
	Match      string               `json:"match"`
	Conditions []filterConditionDTO `json:"conditions"`
	Actions    []filterActionDTO    `json:"actions"`
	Stop       bool                 `json:"stop"`
}

type forwardingDTO struct {
	Enabled   bool     `json:"enabled"`
	Addresses []string `json:"addresses"`
	KeepCopy  bool     `json:"keep_copy"`
}

// filtersRequest lleva, opcionales, la contrasena actual y el codigo con los que el usuario confirma
// un reenvio externo nuevo cuando el directorio responde REAUTH_REQUIRED.
type filtersRequest struct {
	Rules           []filterRuleDTO `json:"rules"`
	Forwarding      forwardingDTO   `json:"forwarding"`
	CurrentPassword string          `json:"current_password"`
	Code            string          `json:"code"`
}

type filtersDTO struct {
	Rules      []filterRuleDTO `json:"rules"`
	Forwarding forwardingDTO   `json:"forwarding"`
	UpdatedAt  *time.Time      `json:"updated_at"`
	Limits     map[string]int  `json:"limits"`
}

func (req filtersRequest) toDomain() domain.MailFiltersInput {
	in := domain.MailFiltersInput{
		Rules:      make([]domain.FilterRule, len(req.Rules)),
		Forwarding: domain.Forwarding{Enabled: req.Forwarding.Enabled, Addresses: req.Forwarding.Addresses, KeepCopy: req.Forwarding.KeepCopy},
	}
	for i, r := range req.Rules {
		rule := domain.FilterRule{ID: r.ID, Name: r.Name, Enabled: r.Enabled, Match: r.Match, Stop: r.Stop,
			Conditions: make([]domain.FilterCondition, len(r.Conditions)), Actions: make([]domain.FilterAction, len(r.Actions))}
		for j, c := range r.Conditions {
			rule.Conditions[j] = domain.FilterCondition{Field: c.Field, Op: c.Op, Value: c.Value}
		}
		for j, a := range r.Actions {
			rule.Actions[j] = domain.FilterAction{Type: a.Type, Folder: a.Folder, Address: a.Address, KeepCopy: a.KeepCopy}
		}
		in.Rules[i] = rule
	}
	return in
}

func toFiltersDTO(f domain.MailFilters) filtersDTO {
	out := filtersDTO{
		Rules:      make([]filterRuleDTO, len(f.Rules)),
		Forwarding: forwardingDTO{Enabled: f.Forwarding.Enabled, Addresses: nonNil(f.Forwarding.Addresses), KeepCopy: f.Forwarding.KeepCopy},
		UpdatedAt:  f.UpdatedAt,
		Limits:     f.Limits,
	}
	if out.Limits == nil {
		out.Limits = map[string]int{}
	}
	for i, r := range f.Rules {
		rule := filterRuleDTO{ID: r.ID, Name: r.Name, Enabled: r.Enabled, Match: r.Match, Stop: r.Stop,
			Conditions: make([]filterConditionDTO, len(r.Conditions)), Actions: make([]filterActionDTO, len(r.Actions))}
		for j, c := range r.Conditions {
			rule.Conditions[j] = filterConditionDTO{Field: c.Field, Op: c.Op, Value: c.Value}
		}
		for j, a := range r.Actions {
			rule.Actions[j] = filterActionDTO{Type: a.Type, Folder: a.Folder, Address: a.Address, KeepCopy: a.KeepCopy}
		}
		out.Rules[i] = rule
	}
	return out
}

func (h *Handler) Filters(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	f, err := h.app.Filters(ctx, sessionFrom(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toFiltersDTO(f))
}

// SetFilters reemplaza las reglas y el reenvio del buzon de la sesion.
func (h *Handler) SetFilters(w http.ResponseWriter, r *http.Request) {
	var req filtersRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxFiltersBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	reauth := domain.Reauthentication{CurrentPassword: req.CurrentPassword, Code: req.Code}
	f, err := h.app.SetFilters(ctx, sessionFrom(r), req.toDomain(), reauth, clientIP(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toFiltersDTO(f))
}

type passwordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
	// Code es obligatorio con la verificacion en dos pasos activa (403 MFA_REQUIRED sin el).
	Code string `json:"code"`
}

// ChangePassword cambia la contrasena del buzon de la sesion. Con la actual incorrecta responde 401
// INVALID_CREDENTIALS y la sesion sigue abierta; con exito la sesion queda revocada y la cookie se
// borra.
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	var req passwordRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxLoginBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	if err := h.app.ChangePassword(ctx, sessionFrom(r), req.CurrentPassword, req.NewPassword, req.Code, clientIP(r)); err != nil {
		h.fail(w, r, err)
		return
	}
	h.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

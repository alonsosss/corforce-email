package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/contacts/internal/app"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const (
	// formBodyLimit acota la definicion de un formulario: 20 campos y sus textos caben de sobra.
	formBodyLimit = 64 << 10
	// defaultFormStatsDays es la ventana de estadisticas si no se pide otra.
	defaultFormStatsDays = 30
	// FormsPublicPath es donde cuelgan, por el gateway, las rutas publicas de los formularios.
	FormsPublicPath = "/api/v1/public/contacts/forms"
)

// formRoutes cuelga de /api/v1/contacts/forms (modulo contacts, recurso forms).
func (h *Handler) formRoutes(r chi.Router) {
	perm := h.perms.RequirePermission
	r.With(perm(modContacts, "forms", "read")).Get("/", h.ListForms)
	r.With(perm(modContacts, "forms", "create")).Post("/", h.CreateForm)
	r.With(perm(modContacts, "forms", "read")).Get("/meta", h.FormMeta)
	r.With(perm(modContacts, "forms", "read")).Get("/{formID}", h.GetForm)
	r.With(perm(modContacts, "forms", "update")).Patch("/{formID}", h.UpdateForm)
	r.With(perm(modContacts, "forms", "delete")).Delete("/{formID}", h.DeleteForm)
	r.With(perm(modContacts, "forms", "read")).Get("/{formID}/stats", h.FormStats)
}

// formEmbed son las direcciones publicas del formulario, todas bajo PUBLIC_BASE_URL.
type formEmbed struct {
	Key           string `json:"key"`
	IframeURL     string `json:"iframe_url"`
	ScriptURL     string `json:"script_url"`
	DefinitionURL string `json:"definition_url"`
	SubmitURL     string `json:"submit_url"`
}

type formResponse struct {
	*domain.SubscriptionForm
	Embed formEmbed `json:"embed"`
}

// FormKey es el identificador publico del formulario: empresa y formulario. No es secreto; lo
// que protege el envio es el token firmado, el origen y los cupos.
func FormKey(tenantID, formID uuid.UUID) string { return tenantID.String() + "." + formID.String() }

// parseFormKey es la inversa de FormKey.
func parseFormKey(key string) (tenantID, formID uuid.UUID, ok bool) {
	t, f, found := strings.Cut(key, ".")
	if !found || len(key) > 80 {
		return uuid.Nil, uuid.Nil, false
	}
	tid, err1 := uuid.Parse(t)
	fid, err2 := uuid.Parse(f)
	if err1 != nil || err2 != nil || tid == uuid.Nil || fid == uuid.Nil {
		return uuid.Nil, uuid.Nil, false
	}
	return tid, fid, true
}

func (h *Handler) formPublicURL(key, suffix string) string {
	return h.publicBaseURL + FormsPublicPath + "/" + key + suffix
}

func (h *Handler) toFormResponse(f *domain.SubscriptionForm) formResponse {
	key := FormKey(f.TenantID, f.ID)
	return formResponse{SubscriptionForm: f, Embed: formEmbed{
		Key:           key,
		IframeURL:     h.formPublicURL(key, "/embed"),
		ScriptURL:     h.formPublicURL(key, "/embed.js"),
		DefinitionURL: h.formPublicURL(key, ""),
		SubmitURL:     h.formPublicURL(key, "/submit"),
	}}
}

func userFrom(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(middleware.GetUserID(r.Context()))
	if err != nil || id == uuid.Nil {
		response.ErrUnauthorized(w, "la sesión no lleva usuario")
		return uuid.Nil, false
	}
	return id, true
}

type formRequest struct {
	Name           *string             `json:"name"`
	Status         *string             `json:"status"`
	ListID         *string             `json:"list_id"`
	Fields         *[]domain.FormField `json:"fields"`
	Texts          *domain.FormTexts   `json:"texts"`
	RedirectURL    json.RawMessage     `json:"redirect_url"`
	AllowedOrigins *[]string           `json:"allowed_origins"`
}

// redirect lee redirect_url: ausente no toca nada, null o "" la quita.
func (req *formRequest) redirect() (*app.OptionalString, error) {
	raw := bytes.TrimSpace(req.RedirectURL)
	if len(raw) == 0 {
		return nil, nil
	}
	if bytes.Equal(raw, []byte("null")) {
		return &app.OptionalString{}, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	if strings.TrimSpace(s) == "" {
		return &app.OptionalString{}, nil
	}
	return &app.OptionalString{Value: &s}, nil
}

func (req *formRequest) validate(v *validate.Validator, create bool) {
	if create {
		if req.Name == nil {
			v.Add("name", "es obligatorio")
		}
		if req.ListID == nil {
			v.Add("list_id", "es obligatorio")
		}
		if req.Fields == nil {
			v.Add("fields", "es obligatorio")
		}
		if req.Texts == nil {
			v.Add("texts", "es obligatorio")
		}
	}
	if req.ListID != nil {
		v.UUID("list_id", *req.ListID)
	}
	if req.Status != nil {
		v.OneOf("status", *req.Status, stringsOf(domain.FormStatuses()))
	}
}

func (h *Handler) CreateForm(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	userID, ok := userFrom(w, r)
	if !ok {
		return
	}
	var req formRequest
	if err := validate.DecodeJSONLimit(w, r, &req, formBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	req.validate(v, true)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	redirect, err := req.redirect()
	if err != nil {
		response.ErrValidation(w, "redirect_url debe ser una cadena o null")
		return
	}
	in := app.FormInput{
		Name: *req.Name, ListID: uuid.MustParse(strings.ToLower(*req.ListID)), Fields: *req.Fields, Texts: *req.Texts,
	}
	if req.Status != nil {
		in.Status = *req.Status
	}
	if redirect != nil {
		in.RedirectURL = redirect.Value
	}
	if req.AllowedOrigins != nil {
		in.AllowedOrigins = *req.AllowedOrigins
	}
	f, err := h.uc.CreateForm(r.Context(), tenantID, userID, in)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, h.toFormResponse(f))
}

func (h *Handler) ListForms(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	page, perPage := parsePagination(r)
	forms, total, err := h.uc.ListForms(r.Context(), tenantID, page, perPage)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]formResponse, len(forms))
	for i := range forms {
		out[i] = h.toFormResponse(&forms[i])
	}
	response.JSONWithMeta(w, http.StatusOK, out, response.PageMeta(total, page, perPage))
}

func (h *Handler) GetForm(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "formID")
	if !ok {
		return
	}
	f, err := h.uc.GetForm(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, h.toFormResponse(f))
}

func (h *Handler) UpdateForm(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "formID")
	if !ok {
		return
	}
	var req formRequest
	if err := validate.DecodeJSONLimit(w, r, &req, formBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	req.validate(v, false)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	redirect, err := req.redirect()
	if err != nil {
		response.ErrValidation(w, "redirect_url debe ser una cadena o null")
		return
	}
	p := app.FormPatch{
		Name: req.Name, Status: req.Status, Fields: req.Fields, Texts: req.Texts,
		RedirectURL: redirect, AllowedOrigins: req.AllowedOrigins,
	}
	if req.ListID != nil {
		listID := uuid.MustParse(strings.ToLower(*req.ListID))
		p.ListID = &listID
	}
	f, err := h.uc.UpdateForm(r.Context(), tenantID, id, p)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, h.toFormResponse(f))
}

func (h *Handler) DeleteForm(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "formID")
	if !ok {
		return
	}
	if err := h.uc.DeleteForm(r.Context(), tenantID, id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) FormStats(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "formID")
	if !ok {
		return
	}
	days := defaultFormStatsDays
	if raw := r.URL.Query().Get("days"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			response.ErrValidation(w, "days debe ser un número de días")
			return
		}
		days = n
	}
	st, err := h.uc.FormStats(r.Context(), tenantID, id, days)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, st)
}

type formLimitsMeta struct {
	MaxFields               int `json:"max_fields"`
	MaxNameLength           int `json:"max_name_length"`
	MaxLabelLength          int `json:"max_label_length"`
	MaxPlaceholderLength    int `json:"max_placeholder_length"`
	MaxTitleLength          int `json:"max_title_length"`
	MaxDescriptionLength    int `json:"max_description_length"`
	MaxSubmitLabelLength    int `json:"max_submit_label_length"`
	MaxConsentTextLength    int `json:"max_consent_text_length"`
	MaxSuccessMessageLength int `json:"max_success_message_length"`
	MaxAllowedOrigins       int `json:"max_allowed_origins"`
	MaxRedirectURLLength    int `json:"max_redirect_url_length"`
	MaxStatsDays            int `json:"max_stats_days"`
}

type formMetaResponse struct {
	BuiltinFields   []domain.FormFieldType `json:"builtin_fields"`
	Statuses        []domain.FormStatus    `json:"statuses"`
	Limits          formLimitsMeta         `json:"limits"`
	MinFillSeconds  int                    `json:"min_fill_seconds"`
	TokenTTLSeconds int                    `json:"token_ttl_seconds"`
}

func (h *Handler) FormMeta(w http.ResponseWriter, r *http.Request) {
	cfg := h.uc.FormPublicConfig()
	response.JSON(w, http.StatusOK, formMetaResponse{
		BuiltinFields: domain.BuiltinFormFields(),
		Statuses:      domain.FormStatuses(),
		Limits: formLimitsMeta{
			MaxFields: domain.MaxFormFields, MaxNameLength: domain.MaxFormNameLength,
			MaxLabelLength: domain.MaxFormLabelLength, MaxPlaceholderLength: domain.MaxFormPlaceholderLength,
			MaxTitleLength: domain.MaxFormTitleLength, MaxDescriptionLength: domain.MaxFormDescriptionLength,
			MaxSubmitLabelLength: domain.MaxFormSubmitLabelLength, MaxConsentTextLength: domain.MaxFormConsentTextLength,
			MaxSuccessMessageLength: domain.MaxFormSuccessMessageLen, MaxAllowedOrigins: domain.MaxFormAllowedOrigins,
			MaxRedirectURLLength: domain.MaxFormRedirectURLLength, MaxStatsDays: app.MaxFormStatsDays,
		},
		MinFillSeconds:  int(cfg.MinFill.Seconds()),
		TokenTTLSeconds: int(cfg.TokenTTL.Seconds()),
	})
}

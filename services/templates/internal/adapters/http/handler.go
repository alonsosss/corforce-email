package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/templates/internal/app"
	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/alonsosss/corforce-email/services/templates/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Modulo y recurso del permiso que exige el handler (tercera capa de control). Las
// acciones se siembran en migrations/registry/010_templates_permissions.sql.
const (
	permModule   = "templates"
	permResource = "templates"

	actionRead    = "read"
	actionCreate  = "create"
	actionUpdate  = "update"
	actionDelete  = "delete"
	actionPublish = "publish"
	actionRender  = "render"
)

// Topes de cuerpo. El contenido admite el HTML maximo mas el sobrecoste de escaparlo en
// JSON; un renderizado solo trae valores.
const (
	maxContentBody = 2 << 20
	maxRenderBody  = 1 << 20
)

type Handler struct {
	uc    *app.UseCase
	authz *authz.Checker
}

func NewHandler(uc *app.UseCase, checker *authz.Checker) *Handler {
	return &Handler{uc: uc, authz: checker}
}

func (h *Handler) perm(action string) func(http.Handler) http.Handler {
	return h.authz.RequirePermission(permModule, permResource, action)
}

// Routes es el API publico, montado en /api/v1/templates detras del gateway.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.With(h.perm(actionRead)).Get("/", h.ListTemplates)
	r.With(h.perm(actionCreate)).Post("/", h.CreateTemplate)
	r.With(h.perm(actionRead)).Get("/meta", h.Meta)
	r.Route("/{id}", func(r chi.Router) {
		r.With(h.perm(actionRead)).Get("/", h.GetTemplate)
		r.With(h.perm(actionUpdate)).Patch("/", h.UpdateTemplate)
		r.With(h.perm(actionDelete)).Delete("/", h.DeleteTemplate)
		r.With(h.perm(actionRead)).Get("/versions", h.ListVersions)
		r.With(h.perm(actionCreate)).Post("/versions", h.CreateVersion)
		r.With(h.perm(actionRead)).Get("/versions/{n}", h.GetVersion)
		r.With(h.perm(actionPublish)).Post("/versions/{n}/publish", h.PublishVersion)
		r.With(h.perm(actionRender)).Post("/render", h.Render)
		r.With(h.perm(actionRender)).Post("/preview", h.Preview)
	})
	return r
}

// InternalRoutes es el API servicio-a-servicio, montado en /internal/templates. La
// autenticacion (token interno) y la empresa (X-Tenant-ID) las resuelven los
// middlewares de main.go; aqui no hay usuario ni permiso que comprobar.
func (h *Handler) InternalRoutes() http.Handler {
	r := chi.NewRouter()
	r.Post("/{id}/render", h.InternalRender)
	return r
}

// ── Contexto y errores ───────────────────────────────────────────────────────

func tenantFrom(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "la sesion no lleva empresa")
		return uuid.Nil, false
	}
	return id, true
}

func userFrom(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(middleware.GetUserID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "la sesion no lleva usuario")
		return uuid.Nil, false
	}
	return id, true
}

func idParam(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.ErrBadRequest(w, "identificador de plantilla no valido")
		return uuid.Nil, false
	}
	return id, true
}

func versionParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	n, err := strconv.Atoi(chi.URLParam(r, "n"))
	if err != nil || n < 1 {
		response.ErrBadRequest(w, "numero de version no valido")
		return 0, false
	}
	return n, true
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrTemplateNotFound), errors.Is(err, domain.ErrVersionNotFound):
		response.ErrNotFound(w, err.Error())
	case errors.Is(err, domain.ErrTemplateNameTaken),
		errors.Is(err, domain.ErrTemplateNotArchived),
		errors.Is(err, domain.ErrTemplateArchived),
		errors.Is(err, domain.ErrVersionAlreadyPublished),
		errors.Is(err, domain.ErrNoPublishedVersion),
		errors.Is(err, domain.ErrVersionNotPublished):
		response.ErrConflict(w, err.Error())
	case errors.Is(err, domain.ErrInvalidTemplate),
		errors.Is(err, domain.ErrInvalidVariableDeclaration),
		errors.Is(err, domain.ErrInvalidVariables),
		errors.Is(err, domain.ErrOutputTooLarge),
		errors.Is(err, domain.ErrNothingToUpdate),
		errors.Is(err, domain.ErrInvalidName),
		errors.Is(err, domain.ErrInvalidKind),
		errors.Is(err, domain.ErrInvalidTemplateStatus):
		response.ErrValidation(w, err.Error())
	case errors.Is(err, domain.ErrMissingCreator):
		response.ErrUnauthorized(w, err.Error())
	default:
		response.Unexpected(w, err)
	}
}

// ── Catalogo ─────────────────────────────────────────────────────────────────

type reservedVariableMeta struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type limitsMeta struct {
	MaxNameLength        int `json:"max_name_length"`
	MaxDescriptionLength int `json:"max_description_length"`
	MaxVariables         int `json:"max_variables"`
	MaxSubjectBytes      int `json:"max_subject_bytes"`
	MaxHTMLBytes         int `json:"max_html_bytes"`
}

type metaResponse struct {
	Kinds             []string               `json:"kinds"`
	Statuses          []string               `json:"statuses"`
	VersionStatuses   []string               `json:"version_statuses"`
	VariableTypes     []string               `json:"variable_types"`
	ReservedVariables []reservedVariableMeta `json:"reserved_variables"`
	Limits            limitsMeta             `json:"limits"`
}

// Meta publica los valores del dominio que la interfaz necesita para validar y ofrecer
// opciones (tipos, estados, variables reservadas y topes), para que no los copie.
func (h *Handler) Meta(w http.ResponseWriter, r *http.Request) {
	reserved := domain.ReservedVariables()
	out := metaResponse{
		Kinds:             domain.Kinds(),
		Statuses:          domain.TemplateStatuses(),
		VersionStatuses:   domain.VersionStatuses(),
		VariableTypes:     domain.VariableTypes(),
		ReservedVariables: make([]reservedVariableMeta, 0, len(reserved)),
		Limits: limitsMeta{
			MaxNameLength: domain.MaxNameLength, MaxDescriptionLength: domain.MaxDescription,
			MaxVariables: domain.MaxVariables, MaxSubjectBytes: domain.MaxSubjectBytes,
			MaxHTMLBytes: domain.MaxHTMLBytes,
		},
	}
	for _, v := range reserved {
		out.ReservedVariables = append(out.ReservedVariables, reservedVariableMeta{Name: v.Name, Type: v.Type})
	}
	response.JSON(w, http.StatusOK, out)
}

// ── Plantillas ───────────────────────────────────────────────────────────────

type contentRequest struct {
	Subject   string            `json:"subject"`
	HTML      string            `json:"html"`
	Text      *string           `json:"text,omitempty"`
	Variables []domain.Variable `json:"variables"`
}

func (c contentRequest) validate(v *validate.Validator) {
	v.Required("subject", c.Subject)
	v.Required("html", c.HTML)
	if len(c.Variables) > domain.MaxVariables {
		v.Add("variables", "supera el maximo de "+strconv.Itoa(domain.MaxVariables))
	}
}

func (c contentRequest) toDomain() domain.Content {
	return domain.Content{Subject: c.Subject, HTML: c.HTML, Text: c.Text, Variables: c.Variables}
}

type createTemplateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Kind        string `json:"kind"`
	contentRequest
}

func (h *Handler) CreateTemplate(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	userID, ok := userFrom(w, r)
	if !ok {
		return
	}
	var req createTemplateRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxContentBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	v.Required("name", req.Name)
	v.MaxLength("name", req.Name, domain.MaxNameLength)
	v.MaxLength("description", req.Description, domain.MaxDescription)
	v.Required("kind", req.Kind)
	v.OneOf("kind", req.Kind, domain.Kinds())
	req.contentRequest.validate(v)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}

	t, ver, err := h.uc.CreateTemplate(r.Context(), tenantID, userID, app.CreateTemplateInput{
		Name: req.Name, Description: req.Description, Kind: req.Kind, Content: req.toDomain(),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	res := templateResponse(t)
	res["current"] = nil
	res["versions"] = []map[string]any{versionResponse(ver)}
	response.JSON(w, http.StatusCreated, res)
}

func (h *Handler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	perPage, _ := strconv.Atoi(q.Get("per_page"))
	page, perPage = app.NormalizePage(page, perPage)

	v := validate.New()
	v.OneOf("kind", q.Get("kind"), domain.Kinds())
	v.OneOf("status", q.Get("status"), domain.TemplateStatuses())
	v.MaxLength("search", q.Get("search"), domain.MaxNameLength)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}

	items, total, err := h.uc.ListTemplates(r.Context(), tenantID, ports.ListFilter{
		Kind: q.Get("kind"), Status: q.Get("status"), Search: q.Get("search"),
		Offset: (page - 1) * perPage, Limit: perPage,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, t := range items {
		out = append(out, templateResponse(t))
	}
	response.JSONWithMeta(w, http.StatusOK, out, response.PageMeta(total, page, perPage))
}

func (h *Handler) GetTemplate(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	detail, err := h.uc.GetTemplate(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	res := templateResponse(detail.Template)
	res["current"] = nil
	if detail.Current != nil {
		res["current"] = versionResponse(detail.Current)
	}
	summaries := make([]map[string]any, 0, len(detail.Versions))
	for _, s := range detail.Versions {
		summaries = append(summaries, versionSummaryResponse(s))
	}
	res["versions"] = summaries
	response.JSON(w, http.StatusOK, res)
}

type updateTemplateRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Status      *string `json:"status,omitempty"`
}

func (h *Handler) UpdateTemplate(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	var req updateTemplateRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	if req.Name != nil {
		v.Required("name", *req.Name)
		v.MaxLength("name", *req.Name, domain.MaxNameLength)
	}
	if req.Description != nil {
		v.MaxLength("description", *req.Description, domain.MaxDescription)
	}
	if req.Status != nil {
		v.Required("status", *req.Status)
		v.OneOf("status", *req.Status, domain.TemplateStatuses())
	}
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	t, err := h.uc.UpdateTemplate(r.Context(), tenantID, id, app.UpdateTemplateInput{
		Name: req.Name, Description: req.Description, Status: req.Status,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, templateResponse(t))
}

func (h *Handler) DeleteTemplate(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	if err := h.uc.DeleteTemplate(r.Context(), tenantID, id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Versiones ────────────────────────────────────────────────────────────────

func (h *Handler) ListVersions(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	versions, err := h.uc.ListVersions(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(versions))
	for _, s := range versions {
		out = append(out, versionSummaryResponse(s))
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) CreateVersion(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	userID, ok := userFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	var req contentRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxContentBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	req.validate(v)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	ver, err := h.uc.CreateVersion(r.Context(), tenantID, id, userID, req.toDomain())
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, versionResponse(ver))
}

func (h *Handler) GetVersion(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	n, ok := versionParam(w, r)
	if !ok {
		return
	}
	ver, err := h.uc.GetVersion(r.Context(), tenantID, id, n)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, versionResponse(ver))
}

func (h *Handler) PublishVersion(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	n, ok := versionParam(w, r)
	if !ok {
		return
	}
	ver, err := h.uc.PublishVersion(r.Context(), tenantID, id, n)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, versionResponse(ver))
}

// ── Renderizado ──────────────────────────────────────────────────────────────

type renderRequest struct {
	Version   *int                       `json:"version,omitempty"`
	Variables map[string]json.RawMessage `json:"variables"`
}

type internalRenderRequest struct {
	renderRequest
	Reserved map[string]string `json:"reserved"`
}

func (h *Handler) Render(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, false)
}

func (h *Handler) Preview(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, true)
}

func (h *Handler) render(w http.ResponseWriter, r *http.Request, preview bool) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	var req renderRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxRenderBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	if req.Version != nil && *req.Version < 1 {
		response.ErrValidation(w, "version debe ser mayor que cero")
		return
	}
	out, err := h.uc.Render(r.Context(), tenantID, id, app.RenderInput{
		Version: req.Version, Values: req.Variables, Preview: preview,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, renderedResponse(out))
}

// InternalRender es lo que llama transactional (y campaigns) por cada envio: siempre
// sobre una version publicada y con las variables reservadas ya resueltas.
func (h *Handler) InternalRender(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	var req internalRenderRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxRenderBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	if req.Version != nil && *req.Version < 1 {
		v.Add("version", "debe ser mayor que cero")
	}
	reservedNames := make([]string, 0, 4)
	for _, rv := range domain.ReservedVariables() {
		reservedNames = append(reservedNames, rv.Name)
	}
	for name := range req.Reserved {
		v.OneOf("reserved", name, reservedNames)
	}
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	out, err := h.uc.Render(r.Context(), tenantID, id, app.RenderInput{
		Version: req.Version, Values: req.Variables, Reserved: req.Reserved,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	// kind es el tipo real de la plantilla: transactional lo exige para que una plantilla
	// transaccional no salga por la via de marketing ni una de marketing por la transaccional.
	res := renderedResponse(out)
	res["kind"] = out.Kind
	response.JSON(w, http.StatusOK, res)
}

// ── Respuestas ───────────────────────────────────────────────────────────────

func templateResponse(t *domain.Template) map[string]any {
	return map[string]any{
		"id":              t.ID.String(),
		"name":            t.Name,
		"description":     t.Description,
		"kind":            t.Kind,
		"status":          t.Status,
		"current_version": t.CurrentVersion,
		"created_by":      t.CreatedBy.String(),
		"created_at":      t.CreatedAt,
		"updated_at":      t.UpdatedAt,
	}
}

func versionResponse(v *domain.Version) map[string]any {
	return map[string]any{
		"id":           v.ID.String(),
		"template_id":  v.TemplateID.String(),
		"version":      v.Version,
		"subject":      v.Subject,
		"html":         v.HTML,
		"text":         v.Text,
		"variables":    v.Variables,
		"status":       v.Status,
		"published_at": v.PublishedAt,
		"created_by":   v.CreatedBy.String(),
		"created_at":   v.CreatedAt,
	}
}

func versionSummaryResponse(s domain.VersionSummary) map[string]any {
	return map[string]any{
		"id":           s.ID.String(),
		"version":      s.Version,
		"status":       s.Status,
		"published_at": s.PublishedAt,
		"created_by":   s.CreatedBy.String(),
		"created_at":   s.CreatedAt,
	}
}

func renderedResponse(out *domain.Rendered) map[string]any {
	return map[string]any{
		"subject": out.Subject,
		"html":    out.HTML,
		"text":    out.Text,
		"version": out.Version,
	}
}

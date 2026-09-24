package http

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/suppression/internal/app"
	"github.com/alonsosss/corforce-email/services/suppression/internal/domain"
	"github.com/alonsosss/corforce-email/services/suppression/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const (
	permModule = "suppression"

	// Topes de cuerpo: la consulta previa al envio lleva hasta 1000 direcciones y la
	// carga masiva hasta 10000 (unos 320 bytes por direccion en el peor caso).
	checkBodyLimit   = 512 << 10
	importBodyLimit  = 4 << 20
	defaultBodyLimit = 64 << 10

	defaultPerPage = 20
	maxPerPage     = 100

	maxEmailLength  = 320
	maxDetailLength = 1000
)

// manualReasons son las causas que admite el alta por el API publico: los demas motivos
// los registra la plataforma a partir de un hecho (rebote, queja, baja).
func manualReasons() []string { return []string{string(domain.ReasonManual)} }

type Handler struct {
	uc    *app.UseCase
	authz *authz.Checker
}

func NewHandler(uc *app.UseCase, checker *authz.Checker) *Handler {
	return &Handler{uc: uc, authz: checker}
}

// PublicRoutes es lo que entra por el gateway con sesion de usuario. El gateway ya gateo
// el modulo; aqui cada accion exige su permiso concreto (tercera capa).
func (h *Handler) PublicRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/health", h.Health)
	r.With(h.authz.RequirePermission(permModule, "entries", "read")).Get("/meta", h.Meta)
	r.With(h.authz.RequirePermission(permModule, "entries", "read")).Post("/check", h.Check)
	r.Route("/entries", func(r chi.Router) {
		r.With(h.authz.RequirePermission(permModule, "entries", "read")).Get("/", h.ListEntries)
		r.With(h.authz.RequirePermission(permModule, "entries", "create")).Post("/", h.CreateEntry)
		r.With(h.authz.RequirePermission(permModule, "entries", "import")).Post("/import", h.ImportEntries)
		r.With(h.authz.RequirePermission(permModule, "entries", "read")).Get("/{id}", h.GetEntry)
		r.With(h.authz.RequirePermission(permModule, "entries", "delete")).Delete("/{id}", h.DeleteEntry)
	})
	r.With(h.authz.RequirePermission(permModule, "entries", "read")).Get("/imports", h.ListImports)
	r.With(h.authz.RequirePermission(permModule, "stats", "read")).Get("/stats", h.Stats)
	return r
}

// InternalRoutes es lo que llaman los demas servicios de la plataforma con el token
// interno y la empresa en X-Tenant-ID: la consulta previa a cada envio y el alta de una
// exclusion a partir de un hecho (baja por enlace, direccion invalida).
func (h *Handler) InternalRoutes() http.Handler {
	r := chi.NewRouter()
	r.Post("/check", h.Check)
	r.Post("/add", h.InternalAdd)
	return r
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// tenantFrom exige la empresa del contexto; sin ella no hay lista que consultar.
func tenantFrom(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "empresa no válida")
		return uuid.Nil, false
	}
	return id, true
}

func parseIDParam(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.ErrBadRequest(w, "identificador de exclusión no válido")
		return uuid.Nil, false
	}
	return id, true
}

func parsePagination(r *http.Request) (int, int) {
	page, perPage := 1, defaultPerPage
	if v, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && v > 0 {
		page = v
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("per_page")); err == nil && v > 0 && v <= maxPerPage {
		perPage = v
	}
	return page, perPage
}

// ── Catalogo ─────────────────────────────────────────────────────────────────

type reasonMeta struct {
	Reason    domain.Reason `json:"reason"`
	Severity  int           `json:"severity"`
	Removable bool          `json:"removable"`
}

type metaResponse struct {
	// Reasons va de mas a menos grave, el orden de domain.Reasons().
	Reasons         []reasonMeta `json:"reasons"`
	ManualReasons   []string     `json:"manual_reasons"`
	MaxCheckEmails  int          `json:"max_check_emails"`
	MaxImportEmails int          `json:"max_import_emails"`
	MaxPerPage      int          `json:"max_per_page"`
	MaxEmailLength  int          `json:"max_email_length"`
	MaxDetailLength int          `json:"max_detail_length"`
}

// Meta publica los motivos, cuales puede retirar un operador y los topes del API, para
// que la interfaz no los copie.
func (h *Handler) Meta(w http.ResponseWriter, r *http.Request) {
	reasons := domain.Reasons()
	out := metaResponse{
		Reasons:         make([]reasonMeta, 0, len(reasons)),
		ManualReasons:   manualReasons(),
		MaxCheckEmails:  app.MaxCheckEmails,
		MaxImportEmails: app.MaxImportEmails,
		MaxPerPage:      maxPerPage,
		MaxEmailLength:  maxEmailLength,
		MaxDetailLength: maxDetailLength,
	}
	for _, reason := range reasons {
		out.Reasons = append(out.Reasons, reasonMeta{Reason: reason, Severity: reason.Severity(), Removable: reason.Removable()})
	}
	response.JSON(w, http.StatusOK, out)
}

// ── Consulta previa al envio ─────────────────────────────────────────────────

type checkRequest struct {
	Emails []string `json:"emails"`
}

type checkResponse struct {
	Suppressed []domain.Suppressed `json:"suppressed"`
}

func (h *Handler) Check(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req checkRequest
	if err := validate.DecodeJSONLimit(w, r, &req, checkBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	if len(req.Emails) > app.MaxCheckEmails {
		response.ErrValidation(w, "emails admite como máximo "+strconv.Itoa(app.MaxCheckEmails)+" direcciones")
		return
	}
	suppressed, err := h.uc.Check(r.Context(), tenantID, req.Emails)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, checkResponse{Suppressed: suppressed})
}

// ── Exclusiones ──────────────────────────────────────────────────────────────

func (h *Handler) ListEntries(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	v := validate.New()
	v.OneOf("reason", q.Get("reason"), reasonNames())
	v.MaxLength("search", q.Get("search"), maxEmailLength)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	page, perPage := parsePagination(r)
	entries, total, err := h.uc.List(r.Context(), tenantID, ports.ListFilter{
		Reason: domain.Reason(q.Get("reason")), Search: q.Get("search"), Page: page, PerPage: perPage,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSONWithMeta(w, http.StatusOK, entries, response.PageMeta(total, page, perPage))
}

func (h *Handler) GetEntry(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := parseIDParam(w, r)
	if !ok {
		return
	}
	e, err := h.uc.Get(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, e)
}

type createEntryRequest struct {
	Email     string     `json:"email"`
	Reason    string     `json:"reason"`
	Detail    string     `json:"detail"`
	ExpiresAt *time.Time `json:"expires_at"`
}

func (h *Handler) CreateEntry(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req createEntryRequest
	if err := validate.DecodeJSONLimit(w, r, &req, defaultBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	if req.Reason == "" {
		req.Reason = string(domain.ReasonManual)
	}
	v := validate.New()
	v.Required("email", req.Email)
	v.MaxLength("email", req.Email, maxEmailLength)
	v.OneOf("reason", req.Reason, manualReasons())
	v.MaxLength("detail", req.Detail, maxDetailLength)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	e, err := h.uc.CreateManual(r.Context(), tenantID, app.CreateManualInput{
		Email: req.Email, Reason: domain.Reason(req.Reason), Detail: req.Detail, ExpiresAt: req.ExpiresAt,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, e)
}

func (h *Handler) DeleteEntry(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := parseIDParam(w, r)
	if !ok {
		return
	}
	if err := h.uc.Remove(r.Context(), tenantID, id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type importRequest struct {
	Emails []string `json:"emails"`
	Reason string   `json:"reason"`
	Detail string   `json:"detail"`
}

type importResponse struct {
	ID      uuid.UUID `json:"id"`
	Total   int       `json:"total"`
	Added   int       `json:"added"`
	Skipped int       `json:"skipped"`
}

func (h *Handler) ImportEntries(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	userID, err := uuid.Parse(middleware.GetUserID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "usuario no válido")
		return
	}
	var req importRequest
	if err := validate.DecodeJSONLimit(w, r, &req, importBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	if req.Reason == "" {
		req.Reason = string(domain.ReasonManual)
	}
	v := validate.New()
	v.OneOf("reason", req.Reason, manualReasons())
	v.MaxLength("detail", req.Detail, maxDetailLength)
	if len(req.Emails) == 0 {
		v.Add("emails", "no puede estar vacío")
	}
	if len(req.Emails) > app.MaxImportEmails {
		v.Add("emails", "admite como máximo "+strconv.Itoa(app.MaxImportEmails)+" direcciones")
	}
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	imp, err := h.uc.Import(r.Context(), tenantID, app.ImportInput{
		Emails: req.Emails, Reason: domain.Reason(req.Reason), Detail: req.Detail, CreatedBy: userID,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, importResponse{ID: imp.ID, Total: imp.Total, Added: imp.Added, Skipped: imp.Skipped})
}

func (h *Handler) ListImports(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	page, perPage := parsePagination(r)
	imports, total, err := h.uc.ListImports(r.Context(), tenantID, page, perPage)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSONWithMeta(w, http.StatusOK, imports, response.PageMeta(total, page, perPage))
}

func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	stats, err := h.uc.Stats(r.Context(), tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, stats)
}

// ── Interno ──────────────────────────────────────────────────────────────────

type internalAddRequest struct {
	Email      string     `json:"email"`
	Reason     string     `json:"reason"`
	Source     string     `json:"source"`
	Detail     string     `json:"detail"`
	MessageID  *uuid.UUID `json:"message_id"`
	CampaignID *uuid.UUID `json:"campaign_id"`
}

type internalAddResponse struct {
	// Entry es la direccion con todas sus causas: reason es la principal, que puede ser
	// otra mas grave que la registrada.
	Entry *domain.Address `json:"entry"`
	// Added: la causa entro, se reactivo o, si es una baja, se volvio a registrar; false si
	// la direccion ya la tenia vigente.
	Added bool `json:"added"`
}

func (h *Handler) InternalAdd(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req internalAddRequest
	if err := validate.DecodeJSONLimit(w, r, &req, defaultBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	v.Required("email", req.Email)
	v.MaxLength("email", req.Email, maxEmailLength)
	v.Required("reason", req.Reason)
	v.OneOf("reason", req.Reason, reasonNames())
	v.Required("source", req.Source)
	v.MaxLength("source", req.Source, 100)
	v.MaxLength("detail", req.Detail, maxDetailLength)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	e, added, err := h.uc.Add(r.Context(), tenantID, app.AddInput{
		Email: req.Email, Reason: domain.Reason(req.Reason), Source: req.Source, Detail: req.Detail,
		MessageID: req.MessageID, CampaignID: req.CampaignID,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if added {
		status = http.StatusCreated
	}
	response.JSON(w, status, internalAddResponse{Entry: e, Added: added})
}

func reasonNames() []string {
	reasons := domain.Reasons()
	out := make([]string, len(reasons))
	for i, r := range reasons {
		out[i] = string(r)
	}
	return out
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrEntryNotFound):
		response.ErrNotFound(w, err.Error())
	case errors.Is(err, domain.ErrEntryAlreadyExists):
		response.ErrConflict(w, err.Error())
	case errors.Is(err, domain.ErrUnsubscribeProtected):
		response.Err(w, http.StatusConflict, "UNSUBSCRIBE_PROTECTED", err.Error())
	case errors.Is(err, domain.ErrInvalidEmail),
		errors.Is(err, domain.ErrInvalidReason),
		errors.Is(err, domain.ErrManualOnly),
		errors.Is(err, domain.ErrExpiryInPast),
		errors.Is(err, domain.ErrTooManyEmails),
		errors.Is(err, domain.ErrNoEmails):
		response.ErrValidation(w, err.Error())
	default:
		response.Unexpected(w, err)
	}
}

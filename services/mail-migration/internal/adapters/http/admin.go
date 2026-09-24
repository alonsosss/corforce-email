package http

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const (
	permModule   = "migration"
	permResource = "jobs"

	// maxCreateBody acota la peticion de alta: el cuerpo lleva una contrasena, no adjuntos.
	maxCreateBody = 16 << 10

	codeSourceHostNotAllowed = "SOURCE_HOST_NOT_ALLOWED"
	codeMailboxNotFound      = "MAILBOX_NOT_FOUND"
	codeMailboxInactive      = "MAILBOX_INACTIVE"
	codeJobAlreadyActive     = "JOB_ALREADY_ACTIVE"
	codeTenantLimitReached   = "TENANT_LIMIT_REACHED"
	codeTenantRateLimited    = "TENANT_RATE_LIMITED"
	codeSourceAuthCooldown   = "SOURCE_AUTH_COOLDOWN"
	codeNotConfigured        = "NOT_CONFIGURED"
	codeJobNotCancellable    = "JOB_NOT_CANCELLABLE"
)

// Handler es la API de administracion: la llama la interfaz por el gateway, con sesion y permisos.
type Handler struct {
	uc    *app.UseCase
	authz *authz.Checker
}

func NewHandler(uc *app.UseCase, checker *authz.Checker) *Handler {
	return &Handler{uc: uc, authz: checker}
}

func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Route("/api/v1/mail-migration", func(r chi.Router) {
		r.Get("/health", h.Health)
		r.With(h.require("read")).Get("/meta", h.Meta)
		r.With(h.require("read")).Get("/jobs", h.List)
		r.With(h.require("create")).Post("/jobs", h.Create)
		r.With(h.require("read")).Get("/jobs/{id}", h.Get)
		r.With(h.require("cancel")).Post("/jobs/{id}/cancel", h.Cancel)
	})
	return r
}

func (h *Handler) require(action string) func(http.Handler) http.Handler {
	return h.authz.RequirePermission(permModule, permResource, action)
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func tenantFrom(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "la petición no lleva empresa")
		return uuid.Nil, false
	}
	return id, true
}

func actorFrom(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(middleware.GetUserID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "la petición no lleva usuario")
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) Meta(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	m, err := h.uc.Meta(r.Context(), tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toMetaDTO(m))
}

type createRequest struct {
	MailboxID      string `json:"mailbox_id"`
	SourceHost     string `json:"source_host"`
	SourcePort     int    `json:"source_port"`
	SourceTLS      string `json:"source_tls"`
	SourceUsername string `json:"source_username"`
	SourcePassword string `json:"source_password"`
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	actorID, ok := actorFrom(w, r)
	if !ok {
		return
	}
	var req createRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxCreateBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	mailboxID, err := uuid.Parse(req.MailboxID)
	if err != nil {
		response.ErrWithDetails(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "mailbox_id no es un identificador", map[string]string{"field": "mailbox_id"})
		return
	}
	job, err := h.uc.Create(r.Context(), tenantID, actorID, app.CreateInput{
		MailboxID: mailboxID,
		Source: domain.Source{
			Host: req.SourceHost, Port: req.SourcePort, TLS: domain.TLSMode(req.SourceTLS),
			Username: req.SourceUsername, Password: req.SourcePassword,
		},
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, toJobDTO(job))
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var filter ports.ListFilter
	if raw := q.Get("mailbox_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			response.ErrBadRequest(w, "mailbox_id no es un identificador")
			return
		}
		filter.MailboxID = &id
	}
	if raw := q.Get("status"); raw != "" {
		status, ok := domain.ParseStatus(raw)
		if !ok {
			response.ErrBadRequest(w, "status no válido")
			return
		}
		filter.Status = &status
	}
	page, _ := strconv.Atoi(q.Get("page"))
	perPage, _ := strconv.Atoi(q.Get("per_page"))
	page, perPage, pg := app.NormalizePage(page, perPage)
	jobs, total, err := h.uc.List(r.Context(), tenantID, filter, pg)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]jobDTO, len(jobs))
	for i := range jobs {
		out[i] = toJobDTO(&jobs[i])
	}
	response.JSONWithMeta(w, http.StatusOK, out, response.PageMetaCapped(total.Value, total.Capped, page, perPage))
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.ErrBadRequest(w, "identificador no válido")
		return
	}
	job, err := h.uc.Get(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toJobDTO(job))
}

func (h *Handler) Cancel(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	actorID, ok := actorFrom(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.ErrBadRequest(w, "identificador no válido")
		return
	}
	job, err := h.uc.Cancel(r.Context(), tenantID, actorID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toJobDTO(job))
}

// writeError traduce los errores conocidos del dominio. Un mensaje de error nunca lleva datos del
// origen: los de validacion nombran solo el campo.
func writeError(w http.ResponseWriter, err error) {
	var fieldErr *domain.FieldError
	switch {
	case errors.Is(err, domain.ErrNotFound):
		response.ErrNotFound(w, err.Error())
	case errors.Is(err, domain.ErrMailboxNotFound):
		response.Err(w, http.StatusNotFound, codeMailboxNotFound, err.Error())
	case errors.Is(err, domain.ErrMailboxInactive):
		response.Err(w, http.StatusConflict, codeMailboxInactive, err.Error())
	case errors.Is(err, domain.ErrJobAlreadyActive):
		response.Err(w, http.StatusConflict, codeJobAlreadyActive, err.Error())
	case errors.Is(err, domain.ErrNotCancellable):
		response.Err(w, http.StatusConflict, codeJobNotCancellable, err.Error())
	case errors.Is(err, domain.ErrTenantLimitReached):
		response.Err(w, http.StatusTooManyRequests, codeTenantLimitReached, err.Error())
	case errors.Is(err, domain.ErrTenantRateLimited):
		response.Err(w, http.StatusTooManyRequests, codeTenantRateLimited, err.Error())
	case errors.Is(err, domain.ErrSourceAuthCooldown):
		response.Err(w, http.StatusTooManyRequests, codeSourceAuthCooldown, err.Error())
	case errors.Is(err, domain.ErrNotConfigured):
		response.Err(w, http.StatusServiceUnavailable, codeNotConfigured, err.Error())
	case errors.Is(err, domain.ErrHostNotAllowed):
		response.ErrWithDetails(w, http.StatusUnprocessableEntity, codeSourceHostNotAllowed, err.Error(), fieldDetails(err))
	case errors.As(err, &fieldErr):
		response.ErrWithDetails(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", fieldErr.Err.Error(), fieldDetails(err))
	default:
		response.Unexpected(w, err)
	}
}

func fieldDetails(err error) map[string]string {
	if field := domain.FieldOf(err); field != "" {
		return map[string]string{"field": field}
	}
	return nil
}

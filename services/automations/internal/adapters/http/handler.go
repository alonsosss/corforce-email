package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/automations/internal/app"
	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/alonsosss/corforce-email/services/automations/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const (
	permModule = "automations"
	// bodyLimit: 40 pasos con sus campos y condiciones caben con holgura.
	bodyLimit      = 512 << 10
	defaultPerPage = 25
	maxPerPage     = 100
	maxSearchLen   = 200
)

// PermissionGuard es la tercera capa de acceso (pkg/authz.Checker la cumple).
type PermissionGuard interface {
	RequirePermission(module, resource, action string) func(http.Handler) http.Handler
}

type Handler struct {
	uc    *app.UseCase
	perms PermissionGuard
}

func NewHandler(uc *app.UseCase, perms PermissionGuard) *Handler {
	return &Handler{uc: uc, perms: perms}
}

// Routes cuelga de /api/v1/automations. El gateway ya gateo el modulo; aqui cada accion
// exige su permiso concreto. Pausar y archivar son del mismo permiso que activar: los
// tres gobiernan si el flujo corre.
func (h *Handler) Routes() http.Handler {
	perm := func(resource, action string) func(http.Handler) http.Handler {
		return h.perms.RequirePermission(permModule, resource, action)
	}
	r := chi.NewRouter()
	r.With(perm("workflows", "read")).Get("/meta", h.Meta)
	r.With(perm("settings", "read")).Get("/double-opt-in", h.GetDOI)
	r.With(perm("settings", "update")).Put("/double-opt-in", h.PutDOI)
	r.With(perm("settings", "read")).Get("/double-opt-in/deliveries", h.ListDeliveries)
	r.Route("/workflows", func(r chi.Router) {
		r.With(perm("workflows", "read")).Get("/", h.ListWorkflows)
		r.With(perm("workflows", "create")).Post("/", h.CreateWorkflow)
		r.With(perm("workflows", "read")).Get("/{id}", h.GetWorkflow)
		r.With(perm("workflows", "update")).Patch("/{id}", h.UpdateWorkflow)
		r.With(perm("workflows", "delete")).Delete("/{id}", h.DeleteWorkflow)
		r.With(perm("workflows", "activate")).Post("/{id}/activate", h.ActivateWorkflow)
		r.With(perm("workflows", "activate")).Post("/{id}/pause", h.PauseWorkflow)
		r.With(perm("workflows", "activate")).Post("/{id}/archive", h.ArchiveWorkflow)
		r.With(perm("runs", "read")).Get("/{id}/runs", h.ListRuns)
	})
	r.With(perm("runs", "read")).Get("/runs/{id}", h.GetRun)
	return r
}

// ── Doble opt-in ─────────────────────────────────────────────────────────────

type doiRequest struct {
	Enabled    bool       `json:"enabled"`
	TemplateID *uuid.UUID `json:"template_id"`
	FromEmail  string     `json:"from_email"`
	FromName   string     `json:"from_name"`
	ReplyTo    string     `json:"reply_to"`
}

func (h *Handler) GetDOI(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	s, err := h.uc.GetDOISettings(r.Context(), tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, s)
}

// PutDOI reemplaza los ajustes. Con plantilla, la comprueba en templates antes de guardar.
func (h *Handler) PutDOI(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req doiRequest
	if err := validate.DecodeJSONLimit(w, r, &req, bodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	s, err := h.uc.UpdateDOISettings(r.Context(), tenantID, domain.DOISettings{
		Enabled: req.Enabled, TemplateID: req.TemplateID, FromEmail: req.FromEmail, FromName: req.FromName, ReplyTo: req.ReplyTo,
	}, userFrom(r))
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, s)
}

// ListDeliveries es el historial del doble opt-in. El enlace de confirmacion no aparece:
// es una credencial y nunca se guarda.
func (h *Handler) ListDeliveries(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var status domain.DOIStatus
	if raw := r.URL.Query().Get("status"); raw != "" {
		st, ok := domain.ParseDOIStatus(raw)
		if !ok {
			response.ErrValidation(w, "status debe ser pending, sent, skipped o failed")
			return
		}
		status = st
	}
	page, perPage := pagination(r)
	list, total, err := h.uc.ListDeliveries(r.Context(), tenantID, status, page, perPage)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSONWithMeta(w, http.StatusOK, list, response.PageMeta(total, page, perPage))
}

// ── Flujos ───────────────────────────────────────────────────────────────────

type triggerDTO struct {
	Type       string     `json:"type"`
	CampaignID *uuid.UUID `json:"campaign_id,omitempty"`
	Attribute  string     `json:"attribute,omitempty"`
	Hour       *int       `json:"hour,omitempty"`
	Timezone   string     `json:"timezone,omitempty"`
}

func (t *triggerDTO) toDomain() domain.Trigger {
	return domain.Trigger{
		Type: domain.TriggerType(strings.TrimSpace(t.Type)), CampaignID: t.CampaignID,
		Attribute: t.Attribute, Hour: t.Hour, Timezone: t.Timezone,
	}
}

type createWorkflowRequest struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Trigger     *triggerDTO   `json:"trigger"`
	ListID      *uuid.UUID    `json:"list_id"`
	ReEntry     bool          `json:"re_entry"`
	Steps       []domain.Step `json:"steps"`
}

func (h *Handler) CreateWorkflow(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	userID := userFrom(r)
	if userID == nil {
		response.ErrUnauthorized(w, "usuario no valido")
		return
	}
	var req createWorkflowRequest
	if err := validate.DecodeJSONLimit(w, r, &req, bodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	if req.Trigger == nil {
		response.ErrValidation(w, "trigger es obligatorio")
		return
	}
	wf, err := h.uc.CreateWorkflow(r.Context(), tenantID, domain.NewWorkflowInput{
		Name: req.Name, Description: req.Description, Trigger: req.Trigger.toDomain(), ListID: req.ListID,
		ReEntry: req.ReEntry, Steps: req.Steps, CreatedBy: *userID,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, wf)
}

func (h *Handler) ListWorkflows(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := ports.WorkflowFilter{Search: strings.TrimSpace(q.Get("search"))}
	if raw := q.Get("status"); raw != "" {
		st, ok := domain.ParseStatus(raw)
		if !ok {
			response.ErrValidation(w, "status debe ser draft, active, paused o archived")
			return
		}
		f.Status = st
	}
	if len(f.Search) > maxSearchLen {
		response.ErrValidation(w, fmt.Sprintf("search admite como maximo %d caracteres", maxSearchLen))
		return
	}
	f.Page, f.PerPage = pagination(r)
	list, total, err := h.uc.ListWorkflows(r.Context(), tenantID, f)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSONWithMeta(w, http.StatusOK, list, response.PageMeta(total, f.Page, f.PerPage))
}

func (h *Handler) GetWorkflow(w http.ResponseWriter, r *http.Request) {
	tenantID, id, ok := tenantAndID(w, r)
	if !ok {
		return
	}
	wf, err := h.uc.GetWorkflow(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, wf)
}

// patchWorkflowRequest: list_id se lee crudo para distinguir null (quitar el filtro) de
// ausente (no tocarlo).
type patchWorkflowRequest struct {
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	Trigger     *triggerDTO     `json:"trigger"`
	ListID      json.RawMessage `json:"list_id"`
	ReEntry     *bool           `json:"re_entry"`
	Steps       *[]domain.Step  `json:"steps"`
}

func (h *Handler) UpdateWorkflow(w http.ResponseWriter, r *http.Request) {
	tenantID, id, ok := tenantAndID(w, r)
	if !ok {
		return
	}
	var req patchWorkflowRequest
	if err := validate.DecodeJSONLimit(w, r, &req, bodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	p := domain.Patch{Name: req.Name, Description: req.Description, ReEntry: req.ReEntry}
	if req.Trigger != nil {
		t := req.Trigger.toDomain()
		p.Trigger = &t
	}
	if req.Steps != nil {
		p.Steps = *req.Steps
		if p.Steps == nil {
			p.Steps = []domain.Step{}
		}
	}
	if raw := bytes.TrimSpace(req.ListID); len(raw) > 0 {
		p.SetListID = true
		if !bytes.Equal(raw, []byte("null")) {
			var listID uuid.UUID
			if err := json.Unmarshal(raw, &listID); err != nil {
				response.ErrValidation(w, "list_id debe ser un UUID o null")
				return
			}
			p.ListID = &listID
		}
	}
	wf, err := h.uc.UpdateWorkflow(r.Context(), tenantID, id, p)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, wf)
}

func (h *Handler) DeleteWorkflow(w http.ResponseWriter, r *http.Request) {
	tenantID, id, ok := tenantAndID(w, r)
	if !ok {
		return
	}
	if err := h.uc.DeleteWorkflow(r.Context(), tenantID, id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ActivateWorkflow(w http.ResponseWriter, r *http.Request) {
	tenantID, id, ok := tenantAndID(w, r)
	if !ok {
		return
	}
	wf, err := h.uc.ActivateWorkflow(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, wf)
}

type pauseRequest struct {
	Reason string `json:"reason"`
}

func (h *Handler) PauseWorkflow(w http.ResponseWriter, r *http.Request) {
	tenantID, id, ok := tenantAndID(w, r)
	if !ok {
		return
	}
	var req pauseRequest
	if r.ContentLength != 0 {
		if err := validate.DecodeJSONLimit(w, r, &req, bodyLimit); err != nil {
			response.ErrBadRequest(w, err.Error())
			return
		}
	}
	if len([]rune(req.Reason)) > domain.MaxPauseReasonLen {
		response.ErrValidation(w, fmt.Sprintf("reason admite como maximo %d caracteres", domain.MaxPauseReasonLen))
		return
	}
	wf, err := h.uc.PauseWorkflow(r.Context(), tenantID, id, req.Reason)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, wf)
}

func (h *Handler) ArchiveWorkflow(w http.ResponseWriter, r *http.Request) {
	tenantID, id, ok := tenantAndID(w, r)
	if !ok {
		return
	}
	wf, err := h.uc.ArchiveWorkflow(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, wf)
}

// ── Ejecuciones ──────────────────────────────────────────────────────────────

func (h *Handler) ListRuns(w http.ResponseWriter, r *http.Request) {
	tenantID, id, ok := tenantAndID(w, r)
	if !ok {
		return
	}
	f := ports.RunFilter{WorkflowID: id}
	if raw := r.URL.Query().Get("status"); raw != "" {
		st, ok := domain.ParseRunStatus(raw)
		if !ok {
			response.ErrValidation(w, "status debe ser waiting, running, completed, failed, cancelled o skipped")
			return
		}
		f.Status = st
	}
	f.Page, f.PerPage = pagination(r)
	runs, total, err := h.uc.ListRuns(r.Context(), tenantID, f)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSONWithMeta(w, http.StatusOK, runs, response.PageMeta(total, f.Page, f.PerPage))
}

func (h *Handler) GetRun(w http.ResponseWriter, r *http.Request) {
	tenantID, id, ok := tenantAndID(w, r)
	if !ok {
		return
	}
	run, err := h.uc.GetRun(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, run)
}

// ── Utilidades ───────────────────────────────────────────────────────────────

func tenantFrom(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "empresa no valida")
		return uuid.Nil, false
	}
	return id, true
}

func tenantAndID(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.ErrValidation(w, "id debe ser un UUID")
		return uuid.Nil, uuid.Nil, false
	}
	return tenantID, id, true
}

func userFrom(r *http.Request) *uuid.UUID {
	id, err := uuid.Parse(middleware.GetUserID(r.Context()))
	if err != nil {
		return nil
	}
	return &id
}

func pagination(r *http.Request) (int, int) {
	page, perPage := 1, defaultPerPage
	if v, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && v > 0 {
		page = v
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("per_page")); err == nil && v > 0 {
		perPage = v
	}
	if perPage > maxPerPage {
		perPage = maxPerPage
	}
	return page, perPage
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrWorkflowNotFound), errors.Is(err, domain.ErrRunNotFound):
		response.ErrNotFound(w, err.Error())
	case errors.Is(err, domain.ErrNameTaken):
		response.Err(w, http.StatusConflict, "NAME_TAKEN", err.Error())
	case errors.Is(err, domain.ErrNotEditable):
		response.Err(w, http.StatusConflict, "NOT_EDITABLE", err.Error())
	case errors.Is(err, domain.ErrNotDeletable):
		response.Err(w, http.StatusConflict, "NOT_DELETABLE", err.Error())
	case errors.Is(err, domain.ErrInvalidTransition):
		response.Err(w, http.StatusConflict, "INVALID_TRANSITION", err.Error())
	case errors.Is(err, domain.ErrConcurrentChange):
		response.Err(w, http.StatusConflict, "CONCURRENT_CHANGE", err.Error())
	case errors.Is(err, domain.ErrTemplateNotFound):
		response.Err(w, http.StatusUnprocessableEntity, "TEMPLATE_NOT_FOUND", err.Error())
	case errors.Is(err, domain.ErrNoPublishedVersion):
		response.Err(w, http.StatusUnprocessableEntity, "TEMPLATE_NOT_PUBLISHED", err.Error())
	case errors.Is(err, domain.ErrTemplateNotMarketing):
		response.Err(w, http.StatusUnprocessableEntity, "TEMPLATE_NOT_MARKETING", err.Error())
	case errors.Is(err, domain.ErrTemplateNotTransactional):
		response.Err(w, http.StatusUnprocessableEntity, "TEMPLATE_NOT_TRANSACTIONAL", err.Error())
	case errors.Is(err, domain.ErrTemplateMissingConfirmURL):
		response.Err(w, http.StatusUnprocessableEntity, "TEMPLATE_MISSING_CONFIRM_URL", err.Error())
	case errors.Is(err, domain.ErrTemplateVariables):
		response.Err(w, http.StatusUnprocessableEntity, "TEMPLATE_VARIABLES", err.Error())
	case errors.Is(err, domain.ErrTemplateVersionRequired):
		response.Err(w, http.StatusUnprocessableEntity, "TEMPLATE_VERSION_REQUIRED", err.Error())
	case errors.Is(err, domain.ErrNothingToUpdate), errors.Is(err, domain.ErrInvalidInput):
		response.ErrValidation(w, err.Error())
	case errors.Is(err, domain.ErrTemplateKindUnknown), errors.Is(err, ports.ErrUnavailable),
		errors.Is(err, context.DeadlineExceeded):
		response.Err(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "un servicio dependiente no respondio; vuelva a intentarlo")
	default:
		response.Unexpected(w, err)
	}
}

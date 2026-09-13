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
	"github.com/alonsosss/corforce-email/services/scheduler/internal/app"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const permModule = "scheduler"

type Handler struct {
	uc    *app.SchedulerUseCase
	authz *authz.Checker
}

func NewHandler(uc *app.SchedulerUseCase, checker *authz.Checker) *Handler {
	return &Handler{uc: uc, authz: checker}
}

func (h *Handler) perm(resource, action string) func(http.Handler) http.Handler {
	return h.authz.RequirePermission(permModule, resource, action)
}

// Routes: cada peticion opera sobre la base de la empresa que llama, de modo que todos los
// permisos son de alcance tenant. Ver los trabajos dice que procesos corren solos y a que
// hora; lanzarlos, desactivarlos o cancelarlos mueve procesos de toda la empresa.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Route("/api/v1/scheduler", func(r chi.Router) {
		r.Route("/jobs", func(r chi.Router) {
			r.With(h.perm("jobs", "read")).Get("/", h.ListJobs)
			r.With(h.perm("jobs", "create")).Post("/", h.CreateJob)
			r.With(h.perm("jobs", "read")).Get("/{id}", h.GetJob)
			r.With(h.perm("jobs", "update")).Put("/{id}", h.UpdateJob)
			r.With(h.perm("jobs", "update")).Post("/{id}/enable", h.EnableJob)
			r.With(h.perm("jobs", "update")).Post("/{id}/disable", h.DisableJob)
			r.With(h.perm("jobs", "run")).Post("/{id}/run", h.RunJob)
			r.With(h.perm("executions", "read")).Get("/{id}/history", h.GetJobHistory)
		})
		r.Route("/executions", func(r chi.Router) {
			r.With(h.perm("executions", "read")).Get("/", h.ListRunning)
			r.With(h.perm("executions", "read")).Get("/{id}", h.GetExecution)
			r.With(h.perm("executions", "cancel")).Post("/{id}/cancel", h.CancelExecution)
			r.With(h.perm("executions", "retry")).Post("/{id}/retry", h.RetryExecution)
		})
		r.Route("/tasks", func(r chi.Router) {
			r.With(h.perm("tasks", "read")).Get("/", h.ListPendingTasks)
			r.With(h.perm("tasks", "create")).Post("/", h.ScheduleTask)
			r.With(h.perm("tasks", "read")).Get("/{id}", h.GetTask)
			r.With(h.perm("tasks", "cancel")).Post("/{id}/cancel", h.CancelTask)
		})
	})
	return r
}

func parseTenantID(r *http.Request) (uuid.UUID, error) {
	return uuid.Parse(middleware.GetTenantID(r.Context()))
}

func parseIDParam(r *http.Request) (uuid.UUID, error) {
	return uuid.Parse(chi.URLParam(r, "id"))
}

func parsePage(r *http.Request) (int, int) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	// Pedir mas de lo permitido se RECORTA al maximo. Antes caia al valor por
	// defecto y el cliente recibia 20 filas creyendo que pedia mas.
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

type createJobReq struct {
	Name            string  `json:"name"`
	Code            string  `json:"code"`
	Description     *string `json:"description"`
	JobType         string  `json:"job_type"`
	CronExpression  *string `json:"cron_expression"`
	IntervalMinutes *int    `json:"interval_minutes"`
	Handler         string  `json:"handler"`
	Payload         *string `json:"payload"`
	MaxRetries      int     `json:"max_retries"`
	TimeoutSeconds  int     `json:"timeout_seconds"`
}

func (h *Handler) CreateJob(w http.ResponseWriter, r *http.Request) {
	var req createJobReq
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	v.Required("name", req.Name)
	v.Required("code", req.Code)
	v.Required("job_type", req.JobType)
	v.Required("handler", req.Handler)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	tenantID, _ := parseTenantID(r)
	var tid *uuid.UUID
	if tenantID != uuid.Nil {
		tid = &tenantID
	}
	job := &domain.JobDefinition{
		TenantID:        tid,
		Name:            req.Name,
		Code:            req.Code,
		Description:     req.Description,
		JobType:         req.JobType,
		CronExpression:  req.CronExpression,
		IntervalMinutes: req.IntervalMinutes,
		Handler:         req.Handler,
		Payload:         req.Payload,
		MaxRetries:      req.MaxRetries,
		TimeoutSeconds:  req.TimeoutSeconds,
	}
	if err := h.uc.CreateJob(r.Context(), job); err != nil {
		if err == domain.ErrJobAlreadyExists {
			response.ErrConflict(w, err.Error())
			return
		}
		response.ErrInternal(w)
		return
	}
	response.JSON(w, http.StatusCreated, jobResponse(job))
}

func (h *Handler) GetJob(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		response.ErrBadRequest(w, "invalid id")
		return
	}
	tenantID, err := parseTenantID(r)
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	job, err := h.uc.GetJob(r.Context(), id, tenantID)
	if err != nil {
		response.ErrNotFound(w, "job not found")
		return
	}
	response.JSON(w, http.StatusOK, jobResponse(job))
}

func (h *Handler) ListJobs(w http.ResponseWriter, r *http.Request) {
	var tenantID *uuid.UUID
	if t := r.URL.Query().Get("tenant_id"); t != "" {
		if id, err := uuid.Parse(t); err == nil {
			tenantID = &id
		}
	}
	var isActive *bool
	if a := r.URL.Query().Get("is_active"); a != "" {
		b := a == "true"
		isActive = &b
	}
	jobs, err := h.uc.ListJobs(r.Context(), tenantID, isActive)
	if err != nil {
		response.ErrInternal(w)
		return
	}
	response.JSON(w, http.StatusOK, jobsResponse(jobs))
}

type updateJobReq struct {
	Name            string  `json:"name"`
	Description     *string `json:"description"`
	JobType         string  `json:"job_type"`
	CronExpression  *string `json:"cron_expression"`
	IntervalMinutes *int    `json:"interval_minutes"`
	Handler         string  `json:"handler"`
	Payload         *string `json:"payload"`
	MaxRetries      int     `json:"max_retries"`
	TimeoutSeconds  int     `json:"timeout_seconds"`
}

func (h *Handler) UpdateJob(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		response.ErrBadRequest(w, "invalid id")
		return
	}
	var req updateJobReq
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	tenantID, err := parseTenantID(r)
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	job, err := h.uc.GetJob(r.Context(), id, tenantID)
	if err != nil {
		response.ErrNotFound(w, "job not found")
		return
	}
	job.Name = req.Name
	job.Description = req.Description
	job.JobType = req.JobType
	job.CronExpression = req.CronExpression
	job.IntervalMinutes = req.IntervalMinutes
	job.Handler = req.Handler
	job.Payload = req.Payload
	job.MaxRetries = req.MaxRetries
	job.TimeoutSeconds = req.TimeoutSeconds
	if err := h.uc.UpdateJob(r.Context(), job); err != nil {
		writeJobError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, jobResponse(job))
}

func (h *Handler) EnableJob(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		response.ErrBadRequest(w, "invalid id")
		return
	}
	tenantID, err := parseTenantID(r)
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	if err := h.uc.EnableJob(r.Context(), id, tenantID); err != nil {
		writeJobError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) DisableJob(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		response.ErrBadRequest(w, "invalid id")
		return
	}
	tenantID, err := parseTenantID(r)
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	if err := h.uc.DisableJob(r.Context(), id, tenantID); err != nil {
		writeJobError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) RunJob(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		response.ErrBadRequest(w, "invalid id")
		return
	}
	tenantID, err := parseTenantID(r)
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	exec, err := h.uc.RunJob(r.Context(), tenantID, id)
	if err != nil {
		writeJobError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, executionResponse(exec))
}

func writeJobError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrJobNotFound):
		response.ErrNotFound(w, "job not found")
	case errors.Is(err, domain.ErrPlatformJob):
		response.ErrForbidden(w, err.Error())
	default:
		response.ErrInternal(w)
	}
}

func (h *Handler) GetJobHistory(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		response.ErrBadRequest(w, "invalid id")
		return
	}
	page, pageSize := parsePage(r)
	execs, total, err := h.uc.GetJobHistory(r.Context(), id, page, pageSize)
	if err != nil {
		response.ErrInternal(w)
		return
	}
	response.JSONWithMeta(w, http.StatusOK, executionsResponse(execs),response.PageMeta(total, page, pageSize))
}

func (h *Handler) ListRunning(w http.ResponseWriter, r *http.Request) {
	execs, err := h.uc.GetRunningJobs(r.Context())
	if err != nil {
		response.ErrInternal(w)
		return
	}
	response.JSON(w, http.StatusOK, executionsResponse(execs))
}

func (h *Handler) GetExecution(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		response.ErrBadRequest(w, "invalid id")
		return
	}
	tenantID, err := parseTenantID(r)
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	exec, err := h.uc.GetExecution(r.Context(), id, tenantID)
	if err != nil {
		response.ErrNotFound(w, "execution not found")
		return
	}
	response.JSON(w, http.StatusOK, executionResponse(exec))
}

func (h *Handler) CancelExecution(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		response.ErrBadRequest(w, "invalid id")
		return
	}
	tenantID, err := parseTenantID(r)
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	if err := h.uc.CancelExecution(r.Context(), id, tenantID); err != nil {
		if err == domain.ErrExecutionNotFound {
			response.ErrNotFound(w, "execution not found")
			return
		}
		response.ErrInternal(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) RetryExecution(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		response.ErrBadRequest(w, "invalid id")
		return
	}
	tenantID, err := parseTenantID(r)
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	exec, err := h.uc.RetryFailedExecution(r.Context(), id, tenantID)
	if err != nil {
		if err == domain.ErrExecutionNotFound || err == domain.ErrJobNotFound {
			response.ErrNotFound(w, err.Error())
			return
		}
		if err == domain.ErrMaxRetriesExceeded {
			response.ErrConflict(w, err.Error())
			return
		}
		response.ErrInternal(w)
		return
	}
	response.JSON(w, http.StatusCreated, executionResponse(exec))
}

type scheduleTaskReq struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	TriggerAt   string  `json:"trigger_at"`
	Handler     string  `json:"handler"`
	Payload     *string `json:"payload"`
}

func (h *Handler) ScheduleTask(w http.ResponseWriter, r *http.Request) {
	var req scheduleTaskReq
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	v.Required("name", req.Name)
	v.Required("trigger_at", req.TriggerAt)
	v.Required("handler", req.Handler)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	triggerAt, err := time.Parse(time.RFC3339, req.TriggerAt)
	if err != nil {
		response.ErrBadRequest(w, "invalid trigger_at format")
		return
	}
	tenantID, err := parseTenantID(r)
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	task := &domain.ScheduledTask{
		TenantID:    tenantID,
		Name:        req.Name,
		Description: req.Description,
		TriggerAt:   triggerAt,
		Handler:     req.Handler,
		Payload:     req.Payload,
	}
	if err := h.uc.ScheduleTask(r.Context(), task); err != nil {
		response.ErrInternal(w)
		return
	}
	response.JSON(w, http.StatusCreated, taskResponse(task))
}

func (h *Handler) GetTask(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		response.ErrBadRequest(w, "invalid id")
		return
	}
	tenantID, err := parseTenantID(r)
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	task, err := h.uc.GetTask(r.Context(), id, tenantID)
	if err != nil {
		response.ErrNotFound(w, "task not found")
		return
	}
	response.JSON(w, http.StatusOK, taskResponse(task))
}

func (h *Handler) CancelTask(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		response.ErrBadRequest(w, "invalid id")
		return
	}
	if err := h.uc.CancelTask(r.Context(), id); err != nil {
		response.ErrInternal(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ListPendingTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := h.uc.ListPendingTasks(r.Context(), time.Now().UTC().Add(24*time.Hour))
	if err != nil {
		response.ErrInternal(w)
		return
	}
	response.JSON(w, http.StatusOK, tasksResponse(tasks))
}

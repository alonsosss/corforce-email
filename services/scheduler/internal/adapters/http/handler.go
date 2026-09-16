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

// Paginacion de los listados: pedir mas de maxPerPage se RECORTA al maximo. Caer al valor
// por defecto hacia que el cliente recibiera 20 filas creyendo que pedia mas.
const (
	defaultPerPage = 20
	maxPerPage     = 100
)

// maxRequestBody acota el cuerpo de un trabajo o una tarea. El payload viaja como texto
// JSON dentro del cuerpo, y el escapado puede duplicar su tamano.
const maxRequestBody = 8 * domain.MaxPayloadBytes

// Codigos de error propios y las claves de error.details de un error de campo: el campo que
// fallo y la regla que incumple (domain.Rule*).
const (
	codeValidation      = "VALIDATION_ERROR"
	codeInvalidTimezone = "INVALID_TIMEZONE"
	codeConflict        = "CONFLICT"
	codeJobAlreadyRun   = "JOB_ALREADY_RUN"
	codeVersionConflict = "VERSION_CONFLICT"
	codeVersionRequired = "VERSION_REQUIRED"
	detailField         = "field"
	detailRule          = "rule"
)

// Deps del adaptador HTTP. Los middlewares de cada superficie los decide main.go: el pool
// de la empresa sale del token en el API y de X-Tenant-ID en las rutas internas.
type Deps struct {
	UC    *app.SchedulerUseCase
	Perms *authz.Checker
	// API son los middlewares del API con sesion (pool de la empresa, limitador).
	API []func(http.Handler) http.Handler
	// Internal son los de las rutas servicio a servicio (pool de la empresa de X-Tenant-ID).
	Internal []func(http.Handler) http.Handler
}

type Handler struct {
	uc       *app.SchedulerUseCase
	authz    *authz.Checker
	api      []func(http.Handler) http.Handler
	internal []func(http.Handler) http.Handler
}

func NewHandler(d Deps) *Handler {
	return &Handler{uc: d.UC, authz: d.Perms, api: d.API, internal: d.Internal}
}

func (h *Handler) perm(resource, action string) func(http.Handler) http.Handler {
	return h.authz.RequirePermission(permModule, resource, action)
}

// Routes monta dos superficies.
//
// El API (/api/v1/scheduler, por el gateway) opera sobre la base de la empresa que llama,
// de modo que todos los permisos son de alcance tenant. Ver los trabajos dice que procesos
// corren solos y a que hora; lanzarlos, desactivarlos o cancelarlos mueve procesos de toda
// la empresa.
//
// Las rutas internas (/internal/scheduler) las llaman los servicios ejecutores para cerrar
// lo que el scheduler les despacho. No pasan por el gateway: main.go exige el token interno
// y la empresa llega en X-Tenant-ID.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Route("/api/v1/scheduler", func(r chi.Router) {
		r.Use(h.api...)
		r.With(h.perm("jobs", "read")).Get("/handlers", h.ListHandlers)
		r.With(h.perm("jobs", "read")).Get("/meta", h.Meta)
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
	r.Route("/internal/scheduler/executions/{id}", func(r chi.Router) {
		r.Use(h.internal...)
		r.Post("/complete", h.CompleteExecution)
		r.Post("/fail", h.FailExecution)
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
		pageSize = defaultPerPage
	}
	if pageSize > maxPerPage {
		pageSize = maxPerPage
	}
	return page, pageSize
}

// fieldError responde con el campo que fallo y la regla que incumple en error.details.
func fieldError(w http.ResponseWriter, status int, code, field, rule, message string) {
	response.ErrWithDetails(w, status, code, message, map[string]string{detailField: field, detailRule: rule})
}

// writeError traduce los errores del caso de uso. Lo no clasificado es un 500 que queda
// registrado.
func writeError(w http.ResponseWriter, err error) {
	var ferr *domain.FieldError
	switch {
	case errors.As(err, &ferr):
		code := codeValidation
		if errors.Is(err, domain.ErrInvalidTimezone) {
			code = codeInvalidTimezone
		}
		fieldError(w, http.StatusUnprocessableEntity, code, ferr.Field, ferr.Rule, err.Error())
	case errors.Is(err, domain.ErrJobNotFound):
		response.ErrNotFound(w, "job not found")
	case errors.Is(err, domain.ErrExecutionNotFound):
		response.ErrNotFound(w, "execution not found")
	case errors.Is(err, domain.ErrTaskNotFound):
		response.ErrNotFound(w, "task not found")
	case errors.Is(err, domain.ErrPlatformJob):
		response.ErrForbidden(w, err.Error())
	case errors.Is(err, domain.ErrJobAlreadyExists):
		fieldError(w, http.StatusConflict, codeConflict, domain.FieldCode, domain.RuleDuplicate, err.Error())
	case errors.Is(err, domain.ErrOneTimeAlreadyRun):
		response.Err(w, http.StatusConflict, codeJobAlreadyRun, err.Error())
	case errors.Is(err, domain.ErrJobVersionConflict):
		response.Err(w, http.StatusConflict, codeVersionConflict, err.Error())
	// El dominio nombra el campo de todo error de validacion; estos casos solo cubren uno que
	// llegara sin el, que sigue siendo un 422 y no un 500.
	case errors.Is(err, domain.ErrInvalidTimezone):
		response.Err(w, http.StatusUnprocessableEntity, codeInvalidTimezone, err.Error())
	case errors.Is(err, domain.ErrHandlerNotAllowed),
		errors.Is(err, domain.ErrInvalidJob),
		errors.Is(err, domain.ErrInvalidTask),
		errors.Is(err, domain.ErrInvalidCron),
		errors.Is(err, domain.ErrInvalidReport):
		response.ErrValidation(w, err.Error())
	case errors.Is(err, domain.ErrExecutionConflict),
		errors.Is(err, domain.ErrExecutionClosed),
		errors.Is(err, domain.ErrExecutionNotRetryable),
		errors.Is(err, domain.ErrAlreadyRetried),
		errors.Is(err, domain.ErrMaxRetriesExceeded),
		errors.Is(err, domain.ErrTaskNotCancellable):
		response.ErrConflict(w, err.Error())
	default:
		response.Unexpected(w, err)
	}
}

// ListHandlers expone el catalogo de manejadores para que la UI no copie la lista.
func (h *Handler) ListHandlers(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, handlersResponse(h.uc.ListHandlers()))
}

type createJobReq struct {
	Name           string  `json:"name"`
	Code           string  `json:"code"`
	Description    *string `json:"description"`
	JobType        string  `json:"job_type"`
	CronExpression *string `json:"cron_expression"`
	// Timezone ausente (o null) es UTC; presente se valida tal cual, vacio incluido.
	Timezone        *string `json:"timezone"`
	IntervalMinutes *int    `json:"interval_minutes"`
	Handler         string  `json:"handler"`
	Payload         *string `json:"payload"`
	MaxRetries      int     `json:"max_retries"`
	TimeoutSeconds  int     `json:"timeout_seconds"`
}

// CreateJob no valida los campos: lo hace el dominio, que nombra el que falla.
func (h *Handler) CreateJob(w http.ResponseWriter, r *http.Request) {
	var req createJobReq
	if err := validate.DecodeJSONLimit(w, r, &req, maxRequestBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	// Por el API solo se crean trabajos de la empresa que llama: sin empresa legible no
	// hay trabajo, nunca uno de plataforma.
	tenantID, err := parseTenantID(r)
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	timezone := domain.DefaultTimezone
	if req.Timezone != nil {
		timezone = *req.Timezone
	}
	job := &domain.JobDefinition{
		TenantID:        &tenantID,
		Name:            req.Name,
		Code:            req.Code,
		Description:     req.Description,
		JobType:         req.JobType,
		CronExpression:  req.CronExpression,
		Timezone:        timezone,
		IntervalMinutes: req.IntervalMinutes,
		Handler:         req.Handler,
		Payload:         req.Payload,
		MaxRetries:      req.MaxRetries,
		TimeoutSeconds:  req.TimeoutSeconds,
	}
	created, err := h.uc.CreateJob(r.Context(), job)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, jobResponse(created))
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
	job, err := h.uc.GetJobOverview(r.Context(), id, tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, jobResponse(job))
}

// ListJobs pagina los trabajos que ve la empresa que llama: los suyos y los de plataforma.
// La empresa sale del token; ninguna query elige otra.
func (h *Handler) ListJobs(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	filter := domain.JobFilter{TenantID: tenantID}
	if a := r.URL.Query().Get("is_active"); a != "" {
		active, err := strconv.ParseBool(a)
		if err != nil {
			writeError(w, domain.NewFieldError(domain.FieldIsActive, domain.RuleInvalidFormat, domain.ErrInvalidQuery, "is_active must be true or false"))
			return
		}
		filter.IsActive = &active
	}
	filter.Page, filter.PerPage = parsePage(r)
	jobs, total, err := h.uc.ListJobs(r.Context(), filter)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSONWithMeta(w, http.StatusOK, jobsResponse(jobs), response.PageMeta(total, filter.Page, filter.PerPage))
}

type updateJobReq struct {
	Name           string  `json:"name"`
	Description    *string `json:"description"`
	JobType        string  `json:"job_type"`
	CronExpression *string `json:"cron_expression"`
	// Timezone ausente (o null) conserva la zona guardada: un cliente que no conoce el campo
	// no devuelve el trabajo a UTC al editarlo.
	Timezone        *string `json:"timezone"`
	IntervalMinutes *int    `json:"interval_minutes"`
	Handler         string  `json:"handler"`
	Payload         *string `json:"payload"`
	MaxRetries      int     `json:"max_retries"`
	TimeoutSeconds  int     `json:"timeout_seconds"`
	// Version es la del trabajo que se leyo (jobDTO.Version). Es obligatoria: sin ella la
	// edicion desharia en silencio la de otro administrador.
	Version *int64 `json:"version"`
}

// UpdateJob reemplaza la definicion con concurrencia optimista. La version va en el cuerpo y
// no en If-Match: la representacion del trabajo lleva estado del calendario (next_run_at,
// last_execution) que cambia en cada despacho, y una ETag fuerte tendria que cambiar con el
// (RFC 9110, 8.8.1 y 13.1.1), asi que un trabajo que corre a menudo rechazaria casi toda
// edicion. Sin version, 428; con una que ya no es la guardada, 409 (RFC 9110, 15.5.10).
func (h *Handler) UpdateJob(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		response.ErrBadRequest(w, "invalid id")
		return
	}
	var req updateJobReq
	if err := validate.DecodeJSONLimit(w, r, &req, maxRequestBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	tenantID, err := parseTenantID(r)
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	if req.Version == nil {
		response.Err(w, http.StatusPreconditionRequired, codeVersionRequired, "version is required: send the version of the job that was read")
		return
	}
	job, err := h.uc.GetJob(r.Context(), id, tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	job.Name = req.Name
	job.Description = req.Description
	job.JobType = req.JobType
	job.CronExpression = req.CronExpression
	if req.Timezone != nil {
		job.Timezone = *req.Timezone
	}
	job.IntervalMinutes = req.IntervalMinutes
	job.Handler = req.Handler
	job.Payload = req.Payload
	job.MaxRetries = req.MaxRetries
	job.TimeoutSeconds = req.TimeoutSeconds
	job.Version = *req.Version
	updated, err := h.uc.UpdateJob(r.Context(), job)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, jobResponse(updated))
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
		writeError(w, err)
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
		writeError(w, err)
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
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, executionResponse(exec))
}

// GetJobHistory pagina el historial de un trabajo que ve la empresa; el de otra empresa es
// 404 como uno que no existe. La pagina se recorta como en /jobs y una enorme no tiene filas.
func (h *Handler) GetJobHistory(w http.ResponseWriter, r *http.Request) {
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
	page, perPage := parsePage(r)
	execs, total, err := h.uc.GetJobHistory(r.Context(), id, tenantID, page, perPage)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSONWithMeta(w, http.StatusOK, executionsResponse(execs), response.PageMeta(total, page, perPage))
}

// ListRunning pagina como /jobs las ejecuciones activas que ve la empresa del token: las
// suyas y las de plataforma, de la mas reciente a la mas antigua.
func (h *Handler) ListRunning(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	page, perPage := parsePage(r)
	execs, total, err := h.uc.GetRunningJobs(r.Context(), tenantID, page, perPage)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSONWithMeta(w, http.StatusOK, executionsResponse(execs), response.PageMeta(total, page, perPage))
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
		writeError(w, err)
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
		writeError(w, err)
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
		writeError(w, err)
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
	if err := validate.DecodeJSONLimit(w, r, &req, maxRequestBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	// Una fecha vacia llega al dominio como cero, que la exige; una ilegible no llega.
	var triggerAt time.Time
	if req.TriggerAt != "" {
		t, err := time.Parse(time.RFC3339, req.TriggerAt)
		if err != nil {
			writeError(w, domain.NewFieldError(domain.FieldTriggerAt, domain.RuleInvalidFormat, domain.ErrInvalidTask, "trigger_at must be an RFC 3339 date-time"))
			return
		}
		triggerAt = t
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
		writeError(w, err)
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
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, taskResponse(task))
}

// CancelTask: 204 si queda cancelada (tambien si ya lo estaba), 404 si no es de la empresa del
// token o no existe, 409 si ya se ejecuto.
func (h *Handler) CancelTask(w http.ResponseWriter, r *http.Request) {
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
	if err := h.uc.CancelTask(r.Context(), id, tenantID); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListPendingTasks pagina como /jobs las tareas pendientes de la empresa del token. La meta
// lleva ademas la ventana del listado, que asi conoce quien solo tiene tasks/read.
func (h *Handler) ListPendingTasks(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	page, perPage := parsePage(r)
	tasks, total, err := h.uc.ListPendingTasks(r.Context(), tenantID, page, perPage)
	if err != nil {
		writeError(w, err)
		return
	}
	writeTasksPage(w, tasks, response.PageMeta(total, page, perPage))
}

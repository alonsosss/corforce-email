package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/app"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Modulo, recursos y acciones del permiso que exige el handler (tercera capa). Se
// siembran en migrations/registry/016_campaigns_permissions.sql.
const (
	permModule   = "campaigns"
	resCampaigns = "campaigns"
	resStats     = "stats"

	actionRead   = "read"
	actionCreate = "create"
	actionUpdate = "update"
	actionDelete = "delete"
	actionSend   = "send"
	actionCancel = "cancel"
)

const (
	defaultBodyLimit = 64 << 10
	defaultPerPage   = 20
	maxPerPage       = 100
	maxSearchLength  = 200
	// testRequestsPerMinute acota los envios de prueba por usuario: cada uno sale a
	// direcciones arbitrarias y consume reputacion de la empresa.
	testRequestsPerMinute = 10
)

type Handler struct {
	uc          *app.UseCase
	authz       *authz.Checker
	testLimiter *middleware.RateLimiter
}

func NewHandler(uc *app.UseCase, checker *authz.Checker) *Handler {
	return &Handler{uc: uc, authz: checker, testLimiter: middleware.NewRateLimiter(testRequestsPerMinute, time.Minute)}
}

func (h *Handler) perm(resource, action string) func(http.Handler) http.Handler {
	return h.authz.RequirePermission(permModule, resource, action)
}

// Routes es el API publico, montado en /api/v1/campaigns detras del gateway.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/health", h.Health)
	r.With(h.perm(resCampaigns, actionRead)).Get("/", h.List)
	r.With(h.perm(resCampaigns, actionCreate)).Post("/", h.Create)
	r.Route("/{id}", func(r chi.Router) {
		r.With(h.perm(resCampaigns, actionRead)).Get("/", h.Get)
		r.With(h.perm(resCampaigns, actionUpdate)).Patch("/", h.Update)
		r.With(h.perm(resCampaigns, actionDelete)).Delete("/", h.Delete)
		r.With(h.perm(resCampaigns, actionSend)).Post("/schedule", h.Schedule)
		r.With(h.perm(resCampaigns, actionSend)).Post("/start", h.Start)
		r.With(h.perm(resCampaigns, actionCancel)).Post("/pause", h.Pause)
		r.With(h.perm(resCampaigns, actionSend)).Post("/resume", h.Resume)
		r.With(h.perm(resCampaigns, actionCancel)).Post("/cancel", h.Cancel)
		r.With(h.perm(resStats, actionRead)).Get("/batches", h.ListBatches)
		r.With(h.perm(resCampaigns, actionSend), h.testLimiter.LimitPerUser).Post("/test", h.SendTest)
	})
	return r
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
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

func idParam(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.ErrBadRequest(w, "identificador de campana no valido")
		return uuid.Nil, false
	}
	return id, true
}

// target resuelve empresa e id de la ruta.
func target(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	id, ok := idParam(w, r)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	return tenantID, id, true
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

// decodeOptional es DecodeJSONLimit para las acciones cuyo cuerpo es opcional: un
// cuerpo vacio no es un error.
func decodeOptional(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return fmt.Errorf("el contenido supera el limite de %d KB", maxBytes/1024)
		}
		return fmt.Errorf("no se pudo leer el cuerpo: %w", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if dec.More() {
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

func writeError(w http.ResponseWriter, err error) {
	var (
		limited  *ports.RateLimitedError
		blocked  *ports.BlockedError
		rejected *ports.RejectedError
	)
	switch {
	case errors.Is(err, domain.ErrCampaignNotFound):
		response.ErrNotFound(w, err.Error())
	case errors.Is(err, domain.ErrNameTaken),
		errors.Is(err, domain.ErrInvalidTransition),
		errors.Is(err, domain.ErrNotEditable),
		errors.Is(err, domain.ErrLockedWhilePaused),
		errors.Is(err, domain.ErrNotDeletable),
		errors.Is(err, domain.ErrConcurrentChange):
		response.ErrConflict(w, err.Error())
	case errors.Is(err, domain.ErrInvalidCampaign),
		errors.Is(err, domain.ErrNothingToUpdate),
		errors.Is(err, domain.ErrScheduleInPast),
		errors.Is(err, domain.ErrScheduleTooFar),
		errors.Is(err, domain.ErrTemplateNotFound),
		errors.Is(err, domain.ErrNoPublishedVersion),
		errors.Is(err, domain.ErrTemplateNotMarketing),
		errors.Is(err, domain.ErrTemplateVersionRequired):
		response.ErrValidation(w, err.Error())
	case errors.As(err, &limited):
		secs := int(math.Ceil(domain.ClampRetryAfter(limited.RetryAfter).Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		response.Err(w, http.StatusTooManyRequests, "RATE_LIMITED", "el envio esta limitado; vuelva a intentarlo mas tarde")
	case errors.As(err, &blocked):
		response.Err(w, http.StatusForbidden, blocked.Code, blocked.Message)
	case errors.As(err, &rejected):
		code := rejected.Code
		if code == "" {
			code = "SEND_REJECTED"
		}
		response.Err(w, http.StatusUnprocessableEntity, code, rejected.Message)
	case errors.Is(err, ports.ErrUnavailable):
		response.Err(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "un servicio necesario no respondio; vuelva a intentarlo")
	default:
		response.Unexpected(w, err)
	}
}

// ── Respuestas ───────────────────────────────────────────────────────────────

type statsResponse struct {
	domain.Counters
	Rates domain.Rates `json:"rates"`
}

type campaignResponse struct {
	ID              uuid.UUID       `json:"id"`
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	Status          domain.Status   `json:"status"`
	PauseReason     string          `json:"pause_reason"`
	FailureReason   string          `json:"failure_reason"`
	TemplateID      uuid.UUID       `json:"template_id"`
	TemplateVersion *int            `json:"template_version"`
	FromEmail       string          `json:"from_email"`
	FromName        string          `json:"from_name"`
	ReplyTo         string          `json:"reply_to"`
	Audience        domain.Audience `json:"audience"`
	ScheduledAt     *time.Time      `json:"scheduled_at"`
	StartedAt       *time.Time      `json:"started_at"`
	CompletedAt     *time.Time      `json:"completed_at"`
	ResumeAfter     *time.Time      `json:"resume_after"`
	CreatedBy       uuid.UUID       `json:"created_by"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	// Stats solo va a quien tiene campaigns/stats/read.
	Stats *statsResponse `json:"stats,omitempty"`
}

func toResponse(c *domain.Campaign, withStats bool) campaignResponse {
	out := campaignResponse{
		ID: c.ID, Name: c.Name, Description: c.Description, Status: c.Status,
		PauseReason: c.PauseReason, FailureReason: c.FailureReason,
		TemplateID: c.TemplateID, TemplateVersion: c.TemplateVersion,
		FromEmail: c.FromEmail, FromName: c.FromName, ReplyTo: c.ReplyTo,
		Audience:    c.Audience.Normalized(),
		ScheduledAt: c.ScheduledAt, StartedAt: c.StartedAt, CompletedAt: c.CompletedAt, ResumeAfter: c.ResumeAfter,
		CreatedBy: c.CreatedBy, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
	if withStats {
		out.Stats = &statsResponse{Counters: c.Counters, Rates: c.Counters.Rates()}
	}
	return out
}

type batchResponse struct {
	ID         uuid.UUID          `json:"id"`
	Seq        int                `json:"seq"`
	Status     domain.BatchStatus `json:"status"`
	Recipients int                `json:"recipients"`
	Accepted   int                `json:"accepted"`
	Suppressed int                `json:"suppressed"`
	Attempts   int                `json:"attempts"`
	LastError  string             `json:"last_error"`
	CreatedAt  time.Time          `json:"created_at"`
	UpdatedAt  time.Time          `json:"updated_at"`
}

// canReadStats consulta el permiso de estadisticas; si no se puede comprobar, no se
// muestran.
func (h *Handler) canReadStats(r *http.Request) bool {
	ok, err := h.authz.Allowed(r.Context(), permModule, resStats, actionRead)
	return err == nil && ok
}

// ── Campanas ─────────────────────────────────────────────────────────────────

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	v := validate.New()
	v.OneOf("status", q.Get("status"), statusNames())
	v.MaxLength("search", q.Get("search"), maxSearchLength)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	page, perPage := parsePagination(r)
	list, total, err := h.uc.List(r.Context(), tenantID, ports.ListFilter{
		Status: domain.Status(q.Get("status")), Search: q.Get("search"), Page: page, PerPage: perPage,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	withStats := h.canReadStats(r)
	out := make([]campaignResponse, len(list))
	for i := range list {
		out[i] = toResponse(&list[i], withStats)
	}
	response.JSONWithMeta(w, http.StatusOK, out, response.PageMeta(total, page, perPage))
}

type createRequest struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	TemplateID  uuid.UUID       `json:"template_id"`
	FromEmail   string          `json:"from_email"`
	FromName    string          `json:"from_name"`
	ReplyTo     string          `json:"reply_to"`
	Audience    domain.Audience `json:"audience"`
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	userID, err := uuid.Parse(middleware.GetUserID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "la sesion no lleva usuario")
		return
	}
	var req createRequest
	if err := validate.DecodeJSONLimit(w, r, &req, defaultBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	c, err := h.uc.Create(r.Context(), tenantID, domain.NewCampaignInput{
		Name: req.Name, Description: req.Description, TemplateID: req.TemplateID,
		FromEmail: req.FromEmail, FromName: req.FromName, ReplyTo: req.ReplyTo,
		Audience: req.Audience, CreatedBy: userID,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, toResponse(c, h.canReadStats(r)))
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	tenantID, id, ok := target(w, r)
	if !ok {
		return
	}
	c, err := h.uc.Get(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toResponse(c, h.canReadStats(r)))
}

type updateRequest struct {
	Name        *string          `json:"name"`
	Description *string          `json:"description"`
	TemplateID  *uuid.UUID       `json:"template_id"`
	FromEmail   *string          `json:"from_email"`
	FromName    *string          `json:"from_name"`
	ReplyTo     *string          `json:"reply_to"`
	Audience    *domain.Audience `json:"audience"`
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	tenantID, id, ok := target(w, r)
	if !ok {
		return
	}
	var req updateRequest
	if err := validate.DecodeJSONLimit(w, r, &req, defaultBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	c, err := h.uc.Update(r.Context(), tenantID, id, domain.Patch{
		Name: req.Name, Description: req.Description, TemplateID: req.TemplateID,
		FromEmail: req.FromEmail, FromName: req.FromName, ReplyTo: req.ReplyTo, Audience: req.Audience,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toResponse(c, h.canReadStats(r)))
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	tenantID, id, ok := target(w, r)
	if !ok {
		return
	}
	if err := h.uc.Delete(r.Context(), tenantID, id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Ciclo de vida ────────────────────────────────────────────────────────────

type scheduleRequest struct {
	ScheduledAt *time.Time `json:"scheduled_at"`
	// TemplateVersion es opcional: sin ella se fija la version publicada ahora.
	TemplateVersion *int `json:"template_version"`
}

func (h *Handler) Schedule(w http.ResponseWriter, r *http.Request) {
	tenantID, id, ok := target(w, r)
	if !ok {
		return
	}
	var req scheduleRequest
	if err := validate.DecodeJSONLimit(w, r, &req, defaultBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	if req.ScheduledAt == nil {
		response.ErrValidation(w, "scheduled_at is required")
		return
	}
	c, err := h.uc.Schedule(r.Context(), tenantID, id, *req.ScheduledAt, req.TemplateVersion)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toResponse(c, h.canReadStats(r)))
}

type startRequest struct {
	TemplateVersion *int `json:"template_version"`
}

func (h *Handler) Start(w http.ResponseWriter, r *http.Request) {
	tenantID, id, ok := target(w, r)
	if !ok {
		return
	}
	var req startRequest
	if err := decodeOptional(w, r, &req, defaultBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	c, err := h.uc.Start(r.Context(), tenantID, id, req.TemplateVersion)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toResponse(c, h.canReadStats(r)))
}

func (h *Handler) Pause(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, h.uc.Pause)
}

func (h *Handler) Resume(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, h.uc.Resume)
}

func (h *Handler) Cancel(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, h.uc.Cancel)
}

func (h *Handler) lifecycle(w http.ResponseWriter, r *http.Request,
	fn func(ctx context.Context, tenantID, id uuid.UUID) (*domain.Campaign, error)) {
	tenantID, id, ok := target(w, r)
	if !ok {
		return
	}
	c, err := fn(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toResponse(c, h.canReadStats(r)))
}

func (h *Handler) ListBatches(w http.ResponseWriter, r *http.Request) {
	tenantID, id, ok := target(w, r)
	if !ok {
		return
	}
	page, perPage := parsePagination(r)
	batches, total, err := h.uc.ListBatches(r.Context(), tenantID, id, page, perPage)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]batchResponse, len(batches))
	for i, b := range batches {
		out[i] = batchResponse{
			ID: b.ID, Seq: b.Seq, Status: b.Status, Recipients: b.Recipients, Accepted: b.Accepted,
			Suppressed: b.Suppressed, Attempts: b.Attempts, LastError: b.LastError,
			CreatedAt: b.CreatedAt, UpdatedAt: b.UpdatedAt,
		}
	}
	response.JSONWithMeta(w, http.StatusOK, out, response.PageMeta(total, page, perPage))
}

type testRequest struct {
	Emails          []string `json:"emails"`
	TemplateVersion *int     `json:"template_version"`
}

func (h *Handler) SendTest(w http.ResponseWriter, r *http.Request) {
	tenantID, id, ok := target(w, r)
	if !ok {
		return
	}
	var req testRequest
	if err := validate.DecodeJSONLimit(w, r, &req, defaultBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	res, err := h.uc.SendTest(r.Context(), tenantID, id, app.TestInput{Emails: req.Emails, TemplateVersion: req.TemplateVersion})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusAccepted, res)
}

func statusNames() []string {
	statuses := domain.Statuses()
	out := make([]string, len(statuses))
	for i, s := range statuses {
		out[i] = string(s)
	}
	return out
}

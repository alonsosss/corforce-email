package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/audit/internal/app"
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/alonsosss/corforce-email/services/audit/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const permModule = "audit"

// verifyRetryAfterSeconds: cuando reintentar una verificacion rechazada por estar ya en curso.
const verifyRetryAfterSeconds = "30"

type Handler struct {
	uc    *app.AuditUseCase
	authz *authz.Checker
}

func NewHandler(uc *app.AuditUseCase, checker *authz.Checker) *Handler {
	return &Handler{uc: uc, authz: checker}
}

func (h *Handler) perm(resource, action string) func(http.Handler) http.Handler {
	return h.authz.RequirePermission(permModule, resource, action)
}

// Routes: el rastro, los eventos de seguridad y la cadena de integridad son de la propia
// empresa (su base solo guarda los suyos), asi que cada ruta exige un permiso de alcance
// tenant y ninguna necesita el rol superadmin. Leer el rastro es leer la actividad de
// todos los usuarios de la empresa: ningun rol lo tiene salvo que se le conceda.
//
// Los apuntes del API no entran por aqui, llegan por el bus (audit.api.write): negar una
// ruta no deja ninguna accion sin registrar.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Route("/logs", func(r chi.Router) {
		r.With(h.perm("logs", "read")).Get("/", h.searchLogs)
		r.With(h.perm("logs", "create")).Post("/", h.createLog)
		r.With(h.perm("logs", "create")).Post("/bulk", h.bulkCreateLogs)
		r.With(h.perm("logs", "read")).Get("/{id}", h.getLog)
	})
	r.Route("/security-events", func(r chi.Router) {
		r.With(h.perm("security_events", "read")).Get("/", h.listSecurityEvents)
		r.With(h.perm("security_events", "read")).Get("/unacknowledged", h.getUnacknowledged)
		r.With(h.perm("security_events", "read")).Get("/{id}", h.getSecurityEvent)
		r.With(h.perm("security_events", "acknowledge")).Post("/{id}/acknowledge", h.acknowledgeEvent)
	})
	// Recorre la cadena de hash entera para detectar registros editados o borrados en
	// la base: es la accion de verificar, no una lectura de estado guardado.
	r.With(h.perm("integrity", "verify")).Get("/integrity", h.verifyIntegrity)
	r.With(h.perm("logs", "read")).Get("/summary", h.getSummary)
	r.With(h.perm("logs", "read")).Get("/user-activity/{userId}", h.getUserActivity)
	r.With(h.perm("logs", "read")).Get("/changes/{logId}", h.getChanges)
	return r
}

func (h *Handler) verifyIntegrity(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrBadRequest(w, "invalid tenant")
		return
	}
	res, err := h.uc.VerifyChainIntegrity(r.Context(), tenantID)
	if errors.Is(err, domain.ErrVerificationBusy) {
		w.Header().Set("Retry-After", verifyRetryAfterSeconds)
		response.Err(w, http.StatusTooManyRequests, "VERIFICATION_BUSY", "ya hay una verificacion de la cadena en curso; reintenta cuando termine")
		return
	}
	if err != nil {
		response.ErrInternal(w)
		return
	}
	response.JSON(w, http.StatusOK, res)
}

func (h *Handler) searchLogs(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrBadRequest(w, "invalid tenant")
		return
	}
	page, pageSize := parsePagination(r)
	query := domain.AuditQuery{TenantID: tenantID}

	if v := r.URL.Query().Get("user_id"); v != "" {
		uid, err := uuid.Parse(v)
		if err != nil {
			response.ErrBadRequest(w, "invalid user_id")
			return
		}
		query.UserID = &uid
	}
	if v := r.URL.Query().Get("module"); v != "" {
		query.Module = &v
	}
	if v := r.URL.Query().Get("resource"); v != "" {
		query.Resource = &v
	}
	if v := r.URL.Query().Get("action"); v != "" {
		query.Action = &v
	}
	if v := r.URL.Query().Get("severity"); v != "" {
		query.Severity = &v
	}
	if v := r.URL.Query().Get("date_from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			response.ErrBadRequest(w, "invalid date_from format")
			return
		}
		query.DateFrom = &t
	}
	if v := r.URL.Query().Get("date_to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			response.ErrBadRequest(w, "invalid date_to format")
			return
		}
		query.DateTo = &t
	}
	if v := r.URL.Query().Get("ip_address"); v != "" {
		query.IPAddress = &v
	}

	logs, total, err := h.uc.SearchAuditLogs(r.Context(), query, page, pageSize)
	if err != nil {
		response.ErrInternal(w)
		return
	}

	response.JSONWithMeta(w, http.StatusOK, logs, response.PageMeta(total, page, pageSize))
}

type createLogRequest struct {
	UserID     string  `json:"user_id"`
	SessionID  *string `json:"session_id"`
	Action     string  `json:"action"`
	Module     string  `json:"module"`
	Resource   string  `json:"resource"`
	ResourceID *string `json:"resource_id"`
	IPAddress  string  `json:"ip_address"`
	UserAgent  *string `json:"user_agent"`
	RequestID  *string `json:"request_id"`
	Before     *string `json:"before"`
	After      *string `json:"after"`
	Severity   string  `json:"severity"`
}

// errOtherUser: con sesion, un apunte solo se registra a nombre de quien la tiene. Solo
// una llamada interna (sin usuario) puede registrar a nombre de otro.
const errOtherUser = "no se puede registrar un apunte a nombre de otro usuario"

// Limites de las columnas de audit.audit_logs (varchar): un valor mas largo lo rechazaria la
// base con un 500 en lugar de un 422 que diga que corregir.
const (
	maxShortField = 100
	maxIPLength   = 45
	// El lote entero se escribe con el candado de la cadena tomado: sin tope, uno grande
	// bloquearia al resto de escritores de la empresa.
	maxBulkLogs = 500
)

// bindSessionUser fija el usuario del apunte al de la sesion. Devuelve false si el cuerpo
// pide registrarlo a nombre de otro.
func bindSessionUser(r *http.Request, req *createLogRequest) bool {
	sessionUser := middleware.GetUserID(r.Context())
	if sessionUser == "" {
		return true
	}
	if req.UserID != "" && req.UserID != sessionUser {
		return false
	}
	req.UserID = sessionUser
	return true
}

func validateLogRequest(v *validate.Validator, req *createLogRequest) {
	v.Required("user_id", req.UserID)
	v.UUID("user_id", req.UserID)
	v.Required("action", req.Action)
	v.MaxLength("action", req.Action, maxShortField)
	v.Required("module", req.Module)
	v.MaxLength("module", req.Module, maxShortField)
	v.Required("resource", req.Resource)
	v.Required("severity", req.Severity)
	v.OneOf("severity", req.Severity, domain.ValidSeverities)
	v.MaxLength("ip_address", req.IPAddress, maxIPLength)
	if req.RequestID != nil {
		v.MaxLength("request_id", *req.RequestID, maxShortField)
	}
	if req.SessionID != nil {
		v.UUID("session_id", *req.SessionID)
	}
	requireJSON(v, "before", req.Before)
	requireJSON(v, "after", req.After)
}

func requireJSON(v *validate.Validator, field string, doc *string) {
	if doc != nil && !json.Valid([]byte(*doc)) {
		v.Add(field, "must be a valid JSON document")
	}
}

// toAuditLog exige una peticion ya validada.
func (req *createLogRequest) toAuditLog(tenantID uuid.UUID) *domain.AuditLog {
	userID, _ := uuid.Parse(req.UserID)
	l := &domain.AuditLog{
		TenantID:   tenantID,
		UserID:     userID,
		Action:     req.Action,
		Module:     req.Module,
		Resource:   req.Resource,
		ResourceID: req.ResourceID,
		IPAddress:  req.IPAddress,
		UserAgent:  req.UserAgent,
		RequestID:  req.RequestID,
		Before:     req.Before,
		After:      req.After,
		Severity:   req.Severity,
	}
	if req.SessionID != nil {
		sid, _ := uuid.Parse(*req.SessionID)
		l.SessionID = &sid
	}
	return l
}

func (h *Handler) createLog(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrBadRequest(w, "invalid tenant")
		return
	}

	var req createLogRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	if !bindSessionUser(r, &req) {
		response.ErrForbidden(w, errOtherUser)
		return
	}

	v := validate.New()
	validateLogRequest(v, &req)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}

	l := req.toAuditLog(tenantID)
	if err := h.uc.LogAction(r.Context(), l); err != nil {
		if errors.Is(err, domain.ErrInvalidSeverity) {
			response.ErrValidation(w, err.Error())
			return
		}
		response.ErrInternal(w)
		return
	}

	if l.Before != nil && l.After != nil {
		_ = h.uc.CompareChanges(r.Context(), l.TenantID, l.ID, *l.Before, *l.After)
	}

	response.JSON(w, http.StatusCreated, l)
}

type bulkLogRequest struct {
	Logs []createLogRequest `json:"logs"`
}

func (h *Handler) bulkCreateLogs(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrBadRequest(w, "invalid tenant")
		return
	}

	var req bulkLogRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	if len(req.Logs) == 0 || len(req.Logs) > maxBulkLogs {
		response.ErrValidation(w, fmt.Sprintf("logs must contain between 1 and %d entries", maxBulkLogs))
		return
	}

	logs := make([]*domain.AuditLog, 0, len(req.Logs))
	for i := range req.Logs {
		entry := &req.Logs[i]
		if !bindSessionUser(r, entry) {
			response.ErrForbidden(w, errOtherUser)
			return
		}
		v := validate.New()
		validateLogRequest(v, entry)
		if !v.Valid() {
			response.ErrValidation(w, fmt.Sprintf("logs[%d]: %s", i, v.Error()))
			return
		}
		logs = append(logs, entry.toAuditLog(tenantID))
	}

	if err := h.uc.BulkLogActions(r.Context(), logs); err != nil {
		if errors.Is(err, domain.ErrInvalidSeverity) {
			response.ErrValidation(w, err.Error())
			return
		}
		response.ErrInternal(w)
		return
	}

	response.JSON(w, http.StatusCreated, map[string]int{"count": len(logs)})
}

func (h *Handler) getLog(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.ErrBadRequest(w, "invalid id")
		return
	}
	tenantID, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrBadRequest(w, "invalid tenant")
		return
	}

	l, err := h.uc.GetAuditLog(r.Context(), id, tenantID)
	if err != nil {
		if errors.Is(err, domain.ErrLogNotFound) {
			response.ErrNotFound(w, err.Error())
			return
		}
		response.ErrInternal(w)
		return
	}

	response.JSON(w, http.StatusOK, l)
}

func (h *Handler) listSecurityEvents(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrBadRequest(w, "invalid tenant")
		return
	}

	page, pageSize := parsePagination(r)
	var filters ports.SecurityFilters

	if v := r.URL.Query().Get("event_type"); v != "" {
		filters.EventType = &v
	}
	if v := r.URL.Query().Get("risk_level"); v != "" {
		filters.RiskLevel = &v
	}
	if v := r.URL.Query().Get("date_from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			response.ErrBadRequest(w, "invalid date_from")
			return
		}
		filters.DateFrom = &t
	}
	if v := r.URL.Query().Get("date_to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			response.ErrBadRequest(w, "invalid date_to")
			return
		}
		filters.DateTo = &t
	}
	if v := r.URL.Query().Get("acknowledged"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			response.ErrBadRequest(w, "invalid acknowledged")
			return
		}
		filters.Acknowledged = &b
	}

	events, total, err := h.uc.GetSecurityEvents(r.Context(), tenantID, filters, page, pageSize)
	if err != nil {
		response.ErrInternal(w)
		return
	}

	response.JSONWithMeta(w, http.StatusOK, events, response.PageMeta(total, page, pageSize))
}

func (h *Handler) getSecurityEvent(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.ErrBadRequest(w, "invalid id")
		return
	}
	tenantID, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrBadRequest(w, "invalid tenant")
		return
	}

	evt, err := h.uc.GetSecurityEvent(r.Context(), id, tenantID)
	if err != nil {
		if errors.Is(err, domain.ErrSecurityEventNotFound) {
			response.ErrNotFound(w, err.Error())
			return
		}
		response.ErrInternal(w)
		return
	}

	response.JSON(w, http.StatusOK, evt)
}

func (h *Handler) acknowledgeEvent(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.ErrBadRequest(w, "invalid id")
		return
	}
	tenantID, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrBadRequest(w, "invalid tenant")
		return
	}

	userID, err := uuid.Parse(middleware.GetUserID(r.Context()))
	if err != nil {
		response.ErrBadRequest(w, "invalid user")
		return
	}

	if err := h.uc.AcknowledgeSecurityEvent(r.Context(), id, tenantID, userID); err != nil {
		if errors.Is(err, domain.ErrSecurityEventNotFound) {
			response.ErrNotFound(w, err.Error())
			return
		}
		response.ErrInternal(w)
		return
	}

	response.JSON(w, http.StatusOK, map[string]string{"status": "acknowledged"})
}

func (h *Handler) getUnacknowledged(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrBadRequest(w, "invalid tenant")
		return
	}

	events, err := h.uc.GetUnacknowledgedSecurityEvents(r.Context(), tenantID)
	if err != nil {
		response.ErrInternal(w)
		return
	}

	response.JSON(w, http.StatusOK, events)
}

func (h *Handler) getSummary(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrBadRequest(w, "invalid tenant")
		return
	}

	from, to, err := parseDateRange(r)
	if err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}

	summary, err := h.uc.GetAuditSummary(r.Context(), tenantID, from, to)
	if err != nil {
		response.ErrInternal(w)
		return
	}

	response.JSON(w, http.StatusOK, summary)
}

func (h *Handler) getUserActivity(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrBadRequest(w, "invalid tenant")
		return
	}

	userID, err := uuid.Parse(chi.URLParam(r, "userId"))
	if err != nil {
		response.ErrBadRequest(w, "invalid userId")
		return
	}

	from, to, err := parseDateRange(r)
	if err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}

	count, err := h.uc.GetUserActivityReport(r.Context(), tenantID, userID, from, to)
	if err != nil {
		response.ErrInternal(w)
		return
	}

	response.JSON(w, http.StatusOK, map[string]interface{}{
		"user_id":       userID,
		"total_actions": count,
		"date_from":     from,
		"date_to":       to,
	})
}

func (h *Handler) getChanges(w http.ResponseWriter, r *http.Request) {
	logID, err := uuid.Parse(chi.URLParam(r, "logId"))
	if err != nil {
		response.ErrBadRequest(w, "invalid logId")
		return
	}
	tenantID, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrBadRequest(w, "invalid tenant")
		return
	}

	records, err := h.uc.GetChanges(r.Context(), tenantID, logID)
	if err != nil {
		response.ErrInternal(w)
		return
	}

	response.JSON(w, http.StatusOK, records)
}

func parsePagination(r *http.Request) (int, int) {
	page := 1
	pageSize := 20
	if v := r.URL.Query().Get("page"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			page = p
		}
	}
	if v := r.URL.Query().Get("per_page"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p <= 100 {
			pageSize = p
		}
	}
	return page, pageSize
}

func parseDateRange(r *http.Request) (time.Time, time.Time, error) {
	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")
	if fromStr == "" || toStr == "" {
		to := time.Now().UTC()
		from := to.AddDate(0, -1, 0)
		return from, to, nil
	}
	from, err := time.Parse(time.RFC3339, fromStr)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("invalid from date")
	}
	to, err := time.Parse(time.RFC3339, toStr)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("invalid to date")
	}
	return from, to, nil
}

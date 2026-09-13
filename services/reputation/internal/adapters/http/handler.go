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
	"github.com/alonsosss/corforce-email/services/reputation/internal/app"
	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/alonsosss/corforce-email/services/reputation/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

const (
	permModule = "reputation"

	bodyLimit      = 16 << 10
	defaultPerPage = 20
	maxPerPage     = 100
)

type Handler struct {
	uc    *app.UseCase
	authz *authz.Checker
}

func NewHandler(uc *app.UseCase, checker *authz.Checker) *Handler {
	return &Handler{uc: uc, authz: checker}
}

// PublicRoutes es lo que entra por el gateway con sesion de usuario. El gateway ya gateo
// el modulo; aqui cada accion exige su permiso concreto. Las rutas de /tenants operan
// sobre cualquier empresa y exigen ademas el rol superadmin en el propio servicio.
func (h *Handler) PublicRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/health", h.Health)
	r.With(h.authz.RequirePermission(permModule, "status", "read")).Get("/status", h.Status)
	r.With(h.authz.RequirePermission(permModule, "history", "read")).Get("/history", h.History)
	r.Route("/tenants", func(r chi.Router) {
		r.Use(middleware.RequireRoles(middleware.RoleSuperadmin))
		r.With(h.authz.RequirePermission(permModule, "tenants", "read")).Get("/", h.ListTenants)
		r.With(h.authz.RequirePermission(permModule, "tenants", "update")).Put("/{tenantID}/limits/{class}", h.SetLimits)
		r.With(h.authz.RequirePermission(permModule, "tenants", "update")).Post("/{tenantID}/{class}/suspend", h.Suspend)
		r.With(h.authz.RequirePermission(permModule, "tenants", "update")).Post("/{tenantID}/{class}/release", h.Release)
	})
	return r
}

// InternalRoutes es lo que llaman los servicios que envian, con el token interno y la
// empresa en X-Tenant-ID: la autorizacion previa a cada envio.
func (h *Handler) InternalRoutes() http.Handler {
	r := chi.NewRouter()
	r.Post("/authorize", h.Authorize)
	return r
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func tenantFrom(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "empresa no valida")
		return uuid.Nil, false
	}
	return id, true
}

func userFrom(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(middleware.GetUserID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "usuario no valido")
		return uuid.Nil, false
	}
	return id, true
}

// targetFrom lee la empresa y la clase de la ruta de plataforma.
func targetFrom(w http.ResponseWriter, r *http.Request) (uuid.UUID, domain.Class, bool) {
	tenantID, err := uuid.Parse(chi.URLParam(r, "tenantID"))
	if err != nil {
		response.ErrBadRequest(w, "identificador de empresa no valido")
		return uuid.Nil, "", false
	}
	class, err := domain.ParseClass(chi.URLParam(r, "class"))
	if err != nil {
		response.ErrValidation(w, err.Error())
		return uuid.Nil, "", false
	}
	return tenantID, class, true
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

// rate da a una tasa su forma de API: texto decimal con la escala con que se guarda.
func rate(d decimal.Decimal) string {
	return d.StringFixed(domain.RateScale)
}

func timeOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

type usageDTO struct {
	Limit int64  `json:"limit"`
	Used  *int64 `json:"used"`
}

func toUsage(u domain.Usage) usageDTO {
	return usageDTO{Limit: u.Limit, Used: u.Used}
}

type monthlyDTO struct {
	Limit     *int64 `json:"limit"`
	Used      int64  `json:"used"`
	Remaining *int64 `json:"remaining"`
}

type recordDTO struct {
	TenantID      uuid.UUID  `json:"tenant_id"`
	Class         string     `json:"class"`
	State         string     `json:"state"`
	Reason        string     `json:"reason"`
	Manual        bool       `json:"manual"`
	BounceRate    string     `json:"bounce_rate"`
	ComplaintRate string     `json:"complaint_rate"`
	ChangedAt     *time.Time `json:"changed_at"`
	ChangedBy     *uuid.UUID `json:"changed_by"`
}

func toRecord(rec domain.Record) recordDTO {
	return recordDTO{
		TenantID: rec.TenantID, Class: string(rec.Class), State: string(rec.State), Reason: rec.Reason,
		Manual: rec.Manual, BounceRate: rate(rec.BounceRate), ComplaintRate: rate(rec.ComplaintRate),
		ChangedAt: timeOrNil(rec.ChangedAt), ChangedBy: rec.ChangedBy,
	}
}

type windowDTO struct {
	Sent          int64  `json:"sent"`
	Bounced       int64  `json:"bounced"`
	Complained    int64  `json:"complained"`
	BounceRate    string `json:"bounce_rate"`
	ComplaintRate string `json:"complaint_rate"`
}

type classSummaryDTO struct {
	Class     string     `json:"class"`
	State     string     `json:"state"`
	Reason    string     `json:"reason"`
	Manual    bool       `json:"manual"`
	ChangedAt *time.Time `json:"changed_at"`
	ChangedBy *uuid.UUID `json:"changed_by"`
	Window    windowDTO  `json:"window"`
}

func toClassSummary(s app.ClassSummary) classSummaryDTO {
	return classSummaryDTO{
		Class: string(s.Record.Class), State: string(s.Record.State), Reason: s.Record.Reason,
		Manual: s.Record.Manual, ChangedAt: timeOrNil(s.Record.ChangedAt), ChangedBy: s.Record.ChangedBy,
		Window: windowDTO{
			Sent: s.Counts.Sent, Bounced: s.Counts.Bounced, Complained: s.Counts.Complained,
			BounceRate: rate(s.BounceRate), ComplaintRate: rate(s.ComplaintRate),
		},
	}
}

type thresholdsDTO struct {
	BounceWarn     string `json:"bounce_warn"`
	BounceBlock    string `json:"bounce_block"`
	ComplaintWarn  string `json:"complaint_warn"`
	ComplaintBlock string `json:"complaint_block"`
}

type classStatusDTO struct {
	classSummaryDTO
	Thresholds thresholdsDTO `json:"thresholds"`
	Hourly     usageDTO      `json:"hourly"`
	Daily      usageDTO      `json:"daily"`
}

type statusDTO struct {
	WindowDays  int              `json:"window_days"`
	WindowStart string           `json:"window_start"`
	MinVolume   int64            `json:"min_volume"`
	Classes     []classStatusDTO `json:"classes"`
}

type authorizeRequest struct {
	Class string `json:"class"`
	Count int64  `json:"count"`
}

type authorizeDTO struct {
	Allowed           bool        `json:"allowed"`
	Class             string      `json:"class"`
	State             string      `json:"state"`
	Reason            string      `json:"reason,omitempty"`
	RetryAfterSeconds *int64      `json:"retry_after_seconds,omitempty"`
	Hourly            usageDTO    `json:"hourly"`
	Daily             usageDTO    `json:"daily"`
	Monthly           *monthlyDTO `json:"monthly"`
}

func (h *Handler) Authorize(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req authorizeRequest
	if err := validate.DecodeJSONLimit(w, r, &req, bodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	v.Required("class", req.Class)
	if req.Class != "" {
		v.OneOf("class", req.Class, domain.ClassNames())
	}
	if req.Count < 1 || req.Count > domain.MaxAuthorizeCount {
		v.Add("count", "debe estar entre 1 y "+strconv.FormatInt(domain.MaxAuthorizeCount, 10))
	}
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	dec, err := h.uc.Authorize(r.Context(), app.AuthorizeInput{TenantID: tenantID, Class: domain.Class(req.Class), Count: req.Count})
	if err != nil {
		writeError(w, err)
		return
	}
	out := authorizeDTO{
		Allowed: dec.Allowed, Class: string(dec.Class), State: string(dec.State), Reason: dec.Reason,
		RetryAfterSeconds: dec.RetryAfterSeconds, Hourly: toUsage(dec.Hourly), Daily: toUsage(dec.Daily),
	}
	if dec.Monthly != nil {
		out.Monthly = &monthlyDTO{Limit: dec.Monthly.Limit, Used: dec.Monthly.Used, Remaining: dec.Monthly.Remaining}
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	st, err := h.uc.Status(r.Context(), tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	out := statusDTO{
		WindowDays: st.WindowDays, WindowStart: st.WindowStart.Format(time.DateOnly), MinVolume: st.MinVolume,
		Classes: make([]classStatusDTO, 0, len(st.Classes)),
	}
	for _, c := range st.Classes {
		out.Classes = append(out.Classes, classStatusDTO{
			classSummaryDTO: toClassSummary(c.ClassSummary),
			Thresholds: thresholdsDTO{
				BounceWarn: rate(c.Thresholds.BounceWarn), BounceBlock: rate(c.Thresholds.BounceBlock),
				ComplaintWarn: rate(c.Thresholds.ComplaintWarn), ComplaintBlock: rate(c.Thresholds.ComplaintBlock),
			},
			Hourly: toUsage(c.Hourly),
			Daily:  toUsage(c.Daily),
		})
	}
	response.JSON(w, http.StatusOK, out)
}

type historyDTO struct {
	ID            uuid.UUID  `json:"id"`
	Class         string     `json:"class"`
	From          string     `json:"from"`
	To            string     `json:"to"`
	Reason        string     `json:"reason"`
	BounceRate    string     `json:"bounce_rate"`
	ComplaintRate string     `json:"complaint_rate"`
	Manual        bool       `json:"manual"`
	ChangedBy     *uuid.UUID `json:"changed_by"`
	CreatedAt     time.Time  `json:"created_at"`
}

func (h *Handler) History(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	class := r.URL.Query().Get("class")
	if class != "" {
		if _, err := domain.ParseClass(class); err != nil {
			response.ErrValidation(w, err.Error())
			return
		}
	}
	page, perPage := parsePagination(r)
	items, total, err := h.uc.History(r.Context(), tenantID, ports.HistoryFilter{Class: domain.Class(class), Page: page, PerPage: perPage})
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]historyDTO, 0, len(items))
	for _, c := range items {
		out = append(out, historyDTO{
			ID: c.ID, Class: string(c.Class), From: string(c.From), To: string(c.To), Reason: c.Reason,
			BounceRate: rate(c.BounceRate), ComplaintRate: rate(c.ComplaintRate), Manual: c.Manual,
			ChangedBy: c.ChangedBy, CreatedAt: c.CreatedAt,
		})
	}
	response.JSONWithMeta(w, http.StatusOK, out, response.PageMeta(total, page, perPage))
}

type tenantDTO struct {
	TenantID    uuid.UUID         `json:"tenant_id"`
	Unavailable bool              `json:"unavailable"`
	Classes     []classSummaryDTO `json:"classes"`
}

func (h *Handler) ListTenants(w http.ResponseWriter, r *http.Request) {
	var filter domain.State
	if s := r.URL.Query().Get("state"); s != "" {
		st, err := domain.ParseState(s)
		if err != nil {
			response.ErrValidation(w, err.Error())
			return
		}
		filter = st
	}
	items, err := h.uc.ListTenants(r.Context(), filter)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]tenantDTO, 0, len(items))
	for _, t := range items {
		dto := tenantDTO{TenantID: t.TenantID, Unavailable: t.Unavailable, Classes: make([]classSummaryDTO, 0, len(t.Classes))}
		for _, c := range t.Classes {
			dto.Classes = append(dto.Classes, toClassSummary(c))
		}
		out = append(out, dto)
	}
	response.JSON(w, http.StatusOK, out)
}

type setLimitsRequest struct {
	Hourly *int64 `json:"hourly"`
	Daily  *int64 `json:"daily"`
}

type overrideDTO struct {
	Hourly    *int64    `json:"hourly"`
	Daily     *int64    `json:"daily"`
	UpdatedBy uuid.UUID `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

type limitsDTO struct {
	TenantID uuid.UUID    `json:"tenant_id"`
	Class    string       `json:"class"`
	Hourly   int64        `json:"hourly"`
	Daily    int64        `json:"daily"`
	Override *overrideDTO `json:"override"`
}

// SetLimits reemplaza los limites propios de la clase: un campo ausente o null vuelve al
// valor por defecto, y los dos ausentes retiran los limites propios.
func (h *Handler) SetLimits(w http.ResponseWriter, r *http.Request) {
	tenantID, class, ok := targetFrom(w, r)
	if !ok {
		return
	}
	userID, ok := userFrom(w, r)
	if !ok {
		return
	}
	var req setLimitsRequest
	if err := validate.DecodeJSONLimit(w, r, &req, bodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	res, err := h.uc.SetLimits(r.Context(), app.SetLimitsInput{
		TenantID: tenantID, Class: class, Hourly: req.Hourly, Daily: req.Daily, UpdatedBy: userID,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	out := limitsDTO{TenantID: tenantID, Class: string(class), Hourly: res.Effective.Hourly, Daily: res.Effective.Daily}
	if o := res.Override; o != nil {
		out.Override = &overrideDTO{Hourly: o.Hourly, Daily: o.Daily, UpdatedBy: o.UpdatedBy, UpdatedAt: o.UpdatedAt}
	}
	response.JSON(w, http.StatusOK, out)
}

type suspendRequest struct {
	Reason string `json:"reason"`
}

func (h *Handler) Suspend(w http.ResponseWriter, r *http.Request) {
	tenantID, class, ok := targetFrom(w, r)
	if !ok {
		return
	}
	userID, ok := userFrom(w, r)
	if !ok {
		return
	}
	var req suspendRequest
	if err := validate.DecodeJSONLimit(w, r, &req, bodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	rec, err := h.uc.Suspend(r.Context(), tenantID, class, req.Reason, userID)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toRecord(rec))
}

func (h *Handler) Release(w http.ResponseWriter, r *http.Request) {
	tenantID, class, ok := targetFrom(w, r)
	if !ok {
		return
	}
	userID, ok := userFrom(w, r)
	if !ok {
		return
	}
	rec, err := h.uc.Release(r.Context(), tenantID, class, userID)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toRecord(rec))
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrTenantNotFound):
		response.ErrNotFound(w, err.Error())
	case errors.Is(err, domain.ErrInvalidClass),
		errors.Is(err, domain.ErrInvalidState),
		errors.Is(err, domain.ErrInvalidCount),
		errors.Is(err, domain.ErrInvalidLimit),
		errors.Is(err, domain.ErrInvalidReason):
		response.ErrValidation(w, err.Error())
	default:
		response.Unexpected(w, err)
	}
}

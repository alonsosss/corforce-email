package http

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/billing/internal/app"
	"github.com/alonsosss/corforce-email/services/billing/internal/domain"
	"github.com/alonsosss/corforce-email/services/billing/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const (
	permModule     = "billing"
	bodyLimit      = 64 << 10
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

func (h *Handler) perm(resource, action string) func(http.Handler) http.Handler {
	return h.authz.RequirePermission(permModule, resource, action)
}

// PublicRoutes es lo que entra por el gateway con sesion. La empresa ve su plan y su
// consumo; la operacion de la plataforma exige ademas el rol superadmin.
func (h *Handler) PublicRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/health", h.Health)
	r.With(h.perm("subscription", "read")).Get("/subscription", h.GetSubscription)
	r.With(h.perm("usage", "read")).Get("/usage", h.GetUsage)

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireRoles(middleware.RoleSuperadmin))
		r.With(h.perm("plans", "read")).Get("/plans", h.ListPlans)
		r.With(h.perm("plans", "create")).Post("/plans", h.CreatePlan)
		r.With(h.perm("plans", "read")).Get("/plans/{id}", h.GetPlan)
		r.With(h.perm("plans", "update")).Patch("/plans/{id}", h.UpdatePlan)
		r.With(h.perm("plans", "delete")).Post("/plans/{id}/retire", h.RetirePlan)
		r.With(h.perm("subscriptions", "read")).Get("/subscriptions", h.ListSubscriptions)
		r.With(h.perm("subscriptions", "update")).Put("/subscriptions/{tenantID}", h.PutSubscription)
		r.With(h.perm("subscriptions", "read")).Get("/usage/{tenantID}", h.GetTenantUsage)
	})
	return r
}

// InternalRoutes es lo que llaman los demas servicios con el token interno y la empresa en
// X-Tenant-ID, antes de crear un buzon o un dominio o de enviar correo.
func (h *Handler) InternalRoutes() http.Handler {
	r := chi.NewRouter()
	r.Post("/entitlements/check", h.CheckEntitlement)
	return r
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func tenantFrom(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil || id == uuid.Nil {
		response.ErrUnauthorized(w, "empresa no valida")
		return uuid.Nil, false
	}
	return id, true
}

func uuidParam(w http.ResponseWriter, r *http.Request, name, label string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		response.ErrBadRequest(w, "identificador de "+label+" no valido")
		return uuid.Nil, false
	}
	return id, true
}

func parsePagination(r *http.Request) (int, int) {
	page, perPage := 1, defaultPerPage
	if v, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && v > 0 {
		page = v
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("per_page")); err == nil && v > 0 {
		perPage = min(v, maxPerPage)
	}
	return page, perPage
}

// ── Empresa ──────────────────────────────────────────────────────────────────

func (h *Handler) GetSubscription(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	sub, plan, err := h.uc.GetSubscription(r.Context(), tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toSubscriptionDTO(sub, plan))
}

func (h *Handler) GetUsage(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	h.writeUsage(w, r, tenantID)
}

func (h *Handler) writeUsage(w http.ResponseWriter, r *http.Request, tenantID uuid.UUID) {
	rep, err := h.uc.TenantUsage(r.Context(), tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toUsageDTO(rep))
}

// ── Planes (plataforma) ──────────────────────────────────────────────────────

type limitRequest struct {
	Resource         string  `json:"resource"`
	Included         *int64  `json:"included"`
	HardLimit        *bool   `json:"hard_limit"`
	OverageUnitPrice *string `json:"overage_unit_price"`
}

type createPlanRequest struct {
	Code          string         `json:"code"`
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	Currency      string         `json:"currency"`
	BasePrice     string         `json:"base_price"`
	BillingPeriod string         `json:"billing_period"`
	Limits        []limitRequest `json:"limits"`
}

type updatePlanRequest struct {
	Name          *string         `json:"name"`
	Description   *string         `json:"description"`
	Currency      *string         `json:"currency"`
	BasePrice     *string         `json:"base_price"`
	BillingPeriod *string         `json:"billing_period"`
	Limits        *[]limitRequest `json:"limits"`
}

// parseLimits lee los limites del cuerpo. Los importes llegan como texto ("0.0015"):
// un numero JSON se leeria como float.
func parseLimits(reqs []limitRequest) ([]domain.PlanLimit, error) {
	out := make([]domain.PlanLimit, 0, len(reqs))
	for _, l := range reqs {
		res, err := domain.ParseResource(l.Resource)
		if err != nil {
			return nil, err
		}
		if l.Included == nil || l.HardLimit == nil {
			return nil, fmt.Errorf("%w: limits: %s necesita included y hard_limit", domain.ErrInvalidPlan, res)
		}
		pl := domain.PlanLimit{Resource: res, Included: *l.Included, HardLimit: *l.HardLimit}
		if l.OverageUnitPrice != nil {
			d, err := domain.ParseUnitPrice(*l.OverageUnitPrice)
			if err != nil {
				return nil, fmt.Errorf("%w: limits: overage_unit_price de %s: %v", domain.ErrInvalidPlan, res, err)
			}
			pl.OverageUnitPrice = &d
		}
		out = append(out, pl)
	}
	return out, nil
}

func (h *Handler) ListPlans(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	v := validate.New()
	v.OneOf("status", status, []string{string(domain.PlanActive), string(domain.PlanRetired)})
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	plans, err := h.uc.ListPlans(r.Context(), domain.PlanStatus(status))
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toPlanDTOs(plans))
}

func (h *Handler) CreatePlan(w http.ResponseWriter, r *http.Request) {
	var req createPlanRequest
	if err := validate.DecodeJSONLimit(w, r, &req, bodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	price, err := domain.ParsePrice(req.BasePrice)
	if err != nil {
		writeError(w, fmt.Errorf("%w: base_price: %v", domain.ErrInvalidPlan, err))
		return
	}
	limits, err := parseLimits(req.Limits)
	if err != nil {
		writeError(w, err)
		return
	}
	p, err := h.uc.CreatePlan(r.Context(), domain.Plan{
		Code: req.Code, Name: req.Name, Description: req.Description, Currency: req.Currency,
		BasePrice: price, BillingPeriod: domain.BillingPeriod(req.BillingPeriod), Limits: limits,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, toPlanDTO(p))
}

func (h *Handler) GetPlan(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(w, r, "id", "plan")
	if !ok {
		return
	}
	p, err := h.uc.GetPlan(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toPlanDTO(p))
}

func (h *Handler) UpdatePlan(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(w, r, "id", "plan")
	if !ok {
		return
	}
	var req updatePlanRequest
	if err := validate.DecodeJSONLimit(w, r, &req, bodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	patch := domain.PlanPatch{Name: req.Name, Description: req.Description, Currency: req.Currency}
	if req.BasePrice != nil {
		price, err := domain.ParsePrice(*req.BasePrice)
		if err != nil {
			writeError(w, fmt.Errorf("%w: base_price: %v", domain.ErrInvalidPlan, err))
			return
		}
		patch.BasePrice = &price
	}
	if req.BillingPeriod != nil {
		bp := domain.BillingPeriod(*req.BillingPeriod)
		patch.BillingPeriod = &bp
	}
	if req.Limits != nil {
		limits, err := parseLimits(*req.Limits)
		if err != nil {
			writeError(w, err)
			return
		}
		patch.Limits, patch.ReplaceLimits = limits, true
	}
	if patch.Empty() {
		response.ErrValidation(w, "el cambio no incluye ningun campo")
		return
	}
	p, err := h.uc.UpdatePlan(r.Context(), id, patch)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toPlanDTO(p))
}

func (h *Handler) RetirePlan(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(w, r, "id", "plan")
	if !ok {
		return
	}
	p, err := h.uc.RetirePlan(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toPlanDTO(p))
}

// ── Suscripciones (plataforma) ───────────────────────────────────────────────

type putSubscriptionRequest struct {
	PlanCode    string       `json:"plan_code"`
	Status      *string      `json:"status"`
	TrialEndsAt optionalTime `json:"trial_ends_at"`
	CancelAt    optionalTime `json:"cancel_at"`
}

func statusNames() []string {
	statuses := domain.SubscriptionStatuses()
	out := make([]string, len(statuses))
	for i, s := range statuses {
		out[i] = string(s)
	}
	return out
}

func (h *Handler) ListSubscriptions(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	v := validate.New()
	v.OneOf("status", status, statusNames())
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	page, perPage := parsePagination(r)
	subs, total, err := h.uc.ListSubscriptions(r.Context(), ports.SubscriptionFilter{
		Status: domain.SubscriptionStatus(status), Page: page, PerPage: perPage,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSONWithMeta(w, http.StatusOK, toSubscriptionDTOs(subs), response.PageMeta(total, page, perPage))
}

func (h *Handler) PutSubscription(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := uuidParam(w, r, "tenantID", "empresa")
	if !ok {
		return
	}
	var req putSubscriptionRequest
	if err := validate.DecodeJSONLimit(w, r, &req, bodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	v.Required("plan_code", req.PlanCode)
	v.MaxLength("plan_code", req.PlanCode, 40)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	upd := domain.SubscriptionUpdate{TrialEndsAt: req.TrialEndsAt.toDomain(), CancelAt: req.CancelAt.toDomain()}
	if req.Status != nil {
		st, err := domain.ParseSubscriptionStatus(*req.Status)
		if err != nil {
			writeError(w, err)
			return
		}
		upd.Status = &st
	}
	sub, created, err := h.uc.PutSubscription(r.Context(), tenantID, app.PutSubscriptionInput{PlanCode: req.PlanCode, Update: upd})
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	response.JSON(w, status, toSubscriptionDTO(sub, nil))
}

func (h *Handler) GetTenantUsage(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := uuidParam(w, r, "tenantID", "empresa")
	if !ok {
		return
	}
	h.writeUsage(w, r, tenantID)
}

// ── Interno ──────────────────────────────────────────────────────────────────

type checkRequest struct {
	Resource string `json:"resource"`
	// Quantity ausente = 1: el caso comun es crear un objeto.
	Quantity *int64 `json:"quantity"`
}

func (h *Handler) CheckEntitlement(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req checkRequest
	if err := validate.DecodeJSONLimit(w, r, &req, bodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	res, err := domain.ParseResource(req.Resource)
	if err != nil {
		writeError(w, err)
		return
	}
	quantity := int64(1)
	if req.Quantity != nil {
		quantity = *req.Quantity
	}
	ent, err := h.uc.CheckEntitlement(r.Context(), tenantID, res, quantity)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toEntitlementDTO(ent))
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrPlanNotFound):
		response.ErrNotFound(w, err.Error())
	case errors.Is(err, domain.ErrSubscriptionNotFound):
		response.Err(w, http.StatusNotFound, "NO_SUBSCRIPTION", err.Error())
	case errors.Is(err, domain.ErrPlanCodeTaken):
		response.Err(w, http.StatusConflict, "PLAN_CODE_TAKEN", err.Error())
	case errors.Is(err, domain.ErrPlanInUse):
		response.Err(w, http.StatusConflict, "PLAN_IN_USE", err.Error())
	case errors.Is(err, domain.ErrPlanRetired):
		response.Err(w, http.StatusConflict, "PLAN_RETIRED", err.Error())
	case errors.Is(err, domain.ErrSubscriptionExists):
		response.ErrConflict(w, err.Error())
	case errors.Is(err, domain.ErrInvalidPlan),
		errors.Is(err, domain.ErrInvalidAmount),
		errors.Is(err, domain.ErrInvalidResource),
		errors.Is(err, domain.ErrInvalidSubscription),
		errors.Is(err, domain.ErrInvalidQuantity),
		errors.Is(err, domain.ErrUnknownPlanCode):
		response.ErrValidation(w, err.Error())
	default:
		response.Unexpected(w, err)
	}
}

package http

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/analytics/internal/app"
	"github.com/alonsosss/corforce-email/services/analytics/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const (
	permModule   = "analytics"
	permResource = "reports"
	permAction   = "read"

	defaultPerPage = 20
	maxPerPage     = 100
)

// Permissions es la tercera capa de acceso (pkg/authz.Checker la cumple).
type Permissions interface {
	RequirePermission(module, resource, action string) func(http.Handler) http.Handler
}

type Handler struct {
	uc    *app.UseCase
	perms Permissions
}

func NewHandler(uc *app.UseCase, perms Permissions) *Handler {
	return &Handler{uc: uc, perms: perms}
}

// Routes es lo que entra por el gateway bajo /api/v1/analytics. Todas son lecturas y
// exigen analytics/reports/read ademas del gateo por modulo del gateway.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(h.perms.RequirePermission(permModule, permResource, permAction))
	r.Get("/overview", h.Overview)
	r.Get("/timeseries", h.Timeseries)
	r.Get("/campaigns", h.ListCampaigns)
	r.Get("/campaigns/{id}", h.GetCampaign)
	r.Get("/campaigns/{id}/links", h.CampaignLinks)
	r.Get("/domains", h.Domains)
	r.Get("/meta", h.Meta)
	return r
}

type pointDTO struct {
	Day string `json:"day"`
	domain.Counters
}

type overviewResponse struct {
	From     string          `json:"from"`
	To       string          `json:"to"`
	Class    *string         `json:"class"`
	Timezone string          `json:"timezone"`
	Totals   domain.Counters `json:"totals"`
	Rates    domain.Rates    `json:"rates"`
}

type timeseriesResponse struct {
	From     string     `json:"from"`
	To       string     `json:"to"`
	Class    *string    `json:"class"`
	Timezone string     `json:"timezone"`
	Points   []pointDTO `json:"points"`
}

type campaignDTO struct {
	CampaignID  uuid.UUID       `json:"campaign_id"`
	Status      *string         `json:"status"`
	StartedAt   *time.Time      `json:"started_at"`
	CompletedAt *time.Time      `json:"completed_at"`
	FirstDay    *string         `json:"first_day"`
	LastDay     *string         `json:"last_day"`
	Totals      domain.Counters `json:"totals"`
	Rates       domain.Rates    `json:"rates"`
}

type campaignDetailResponse struct {
	campaignDTO
	From     string     `json:"from"`
	To       string     `json:"to"`
	Timezone string     `json:"timezone"`
	Points   []pointDTO `json:"points"`
}

type linkDTO struct {
	// URL vacia: los clics de las URL por encima del tope de la campana.
	URL            string    `json:"url"`
	Other          bool      `json:"other"`
	Clicks         int64     `json:"clicks"`
	UniqueClicks   int64     `json:"unique_clicks"`
	FirstClickedAt time.Time `json:"first_clicked_at"`
	LastClickedAt  time.Time `json:"last_clicked_at"`
}

type campaignLinksResponse struct {
	CampaignID  uuid.UUID `json:"campaign_id"`
	TotalLinks  int64     `json:"total_links"`
	TotalClicks int64     `json:"total_clicks"`
	Limit       int       `json:"limit"`
	Links       []linkDTO `json:"links"`
}

type domainDTO struct {
	RecipientDomain string          `json:"recipient_domain"`
	Totals          domain.Counters `json:"totals"`
	Rates           domain.Rates    `json:"rates"`
}

type domainsResponse struct {
	From     string      `json:"from"`
	To       string      `json:"to"`
	Class    *string     `json:"class"`
	Timezone string      `json:"timezone"`
	Limit    int         `json:"limit"`
	Domains  []domainDTO `json:"domains"`
}

func (h *Handler) Overview(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	q, ok := h.classQuery(w, r)
	if !ok {
		return
	}
	totals, err := h.uc.Overview(r.Context(), tenantID, q)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, overviewResponse{
		From: domain.FormatDate(q.Range.From), To: domain.FormatDate(q.Range.To), Class: classOf(q.Class),
		Timezone: domain.ReportTimezone, Totals: totals, Rates: totals.Rates(),
	})
}

func (h *Handler) Timeseries(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	q, ok := h.classQuery(w, r)
	if !ok {
		return
	}
	series, err := h.uc.Timeseries(r.Context(), tenantID, q)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, timeseriesResponse{
		From: domain.FormatDate(q.Range.From), To: domain.FormatDate(q.Range.To), Class: classOf(q.Class),
		Timezone: domain.ReportTimezone, Points: points(series),
	})
}

func (h *Handler) ListCampaigns(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	page, perPage := parsePagination(r)
	list, total, err := h.uc.Campaigns(r.Context(), tenantID, page, perPage)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]campaignDTO, 0, len(list))
	for _, s := range list {
		out = append(out, toCampaignDTO(s))
	}
	response.JSONWithMeta(w, http.StatusOK, out, response.PageMeta(total, page, perPage))
}

func (h *Handler) GetCampaign(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	campaignID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.ErrBadRequest(w, "identificador de campaña no válido")
		return
	}
	q := r.URL.Query()
	detail, err := h.uc.Campaign(r.Context(), tenantID, campaignID, q.Get("from"), q.Get("to"))
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, campaignDetailResponse{
		campaignDTO: toCampaignDTO(detail.Summary),
		From:        domain.FormatDate(detail.Range.From), To: domain.FormatDate(detail.Range.To),
		Timezone: domain.ReportTimezone, Points: points(detail.Series),
	})
}

// CampaignLinks devuelve los clics por enlace de una campana. Una campana sin clics (o que
// analytics aun no conoce) responde con la lista vacia: no hay nada que ocultar.
func (h *Handler) CampaignLinks(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	campaignID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.ErrBadRequest(w, "identificador de campaña no válido")
		return
	}
	limit, err := domain.ParseLinksLimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeError(w, err)
		return
	}
	links, err := h.uc.CampaignLinks(r.Context(), tenantID, campaignID, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]linkDTO, 0, len(links.Links))
	for _, l := range links.Links {
		out = append(out, linkDTO{
			URL: l.URL, Other: l.URL == domain.OtherLinks, Clicks: l.Clicks, UniqueClicks: l.UniqueClicks,
			FirstClickedAt: l.FirstClickedAt.UTC(), LastClickedAt: l.LastClickedAt.UTC(),
		})
	}
	response.JSON(w, http.StatusOK, campaignLinksResponse{
		CampaignID: campaignID, TotalLinks: links.TotalLinks, TotalClicks: links.TotalClicks,
		Limit: limit, Links: out,
	})
}

func (h *Handler) Domains(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	q, ok := h.classQuery(w, r)
	if !ok {
		return
	}
	limit, err := domain.ParseDomainLimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeError(w, err)
		return
	}
	list, err := h.uc.Domains(r.Context(), tenantID, q, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]domainDTO, 0, len(list))
	for _, d := range list {
		out = append(out, domainDTO{RecipientDomain: d.RecipientDomain, Totals: d.Totals, Rates: d.Totals.Rates()})
	}
	response.JSON(w, http.StatusOK, domainsResponse{
		From: domain.FormatDate(q.Range.From), To: domain.FormatDate(q.Range.To), Class: classOf(q.Class),
		Timezone: domain.ReportTimezone, Limit: limit, Domains: out,
	})
}

// tenantFrom exige la empresa del contexto; sin ella no hay datos que consultar.
func tenantFrom(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "empresa no válida")
		return uuid.Nil, false
	}
	return id, true
}

// classQuery lee from, to y class; responde 422 si no cumplen el contrato.
func (h *Handler) classQuery(w http.ResponseWriter, r *http.Request) (domain.ClassQuery, bool) {
	q := r.URL.Query()
	rng, err := h.uc.ParseRange(q.Get("from"), q.Get("to"))
	if err != nil {
		writeError(w, err)
		return domain.ClassQuery{}, false
	}
	class, err := domain.ParseClassFilter(q.Get("class"))
	if err != nil {
		writeError(w, err)
		return domain.ClassQuery{}, false
	}
	return domain.ClassQuery{Range: rng, Class: class}, true
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

func classOf(c domain.Class) *string {
	if c == "" {
		return nil
	}
	s := string(c)
	return &s
}

func points(series []domain.DayCounters) []pointDTO {
	out := make([]pointDTO, 0, len(series))
	for _, p := range series {
		out = append(out, pointDTO{Day: domain.FormatDate(p.Day), Counters: p.Counters})
	}
	return out
}

func toCampaignDTO(s domain.CampaignSummary) campaignDTO {
	return campaignDTO{
		CampaignID: s.CampaignID, Status: s.Status,
		StartedAt: utc(s.StartedAt), CompletedAt: utc(s.CompletedAt),
		FirstDay: date(s.FirstDay), LastDay: date(s.LastDay),
		Totals: s.Totals, Rates: s.Totals.Rates(),
	}
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func date(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := domain.FormatDate(*t)
	return &s
}

func writeError(w http.ResponseWriter, err error) {
	var ve *domain.ValidationError
	switch {
	case errors.As(err, &ve):
		response.ErrValidation(w, ve.Error())
	case errors.Is(err, domain.ErrCampaignNotFound):
		response.ErrNotFound(w, err.Error())
	default:
		response.Unexpected(w, err)
	}
}

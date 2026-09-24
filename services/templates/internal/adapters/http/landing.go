package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/templates/internal/app"
	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/alonsosss/corforce-email/services/templates/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Recurso de las paginas de aterrizaje: migrations/registry/043_capture_permissions.sql.
const permPages = "pages"

// maxPageBody admite el HTML y el CSS maximos escapados en JSON y el documento del editor.
const maxPageBody = 2*(domain.MaxPageHTMLBytes+domain.MaxPageCSSBytes) + 2*domain.MaxEditorBytes

// pageRoutes cuelga de /api/v1/templates/pages.
func (h *Handler) pageRoutes(r chi.Router) {
	r.With(h.permOn(permPages, actionRead)).Get("/", h.ListPages)
	r.With(h.permOn(permPages, actionCreate)).Post("/", h.CreatePage)
	r.With(h.permOn(permPages, actionRead)).Get("/meta", h.PageMeta)
	r.Route("/{pageID}", func(r chi.Router) {
		r.With(h.permOn(permPages, actionRead)).Get("/", h.GetPage)
		r.With(h.permOn(permPages, actionUpdate)).Patch("/", h.UpdatePage)
		r.With(h.permOn(permPages, actionDelete)).Delete("/", h.DeletePage)
		r.With(h.permOn(permPages, actionCreate)).Post("/versions", h.CreatePageVersion)
		r.With(h.permOn(permPages, actionRead)).Get("/versions/{n}", h.GetPageVersion)
		r.With(h.permOn(permPages, actionPublish)).Post("/versions/{n}/publish", h.PublishPageVersion)
		r.With(h.permOn(permPages, actionPublish)).Post("/unpublish", h.UnpublishPage)
	})
}

func pageIDParam(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "pageID"))
	if err != nil {
		response.ErrBadRequest(w, "identificador de pagina no valido")
		return uuid.Nil, false
	}
	return id, true
}

// writePageError traduce los errores propios de las paginas y delega el resto.
func writePageError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrPageNotFound), errors.Is(err, domain.ErrPageVersionNotFound):
		response.ErrNotFound(w, err.Error())
	case errors.Is(err, domain.ErrPageNameTaken), errors.Is(err, domain.ErrPageSlugTaken),
		errors.Is(err, domain.ErrPageNotArchived), errors.Is(err, domain.ErrPageArchived),
		errors.Is(err, domain.ErrPageNotPublished), errors.Is(err, domain.ErrVersionAlreadyPublished):
		response.ErrConflict(w, err.Error())
	case errors.Is(err, domain.ErrInvalidPage), errors.Is(err, domain.ErrNothingToUpdate):
		response.ErrValidation(w, err.Error())
	case errors.Is(err, app.ErrPagesUnavailable):
		response.Err(w, http.StatusServiceUnavailable, "PAGES_UNAVAILABLE", err.Error())
	default:
		writeError(w, err)
	}
}

func (h *Handler) pageResponse(ctx context.Context, p *domain.LandingPage) (map[string]any, error) {
	prefix, err := h.uc.PagePublicPrefix(ctx, p.TenantID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id": p.ID, "name": p.Name, "slug": p.Slug, "status": p.Status, "noindex": p.NoIndex,
		"current_version": p.CurrentVersion, "public_url": prefix + p.Slug,
		"created_by": p.CreatedBy, "created_at": p.CreatedAt, "updated_at": p.UpdatedAt,
	}, nil
}

func pageVersionResponse(v *domain.LandingVersion) map[string]any {
	if v == nil {
		return nil
	}
	var editor any
	if v.Content.Editor != nil {
		editor = v.Content.Editor
	}
	return map[string]any{
		"id": v.ID, "page_id": v.PageID, "version": v.Version, "title": v.Content.Title,
		"description": v.Content.Description, "html": v.Content.HTML, "css": v.Content.CSS, "editor": editor,
		"status": v.Status, "published_at": v.PublishedAt, "created_by": v.CreatedBy, "created_at": v.CreatedAt,
	}
}

func (h *Handler) writePageDetail(w http.ResponseWriter, r *http.Request, status int, d *domain.LandingDetail) {
	page, err := h.pageResponse(r.Context(), d.Page)
	if err != nil {
		writePageError(w, err)
		return
	}
	versions := make([]map[string]any, 0, len(d.Versions))
	for _, s := range d.Versions {
		versions = append(versions, map[string]any{
			"id": s.ID, "version": s.Version, "status": s.Status, "published_at": s.PublishedAt,
			"created_by": s.CreatedBy, "created_at": s.CreatedAt,
		})
	}
	var current any
	if d.Current != nil {
		current = pageVersionResponse(d.Current)
	}
	response.JSON(w, status, map[string]any{"page": page, "current": current, "versions": versions})
}

type pageContentRequest struct {
	Title       string                     `json:"title"`
	Description string                     `json:"description"`
	HTML        string                     `json:"html"`
	CSS         string                     `json:"css"`
	Editor      *domain.PageEditorDocument `json:"editor"`
}

func (c *pageContentRequest) input() app.PageContentInput {
	return app.PageContentInput{Title: c.Title, Description: c.Description, HTML: c.HTML, CSS: c.CSS, Editor: c.Editor}
}

type createPageRequest struct {
	Name    string              `json:"name"`
	Slug    string              `json:"slug"`
	NoIndex *bool               `json:"noindex"`
	Content *pageContentRequest `json:"content"`
}

func (h *Handler) CreatePage(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	userID, ok := userFrom(w, r)
	if !ok {
		return
	}
	var req createPageRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxPageBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	in := app.CreatePageInput{Name: req.Name, Slug: req.Slug, NoIndex: true}
	if req.NoIndex != nil {
		in.NoIndex = *req.NoIndex
	}
	if req.Content != nil {
		c := req.Content.input()
		in.Content = &c
	}
	d, err := h.uc.CreatePage(r.Context(), tenantID, userID, in)
	if err != nil {
		writePageError(w, err)
		return
	}
	h.writePageDetail(w, r, http.StatusCreated, d)
}

func (h *Handler) ListPages(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	perPage, _ := strconv.Atoi(q.Get("per_page"))
	page, perPage = app.NormalizePage(page, perPage)
	v := validate.New()
	v.OneOf("status", q.Get("status"), domain.PageStatuses())
	v.MaxLength("search", q.Get("search"), domain.MaxNameLength)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	items, total, err := h.uc.ListPages(r.Context(), tenantID, ports.PageFilter{
		Status: q.Get("status"), Search: q.Get("search"), Offset: (page - 1) * perPage, Limit: perPage,
	})
	if err != nil {
		writePageError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, p := range items {
		item, err := h.pageResponse(r.Context(), p)
		if err != nil {
			writePageError(w, err)
			return
		}
		out = append(out, item)
	}
	response.JSONWithMeta(w, http.StatusOK, out, response.PageMeta(total, page, perPage))
}

func (h *Handler) GetPage(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := pageIDParam(w, r)
	if !ok {
		return
	}
	d, err := h.uc.GetPage(r.Context(), tenantID, id)
	if err != nil {
		writePageError(w, err)
		return
	}
	h.writePageDetail(w, r, http.StatusOK, d)
}

type updatePageRequest struct {
	Name    *string `json:"name"`
	Slug    *string `json:"slug"`
	NoIndex *bool   `json:"noindex"`
	Status  *string `json:"status"`
}

func (h *Handler) UpdatePage(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := pageIDParam(w, r)
	if !ok {
		return
	}
	var req updatePageRequest
	if err := validate.DecodeJSONLimit(w, r, &req, 16<<10); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	if req.Status != nil {
		v := validate.New()
		v.OneOf("status", *req.Status, domain.PageStatuses())
		if !v.Valid() {
			response.ErrValidation(w, v.Error())
			return
		}
	}
	d, err := h.uc.UpdatePage(r.Context(), tenantID, id, app.UpdatePageInput{
		Name: req.Name, Slug: req.Slug, NoIndex: req.NoIndex, Status: req.Status,
	})
	if err != nil {
		writePageError(w, err)
		return
	}
	h.writePageDetail(w, r, http.StatusOK, d)
}

func (h *Handler) DeletePage(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := pageIDParam(w, r)
	if !ok {
		return
	}
	if err := h.uc.DeletePage(r.Context(), tenantID, id); err != nil {
		writePageError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) CreatePageVersion(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	userID, ok := userFrom(w, r)
	if !ok {
		return
	}
	id, ok := pageIDParam(w, r)
	if !ok {
		return
	}
	var req pageContentRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxPageBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v, err := h.uc.CreatePageVersion(r.Context(), tenantID, id, userID, req.input())
	if err != nil {
		writePageError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, pageVersionResponse(v))
}

func (h *Handler) GetPageVersion(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := pageIDParam(w, r)
	if !ok {
		return
	}
	n, ok := versionParam(w, r)
	if !ok {
		return
	}
	v, err := h.uc.GetPageVersion(r.Context(), tenantID, id, n)
	if err != nil {
		writePageError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, pageVersionResponse(v))
}

func (h *Handler) PublishPageVersion(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := pageIDParam(w, r)
	if !ok {
		return
	}
	n, ok := versionParam(w, r)
	if !ok {
		return
	}
	d, err := h.uc.PublishPageVersion(r.Context(), tenantID, id, n)
	if err != nil {
		writePageError(w, err)
		return
	}
	h.writePageDetail(w, r, http.StatusOK, d)
}

func (h *Handler) UnpublishPage(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := pageIDParam(w, r)
	if !ok {
		return
	}
	d, err := h.uc.UnpublishPage(r.Context(), tenantID, id)
	if err != nil {
		writePageError(w, err)
		return
	}
	h.writePageDetail(w, r, http.StatusOK, d)
}

func (h *Handler) PageMeta(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	prefix, err := h.uc.PagePublicPrefix(r.Context(), tenantID)
	if err != nil {
		writePageError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{
		"editor_kinds": domain.PageEditorKinds(), "statuses": domain.PageStatuses(),
		"max_html_bytes": domain.MaxPageHTMLBytes, "max_css_bytes": domain.MaxPageCSSBytes,
		"max_editor_bytes": domain.MaxEditorBytes, "max_name_length": domain.MaxNameLength,
		"max_title_length": domain.MaxPageTitleLength, "max_description_length": domain.MaxPageDescriptionLen,
		"max_slug_length": domain.MaxPageSlugLength, "slug_pattern": domain.PageSlugPattern,
		"public_prefix": prefix,
	})
}

// ── Publico ──────────────────────────────────────────────────────────────────

// publicPageTimeout acota lo que espera una pagina publica a la base.
const publicPageTimeout = 10 * time.Second

// PublicRoutes cuelga de /api/v1/public/templates: sin sesion, por el gateway (y su alias
// /p/<empresa>/<slug>). Solo sirve versiones publicadas de paginas activas.
func (h *Handler) PublicRoutes(tenants *db.TenantDB, logger *zap.Logger) http.Handler {
	r := chi.NewRouter()
	r.Get("/pages/{tenant}/{slug}", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), publicPageTimeout)
		defer cancel()
		tenantID, pool, err := tenants.ResolveTenantBySlug(ctx, chi.URLParam(r, "tenant"))
		if err != nil {
			if db.IsUnknownTenant(err) {
				writePublicPageMissing(w)
				return
			}
			logger.Error("templates: base de la empresa no disponible para una pagina publica", zap.Error(err))
			http.Error(w, "servicio no disponible", http.StatusServiceUnavailable)
			return
		}
		id, err := uuid.Parse(tenantID)
		if err != nil {
			writePublicPageMissing(w)
			return
		}
		page, err := h.uc.ServePage(db.WithTenant(ctx, pool, tenantID), id, chi.URLParam(r, "slug"))
		if err != nil {
			if errors.Is(err, domain.ErrPageNotFound) {
				writePublicPageMissing(w)
				return
			}
			logger.Error("templates: no se pudo servir una pagina publica", zap.Error(err))
			http.Error(w, "servicio no disponible", http.StatusServiceUnavailable)
			return
		}
		hdr := w.Header()
		hdr.Set("Content-Type", "text/html; charset=utf-8")
		hdr.Set("Content-Security-Policy", page.CSP)
		hdr.Set("Cache-Control", "public, max-age=60")
		hdr.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		hdr.Set("X-Content-Type-Options", "nosniff")
		if page.NoIndex {
			hdr.Set("X-Robots-Tag", "noindex, nofollow")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(page.Body)
	})
	return r
}

func writePublicPageMissing(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte("pagina no encontrada\n"))
}

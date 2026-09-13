package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/contacts/internal/app"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/alonsosss/corforce-email/services/contacts/internal/ports"
	"github.com/alonsosss/corforce-email/services/contacts/internal/segment"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	modContacts = "contacts"
	modSegments = "segments"

	defaultBodyLimit = 64 << 10
	// segmentBodyLimit deja margen sobre la definicion para nombre y descripcion.
	segmentBodyLimit = segment.MaxDefinitionBytes + 16<<10
	// membersBodyLimit: 1000 uuids en JSON son unos 40 KB.
	membersBodyLimit = 128 << 10
	// confirmBodyLimit acota el POST de confirmacion (un formulario sin campos).
	confirmBodyLimit = 4 << 10
	// importRowBudget es el presupuesto de bytes por fila de importacion.
	importRowBudget = 1 << 10
	// maxEvidenceBytes acota la evidencia libre de un consentimiento.
	maxEvidenceBytes = 8 << 10

	defaultPerPage = 25
	maxPerPage     = 100

	maxEmailLength  = 320
	maxSearchLength = 200
	maxTagLength    = 64
)

// PermissionGuard es la tercera capa de acceso (pkg/authz.Checker la cumple).
type PermissionGuard interface {
	RequirePermission(module, resource, action string) func(http.Handler) http.Handler
}

type Deps struct {
	UC       *app.UseCase
	Perms    PermissionGuard
	TenantDB *db.TenantDB
	Logger   *zap.Logger
}

type Handler struct {
	uc       *app.UseCase
	perms    PermissionGuard
	tenantDB *db.TenantDB
	logger   *zap.Logger
}

func NewHandler(d Deps) *Handler {
	return &Handler{uc: d.UC, perms: d.Perms, tenantDB: d.TenantDB, logger: d.Logger}
}

// ContactRoutes cuelga de /api/v1/contacts (modulo contacts). El gateway ya gateo el
// modulo; aqui cada accion exige su permiso concreto.
func (h *Handler) ContactRoutes() http.Handler {
	perm := h.perms.RequirePermission
	r := chi.NewRouter()
	r.Get("/health", h.Health)
	r.With(perm(modContacts, "contacts", "read")).Get("/meta", h.ContactMeta)
	r.With(perm(modContacts, "contacts", "read")).Get("/", h.ListContacts)
	r.With(perm(modContacts, "contacts", "create")).Post("/", h.CreateContact)

	r.Route("/imports", func(r chi.Router) {
		r.With(perm(modContacts, "contacts", "import")).Post("/", h.Import)
		r.With(perm(modContacts, "contacts", "import")).Get("/", h.ListImports)
		r.With(perm(modContacts, "contacts", "import")).Get("/{importID}", h.GetImport)
	})
	r.Route("/lists", func(r chi.Router) {
		r.With(perm(modContacts, "lists", "read")).Get("/", h.ListLists)
		r.With(perm(modContacts, "lists", "create")).Post("/", h.CreateList)
		r.With(perm(modContacts, "lists", "read")).Get("/{listID}", h.GetList)
		r.With(perm(modContacts, "lists", "update")).Patch("/{listID}", h.UpdateList)
		r.With(perm(modContacts, "lists", "delete")).Delete("/{listID}", h.DeleteList)
		r.With(perm(modContacts, "lists", "update")).Post("/{listID}/members", h.AddMembers)
		r.With(perm(modContacts, "lists", "update")).Post("/{listID}/members/remove", h.RemoveMembers)
	})
	r.Route("/attributes", func(r chi.Router) {
		r.With(perm(modContacts, "attributes", "read")).Get("/", h.ListAttributes)
		r.With(perm(modContacts, "attributes", "create")).Post("/", h.CreateAttribute)
		r.With(perm(modContacts, "attributes", "update")).Patch("/{key}", h.UpdateAttribute)
		r.With(perm(modContacts, "attributes", "delete")).Delete("/{key}", h.DeleteAttribute)
	})
	r.Route("/{id}", func(r chi.Router) {
		r.With(perm(modContacts, "contacts", "read")).Get("/", h.GetContact)
		r.With(perm(modContacts, "contacts", "update")).Patch("/", h.UpdateContact)
		r.With(perm(modContacts, "contacts", "delete")).Delete("/", h.DeleteContact)
		r.With(perm(modContacts, "contacts", "export")).Get("/export", h.ExportContact)
		r.With(perm(modContacts, "consents", "read")).Get("/consents", h.ListConsents)
		r.With(perm(modContacts, "consents", "create")).Post("/consent", h.RecordConsent)
		r.With(perm(modContacts, "consents", "create")).Post("/consent/request", h.RequestConfirmation)
	})
	return r
}

// SegmentRoutes cuelga de /api/v1/segments (modulo segments). La previsualizacion viaja
// en POST por llevar la definicion y el gateway la gatea como lectura (read_posts).
func (h *Handler) SegmentRoutes() http.Handler {
	perm := h.perms.RequirePermission
	r := chi.NewRouter()
	r.With(perm(modSegments, "segments", "read")).Get("/", h.ListSegments)
	r.With(perm(modSegments, "segments", "create")).Post("/", h.CreateSegment)
	r.With(perm(modSegments, "segments", "read")).Get("/meta", h.SegmentMeta)
	r.With(perm(modSegments, "segments", "preview")).Post("/preview", h.PreviewSegment)
	r.With(perm(modSegments, "segments", "read")).Get("/{id}", h.GetSegment)
	r.With(perm(modSegments, "segments", "update")).Patch("/{id}", h.UpdateSegment)
	r.With(perm(modSegments, "segments", "delete")).Delete("/{id}", h.DeleteSegment)
	r.With(perm(modSegments, "segments", "read")).Get("/{id}/contacts", h.SegmentContacts)
	return r
}

// PublicRoutes cuelga de /api/v1/public/contacts: sin sesion, por el gateway. La empresa
// sale del enlace (t) y la autorizacion es el propio token (k).
func (h *Handler) PublicRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/confirm", h.ConfirmPage)
	r.Post("/confirm", h.Confirm)
	return r
}

// InternalRoutes cuelga de /internal/contacts: servicio a servicio con el token interno
// y la empresa en X-Tenant-ID. La audiencia la pide campaigns; los enviables por id, la
// pertenencia a una lista y el alta y baja de miembros, automations (las dos ultimas son
// los mismos casos de uso que el API con sesion).
func (h *Handler) InternalRoutes() http.Handler {
	r := chi.NewRouter()
	r.Post("/audience", h.Audience)
	r.Post("/sendable", h.Sendable)
	r.Post("/lists/{listID}/members", h.AddMembers)
	r.Post("/lists/{listID}/members/remove", h.RemoveMembers)
	r.Post("/lists/{listID}/members/check", h.CheckMembers)
	return r
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
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

func uuidParam(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		response.ErrBadRequest(w, "identificador no valido: "+name)
		return uuid.Nil, false
	}
	return id, true
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

// decodeOptionalJSON admite un cuerpo vacio (no hay nada que decodificar) y, si lo hay,
// aplica las mismas reglas que validate.DecodeJSONLimit.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return fmt.Errorf("el contenido supera el limite de %d KB", limit/1024)
		}
		return err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if dec.More() {
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

// parseEvidence exige un objeto JSON acotado y conserva los numeros tal cual.
func parseEvidence(raw json.RawMessage) (map[string]any, error) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return nil, nil
	}
	if len(t) > maxEvidenceBytes {
		return nil, fmt.Errorf("evidence admite como maximo %d KB", maxEvidenceBytes>>10)
	}
	dec := json.NewDecoder(bytes.NewReader(t))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil || out == nil {
		return nil, errors.New("evidence debe ser un objeto JSON")
	}
	return out, nil
}

// ── Contactos ────────────────────────────────────────────────────────────────

type consentRequest struct {
	Status    string          `json:"status"`
	Method    string          `json:"method"`
	Source    string          `json:"source"`
	IP        string          `json:"ip"`
	UserAgent string          `json:"user_agent"`
	Evidence  json.RawMessage `json:"evidence"`
}

// validate comprueba la forma; las reglas (quien puede conceder) son del dominio.
func (c *consentRequest) validate(v *validate.Validator, statuses []string) {
	v.Required("consent.status", c.Status)
	v.OneOf("consent.status", c.Status, statuses)
	v.Required("consent.method", c.Method)
	v.OneOf("consent.method", c.Method, stringsOf(domain.APIMethods()))
	v.Required("consent.source", c.Source)
	v.MaxLength("consent.source", c.Source, domain.MaxConsentSource)
	v.MaxLength("consent.ip", c.IP, 64)
	v.MaxLength("consent.user_agent", c.UserAgent, 2*domain.MaxUserAgent)
}

func (c *consentRequest) toInput() (app.ConsentInput, error) {
	evidence, err := parseEvidence(c.Evidence)
	if err != nil {
		return app.ConsentInput{}, err
	}
	return app.ConsentInput{
		Status: c.Status, Method: c.Method, Source: c.Source, IP: c.IP, UserAgent: c.UserAgent, Evidence: evidence,
	}, nil
}

type createContactRequest struct {
	Email      string                     `json:"email"`
	FirstName  string                     `json:"first_name"`
	LastName   string                     `json:"last_name"`
	Locale     string                     `json:"locale"`
	Timezone   string                     `json:"timezone"`
	Attributes map[string]json.RawMessage `json:"attributes"`
	Tags       []string                   `json:"tags"`
	Source     string                     `json:"source"`
	Consent    *consentRequest            `json:"consent"`
}

func (h *Handler) CreateContact(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req createContactRequest
	if err := validate.DecodeJSONLimit(w, r, &req, defaultBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	v.Required("email", req.Email)
	v.MaxLength("email", req.Email, maxEmailLength)
	v.OneOf("source", req.Source, []string{string(domain.SourceAPI), string(domain.SourceForm), string(domain.SourceIntegration)})
	if req.Consent != nil {
		req.Consent.validate(v, []string{string(domain.ConsentGranted)})
	}
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	in := app.CreateContactInput{
		Email: req.Email, FirstName: req.FirstName, LastName: req.LastName, Locale: req.Locale,
		Timezone: req.Timezone, Attributes: req.Attributes, Tags: req.Tags, Source: req.Source,
	}
	if req.Consent != nil {
		consent, err := req.Consent.toInput()
		if err != nil {
			response.ErrValidation(w, err.Error())
			return
		}
		in.Consent = &consent
	}
	c, err := h.uc.CreateContact(r.Context(), tenantID, in)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, c)
}

func (h *Handler) ListContacts(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	v := validate.New()
	v.MaxLength("search", q.Get("search"), maxSearchLength)
	v.MaxLength("tag", q.Get("tag"), maxTagLength)
	v.OneOf("status", q.Get("status"), statusNames())
	v.UUID("list_id", q.Get("list_id"))
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	page, perPage := parsePagination(r)
	f := ports.ContactFilter{
		Search: q.Get("search"), Status: domain.Status(q.Get("status")), Tag: q.Get("tag"), Page: page, PerPage: perPage,
	}
	if s := q.Get("list_id"); s != "" {
		id := uuid.MustParse(strings.ToLower(s))
		f.ListID = &id
	}
	contacts, total, err := h.uc.ListContacts(r.Context(), tenantID, f)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSONWithMeta(w, http.StatusOK, contacts, response.PageMeta(total, page, perPage))
}

func (h *Handler) GetContact(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "id")
	if !ok {
		return
	}
	c, err := h.uc.GetContact(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, c)
}

type updateContactRequest struct {
	Email      *string                    `json:"email"`
	FirstName  *string                    `json:"first_name"`
	LastName   *string                    `json:"last_name"`
	Locale     *string                    `json:"locale"`
	Timezone   *string                    `json:"timezone"`
	Attributes map[string]json.RawMessage `json:"attributes"`
	Tags       *[]string                  `json:"tags"`
}

func (h *Handler) UpdateContact(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "id")
	if !ok {
		return
	}
	var req updateContactRequest
	if err := validate.DecodeJSONLimit(w, r, &req, defaultBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	c, err := h.uc.UpdateContact(r.Context(), tenantID, id, app.UpdateContactInput{
		Email: req.Email, FirstName: req.FirstName, LastName: req.LastName, Locale: req.Locale,
		Timezone: req.Timezone, Attributes: req.Attributes, Tags: req.Tags,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, c)
}

func (h *Handler) DeleteContact(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.uc.DeleteContact(r.Context(), tenantID, id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ExportContact(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "id")
	if !ok {
		return
	}
	export, err := h.uc.ExportContact(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	response.JSON(w, http.StatusOK, export)
}

// ── Consentimiento ───────────────────────────────────────────────────────────

func (h *Handler) ListConsents(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "id")
	if !ok {
		return
	}
	consents, err := h.uc.ListConsents(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, consents)
}

func (h *Handler) RecordConsent(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "id")
	if !ok {
		return
	}
	var req consentRequest
	if err := validate.DecodeJSONLimit(w, r, &req, defaultBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	req.validate(v, stringsOf(domain.GrantStatuses()))
	if !v.Valid() {
		response.ErrValidation(w, strings.ReplaceAll(v.Error(), "consent.", ""))
		return
	}
	in, err := req.toInput()
	if err != nil {
		response.ErrValidation(w, err.Error())
		return
	}
	consent, err := h.uc.RecordConsent(r.Context(), tenantID, id, in)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, consent)
}

type confirmationRequest struct {
	Source string `json:"source"`
}

func (h *Handler) RequestConfirmation(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "id")
	if !ok {
		return
	}
	var req confirmationRequest
	if err := decodeOptionalJSON(w, r, &req, defaultBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	v.MaxLength("source", req.Source, domain.MaxConsentSource)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	out, err := h.uc.RequestConfirmation(r.Context(), tenantID, id, req.Source)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusAccepted, out)
}

// ── Importacion ──────────────────────────────────────────────────────────────

type importRowRequest struct {
	Email      string                     `json:"email"`
	FirstName  string                     `json:"first_name"`
	LastName   string                     `json:"last_name"`
	Locale     string                     `json:"locale"`
	Timezone   string                     `json:"timezone"`
	Attributes map[string]json.RawMessage `json:"attributes"`
	Tags       []string                   `json:"tags"`
}

type importConsentRequest struct {
	Status string `json:"status"`
	Basis  string `json:"basis"`
}

type importRequest struct {
	Rows           []importRowRequest    `json:"rows"`
	ListID         *uuid.UUID            `json:"list_id"`
	UpdateExisting bool                  `json:"update_existing"`
	Consent        *importConsentRequest `json:"consent"`
}

type importResponse struct {
	ID      uuid.UUID            `json:"id"`
	Status  domain.ImportStatus  `json:"status"`
	Total   int                  `json:"total"`
	Created int                  `json:"created"`
	Updated int                  `json:"updated"`
	Skipped int                  `json:"skipped"`
	Errors  []domain.ImportError `json:"errors"`
	// Suppressed: de los creados, cuantos entraron ya excluidos, por estado.
	Suppressed map[domain.Status]int `json:"suppressed"`
}

func (h *Handler) Import(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	userID, err := uuid.Parse(middleware.GetUserID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "usuario no valido")
		return
	}
	maxRows := h.uc.ImportMaxRows()
	var req importRequest
	if err := validate.DecodeJSONLimit(w, r, &req, int64(maxRows)*importRowBudget+defaultBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	consent := importConsentRequest{Status: string(domain.ConsentNone)}
	if req.Consent != nil {
		consent = *req.Consent
	}
	v := validate.New()
	if len(req.Rows) == 0 {
		v.Add("rows", "no puede estar vacio")
	}
	if len(req.Rows) > maxRows {
		v.Add("rows", "admite como maximo "+strconv.Itoa(maxRows)+" filas")
	}
	v.Required("consent.status", consent.Status)
	v.OneOf("consent.status", consent.Status, stringsOf(importConsentStatuses()))
	if consent.Status == string(domain.ConsentGranted) {
		v.Required("consent.basis", consent.Basis)
	}
	v.MaxLength("consent.basis", consent.Basis, app.MaxConsentBasis)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	rows := make([]app.ImportRow, len(req.Rows))
	for i, row := range req.Rows {
		rows[i] = app.ImportRow{
			Email: row.Email, FirstName: row.FirstName, LastName: row.LastName, Locale: row.Locale,
			Timezone: row.Timezone, Attributes: row.Attributes, Tags: row.Tags,
		}
	}
	imp, err := h.uc.Import(r.Context(), tenantID, app.ImportInput{
		Rows: rows, ListID: req.ListID, UpdateExisting: req.UpdateExisting,
		GrantConsent: consent.Status == string(domain.ConsentGranted), ConsentBasis: consent.Basis, CreatedBy: userID,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, importResponse{
		ID: imp.ID, Status: imp.Status, Total: imp.Total, Created: imp.Created,
		Updated: imp.Updated, Skipped: imp.Skipped, Errors: imp.Errors, Suppressed: imp.Suppressed,
	})
}

func (h *Handler) ListImports(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	page, perPage := parsePagination(r)
	imports, total, err := h.uc.ListImports(r.Context(), tenantID, page, perPage)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSONWithMeta(w, http.StatusOK, imports, response.PageMeta(total, page, perPage))
}

func (h *Handler) GetImport(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "importID")
	if !ok {
		return
	}
	imp, err := h.uc.GetImport(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, imp)
}

// ── Listas ───────────────────────────────────────────────────────────────────

type listRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

func validateListRequest(v *validate.Validator, req listRequest, create bool) {
	if create && req.Name == nil {
		v.Add("name", "es obligatorio")
	}
	if req.Name != nil {
		v.Required("name", *req.Name)
		v.MaxLength("name", *req.Name, 200)
	}
	if req.Description != nil {
		v.MaxLength("description", *req.Description, 2000)
	}
}

func (h *Handler) CreateList(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req listRequest
	if err := validate.DecodeJSONLimit(w, r, &req, defaultBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	validateListRequest(v, req, true)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	desc := ""
	if req.Description != nil {
		desc = *req.Description
	}
	l, err := h.uc.CreateList(r.Context(), tenantID, *req.Name, desc)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, l)
}

func (h *Handler) ListLists(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	page, perPage := parsePagination(r)
	lists, total, err := h.uc.ListLists(r.Context(), tenantID, page, perPage)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSONWithMeta(w, http.StatusOK, lists, response.PageMeta(total, page, perPage))
}

func (h *Handler) GetList(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "listID")
	if !ok {
		return
	}
	l, err := h.uc.GetList(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, l)
}

func (h *Handler) UpdateList(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "listID")
	if !ok {
		return
	}
	var req listRequest
	if err := validate.DecodeJSONLimit(w, r, &req, defaultBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	validateListRequest(v, req, false)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	l, err := h.uc.UpdateList(r.Context(), tenantID, id, req.Name, req.Description)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, l)
}

func (h *Handler) DeleteList(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "listID")
	if !ok {
		return
	}
	if err := h.uc.DeleteList(r.Context(), tenantID, id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type membersRequest struct {
	ContactIDs []uuid.UUID `json:"contact_ids"`
}

// decodeMembers lee y valida el cuerpo comun de alta y baja de miembros.
func decodeMembers(w http.ResponseWriter, r *http.Request) (tenantID, listID uuid.UUID, ids []uuid.UUID, ok bool) {
	if tenantID, ok = tenantFrom(w, r); !ok {
		return
	}
	if listID, ok = uuidParam(w, r, "listID"); !ok {
		return
	}
	var req membersRequest
	if err := validate.DecodeJSONLimit(w, r, &req, membersBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return tenantID, listID, nil, false
	}
	v := validate.New()
	if len(req.ContactIDs) == 0 {
		v.Add("contact_ids", "no puede estar vacio")
	}
	if len(req.ContactIDs) > app.MaxMembersPerRequest {
		v.Add("contact_ids", "admite como maximo "+strconv.Itoa(app.MaxMembersPerRequest)+" contactos")
	}
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return tenantID, listID, nil, false
	}
	return tenantID, listID, req.ContactIDs, true
}

func (h *Handler) AddMembers(w http.ResponseWriter, r *http.Request) {
	tenantID, listID, ids, ok := decodeMembers(w, r)
	if !ok {
		return
	}
	res, err := h.uc.AddMembers(r.Context(), tenantID, listID, ids)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, res)
}

func (h *Handler) RemoveMembers(w http.ResponseWriter, r *http.Request) {
	tenantID, listID, ids, ok := decodeMembers(w, r)
	if !ok {
		return
	}
	res, err := h.uc.RemoveMembers(r.Context(), tenantID, listID, ids)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, res)
}

// ── Atributos ────────────────────────────────────────────────────────────────

type createAttributeRequest struct {
	Key      string `json:"key"`
	Type     string `json:"type"`
	Label    string `json:"label"`
	Required bool   `json:"required"`
}

func (h *Handler) ListAttributes(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	defs, err := h.uc.ListAttributes(r.Context(), tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, defs)
}

func (h *Handler) CreateAttribute(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req createAttributeRequest
	if err := validate.DecodeJSONLimit(w, r, &req, defaultBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	v.Required("key", req.Key)
	v.Required("type", req.Type)
	v.MaxLength("label", req.Label, 200)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	d, err := h.uc.CreateAttribute(r.Context(), tenantID, app.CreateAttributeInput{
		Key: req.Key, Type: req.Type, Label: req.Label, Required: req.Required,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, d)
}

type updateAttributeRequest struct {
	Label    *string `json:"label"`
	Required *bool   `json:"required"`
}

func (h *Handler) UpdateAttribute(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req updateAttributeRequest
	if err := validate.DecodeJSONLimit(w, r, &req, defaultBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	if req.Label != nil {
		v := validate.New()
		v.MaxLength("label", *req.Label, 200)
		if !v.Valid() {
			response.ErrValidation(w, v.Error())
			return
		}
	}
	d, err := h.uc.UpdateAttribute(r.Context(), tenantID, chi.URLParam(r, "key"), req.Label, req.Required)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, d)
}

func (h *Handler) DeleteAttribute(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	if err := h.uc.DeleteAttribute(r.Context(), tenantID, chi.URLParam(r, "key")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Segmentos ────────────────────────────────────────────────────────────────

type segmentRequest struct {
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	Definition  json.RawMessage `json:"definition"`
}

func validateSegmentRequest(v *validate.Validator, req segmentRequest, create bool) {
	if create {
		if req.Name == nil {
			v.Add("name", "es obligatorio")
		}
		if len(bytes.TrimSpace(req.Definition)) == 0 {
			v.Add("definition", "es obligatoria")
		}
	}
	if req.Name != nil {
		v.Required("name", *req.Name)
		v.MaxLength("name", *req.Name, 200)
	}
	if req.Description != nil {
		v.MaxLength("description", *req.Description, 2000)
	}
}

func (h *Handler) CreateSegment(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req segmentRequest
	if err := validate.DecodeJSONLimit(w, r, &req, segmentBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	validateSegmentRequest(v, req, true)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	desc := ""
	if req.Description != nil {
		desc = *req.Description
	}
	s, err := h.uc.CreateSegment(r.Context(), tenantID, app.SegmentInput{Name: *req.Name, Description: desc, Definition: req.Definition})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, s)
}

func (h *Handler) ListSegments(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	page, perPage := parsePagination(r)
	segs, total, err := h.uc.ListSegments(r.Context(), tenantID, page, perPage)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSONWithMeta(w, http.StatusOK, segs, response.PageMeta(total, page, perPage))
}

func (h *Handler) GetSegment(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "id")
	if !ok {
		return
	}
	s, err := h.uc.GetSegment(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, s)
}

func (h *Handler) UpdateSegment(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "id")
	if !ok {
		return
	}
	var req segmentRequest
	if err := validate.DecodeJSONLimit(w, r, &req, segmentBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	validateSegmentRequest(v, req, false)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	s, err := h.uc.UpdateSegment(r.Context(), tenantID, id, app.UpdateSegmentInput{
		Name: req.Name, Description: req.Description, Definition: req.Definition,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, s)
}

func (h *Handler) DeleteSegment(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.uc.DeleteSegment(r.Context(), tenantID, id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SegmentMeta publica el catalogo del DSL con el esquema de la empresa: el editor de
// segmentos lo usa en lugar de copiar campos, operadores y valores.
func (h *Handler) SegmentMeta(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	catalog, err := h.uc.SegmentMeta(r.Context(), tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, catalog)
}

type previewRequest struct {
	Definition json.RawMessage `json:"definition"`
}

func (h *Handler) PreviewSegment(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req previewRequest
	if err := validate.DecodeJSONLimit(w, r, &req, segmentBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	if len(bytes.TrimSpace(req.Definition)) == 0 {
		response.ErrValidation(w, "definition: es obligatoria")
		return
	}
	preview, err := h.uc.PreviewSegment(r.Context(), tenantID, req.Definition)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, preview)
}

func (h *Handler) SegmentContacts(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := uuidParam(w, r, "id")
	if !ok {
		return
	}
	page, perPage := parsePagination(r)
	contacts, total, err := h.uc.SegmentContacts(r.Context(), tenantID, id, page, perPage)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSONWithMeta(w, http.StatusOK, contacts, response.PageMeta(total, page, perPage))
}

// ── Audiencia (interno) ──────────────────────────────────────────────────────

type audienceRequest struct {
	ListIDs           []uuid.UUID `json:"list_ids"`
	SegmentIDs        []uuid.UUID `json:"segment_ids"`
	ExcludeSegmentIDs []uuid.UUID `json:"exclude_segment_ids"`
	Cursor            string      `json:"cursor"`
	Limit             int         `json:"limit"`
}

// audienceContact es lo que campaigns necesita para personalizar y enviar; nada de
// estado, etiquetas ni fechas.
type audienceContact struct {
	ID         uuid.UUID      `json:"id"`
	Email      string         `json:"email"`
	FirstName  string         `json:"first_name"`
	LastName   string         `json:"last_name"`
	Locale     *string        `json:"locale"`
	Timezone   *string        `json:"timezone"`
	Attributes map[string]any `json:"attributes"`
}

type audienceResponse struct {
	Contacts   []audienceContact `json:"contacts"`
	NextCursor *string           `json:"next_cursor"`
}

func (h *Handler) Audience(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req audienceRequest
	if err := validate.DecodeJSONLimit(w, r, &req, defaultBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	if req.Limit < 0 || req.Limit > app.MaxAudienceLimit {
		v.Add("limit", "debe estar entre 1 y "+strconv.Itoa(app.MaxAudienceLimit))
	}
	v.MaxLength("cursor", req.Cursor, 64)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	page, err := h.uc.Audience(r.Context(), tenantID, app.AudienceInput{
		ListIDs: req.ListIDs, SegmentIDs: req.SegmentIDs, ExcludeSegmentIDs: req.ExcludeSegmentIDs,
		Cursor: req.Cursor, Limit: req.Limit,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	out := audienceResponse{Contacts: make([]audienceContact, len(page.Contacts)), NextCursor: page.NextCursor}
	for i, c := range page.Contacts {
		out.Contacts[i] = toAudienceContact(c)
	}
	response.JSON(w, http.StatusOK, out)
}

func toAudienceContact(c domain.Contact) audienceContact {
	return audienceContact{
		ID: c.ID, Email: c.Email, FirstName: c.FirstName, LastName: c.LastName,
		Locale: c.Locale, Timezone: c.Timezone, Attributes: c.Attributes,
	}
}

// ── Enviables y pertenencia a listas (interno) ──────────────────────────────

type sendableRequest struct {
	ContactIDs []uuid.UUID `json:"contact_ids"`
}

type sendableResponse struct {
	Contacts []audienceContact `json:"contacts"`
}

// Sendable devuelve, de los ids pedidos, SOLO los contactos enviables con lo necesario
// para personalizar (el mismo contrato de contacto que la audiencia).
func (h *Handler) Sendable(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req sendableRequest
	if err := validate.DecodeJSONLimit(w, r, &req, membersBodyLimit); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	if len(req.ContactIDs) == 0 {
		v.Add("contact_ids", "no puede estar vacio")
	}
	if len(req.ContactIDs) > app.MaxSendableIDs {
		v.Add("contact_ids", "admite como maximo "+strconv.Itoa(app.MaxSendableIDs)+" contactos")
	}
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	contacts, err := h.uc.SendableContacts(r.Context(), tenantID, req.ContactIDs)
	if err != nil {
		writeError(w, err)
		return
	}
	out := sendableResponse{Contacts: make([]audienceContact, len(contacts))}
	for i, c := range contacts {
		out.Contacts[i] = toAudienceContact(c)
	}
	response.JSON(w, http.StatusOK, out)
}

type membershipResponse struct {
	ContactIDs []uuid.UUID `json:"contact_ids"`
}

// CheckMembers dice cuales de los contactos pedidos estan en la lista; 404 si la lista no
// existe.
func (h *Handler) CheckMembers(w http.ResponseWriter, r *http.Request) {
	tenantID, listID, ids, ok := decodeMembers(w, r)
	if !ok {
		return
	}
	members, err := h.uc.ListMembersAmong(r.Context(), tenantID, listID, ids)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, membershipResponse{ContactIDs: members})
}

// ── Doble opt-in (publico) ───────────────────────────────────────────────────

// confirmParams lee t (empresa) y k (token) de la URL o, en el POST, del formulario.
func confirmParams(r *http.Request) (uuid.UUID, string, bool) {
	get := func(name string) string {
		if v := r.URL.Query().Get(name); v != "" {
			return v
		}
		if r.Method == http.MethodPost {
			return r.PostFormValue(name)
		}
		return ""
	}
	tenantID, err := uuid.Parse(get("t"))
	k := get("k")
	if err != nil || k == "" || len(k) > 128 {
		return uuid.Nil, "", false
	}
	return tenantID, k, true
}

// tenantContext resuelve la base de la empresa del enlace. Una empresa inexistente es un
// enlace no valido, igual que un token falso.
func (h *Handler) tenantContext(w http.ResponseWriter, r *http.Request, tenantID uuid.UUID) (context.Context, bool) {
	pool, err := h.tenantDB.ResolveForTenant(r.Context(), tenantID.String())
	if err != nil {
		if db.IsUnknownTenant(err) {
			writePage(w, http.StatusForbidden, pageInvalidLink)
			return nil, false
		}
		h.logger.Error("contacts: base de la empresa no disponible para confirmar", zap.Error(err))
		writePage(w, http.StatusServiceUnavailable, pageUnavailable)
		return nil, false
	}
	return db.WithTenant(r.Context(), pool, tenantID.String()), true
}

func (h *Handler) confirmFailed(w http.ResponseWriter, err error) {
	if errors.Is(err, domain.ErrInvalidConfirmation) {
		writePage(w, http.StatusForbidden, pageInvalidLink)
		return
	}
	h.logger.Error("contacts: la confirmacion no se pudo registrar", zap.Error(err))
	writePage(w, http.StatusServiceUnavailable, pageUnavailable)
}

// ConfirmPage (GET) no confirma: muestra el boton si el enlace sigue vigente.
func (h *Handler) ConfirmPage(w http.ResponseWriter, r *http.Request) {
	tenantID, k, ok := confirmParams(r)
	if !ok {
		writePage(w, http.StatusForbidden, pageInvalidLink)
		return
	}
	ctx, ok := h.tenantContext(w, r, tenantID)
	if !ok {
		return
	}
	if err := h.uc.CheckConfirmation(ctx, tenantID, k); err != nil {
		h.confirmFailed(w, err)
		return
	}
	writePage(w, http.StatusOK, pageConfirm(r.URL.RequestURI()))
}

// Confirm (POST) registra el consentimiento con la ip que el gateway puso en X-Real-IP
// (el cliente no puede escribirla: el gateway la consume y la reemite) y el user agent.
func (h *Handler) Confirm(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, confirmBodyLimit)
	tenantID, k, ok := confirmParams(r)
	if !ok {
		writePage(w, http.StatusForbidden, pageInvalidLink)
		return
	}
	ctx, ok := h.tenantContext(w, r, tenantID)
	if !ok {
		return
	}
	if err := h.uc.Confirm(ctx, tenantID, k, r.Header.Get("X-Real-IP"), r.UserAgent()); err != nil {
		h.confirmFailed(w, err)
		return
	}
	writePage(w, http.StatusOK, pageConfirmed)
}

// ── Errores ──────────────────────────────────────────────────────────────────

func statusNames() []string { return stringsOf(domain.Statuses()) }

// importConsentStatuses: una importacion declara consentimiento concedido con su base
// legal o ninguno.
func importConsentStatuses() []domain.ConsentStatus {
	return []domain.ConsentStatus{domain.ConsentGranted, domain.ConsentNone}
}

func stringsOf[T ~string](in []T) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	return out
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrContactNotFound),
		errors.Is(err, domain.ErrListNotFound),
		errors.Is(err, domain.ErrSegmentNotFound),
		errors.Is(err, domain.ErrAttributeNotFound),
		errors.Is(err, domain.ErrImportNotFound):
		response.ErrNotFound(w, err.Error())
	case errors.Is(err, domain.ErrContactExists):
		response.Err(w, http.StatusConflict, "CONTACT_EXISTS", err.Error())
	case errors.Is(err, domain.ErrListExists):
		response.Err(w, http.StatusConflict, "LIST_EXISTS", err.Error())
	case errors.Is(err, domain.ErrSegmentExists):
		response.Err(w, http.StatusConflict, "SEGMENT_EXISTS", err.Error())
	case errors.Is(err, domain.ErrAttributeExists):
		response.Err(w, http.StatusConflict, "ATTRIBUTE_EXISTS", err.Error())
	case errors.Is(err, domain.ErrListInUse):
		response.Err(w, http.StatusConflict, "LIST_IN_USE", err.Error())
	case errors.Is(err, domain.ErrAttributeInUse):
		response.Err(w, http.StatusConflict, "ATTRIBUTE_IN_USE", err.Error())
	case errors.Is(err, domain.ErrResubscribeRequiresOptIn):
		response.Err(w, http.StatusConflict, "RESUBSCRIBE_REQUIRES_OPT_IN", err.Error())
	case errors.Is(err, domain.ErrConsentAlreadyGranted):
		response.Err(w, http.StatusConflict, "CONSENT_ALREADY_GRANTED", err.Error())
	case errors.Is(err, domain.ErrContactNotReachable):
		response.Err(w, http.StatusConflict, "CONTACT_NOT_REACHABLE", err.Error())
	// Antes que DeadlineExceeded: el plazo agotado de la llamada a suppression no es una
	// consulta de segmento lenta.
	case errors.Is(err, app.ErrSuppressionUnavailable):
		response.Err(w, http.StatusServiceUnavailable, "SUPPRESSION_UNAVAILABLE", app.ErrSuppressionUnavailable.Error())
	case errors.Is(err, context.DeadlineExceeded):
		response.Err(w, http.StatusServiceUnavailable, "QUERY_TIMEOUT", "la consulta supero el tiempo maximo; acota el segmento")
	case isValidationError(err):
		response.ErrValidation(w, err.Error())
	default:
		response.Unexpected(w, err)
	}
}

var validationErrors = []error{
	domain.ErrInvalidEmail, domain.ErrEmailImmutable, domain.ErrInvalidLocale, domain.ErrInvalidTimezone,
	domain.ErrInvalidTag, domain.ErrTooManyTags, domain.ErrInvalidName, domain.ErrInvalidStatus,
	domain.ErrInvalidSource, domain.ErrInvalidAttributeKey, domain.ErrReservedAttributeKey,
	domain.ErrInvalidAttributeType, domain.ErrUndeclaredAttribute, domain.ErrAttributeValue,
	domain.ErrRequiredAttribute, domain.ErrTooManyAttributes, domain.ErrInvalidConsentStatus,
	domain.ErrInvalidConsentMethod, domain.ErrInvalidIP, domain.ErrInvalidSegment, domain.ErrInvalidCursor,
	domain.ErrNoImportRows, domain.ErrTooManyRows, domain.ErrConsentBasis, domain.ErrInvalidAudience,
	domain.ErrInvalidListName, domain.ErrInvalidSegmentName, domain.ErrTooManyMembers, domain.ErrInvalidLimit,
	domain.ErrInvalidContactIDs,
}

func isValidationError(err error) bool {
	for _, target := range validationErrors {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

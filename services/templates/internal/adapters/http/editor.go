package http

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/templates/internal/app"
	"github.com/alonsosss/corforce-email/services/templates/internal/deliverability"
	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// maxUploadBody: la imagen mas la cabecera multipart y un nombre de fichero largo.
const maxUploadBody = domain.MaxAssetBytes + 64<<10

// ── Verificacion de entregabilidad ───────────────────────────────────────────

type checkRequest struct {
	Kind      string                     `json:"kind"`
	Subject   string                     `json:"subject"`
	HTML      string                     `json:"html"`
	Text      *string                    `json:"text,omitempty"`
	Variables map[string]json.RawMessage `json:"variables"`
}

func (h *Handler) CheckContent(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req checkRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxContentBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	v.Required("kind", req.Kind)
	v.OneOf("kind", req.Kind, domain.Kinds())
	v.Required("subject", req.Subject)
	v.Required("html", req.HTML)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	report, err := h.uc.CheckContent(r.Context(), tenantID, app.CheckInput{
		Kind: req.Kind, Subject: req.Subject, HTML: req.HTML, Text: req.Text, Values: req.Variables,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, report)
}

func (h *Handler) CheckVersion(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	n, ok := versionParam(w, r)
	if !ok {
		return
	}
	report, err := h.uc.CheckVersion(r.Context(), tenantID, id, n)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, report)
}

type deliverabilityFailure struct {
	Error struct {
		Code    string                 `json:"code"`
		Message string                 `json:"message"`
		Issues  []deliverability.Issue `json:"issues"`
	} `json:"error"`
}

// writeDeliverabilityFailed responde 409 con el sobre de error de pkg/response y las
// incidencias del informe en error.issues.
func writeDeliverabilityFailed(w http.ResponseWriter, de *app.DeliverabilityError) {
	var body deliverabilityFailure
	body.Error.Code = "DELIVERABILITY_FAILED"
	body.Error.Message = de.Error()
	body.Error.Issues = de.Report.Issues
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(body)
}

// ── Kit de marca ─────────────────────────────────────────────────────────────

type brandFooterJSON struct {
	Company      string `json:"company"`
	Address      string `json:"address"`
	Website      string `json:"website"`
	SupportEmail string `json:"support_email"`
}

type brandKitRequest struct {
	LogoAssetID *string         `json:"logo_asset_id"`
	Colors      []string        `json:"colors"`
	Fonts       []string        `json:"fonts"`
	Footer      brandFooterJSON `json:"footer"`
}

func (h *Handler) GetBrandKit(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	view, err := h.uc.GetBrandKit(r.Context(), tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, brandKitResponse(view))
}

func (h *Handler) UpdateBrandKit(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	userID, ok := userFrom(w, r)
	if !ok {
		return
	}
	var req brandKitRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	kit := domain.BrandKit{
		Colors: req.Colors, Fonts: req.Fonts,
		Footer: domain.BrandFooter{
			Company: req.Footer.Company, Address: req.Footer.Address,
			Website: req.Footer.Website, SupportEmail: req.Footer.SupportEmail,
		},
	}
	if req.LogoAssetID != nil && *req.LogoAssetID != "" {
		id, err := uuid.Parse(*req.LogoAssetID)
		if err != nil {
			response.ErrValidation(w, "logo_asset_id no es un identificador valido")
			return
		}
		kit.LogoAssetID = &id
	}
	view, err := h.uc.UpdateBrandKit(r.Context(), tenantID, userID, kit)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, brandKitResponse(view))
}

func brandKitResponse(v *app.BrandKitView) map[string]any {
	k := v.Kit
	var logo any
	if k.LogoAssetID != nil {
		logo = k.LogoAssetID.String()
	}
	var logoURL any
	if v.LogoURL != "" {
		logoURL = v.LogoURL
	}
	return map[string]any{
		"logo_asset_id": logo,
		"logo_url":      logoURL,
		"colors":        nonNil(k.Colors),
		"fonts":         nonNil(k.Fonts),
		"footer": brandFooterJSON{
			Company: k.Footer.Company, Address: k.Footer.Address,
			Website: k.Footer.Website, SupportEmail: k.Footer.SupportEmail,
		},
		"updated_at": k.UpdatedAt,
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ── Imagenes ─────────────────────────────────────────────────────────────────

// UploadAsset recibe multipart/form-data con la imagen en el campo file. Sin almacen o sin
// ClamAV responde 503 antes de leer el cuerpo.
func (h *Handler) UploadAsset(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	userID, ok := userFrom(w, r)
	if !ok {
		return
	}
	if err := h.uc.AssetUploadsAvailable(); err != nil {
		writeError(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBody)
	name, data, err := readFilePart(r)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			response.Err(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE",
				"la imagen supera "+strconv.Itoa(domain.MaxAssetBytes)+" bytes")
			return
		}
		response.ErrBadRequest(w, err.Error())
		return
	}
	if data == nil {
		response.ErrValidation(w, "file es obligatorio")
		return
	}
	view, created, err := h.uc.UploadAsset(r.Context(), tenantID, userID, name, data)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	response.JSON(w, status, assetResponse(view))
}

var errNotMultipart = errors.New("se esperaba multipart/form-data con el campo file")

// readFilePart lee la primera parte llamada file sin pasar por disco; data es nil si no llega.
func readFilePart(r *http.Request) (string, []byte, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return "", nil, errNotMultipart
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return "", nil, nil
		}
		if err != nil {
			return "", nil, err
		}
		if part.FormName() != "file" {
			if _, err := io.Copy(io.Discard, part); err != nil {
				return "", nil, err
			}
			continue
		}
		data, err := io.ReadAll(io.LimitReader(part, domain.MaxAssetBytes+1))
		if err != nil {
			return "", nil, err
		}
		if data == nil {
			data = []byte{}
		}
		return part.FileName(), data, nil
	}
}

func (h *Handler) ListAssets(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit := 0
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > domain.MaxAssetPage {
			response.ErrValidation(w, "limit debe estar entre 1 y "+strconv.Itoa(domain.MaxAssetPage))
			return
		}
		limit = n
	}
	items, next, err := h.uc.ListAssets(r.Context(), tenantID, limit, q.Get("cursor"))
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for i := range items {
		out = append(out, assetResponse(&items[i]))
	}
	var nextCursor any
	if next != "" {
		nextCursor = next
	}
	response.JSON(w, http.StatusOK, map[string]any{"items": out, "next_cursor": nextCursor})
}

func (h *Handler) DeleteAsset(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "assetID"))
	if err != nil {
		response.ErrBadRequest(w, "identificador de imagen no valido")
		return
	}
	if err := h.uc.DeleteAsset(r.Context(), tenantID, id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func assetResponse(v *app.AssetView) map[string]any {
	a := v.Asset
	return map[string]any{
		"id":           a.ID.String(),
		"url":          v.URL,
		"content_type": a.ContentType,
		"size_bytes":   a.SizeBytes,
		"width":        a.Width,
		"height":       a.Height,
		"name":         a.Name,
		"created_at":   a.CreatedAt,
	}
}

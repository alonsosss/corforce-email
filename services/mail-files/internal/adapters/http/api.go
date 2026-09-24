// Package http expone mail-files: la API interna que usa el webmail (/internal/mail-files, que el
// gateway no enruta) y la ruta publica del enlace (/api/v1/public/files, routes.json).
package http

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/mail-files/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	// InternalPrefix es la API del webmail.
	InternalPrefix = "/internal/mail-files"
	// TenantHeader y MailboxHeader nombran la empresa y el buzon de la sesion del webmail, que solo
	// pone el webmail junto al token del gateway (el mismo contrato que mail-dav).
	TenantHeader  = "X-Mailbox-Tenant-ID"
	MailboxHeader = "X-Mailbox-ID"

	// busyRetryAfter es lo que se pide esperar cuando todas las ranuras de subida estan ocupadas.
	busyRetryAfter = "10"
)

// UseCase es lo que el adaptador necesita de app.UseCase.
type UseCase interface {
	Enabled() bool
	Policy() domain.Policy
	Upload(ctx context.Context, owner domain.Owner, name string, body io.Reader, opts domain.UploadOptions) (domain.SharedFile, error)
	List(ctx context.Context, owner domain.Owner) (domain.Listing, error)
	Revoke(ctx context.Context, owner domain.Owner, id uuid.UUID) (domain.SharedFile, error)
	Inspect(ctx context.Context, c domain.LinkClaims, signature string) (domain.File, error)
	Download(ctx context.Context, c domain.LinkClaims, signature string) (domain.File, io.ReadCloser, error)
}

// Config son los plazos de las transferencias. El servidor corta a los 15 s de lectura y 30 s de
// escritura; una subida o una descarga de cien megas los amplia hasta estos.
type Config struct {
	UploadTimeout   time.Duration
	DownloadTimeout time.Duration
	// OperationTimeout acota el resto de peticiones.
	OperationTimeout time.Duration
}

type Handler struct {
	uc     UseCase
	cfg    Config
	logger *zap.Logger
}

func NewHandler(uc UseCase, cfg Config, logger *zap.Logger) (*Handler, error) {
	if cfg.UploadTimeout <= 0 || cfg.DownloadTimeout <= 0 || cfg.OperationTimeout <= 0 {
		return nil, errors.New("mail-files: plazos del API deben ser positivos")
	}
	return &Handler{uc: uc, cfg: cfg, logger: logger}, nil
}

// InternalRoutes es la API del webmail. Va detras de RequireInternalCaller.
func (h *Handler) InternalRoutes() http.Handler {
	r := chi.NewRouter()
	r.Route(InternalPrefix, func(r chi.Router) {
		r.Get("/meta", h.Meta)
		r.Get("/files", h.List)
		r.Post("/files", h.Upload)
		r.Delete("/files/{id}", h.Revoke)
	})
	return r
}

// PublicRoutes es el enlace que llega por correo.
func (h *Handler) PublicRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get(PublicPattern, h.Landing)
	r.Post(PublicPattern, h.Serve)
	return r
}

type limitsDTO struct {
	MaxFileBytes        int64 `json:"max_file_bytes"`
	DefaultExpiryDays   int   `json:"default_expiry_days"`
	MaxExpiryDays       int   `json:"max_expiry_days"`
	DefaultMaxDownloads int   `json:"default_max_downloads"`
	MaxDownloads        int   `json:"max_downloads"`
	MailboxQuotaBytes   int64 `json:"mailbox_quota_bytes"`
	TenantQuotaBytes    int64 `json:"tenant_quota_bytes"`
	MaxActivePerMailbox int   `json:"max_active_per_mailbox"`
}

func toLimits(p domain.Policy) limitsDTO {
	return limitsDTO{
		MaxFileBytes: p.MaxFileBytes, DefaultExpiryDays: p.DefaultExpiryDays, MaxExpiryDays: p.MaxExpiryDays,
		DefaultMaxDownloads: p.DefaultMaxDownloads, MaxDownloads: p.MaxDownloads,
		MailboxQuotaBytes: p.MailboxQuotaBytes, TenantQuotaBytes: p.TenantQuotaBytes, MaxActivePerMailbox: p.MaxActivePerMailbox,
	}
}

type metaDTO struct {
	Enabled bool      `json:"enabled"`
	Limits  limitsDTO `json:"limits"`
}

type usageDTO struct {
	MailboxBytes  int64 `json:"mailbox_bytes"`
	MailboxActive int   `json:"mailbox_active"`
	TenantBytes   int64 `json:"tenant_bytes"`
}

type fileDTO struct {
	ID                 string     `json:"id"`
	Name               string     `json:"name"`
	SizeBytes          int64      `json:"size_bytes"`
	SHA256             string     `json:"sha256"`
	URL                string     `json:"url"`
	State              string     `json:"state"`
	ExpiresAt          time.Time  `json:"expires_at"`
	MaxDownloads       int        `json:"max_downloads"`
	Downloads          int        `json:"downloads"`
	RemainingDownloads int        `json:"remaining_downloads"`
	CreatedAt          time.Time  `json:"created_at"`
	LastDownloadAt     *time.Time `json:"last_download_at"`
	RevokedAt          *time.Time `json:"revoked_at"`
}

type listDTO struct {
	Enabled bool      `json:"enabled"`
	Items   []fileDTO `json:"items"`
	Usage   usageDTO  `json:"usage"`
	Limits  limitsDTO `json:"limits"`
}

func toFile(f domain.SharedFile, now time.Time) fileDTO {
	state := f.State(now)
	out := fileDTO{
		ID: f.ID.String(), Name: f.Name, SizeBytes: f.SizeBytes, SHA256: f.SHA256, State: string(state),
		ExpiresAt: f.ExpiresAt, MaxDownloads: f.MaxDownloads, Downloads: f.Downloads,
		RemainingDownloads: f.RemainingDownloads(), CreatedAt: f.CreatedAt,
		LastDownloadAt: f.LastDownloadAt, RevokedAt: f.RevokedAt,
	}
	// Solo se entrega el enlace mientras sirve: uno cerrado no se vuelve a pegar en un correo.
	if state == domain.StateActive {
		out.URL = f.URL
	}
	return out
}

func (h *Handler) Meta(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, metaDTO{Enabled: h.uc.Enabled(), Limits: toLimits(h.uc.Policy())})
}

// owner lee la empresa y el buzon de las cabeceras del webmail.
func owner(r *http.Request) (domain.Owner, bool) {
	tenant, terr := uuid.Parse(strings.TrimSpace(r.Header.Get(TenantHeader)))
	mailbox, merr := uuid.Parse(strings.TrimSpace(r.Header.Get(MailboxHeader)))
	if terr != nil || merr != nil || tenant == uuid.Nil || mailbox == uuid.Nil {
		return domain.Owner{}, false
	}
	return domain.Owner{TenantID: tenant, MailboxID: mailbox}, true
}

func (h *Handler) withOwner(w http.ResponseWriter, r *http.Request) (domain.Owner, bool) {
	o, ok := owner(r)
	if !ok {
		response.Err(w, http.StatusBadRequest, "MAILBOX_REQUIRED", "falta la empresa o el buzon de la sesion")
	}
	return o, ok
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	o, ok := h.withOwner(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.OperationTimeout)
	defer cancel()
	listing, err := h.uc.List(ctx, o)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	now := time.Now()
	out := listDTO{
		Enabled: h.uc.Enabled(), Items: make([]fileDTO, len(listing.Items)), Limits: toLimits(listing.Policy),
		Usage: usageDTO{MailboxBytes: listing.Usage.MailboxBytes, MailboxActive: listing.Usage.MailboxActive, TenantBytes: listing.Usage.TenantBytes},
	}
	for i, f := range listing.Items {
		out.Items[i] = toFile(f, now)
	}
	response.JSON(w, http.StatusOK, out)
}

// Upload recibe el fichero en crudo (cuerpo de la peticion) con su nombre y las opciones en la
// consulta: name, expires_in_days y max_downloads.
func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	o, ok := h.withOwner(w, r)
	if !ok {
		return
	}
	extendDeadlines(w, h.cfg.UploadTimeout)
	q := r.URL.Query()
	opts, err := uploadOptions(q.Get("expires_in_days"), q.Get("max_downloads"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if r.ContentLength > h.uc.Policy().MaxFileBytes {
		h.fail(w, r, domain.ErrTooLarge)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.UploadTimeout)
	defer cancel()
	file, err := h.uc.Upload(ctx, o, q.Get("name"), r.Body, opts)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusCreated, toFile(file, time.Now()))
}

func uploadOptions(days, downloads string) (domain.UploadOptions, error) {
	var opts domain.UploadOptions
	var err error
	if days != "" {
		if opts.ExpiresInDays, err = strconv.Atoi(days); err != nil || opts.ExpiresInDays < 1 {
			return opts, domain.NewValidationError("expires_in_days", "la caducidad debe ser un numero de dias")
		}
	}
	if downloads != "" {
		if opts.MaxDownloads, err = strconv.Atoi(downloads); err != nil || opts.MaxDownloads < 1 {
			return opts, domain.NewValidationError("max_downloads", "las descargas deben ser un numero positivo")
		}
	}
	return opts, nil
}

func (h *Handler) Revoke(w http.ResponseWriter, r *http.Request) {
	o, ok := h.withOwner(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, r, domain.ErrNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.OperationTimeout)
	defer cancel()
	file, err := h.uc.Revoke(ctx, o, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toFile(file, time.Now()))
}

// fail traduce los errores del dominio al envelope. Los codigos HTTP son los que el webmail entrega
// tal cual al usuario (400, 404, 413, 422, 429, 503, 507).
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var verr *domain.ValidationError
	switch {
	case errors.As(err, &verr):
		response.ErrWithDetails(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", verr.Message, map[string]string{"field": verr.Field})
	case errors.Is(err, domain.ErrTooLarge):
		response.ErrWithDetails(w, http.StatusRequestEntityTooLarge, "FILE_TOO_LARGE", err.Error(),
			map[string]string{"limit": strconv.FormatInt(h.uc.Policy().MaxFileBytes, 10)})
	case errors.Is(err, domain.ErrEmpty):
		response.Err(w, http.StatusUnprocessableEntity, "FILE_EMPTY", domain.ErrEmpty.Error())
	case errors.Is(err, domain.ErrIncomplete):
		response.Err(w, http.StatusBadRequest, "UPLOAD_INCOMPLETE", domain.ErrIncomplete.Error())
	case errors.Is(err, domain.ErrInfected):
		response.Err(w, http.StatusUnprocessableEntity, "FILE_INFECTED", domain.ErrInfected.Error())
	case errors.Is(err, domain.ErrNotRevocable):
		response.Err(w, http.StatusUnprocessableEntity, "FILE_NOT_ACTIVE", domain.ErrNotRevocable.Error())
	case errors.Is(err, domain.ErrMailboxQuota):
		response.Err(w, http.StatusInsufficientStorage, "MAILBOX_FILES_QUOTA_EXCEEDED", domain.ErrMailboxQuota.Error())
	case errors.Is(err, domain.ErrTenantQuota):
		response.Err(w, http.StatusInsufficientStorage, "TENANT_FILES_QUOTA_EXCEEDED", domain.ErrTenantQuota.Error())
	case errors.Is(err, domain.ErrTooManyFiles):
		response.ErrWithDetails(w, http.StatusInsufficientStorage, "SHARED_FILES_LIMIT", domain.ErrTooManyFiles.Error(),
			map[string]string{"limit": strconv.Itoa(h.uc.Policy().MaxActivePerMailbox)})
	case errors.Is(err, domain.ErrNotFound):
		response.Err(w, http.StatusNotFound, "FILE_NOT_FOUND", domain.ErrNotFound.Error())
	case errors.Is(err, domain.ErrBusy):
		w.Header().Set("Retry-After", busyRetryAfter)
		response.Err(w, http.StatusTooManyRequests, "UPLOADS_BUSY", domain.ErrBusy.Error())
	case errors.Is(err, domain.ErrStorageDisabled):
		response.Err(w, http.StatusServiceUnavailable, "LARGE_FILES_DISABLED", domain.ErrStorageDisabled.Error())
	case errors.Is(err, domain.ErrScanUnavailable):
		h.logUnavailable(r, err)
		response.Err(w, http.StatusServiceUnavailable, "SCAN_UNAVAILABLE", domain.ErrScanUnavailable.Error())
	case errors.Is(err, domain.ErrUnavailable), errors.Is(err, domain.ErrTenantUnknown), errors.Is(err, context.DeadlineExceeded):
		h.logUnavailable(r, err)
		response.Err(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", domain.ErrUnavailable.Error())
	default:
		response.Unexpected(w, err)
	}
}

func (h *Handler) logUnavailable(r *http.Request, err error) {
	h.logger.Warn("mail-files: dependencia no disponible", zap.String("path", r.URL.Path), zap.Error(err))
}

// extendDeadlines amplia los plazos del servidor HTTP para una transferencia grande. Si el
// ResponseWriter no lo admite se quedan los generales: la transferencia puede cortarse, pero nunca
// queda abierta sin limite.
func extendDeadlines(w http.ResponseWriter, d time.Duration) {
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(d)
	_ = rc.SetReadDeadline(deadline)
	_ = rc.SetWriteDeadline(deadline)
}

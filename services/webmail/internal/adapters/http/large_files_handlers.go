package http

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/go-chi/chi/v5"
)

// Ficheros grandes por enlace (docs/adr/0014): el buzon sube el fichero, mail-files lo analiza con
// ClamAV y lo guarda, y el correo lleva el enlace. El fichero pasa en flujo, sin cargarse en memoria.

const largeFileField = "file"

type largeFileLimitsDTO struct {
	MaxFileBytes        int64 `json:"max_file_bytes"`
	DefaultExpiryDays   int   `json:"default_expiry_days"`
	MaxExpiryDays       int   `json:"max_expiry_days"`
	DefaultMaxDownloads int   `json:"default_max_downloads"`
	MaxDownloads        int   `json:"max_downloads"`
	MailboxQuotaBytes   int64 `json:"mailbox_quota_bytes"`
	TenantQuotaBytes    int64 `json:"tenant_quota_bytes"`
	MaxActivePerMailbox int   `json:"max_active_per_mailbox"`
}

type largeFileUsageDTO struct {
	MailboxBytes  int64 `json:"mailbox_bytes"`
	MailboxActive int   `json:"mailbox_active"`
	TenantBytes   int64 `json:"tenant_bytes"`
}

type largeFileDTO struct {
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

type largeFileListingDTO struct {
	Enabled bool               `json:"enabled"`
	Items   []largeFileDTO     `json:"items"`
	Usage   largeFileUsageDTO  `json:"usage"`
	Limits  largeFileLimitsDTO `json:"limits"`
}

func toLargeFileDTO(f domain.LargeFile) largeFileDTO {
	return largeFileDTO{
		ID: f.ID, Name: f.Name, SizeBytes: f.SizeBytes, SHA256: f.SHA256, URL: f.URL, State: f.State,
		ExpiresAt: f.ExpiresAt, MaxDownloads: f.MaxDownloads, Downloads: f.Downloads,
		RemainingDownloads: f.RemainingDownloads, CreatedAt: f.CreatedAt, LastDownloadAt: f.LastDownloadAt, RevokedAt: f.RevokedAt,
	}
}

func toLargeFileListingDTO(l domain.LargeFileListing) largeFileListingDTO {
	out := largeFileListingDTO{
		Enabled: l.Enabled,
		Items:   make([]largeFileDTO, len(l.Items)),
		Usage:   largeFileUsageDTO{MailboxBytes: l.Usage.MailboxBytes, MailboxActive: l.Usage.MailboxActive, TenantBytes: l.Usage.TenantBytes},
		Limits: largeFileLimitsDTO{
			MaxFileBytes: l.Limits.MaxFileBytes, DefaultExpiryDays: l.Limits.DefaultExpiryDays, MaxExpiryDays: l.Limits.MaxExpiryDays,
			DefaultMaxDownloads: l.Limits.DefaultMaxDownloads, MaxDownloads: l.Limits.MaxDownloads,
			MailboxQuotaBytes: l.Limits.MailboxQuotaBytes, TenantQuotaBytes: l.Limits.TenantQuotaBytes,
			MaxActivePerMailbox: l.Limits.MaxActivePerMailbox,
		},
	}
	for i, f := range l.Items {
		out.Items[i] = toLargeFileDTO(f)
	}
	return out
}

// largeFileFail responde los errores propios de la funcion y deja el resto al manejo comun.
func (h *Handler) largeFileFail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrLargeFilesDisabled):
		response.Err(w, http.StatusServiceUnavailable, "LARGE_FILES_DISABLED", domain.ErrLargeFilesDisabled.Error())
	case errors.Is(err, domain.ErrLargeFileTooLarge):
		response.ErrWithDetails(w, http.StatusRequestEntityTooLarge, "FILE_TOO_LARGE", domain.ErrLargeFileTooLarge.Error(),
			map[string]string{"limit": strconv.FormatInt(h.cfg.MaxLargeFileBytes, 10)})
	default:
		h.fail(w, r, err)
	}
}

func (h *Handler) LargeFiles(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	listing, err := h.app.LargeFiles(ctx, sessionFrom(r))
	if err != nil {
		h.largeFileFail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toLargeFileListingDTO(listing))
}

// UploadLargeFile recibe multipart/form-data con un solo campo, file, y las opciones en la consulta
// (expires_in_days, max_downloads). La parte va en flujo a mail-files.
func (h *Handler) UploadLargeFile(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, h.cfg.TransferTimeout)
	limit := h.cfg.MaxLargeFileBytes + multipartOverhead
	if h.cfg.MaxLargeFileBytes <= 0 || r.ContentLength > limit {
		h.largeFileFail(w, r, domain.ErrLargeFileTooLarge)
		return
	}
	opts, err := largeFileOptions(r)
	if err != nil {
		h.largeFileFail(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	mr, err := r.MultipartReader()
	if err != nil {
		h.largeFileFail(w, r, domain.NewValidationError("body", "se esperaba multipart/form-data"))
		return
	}
	part, err := mr.NextPart()
	if err != nil {
		h.largeFileFail(w, r, largeFileBodyError(err, domain.NewValidationError(largeFileField, "falta el fichero")))
		return
	}
	defer part.Close()
	if part.FormName() != largeFileField {
		h.largeFileFail(w, r, domain.NewValidationError(part.FormName(), "campo no admitido"))
		return
	}
	body := &bodyTracker{r: part}
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.TransferTimeout)
	defer cancel()
	file, err := h.app.UploadLargeFile(ctx, sessionFrom(r), part.FileName(), body, opts)
	if err != nil {
		// Un corte por el tope del cuerpo llega a mail-files como una subida rota: se responde como lo
		// que es.
		h.largeFileFail(w, r, largeFileBodyError(body.err, err))
		return
	}
	response.JSON(w, http.StatusCreated, toLargeFileDTO(file))
}

func largeFileOptions(r *http.Request) (domain.LargeFileOptions, error) {
	var opts domain.LargeFileOptions
	q := r.URL.Query()
	var err error
	if v := q.Get("expires_in_days"); v != "" {
		if opts.ExpiresInDays, err = strconv.Atoi(v); err != nil || opts.ExpiresInDays < 1 {
			return opts, domain.NewValidationError("expires_in_days", "la caducidad debe ser un número de días")
		}
	}
	if v := q.Get("max_downloads"); v != "" {
		if opts.MaxDownloads, err = strconv.Atoi(v); err != nil || opts.MaxDownloads < 1 {
			return opts, domain.NewValidationError("max_downloads", "las descargas deben ser un número positivo")
		}
	}
	return opts, nil
}

// largeFileBodyError devuelve ErrLargeFileTooLarge si el cuerpo se corto por el tope y fallback si no.
func largeFileBodyError(bodyErr, fallback error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(bodyErr, &tooLarge) {
		return domain.ErrLargeFileTooLarge
	}
	return fallback
}

// bodyTracker guarda el error con el que se corto la lectura del cuerpo.
type bodyTracker struct {
	r   io.Reader
	err error
}

func (b *bodyTracker) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		b.err = err
	}
	return n, err
}

func (h *Handler) RevokeLargeFile(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	file, err := h.app.RevokeLargeFile(ctx, sessionFrom(r), chi.URLParam(r, "id"))
	if err != nil {
		h.largeFileFail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toLargeFileDTO(file))
}

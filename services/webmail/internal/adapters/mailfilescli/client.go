// Package mailfilescli lleva a mail-files los ficheros grandes que el buzon comparte por enlace, por su
// API interna (/internal/mail-files, que el gateway no enruta). La identidad viaja en las cabeceras que
// solo pone el webmail (X-Mailbox-Tenant-ID y X-Mailbox-ID), con la empresa y el buzon de la sesion,
// junto al token de gateway: el mismo contrato que mail-dav.
package mailfilescli

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/internalapi"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

const (
	filesPath = "/internal/mail-files/files"

	tenantHeader  = "X-Mailbox-Tenant-ID"
	mailboxHeader = "X-Mailbox-ID"

	requestTimeout = 10 * time.Second
)

// Client implementa ports.LargeFiles.
type Client struct {
	api *internalapi.Caller
	// upload lleva el fichero: un plazo de transferencia y sin reintentos (el cuerpo va en flujo y no
	// se puede repetir).
	upload *internalapi.Caller
}

// New valida la URL de mail-files (MAIL_FILES_URL). transferTimeout acota una subida entera, analisis
// antivirus incluido.
func New(baseURL, token string, transferTimeout time.Duration) (*Client, error) {
	base, err := internalapi.NewBaseURL("MAIL_FILES_URL", baseURL)
	if err != nil {
		return nil, err
	}
	return &Client{
		api: internalapi.New("mail-files", base, token,
			httpclient.New("mail-files", httpclient.Options{Timeout: requestTimeout, MaxAttempts: 3})),
		upload: internalapi.New("mail-files", base, token,
			httpclient.New("mail-files-upload", httpclient.Options{Timeout: transferTimeout, MaxAttempts: 1})),
	}, nil
}

func identity(mb domain.MailboxRef) http.Header {
	h := http.Header{}
	h.Set(tenantHeader, mb.TenantID)
	h.Set(mailboxHeader, mb.MailboxID)
	return h
}

// passthrough entrega al usuario los rechazos de mail-files tal cual: los topes y las cuotas son suyos.
var passthrough = internalapi.Errors{Passthrough: true}

type limitsJSON struct {
	MaxFileBytes        int64 `json:"max_file_bytes"`
	DefaultExpiryDays   int   `json:"default_expiry_days"`
	MaxExpiryDays       int   `json:"max_expiry_days"`
	DefaultMaxDownloads int   `json:"default_max_downloads"`
	MaxDownloads        int   `json:"max_downloads"`
	MailboxQuotaBytes   int64 `json:"mailbox_quota_bytes"`
	TenantQuotaBytes    int64 `json:"tenant_quota_bytes"`
	MaxActivePerMailbox int   `json:"max_active_per_mailbox"`
}

type fileJSON struct {
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

func (f fileJSON) toDomain() domain.LargeFile {
	return domain.LargeFile{
		ID: f.ID, Name: f.Name, SizeBytes: f.SizeBytes, SHA256: f.SHA256, URL: f.URL, State: f.State,
		ExpiresAt: f.ExpiresAt, MaxDownloads: f.MaxDownloads, Downloads: f.Downloads,
		RemainingDownloads: f.RemainingDownloads, CreatedAt: f.CreatedAt, LastDownloadAt: f.LastDownloadAt, RevokedAt: f.RevokedAt,
	}
}

type listJSON struct {
	Enabled bool       `json:"enabled"`
	Items   []fileJSON `json:"items"`
	Usage   struct {
		MailboxBytes  int64 `json:"mailbox_bytes"`
		MailboxActive int   `json:"mailbox_active"`
		TenantBytes   int64 `json:"tenant_bytes"`
	} `json:"usage"`
	Limits limitsJSON `json:"limits"`
}

func (c *Client) ListLargeFiles(ctx context.Context, mb domain.MailboxRef) (domain.LargeFileListing, error) {
	var out listJSON
	if err := c.api.Do(ctx, internalapi.Request{Method: http.MethodGet, Path: filesPath, Header: identity(mb), Out: &out, Errors: passthrough}); err != nil {
		return domain.LargeFileListing{}, err
	}
	l := out.Limits
	listing := domain.LargeFileListing{
		Enabled: out.Enabled,
		Items:   make([]domain.LargeFile, len(out.Items)),
		Usage:   domain.LargeFileUsage{MailboxBytes: out.Usage.MailboxBytes, MailboxActive: out.Usage.MailboxActive, TenantBytes: out.Usage.TenantBytes},
		Limits: domain.LargeFileLimits{
			MaxFileBytes: l.MaxFileBytes, DefaultExpiryDays: l.DefaultExpiryDays, MaxExpiryDays: l.MaxExpiryDays,
			DefaultMaxDownloads: l.DefaultMaxDownloads, MaxDownloads: l.MaxDownloads, MailboxQuotaBytes: l.MailboxQuotaBytes,
			TenantQuotaBytes: l.TenantQuotaBytes, MaxActivePerMailbox: l.MaxActivePerMailbox,
		},
	}
	for i, f := range out.Items {
		listing.Items[i] = f.toDomain()
	}
	return listing, nil
}

// UploadLargeFile no se reintenta: el cuerpo es el flujo de la peticion del navegador.
func (c *Client) UploadLargeFile(ctx context.Context, mb domain.MailboxRef, name string, body io.Reader, opts domain.LargeFileOptions) (domain.LargeFile, error) {
	query := url.Values{}
	query.Set("name", name)
	if opts.ExpiresInDays > 0 {
		query.Set("expires_in_days", strconv.Itoa(opts.ExpiresInDays))
	}
	if opts.MaxDownloads > 0 {
		query.Set("max_downloads", strconv.Itoa(opts.MaxDownloads))
	}
	var out fileJSON
	err := c.upload.Do(ctx, internalapi.Request{Method: http.MethodPost, Path: filesPath, Query: query, Header: identity(mb),
		Raw: body, ContentType: "application/octet-stream", Out: &out, Errors: passthrough})
	return out.toDomain(), err
}

func (c *Client) RevokeLargeFile(ctx context.Context, mb domain.MailboxRef, id string) (domain.LargeFile, error) {
	var out fileJSON
	err := c.api.Do(ctx, internalapi.Request{Method: http.MethodDelete, Path: filesPath + "/" + url.PathEscape(id), Header: identity(mb),
		Out: &out, Errors: passthrough})
	return out.toDomain(), err
}

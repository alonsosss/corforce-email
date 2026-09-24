package maildirectorycli

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/internalapi"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

const scheduledPath = "/internal/mail-directory/scheduled-sends"

type newScheduledBody struct {
	Username    string    `json:"username"`
	MessageID   string    `json:"message_id"`
	Folder      string    `json:"folder"`
	UIDValidity uint32    `json:"uid_validity"`
	UID         uint32    `json:"uid"`
	SendAt      time.Time `json:"send_at"`
	Subject     string    `json:"subject"`
	Recipients  []string  `json:"recipients"`
}

// scheduledRow es una fila tal como la devuelve el directorio: la parte publica (id, send_at,
// subject, recipients, created_at, status) y la referencia IMAP que el webmail necesita para
// cancelarla o enviarla.
type scheduledRow struct {
	ID          string    `json:"id"`
	Username    string    `json:"username"`
	SendAt      time.Time `json:"send_at"`
	Subject     string    `json:"subject"`
	Recipients  []string  `json:"recipients"`
	CreatedAt   time.Time `json:"created_at"`
	Status      string    `json:"status"`
	MessageID   string    `json:"message_id"`
	Folder      string    `json:"folder"`
	UIDValidity uint32    `json:"uid_validity"`
	UID         uint32    `json:"uid"`
}

func (r scheduledRow) toDomain() domain.ScheduledSend {
	return domain.ScheduledSend{
		ID: r.ID, SendAt: r.SendAt, Subject: r.Subject, Recipients: nonNilStrings(r.Recipients), CreatedAt: r.CreatedAt,
		Status: domain.ScheduledStatus(r.Status), MessageID: r.MessageID, Folder: r.Folder, UIDValidity: r.UIDValidity, UID: r.UID,
	}
}

// scheduledErrors traduce los 409 por su codigo: una fila en curso o ya enviada, un cierre de una fila
// no reclamada y el tope de pendientes por buzon. Un CONFLICT (el mismo Message-ID ya activo) no
// deberia ocurrir nunca: queda como indisponibilidad.
var scheduledErrors = internalapi.Errors{
	ByCode: map[string]error{
		"SCHEDULED_SEND_NOT_PENDING": domain.ErrScheduledNotPending,
		"SCHEDULED_SEND_NOT_CLAIMED": domain.ErrScheduledNotClaimed,
		"SCHEDULED_SEND_LIMIT":       domain.ErrScheduledLimit,
	},
	ByStatus: map[int]error{http.StatusNotFound: domain.ErrScheduledNotFound},
	Field:    "send_at",
}

// CreateScheduled registra un envio programado y devuelve el id de la fila (el directorio responde
// 201 con la fila entera). POST no se reintenta: un reintento tras un corte podria registrar dos
// filas.
func (c *Client) CreateScheduled(ctx context.Context, in domain.NewScheduledSend) (string, error) {
	body := newScheduledBody{
		Username: in.Username, MessageID: in.MessageID, Folder: in.Folder, UIDValidity: in.UIDValidity, UID: in.UID,
		SendAt: in.SendAt.UTC(), Subject: in.Subject, Recipients: nonNilStrings(in.Recipients),
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPost, Path: scheduledPath, Body: body, Out: &out, Errors: scheduledErrors}); err != nil {
		return "", err
	}
	if !domain.ValidUUID(out.ID) {
		return "", c.api.Unavailable("id de envio programado invalido")
	}
	return out.ID, nil
}

// ListScheduled devuelve las filas del buzon.
func (c *Client) ListScheduled(ctx context.Context, username string) ([]domain.ScheduledSend, error) {
	var rows []scheduledRow
	if err := c.api.Do(ctx, internalapi.Request{Method: http.MethodGet, Path: scheduledPath, Query: usernameQuery(username), Out: &rows}); err != nil {
		return nil, err
	}
	out := make([]domain.ScheduledSend, len(rows))
	for i, r := range rows {
		out[i] = r.toDomain()
	}
	return out, nil
}

// RescheduleScheduled cambia la hora de una fila pendiente del buzon.
func (c *Client) RescheduleScheduled(ctx context.Context, username, id string, at time.Time) (domain.ScheduledSend, error) {
	body := struct {
		SendAt time.Time `json:"send_at"`
	}{SendAt: at.UTC()}
	var row scheduledRow
	if err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPatch, Path: scheduledPath + "/" + url.PathEscape(id), Query: usernameQuery(username), Body: body, Out: &row, Errors: scheduledErrors}); err != nil {
		return domain.ScheduledSend{}, err
	}
	return row.toDomain(), nil
}

// CancelScheduled cancela una fila pendiente del buzon.
func (c *Client) CancelScheduled(ctx context.Context, username, id string) error {
	return c.api.Do(ctx, internalapi.Request{Method: http.MethodDelete, Path: scheduledPath + "/" + url.PathEscape(id), Query: usernameQuery(username), Errors: scheduledErrors})
}

type claimBody struct {
	Limit        int `json:"limit"`
	LeaseSeconds int `json:"lease_seconds"`
}

// ClaimScheduled reclama filas vencidas de toda la celda con arriendo.
func (c *Client) ClaimScheduled(ctx context.Context, limit int, lease time.Duration) ([]domain.ScheduledClaim, error) {
	var rows []scheduledRow
	body := claimBody{Limit: limit, LeaseSeconds: int(lease.Seconds())}
	if err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPost, Path: scheduledPath + "/claim", Body: body, Out: &rows}); err != nil {
		return nil, err
	}
	out := make([]domain.ScheduledClaim, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.ScheduledClaim{
			ID: r.ID, Username: r.Username, MessageID: r.MessageID, Folder: r.Folder,
			UIDValidity: r.UIDValidity, UID: r.UID, SendAt: r.SendAt,
		})
	}
	return out, nil
}

type finishBody struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	Retry  bool   `json:"retry"`
}

// FinishScheduled cierra una fila reclamada. retry solo cuenta con failed: el fallo fue de
// infraestructura y el directorio la reprograma con espera creciente.
func (c *Client) FinishScheduled(ctx context.Context, id string, outcome domain.ScheduledOutcome) error {
	body := finishBody{Status: string(outcome.Status), Error: outcome.Error, Retry: outcome.Status == domain.ScheduledFailed && outcome.Retry}
	return c.api.Do(ctx, internalapi.Request{Method: http.MethodPost, Path: scheduledPath + "/" + url.PathEscape(id) + "/finish", Body: body, Errors: scheduledErrors})
}

package maildirectorycli

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/internalapi"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

const (
	remindersPath    = "/internal/mail-directory/reminders"
	quickRepliesPath = "/internal/mail-directory/quick-replies"
)

type newReminderBody struct {
	Username     string    `json:"username"`
	Kind         string    `json:"kind"`
	MessageID    string    `json:"message_id"`
	Folder       string    `json:"folder"`
	UIDValidity  uint32    `json:"uid_validity"`
	UID          uint32    `json:"uid"`
	ReturnFolder string    `json:"return_folder"`
	DueAt        time.Time `json:"due_at"`
	Subject      string    `json:"subject"`
	Addresses    []string  `json:"addresses"`
}

// reminderRow es una fila tal como la devuelve el directorio.
type reminderRow struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	Kind         string    `json:"kind"`
	MessageID    string    `json:"message_id"`
	Folder       string    `json:"folder"`
	UIDValidity  uint32    `json:"uid_validity"`
	UID          uint32    `json:"uid"`
	ReturnFolder string    `json:"return_folder"`
	Subject      string    `json:"subject"`
	Addresses    []string  `json:"addresses"`
	DueAt        time.Time `json:"due_at"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
}

func (r reminderRow) toDomain() domain.Reminder {
	return domain.Reminder{
		ID: r.ID, Kind: domain.ReminderKind(r.Kind), MessageID: r.MessageID, Folder: r.Folder, UIDValidity: r.UIDValidity, UID: r.UID,
		ReturnFolder: r.ReturnFolder, Subject: r.Subject, Addresses: nonNilStrings(r.Addresses), DueAt: r.DueAt,
		Status: domain.ReminderStatus(r.Status), CreatedAt: r.CreatedAt,
	}
}

// reminderErrors traduce los 409 por su codigo; CONFLICT es el mismo mensaje con otro recordatorio
// activo del mismo tipo (el indice unico de la tabla).
var reminderErrors = internalapi.Errors{
	ByCode: map[string]error{
		"REMINDER_NOT_PENDING": domain.ErrReminderNotPending,
		"REMINDER_NOT_CLAIMED": domain.ErrReminderNotClaimed,
		"REMINDER_LIMIT":       domain.ErrReminderLimit,
		"CONFLICT":             domain.ErrReminderExists,
	},
	ByStatus: map[int]error{http.StatusNotFound: domain.ErrReminderNotFound},
	Field:    "due_at",
}

// CreateReminder registra un recordatorio. POST no se reintenta: un reintento tras un corte podria
// registrar dos filas (el indice unico solo cubre las que llevan Message-ID).
func (c *Client) CreateReminder(ctx context.Context, in domain.NewReminder) (domain.Reminder, error) {
	body := newReminderBody{
		Username: in.Username, Kind: string(in.Kind), MessageID: in.MessageID, Folder: in.Folder, UIDValidity: in.UIDValidity,
		UID: in.UID, ReturnFolder: in.ReturnFolder, DueAt: in.DueAt.UTC(), Subject: in.Subject, Addresses: nonNilStrings(in.Addresses),
	}
	var row reminderRow
	if err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPost, Path: remindersPath, Body: body, Out: &row, Errors: reminderErrors}); err != nil {
		return domain.Reminder{}, err
	}
	if !domain.ValidUUID(row.ID) {
		return domain.Reminder{}, c.api.Unavailable("id de recordatorio inválido")
	}
	return row.toDomain(), nil
}

// ListReminders devuelve los recordatorios del buzon de ese tipo.
func (c *Client) ListReminders(ctx context.Context, username string, kind domain.ReminderKind) ([]domain.Reminder, error) {
	q := usernameQuery(username)
	q.Set("kind", string(kind))
	var rows []reminderRow
	if err := c.api.Do(ctx, internalapi.Request{Method: http.MethodGet, Path: remindersPath, Query: q, Out: &rows, Errors: reminderErrors}); err != nil {
		return nil, err
	}
	out := make([]domain.Reminder, len(rows))
	for i, r := range rows {
		out[i] = r.toDomain()
	}
	return out, nil
}

// RescheduleReminder cambia la hora de un recordatorio pendiente del buzon.
func (c *Client) RescheduleReminder(ctx context.Context, username, id string, at time.Time) (domain.Reminder, error) {
	body := struct {
		DueAt time.Time `json:"due_at"`
	}{DueAt: at.UTC()}
	var row reminderRow
	if err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPatch, Path: remindersPath + "/" + url.PathEscape(id), Query: usernameQuery(username), Body: body, Out: &row, Errors: reminderErrors}); err != nil {
		return domain.Reminder{}, err
	}
	return row.toDomain(), nil
}

// CancelReminder cancela un recordatorio pendiente del buzon.
func (c *Client) CancelReminder(ctx context.Context, username, id string) error {
	return c.api.Do(ctx, internalapi.Request{Method: http.MethodDelete, Path: remindersPath + "/" + url.PathEscape(id), Query: usernameQuery(username), Errors: reminderErrors})
}

// ClaimReminders reclama recordatorios vencidos de toda la celda con arriendo.
func (c *Client) ClaimReminders(ctx context.Context, limit int, lease time.Duration) ([]domain.ReminderClaim, error) {
	var rows []reminderRow
	body := claimBody{Limit: limit, LeaseSeconds: int(lease.Seconds())}
	if err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPost, Path: remindersPath + "/claim", Body: body, Out: &rows, Errors: reminderErrors}); err != nil {
		return nil, err
	}
	out := make([]domain.ReminderClaim, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.ReminderClaim{Reminder: r.toDomain(), Username: r.Username})
	}
	return out, nil
}

type finishReminderBody struct {
	Status string `json:"status"`
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
	Retry  bool   `json:"retry"`
}

// FinishReminder cierra un recordatorio reclamado.
func (c *Client) FinishReminder(ctx context.Context, id string, outcome domain.ReminderOutcome) error {
	body := finishReminderBody{
		Status: string(outcome.Status), Result: string(outcome.Result), Error: outcome.Error,
		Retry: outcome.Status == domain.ReminderFailed && outcome.Retry,
	}
	return c.api.Do(ctx, internalapi.Request{Method: http.MethodPost, Path: remindersPath + "/" + url.PathEscape(id) + "/finish", Body: body, Errors: reminderErrors})
}

type quickReplyBody struct {
	Name string `json:"name"`
	HTML string `json:"html"`
	Text string `json:"text"`
}

type quickReplyRow struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	HTML      string    `json:"html"`
	Text      string    `json:"text"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (r quickReplyRow) toDomain() domain.QuickReply {
	return domain.QuickReply{ID: r.ID, Name: r.Name, HTML: r.HTML, Text: r.Text, UpdatedAt: r.UpdatedAt}
}

var quickReplyErrors = internalapi.Errors{
	ByCode: map[string]error{
		"QUICK_REPLY_LIMIT": domain.ErrQuickReplyLimit,
		"CONFLICT":          domain.ErrQuickReplyExists,
	},
	ByStatus: map[int]error{http.StatusNotFound: domain.ErrQuickReplyNotFound},
	Field:    "html",
}

// QuickReplies lee las respuestas rapidas del buzon con los topes del directorio.
func (c *Client) QuickReplies(ctx context.Context, username string) (domain.QuickReplyList, error) {
	var out struct {
		Items  []quickReplyRow `json:"items"`
		Limits struct {
			MaxItems     int `json:"max_items"`
			MaxNameChars int `json:"max_name_chars"`
			MaxHTMLBytes int `json:"max_html_bytes"`
			MaxTextBytes int `json:"max_text_bytes"`
		} `json:"limits"`
	}
	if err := c.api.Do(ctx, internalapi.Request{Method: http.MethodGet, Path: quickRepliesPath, Query: usernameQuery(username), Out: &out, Errors: quickReplyErrors}); err != nil {
		return domain.QuickReplyList{}, err
	}
	list := domain.QuickReplyList{
		Items: make([]domain.QuickReply, len(out.Items)),
		Limits: domain.QuickReplyLimits{
			MaxItems: out.Limits.MaxItems, MaxNameChars: out.Limits.MaxNameChars,
			MaxHTMLBytes: out.Limits.MaxHTMLBytes, MaxTextBytes: out.Limits.MaxTextBytes,
		},
	}
	for i, r := range out.Items {
		list.Items[i] = r.toDomain()
	}
	return list, nil
}

// CreateQuickReply guarda una respuesta nueva con el HTML ya saneado y su texto.
func (c *Client) CreateQuickReply(ctx context.Context, username string, q domain.QuickReply) (domain.QuickReply, error) {
	var row quickReplyRow
	body := quickReplyBody{Name: q.Name, HTML: q.HTML, Text: q.Text}
	if err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPost, Path: quickRepliesPath, Query: usernameQuery(username), Body: body, Out: &row, Errors: quickReplyErrors}); err != nil {
		return domain.QuickReply{}, err
	}
	if !domain.ValidUUID(row.ID) {
		return domain.QuickReply{}, c.api.Unavailable("id de respuesta rápida inválido")
	}
	return row.toDomain(), nil
}

// UpdateQuickReply reemplaza el nombre y el contenido de una respuesta del buzon.
func (c *Client) UpdateQuickReply(ctx context.Context, username string, q domain.QuickReply) (domain.QuickReply, error) {
	var row quickReplyRow
	body := quickReplyBody{Name: q.Name, HTML: q.HTML, Text: q.Text}
	if err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPut, Path: quickRepliesPath + "/" + url.PathEscape(q.ID), Query: usernameQuery(username), Body: body, Out: &row, Errors: quickReplyErrors}); err != nil {
		return domain.QuickReply{}, err
	}
	return row.toDomain(), nil
}

// DeleteQuickReply borra una respuesta del buzon.
func (c *Client) DeleteQuickReply(ctx context.Context, username, id string) error {
	return c.api.Do(ctx, internalapi.Request{Method: http.MethodDelete, Path: quickRepliesPath + "/" + url.PathEscape(id), Query: usernameQuery(username), Errors: quickReplyErrors})
}

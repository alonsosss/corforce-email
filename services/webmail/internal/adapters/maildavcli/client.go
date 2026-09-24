// Package maildavcli lleva la libreta personal y el calendario del buzon a mail-dav por su API
// JSON interna (/internal/mail-dav/contacts... y /internal/mail-dav/calendar/events...), sobre
// los mismos casos de uso que CardDAV y CalDAV. La identidad viaja en cabeceras que solo pone el
// webmail (X-Mailbox-Tenant-ID y X-Mailbox-ID), con la empresa y el buzon de la sesion, junto al
// token de gateway: mail-dav vive en la base de la empresa y no conoce la sesion del webmail.
package maildavcli

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/internalapi"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

const (
	contactsPath = "/internal/mail-dav/contacts"
	eventsPath   = "/internal/mail-dav/calendar/events"

	tenantHeader  = "X-Mailbox-Tenant-ID"
	mailboxHeader = "X-Mailbox-ID"
	addressHeader = "X-Mailbox-Address"

	requestTimeout = 10 * time.Second
)

// Client implementa ports.ContactBook y ports.Calendar.
type Client struct {
	api *internalapi.Caller
	// transfer lleva la exportacion y la importacion, que mueven la libreta entera: un plazo por
	// intento mas largo y sin reintentos.
	transfer *internalapi.Caller
}

// New valida la URL de mail-dav (MAIL_DAV_URL). transferTimeout acota la exportacion y la
// importacion de la libreta.
func New(baseURL, token string, transferTimeout time.Duration) (*Client, error) {
	base, err := internalapi.NewBaseURL("MAIL_DAV_URL", baseURL)
	if err != nil {
		return nil, err
	}
	return &Client{
		api: internalapi.New("mail-dav", base, token,
			httpclient.New("mail-dav", httpclient.Options{Timeout: requestTimeout, MaxAttempts: 3})),
		transfer: internalapi.New("mail-dav", base, token,
			httpclient.New("mail-dav-transfer", httpclient.Options{Timeout: transferTimeout, MaxAttempts: 1})),
	}, nil
}

func identity(mb domain.MailboxRef) http.Header {
	h := http.Header{}
	h.Set(tenantHeader, mb.TenantID)
	h.Set(mailboxHeader, mb.MailboxID)
	// La direccion solo viaja si es una direccion: un nombre de buzon de otra forma no registra nada.
	if a, err := domain.NewAddress("address", "", mb.Address); err == nil {
		h.Set(addressHeader, strings.ToLower(a.Email))
	}
	return h
}

func withIfMatch(h http.Header, ifMatch string) http.Header {
	if ifMatch != "" {
		h.Set("If-Match", ifMatch)
	}
	return h
}

// passthrough entrega al usuario los rechazos de mail-dav tal cual (codigo, mensaje, detalles, ETag
// y Retry-After): la validacion y los topes son suyos y el webmail no los copia.
var passthrough = internalapi.Errors{Passthrough: true}

const metaPath = "/internal/mail-dav/meta"

// Limits lee los topes de la libreta y del calendario (GET /internal/mail-dav/meta), que no dependen
// del buzon.
func (c *Client) Limits(ctx context.Context) (map[string]int64, error) {
	var out struct {
		Limits map[string]int64 `json:"limits"`
	}
	if err := c.api.Do(ctx, internalapi.Request{Method: http.MethodGet, Path: metaPath, Out: &out, Errors: passthrough}); err != nil {
		return nil, err
	}
	if out.Limits == nil {
		out.Limits = map[string]int64{}
	}
	return out.Limits, nil
}

type contactValueJSON struct {
	Value string `json:"value"`
	Type  string `json:"type"`
}

type contactInputJSON struct {
	Name         string             `json:"name"`
	GivenName    string             `json:"given_name"`
	FamilyName   string             `json:"family_name"`
	Emails       []contactValueJSON `json:"emails"`
	Phones       []contactValueJSON `json:"phones"`
	Organization string             `json:"organization"`
	Title        string             `json:"title"`
	Notes        string             `json:"notes"`
	Birthday     string             `json:"birthday"`
}

type contactJSON struct {
	contactInputJSON
	ID        string     `json:"id"`
	ETag      string     `json:"etag"`
	UpdatedAt *time.Time `json:"updated_at"`
}

func toContactValues(list []domain.ContactValue) []contactValueJSON {
	out := make([]contactValueJSON, len(list))
	for i, v := range list {
		out[i] = contactValueJSON{Value: v.Value, Type: v.Type}
	}
	return out
}

func fromContactValues(list []contactValueJSON) []domain.ContactValue {
	out := make([]domain.ContactValue, len(list))
	for i, v := range list {
		out[i] = domain.ContactValue{Value: v.Value, Type: v.Type}
	}
	return out
}

func toContactInput(in domain.ContactInput) contactInputJSON {
	return contactInputJSON{
		Name: in.Name, GivenName: in.GivenName, FamilyName: in.FamilyName,
		Emails: toContactValues(in.Emails), Phones: toContactValues(in.Phones),
		Organization: in.Organization, Title: in.Title, Notes: in.Notes, Birthday: in.Birthday,
	}
}

func (c contactJSON) toDomain() domain.Contact {
	return domain.Contact{
		ContactInput: domain.ContactInput{
			Name: c.Name, GivenName: c.GivenName, FamilyName: c.FamilyName,
			Emails: fromContactValues(c.Emails), Phones: fromContactValues(c.Phones),
			Organization: c.Organization, Title: c.Title, Notes: c.Notes, Birthday: c.Birthday,
		},
		ID: c.ID, ETag: c.ETag, UpdatedAt: c.UpdatedAt,
	}
}

func (c *Client) ListContacts(ctx context.Context, mb domain.MailboxRef, q domain.ContactQuery) (domain.ContactPage, error) {
	query := url.Values{}
	if q.Search != "" {
		query.Set("q", q.Search)
	}
	if q.Page > 0 {
		query.Set("page", strconv.Itoa(q.Page))
	}
	if q.PerPage > 0 {
		query.Set("per_page", strconv.Itoa(q.PerPage))
	}
	var rows []contactJSON
	var meta internalapi.Meta
	err := c.api.Do(ctx, internalapi.Request{Method: http.MethodGet, Path: contactsPath, Query: query, Header: identity(mb),
		Out: &rows, MetaOut: &meta, Errors: passthrough})
	if err != nil {
		return domain.ContactPage{}, err
	}
	page := domain.ContactPage{Items: make([]domain.Contact, len(rows)), Total: meta.Total, Page: meta.Page, PerPage: meta.PerPage}
	for i, r := range rows {
		page.Items[i] = r.toDomain()
	}
	return page, nil
}

func (c *Client) Contact(ctx context.Context, mb domain.MailboxRef, id string) (domain.Contact, error) {
	var out contactJSON
	err := c.api.Do(ctx, internalapi.Request{Method: http.MethodGet, Path: contactsPath + "/" + url.PathEscape(id), Header: identity(mb),
		Out: &out, Errors: passthrough})
	return out.toDomain(), err
}

// CreateContact no se reintenta (POST): un reintento tras un corte crearia el contacto dos veces.
func (c *Client) CreateContact(ctx context.Context, mb domain.MailboxRef, in domain.ContactInput) (domain.Contact, error) {
	var out contactJSON
	err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPost, Path: contactsPath, Header: identity(mb),
		Body: toContactInput(in), Out: &out, Errors: passthrough})
	return out.toDomain(), err
}

func (c *Client) UpdateContact(ctx context.Context, mb domain.MailboxRef, id string, in domain.ContactInput, ifMatch string) (domain.Contact, error) {
	var out contactJSON
	err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPut, Path: contactsPath + "/" + url.PathEscape(id),
		Header: withIfMatch(identity(mb), ifMatch), Body: toContactInput(in), Out: &out, Errors: passthrough})
	return out.toDomain(), err
}

func (c *Client) DeleteContact(ctx context.Context, mb domain.MailboxRef, id string) error {
	return c.api.Do(ctx, internalapi.Request{Method: http.MethodDelete, Path: contactsPath + "/" + url.PathEscape(id),
		Header: identity(mb), Errors: passthrough})
}

// ExportContacts devuelve el cuerpo text/vcard de mail-dav tal cual; lo cierra quien lo pide.
func (c *Client) ExportContacts(ctx context.Context, mb domain.MailboxRef) (io.ReadCloser, error) {
	h := identity(mb)
	h.Set("Accept", "text/vcard")
	return c.transfer.Stream(ctx, internalapi.Request{Method: http.MethodGet, Path: contactsPath + "/export", Header: h, Errors: passthrough})
}

type importJSON struct {
	Imported int `json:"imported"`
	Updated  int `json:"updated"`
	Skipped  []struct {
		Index  int    `json:"index"`
		Reason string `json:"reason"`
	} `json:"skipped"`
}

// ImportContacts reenvia el fichero como multipart con el campo file, el mismo contrato que la ruta
// publica.
func (c *Client) ImportContacts(ctx context.Context, mb domain.MailboxRef, filename string, data []byte) (domain.ImportResult, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part := textproto.MIMEHeader{}
	part.Set("Content-Disposition", multipartDisposition(filename))
	part.Set("Content-Type", "text/vcard")
	pw, err := w.CreatePart(part)
	if err != nil {
		return domain.ImportResult{}, c.transfer.Unavailable(err.Error())
	}
	if _, err := pw.Write(data); err != nil {
		return domain.ImportResult{}, c.transfer.Unavailable(err.Error())
	}
	if err := w.Close(); err != nil {
		return domain.ImportResult{}, c.transfer.Unavailable(err.Error())
	}
	var out importJSON
	err = c.transfer.Do(ctx, internalapi.Request{Method: http.MethodPost, Path: contactsPath + "/import", Header: identity(mb),
		Raw: &buf, ContentType: w.FormDataContentType(), Out: &out, Errors: passthrough})
	if err != nil {
		return domain.ImportResult{}, err
	}
	res := domain.ImportResult{Imported: out.Imported, Updated: out.Updated, Skipped: make([]domain.ImportSkip, len(out.Skipped))}
	for i, s := range out.Skipped {
		res.Skipped[i] = domain.ImportSkip{Index: s.Index, Reason: s.Reason}
	}
	return res, nil
}

func multipartDisposition(filename string) string {
	return mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": filename})
}

type recurrenceJSON struct {
	Freq     string   `json:"freq"`
	Interval int      `json:"interval"`
	Count    *int     `json:"count"`
	Until    *string  `json:"until"`
	ByDay    []string `json:"by_day"`
}

type partyJSON struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

type attendeeJSON struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	PartStat string `json:"partstat"`
}

type eventInputJSON struct {
	Title           string          `json:"title"`
	Start           string          `json:"start"`
	End             string          `json:"end"`
	AllDay          bool            `json:"all_day"`
	TimeZone        string          `json:"timezone"`
	Location        string          `json:"location"`
	Description     string          `json:"description"`
	Recurrence      *recurrenceJSON `json:"recurrence"`
	ReminderMinutes *int            `json:"reminder_minutes"`
	Organizer       *partyJSON      `json:"organizer,omitempty"`
	Attendees       []attendeeJSON  `json:"attendees"`
}

type eventJSON struct {
	eventInputJSON
	ID   string `json:"id"`
	ETag string `json:"etag"`
}

type occurrenceJSON struct {
	ID           string `json:"id"`
	Start        string `json:"start"`
	End          string `json:"end"`
	AllDay       bool   `json:"all_day"`
	Title        string `json:"title"`
	Location     string `json:"location"`
	Recurring    bool   `json:"recurring"`
	RecurrenceID string `json:"recurrence_id"`
}

func toEventInput(in domain.EventInput) eventInputJSON {
	out := eventInputJSON{
		Title: in.Title, Start: in.Start, End: in.End, AllDay: in.AllDay, TimeZone: in.TimeZone, Location: in.Location,
		Description: in.Description, ReminderMinutes: in.ReminderMinutes, Attendees: []attendeeJSON{},
	}
	if r := in.Recurrence; r != nil {
		out.Recurrence = &recurrenceJSON{Freq: r.Freq, Interval: r.Interval, Count: r.Count, Until: r.Until, ByDay: r.ByDay}
	}
	if o := in.Organizer; o != nil {
		out.Organizer = &partyJSON{Email: o.Email, Name: o.Name}
	}
	for _, a := range in.Attendees {
		out.Attendees = append(out.Attendees, attendeeJSON{Email: a.Email, Name: a.Name, PartStat: a.PartStat})
	}
	return out
}

func (e eventJSON) toDomain() domain.Event {
	out := domain.Event{
		EventInput: domain.EventInput{
			Title: e.Title, Start: e.Start, End: e.End, AllDay: e.AllDay, TimeZone: e.TimeZone, Location: e.Location,
			Description: e.Description, ReminderMinutes: e.ReminderMinutes,
		},
		ID: e.ID, ETag: e.ETag,
	}
	if r := e.Recurrence; r != nil {
		out.Recurrence = &domain.Recurrence{Freq: r.Freq, Interval: r.Interval, Count: r.Count, Until: r.Until, ByDay: r.ByDay}
	}
	if o := e.Organizer; o != nil {
		out.Organizer = &domain.Party{Email: o.Email, Name: o.Name}
	}
	for _, a := range e.Attendees {
		out.Attendees = append(out.Attendees, domain.Attendee{Email: a.Email, Name: a.Name, PartStat: a.PartStat})
	}
	return out
}

func (c *Client) Occurrences(ctx context.Context, mb domain.MailboxRef, w domain.EventWindow) ([]domain.Occurrence, error) {
	query := url.Values{"start": {w.Start.UTC().Format(time.RFC3339)}, "end": {w.End.UTC().Format(time.RFC3339)}}
	var rows []occurrenceJSON
	err := c.api.Do(ctx, internalapi.Request{Method: http.MethodGet, Path: eventsPath, Query: query, Header: identity(mb),
		Out: &rows, Errors: passthrough})
	if err != nil {
		return nil, err
	}
	out := make([]domain.Occurrence, len(rows))
	for i, r := range rows {
		out[i] = domain.Occurrence{ID: r.ID, Start: r.Start, End: r.End, AllDay: r.AllDay, Title: r.Title, Location: r.Location, Recurring: r.Recurring, RecurrenceID: r.RecurrenceID}
	}
	return out, nil
}

func (c *Client) Event(ctx context.Context, mb domain.MailboxRef, id string) (domain.Event, error) {
	var out eventJSON
	err := c.api.Do(ctx, internalapi.Request{Method: http.MethodGet, Path: eventsPath + "/" + url.PathEscape(id), Header: identity(mb),
		Out: &out, Errors: passthrough})
	return out.toDomain(), err
}

func (c *Client) CreateEvent(ctx context.Context, mb domain.MailboxRef, in domain.EventInput) (domain.Event, error) {
	var out eventJSON
	err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPost, Path: eventsPath, Header: identity(mb),
		Body: toEventInput(in), Out: &out, Errors: passthrough})
	return out.toDomain(), err
}

func (c *Client) UpdateEvent(ctx context.Context, mb domain.MailboxRef, id string, in domain.EventInput, ifMatch string) (domain.Event, error) {
	var out eventJSON
	err := c.api.Do(ctx, internalapi.Request{Method: http.MethodPut, Path: eventsPath + "/" + url.PathEscape(id),
		Header: withIfMatch(identity(mb), ifMatch), Body: toEventInput(in), Out: &out, Errors: passthrough})
	return out.toDomain(), err
}

func (c *Client) DeleteEvent(ctx context.Context, mb domain.MailboxRef, id string) error {
	return c.api.Do(ctx, internalapi.Request{Method: http.MethodDelete, Path: eventsPath + "/" + url.PathEscape(id),
		Header: identity(mb), Errors: passthrough})
}

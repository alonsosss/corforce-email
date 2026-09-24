package http

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

const (
	// maxContactBody y maxEventBody acotan el JSON antes de llegar a mail-dav, que aplica sus
	// topes de tarjeta y de evento.
	maxContactBody = 256 << 10
	maxEventBody   = 256 << 10
	importField    = "file"
	exportFilename = "contactos.vcf"
)

type contactValueDTO struct {
	Value string `json:"value"`
	Type  string `json:"type"`
}

type contactInputDTO struct {
	Name         string            `json:"name"`
	GivenName    string            `json:"given_name"`
	FamilyName   string            `json:"family_name"`
	Emails       []contactValueDTO `json:"emails"`
	Phones       []contactValueDTO `json:"phones"`
	Organization string            `json:"organization"`
	Title        string            `json:"title"`
	Notes        string            `json:"notes"`
	Birthday     string            `json:"birthday"`
}

type contactDTO struct {
	ID string `json:"id"`
	contactInputDTO
	ETag      string     `json:"etag"`
	UpdatedAt *time.Time `json:"updated_at"`
}

func toContactValueDTOs(list []domain.ContactValue) []contactValueDTO {
	out := make([]contactValueDTO, len(list))
	for i, v := range list {
		out[i] = contactValueDTO{Value: v.Value, Type: v.Type}
	}
	return out
}

func fromContactValueDTOs(list []contactValueDTO) []domain.ContactValue {
	out := make([]domain.ContactValue, len(list))
	for i, v := range list {
		out[i] = domain.ContactValue{Value: v.Value, Type: v.Type}
	}
	return out
}

func (in contactInputDTO) toDomain() domain.ContactInput {
	return domain.ContactInput{
		Name: in.Name, GivenName: in.GivenName, FamilyName: in.FamilyName,
		Emails: fromContactValueDTOs(in.Emails), Phones: fromContactValueDTOs(in.Phones),
		Organization: in.Organization, Title: in.Title, Notes: in.Notes, Birthday: in.Birthday,
	}
}

func toContactDTO(c domain.Contact) contactDTO {
	return contactDTO{
		ID: c.ID, ETag: c.ETag, UpdatedAt: c.UpdatedAt,
		contactInputDTO: contactInputDTO{
			Name: c.Name, GivenName: c.GivenName, FamilyName: c.FamilyName,
			Emails: toContactValueDTOs(c.Emails), Phones: toContactValueDTOs(c.Phones),
			Organization: c.Organization, Title: c.Title, Notes: c.Notes, Birthday: c.Birthday,
		},
	}
}

func ifMatch(r *http.Request) string { return strings.TrimSpace(r.Header.Get("If-Match")) }

// withETag anuncia la version del recurso, que el cliente devuelve en If-Match al editarlo.
func withETag(w http.ResponseWriter, etag string) {
	if domain.ValidateIfMatch(etag) == nil && etag != "" {
		w.Header().Set("ETag", etag)
	}
}

type davMetaDTO struct {
	Limits map[string]int64 `json:"limits"`
}

// DAVMeta sirve los topes de la libreta personal y del calendario que aplica mail-dav (tamano de
// importacion, tarjetas por fichero, ventana de la agenda, paginas...), para que la interfaz no los
// copie.
func (h *Handler) DAVMeta(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	limits, err := h.app.DAVLimits(ctx, sessionFrom(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, davMetaDTO{Limits: limits})
}

// ListContacts busca en la libreta personal (?q=, ?page=, ?per_page=). Los topes de pagina los
// aplica mail-dav.
func (h *Handler) ListContacts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, err := optionalInt(q.Get("page"), "page")
	if err != nil {
		writeError(w, err)
		return
	}
	perPage, err := optionalInt(q.Get("per_page"), "per_page")
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	result, err := h.app.ListContacts(ctx, sessionFrom(r), domain.ContactQuery{Search: q.Get("q"), Page: page, PerPage: perPage})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]contactDTO, len(result.Items))
	for i, c := range result.Items {
		out[i] = toContactDTO(c)
	}
	if result.Page > 0 {
		page = result.Page
	}
	if result.PerPage > 0 {
		perPage = result.PerPage
	}
	response.JSONWithMeta(w, http.StatusOK, out, response.PageMeta(result.Total, max(page, 1), max(perPage, 1)))
}

func (h *Handler) Contact(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	c, err := h.app.Contact(ctx, sessionFrom(r), chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	withETag(w, c.ETag)
	response.JSON(w, http.StatusOK, toContactDTO(c))
}

func (h *Handler) CreateContact(w http.ResponseWriter, r *http.Request) {
	var req contactInputDTO
	if err := validate.DecodeJSONLimit(w, r, &req, maxContactBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	c, err := h.app.CreateContact(ctx, sessionFrom(r), req.toDomain())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	withETag(w, c.ETag)
	response.JSON(w, http.StatusCreated, toContactDTO(c))
}

func (h *Handler) UpdateContact(w http.ResponseWriter, r *http.Request) {
	var req contactInputDTO
	if err := validate.DecodeJSONLimit(w, r, &req, maxContactBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	c, err := h.app.UpdateContact(ctx, sessionFrom(r), chi.URLParam(r, "id"), req.toDomain(), ifMatch(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	withETag(w, c.ETag)
	response.JSON(w, http.StatusOK, toContactDTO(c))
}

func (h *Handler) DeleteContact(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	if err := h.app.DeleteContact(ctx, sessionFrom(r), chi.URLParam(r, "id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ExportContacts entrega todas las tarjetas como un fichero .vcf descargable.
func (h *Handler) ExportContacts(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, h.cfg.TransferTimeout)
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.TransferTimeout)
	defer cancel()
	started := false
	err := h.app.ExportContacts(ctx, sessionFrom(r), func(body io.Reader) error {
		hd := w.Header()
		hd.Set("Content-Type", "text/vcard; charset=utf-8")
		hd.Set("Content-Disposition", contentDisposition(exportFilename))
		hd.Set("Content-Security-Policy", partCSP)
		hd.Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		started = true
		_, err := io.Copy(w, body)
		return err
	})
	if err == nil {
		return
	}
	if started {
		h.logger.Warn("webmail: exportacion de la libreta interrumpida", zap.String("request_id", middleware.GetRequestID(r.Context())), zap.Error(err))
		panic(http.ErrAbortHandler)
	}
	h.fail(w, r, err)
}

type importSkipDTO struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

type importDTO struct {
	Imported int             `json:"imported"`
	Updated  int             `json:"updated"`
	Skipped  []importSkipDTO `json:"skipped"`
}

// ImportContacts importa un fichero vCard (campo multipart file, varias tarjetas). Se lee entero
// en memoria con el tope de /meta: no se escribe en disco.
func (h *Handler) ImportContacts(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, h.cfg.TransferTimeout)
	limit := h.app.Meta().MaxImportBytes
	r.Body = http.MaxBytesReader(w, r.Body, limit+multipartOverhead)
	filename, data, err := readImportFile(r, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.TransferTimeout)
	defer cancel()
	res, err := h.app.ImportContacts(ctx, sessionFrom(r), filename, data)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := importDTO{Imported: res.Imported, Updated: res.Updated, Skipped: make([]importSkipDTO, len(res.Skipped))}
	for i, s := range res.Skipped {
		out.Skipped[i] = importSkipDTO{Index: s.Index, Reason: s.Reason}
	}
	response.JSON(w, http.StatusOK, out)
}

func readImportFile(r *http.Request, limit int64) (string, []byte, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return "", nil, domain.NewValidationError("body", "se esperaba multipart/form-data")
	}
	var filename string
	var data []byte
	found := false
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", nil, importBodyError(err)
		}
		if part.FormName() != importField || found {
			return "", nil, domain.NewValidationError(part.FormName(), "campo no admitido")
		}
		found = true
		filename = part.FileName()
		data, err = io.ReadAll(io.LimitReader(part, limit+1))
		if err != nil {
			return "", nil, importBodyError(err)
		}
		if int64(len(data)) > limit {
			return "", nil, domain.ErrImportTooLarge
		}
	}
	if !found {
		return "", nil, domain.NewValidationError(importField, "falta el fichero")
	}
	return filename, data, nil
}

func importBodyError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return domain.ErrImportTooLarge
	}
	return domain.NewValidationError("body", "formulario multipart inválido")
}

type recurrenceDTO struct {
	Freq     string   `json:"freq"`
	Interval int      `json:"interval"`
	Count    *int     `json:"count"`
	Until    *string  `json:"until"`
	ByDay    []string `json:"by_day"`
}

type partyDTO struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

type attendeeDTO struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	PartStat string `json:"partstat"`
}

// eventInputDTO es un evento tal como lo edita la interfaz. El organizador solo sale: con invitados lo pone el
// servicio con el buzon de la sesion.
type eventInputDTO struct {
	Title           string         `json:"title"`
	Start           string         `json:"start"`
	End             string         `json:"end"`
	AllDay          bool           `json:"all_day"`
	TimeZone        string         `json:"timezone"`
	Location        string         `json:"location"`
	Description     string         `json:"description"`
	Recurrence      *recurrenceDTO `json:"recurrence"`
	ReminderMinutes *int           `json:"reminder_minutes"`
	Organizer       *partyDTO      `json:"organizer"`
	Attendees       []attendeeDTO  `json:"attendees"`
}

type eventDTO struct {
	ID   string `json:"id"`
	ETag string `json:"etag"`
	eventInputDTO
}

type occurrenceDTO struct {
	ID           string `json:"id"`
	Start        string `json:"start"`
	End          string `json:"end"`
	AllDay       bool   `json:"all_day"`
	Title        string `json:"title"`
	Location     string `json:"location"`
	Recurring    bool   `json:"recurring"`
	RecurrenceID string `json:"recurrence_id"`
}

func (in eventInputDTO) toDomain() domain.EventInput {
	out := domain.EventInput{
		Title: in.Title, Start: in.Start, End: in.End, AllDay: in.AllDay, TimeZone: in.TimeZone, Location: in.Location,
		Description: in.Description, ReminderMinutes: in.ReminderMinutes,
	}
	if rr := in.Recurrence; rr != nil {
		out.Recurrence = &domain.Recurrence{Freq: rr.Freq, Interval: rr.Interval, Count: rr.Count, Until: rr.Until, ByDay: rr.ByDay}
	}
	for _, a := range in.Attendees {
		out.Attendees = append(out.Attendees, domain.Attendee{Email: a.Email, Name: a.Name, PartStat: a.PartStat})
	}
	return out
}

func toEventDTO(e domain.Event) eventDTO {
	out := eventDTO{
		ID: e.ID, ETag: e.ETag,
		eventInputDTO: eventInputDTO{
			Title: e.Title, Start: e.Start, End: e.End, AllDay: e.AllDay, TimeZone: e.TimeZone, Location: e.Location,
			Description: e.Description, ReminderMinutes: e.ReminderMinutes, Attendees: []attendeeDTO{},
		},
	}
	if rr := e.Recurrence; rr != nil {
		out.Recurrence = &recurrenceDTO{Freq: rr.Freq, Interval: rr.Interval, Count: rr.Count, Until: rr.Until, ByDay: nonNil(rr.ByDay)}
	}
	if o := e.Organizer; o != nil {
		out.Organizer = &partyDTO{Email: o.Email, Name: o.Name}
	}
	for _, a := range e.Attendees {
		out.Attendees = append(out.Attendees, attendeeDTO{Email: a.Email, Name: a.Name, PartStat: a.PartStat})
	}
	return out
}

// Occurrences devuelve las apariciones de los eventos en la ventana [start, end) (RFC 3339). El
// tope de la ventana lo aplica mail-dav.
func (h *Handler) Occurrences(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	window, err := domain.NewEventWindow(q.Get("start"), q.Get("end"))
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	list, err := h.app.Occurrences(ctx, sessionFrom(r), window)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]occurrenceDTO, len(list))
	for i, o := range list {
		out[i] = occurrenceDTO{ID: o.ID, Start: o.Start, End: o.End, AllDay: o.AllDay, Title: o.Title, Location: o.Location, Recurring: o.Recurring, RecurrenceID: o.RecurrenceID}
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) Event(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	e, err := h.app.Event(ctx, sessionFrom(r), chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	withETag(w, e.ETag)
	response.JSON(w, http.StatusOK, toEventDTO(e))
}

func (h *Handler) CreateEvent(w http.ResponseWriter, r *http.Request) {
	var req eventInputDTO
	if err := validate.DecodeJSONLimit(w, r, &req, maxEventBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	e, err := h.app.CreateEvent(ctx, sessionFrom(r), req.toDomain(), notifyParam(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	withETag(w, e.ETag)
	response.JSON(w, http.StatusCreated, toSavedEventDTO(e))
}

func (h *Handler) UpdateEvent(w http.ResponseWriter, r *http.Request) {
	var req eventInputDTO
	if err := validate.DecodeJSONLimit(w, r, &req, maxEventBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	e, err := h.app.UpdateEvent(ctx, sessionFrom(r), chi.URLParam(r, "id"), req.toDomain(), ifMatch(r), notifyParam(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	withETag(w, e.ETag)
	response.JSON(w, http.StatusOK, toSavedEventDTO(e))
}

// DeleteEvent borra el evento entero, con toda su serie. Si el buzon lo organizaba con invitados, responde 200 con
// lo que paso con la cancelacion; si no, 204.
func (h *Handler) DeleteEvent(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	delivery, err := h.app.DeleteEvent(ctx, sessionFrom(r), chi.URLParam(r, "id"), notifyParam(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if delivery == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	response.JSON(w, http.StatusOK, map[string]*deliveryDTO{"invitations": toDeliveryDTO(delivery)})
}

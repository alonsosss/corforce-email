package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	apiresponse "github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// API interna de contactos y calendario para el webmail (docs/Plan_Webmail_Competitivo.md, 4.3). Solo la
// llaman servicios de la plataforma con el token interno; el gateway no enruta /internal. El buzon no se
// autentica aqui: lo nombran las cabeceras que pone el webmail con la identidad que mail-auth le devolvio al
// abrir la sesion, y todo se hace con esa identidad acotada (empresa y buzon), como una peticion DAV.
const (
	InternalPrefix = "/internal/mail-dav"
	TenantHeader   = "X-Mailbox-Tenant-ID"
	MailboxHeader  = "X-Mailbox-ID"
	// AddressHeader es la direccion del buzon (opcional): con ella el buzon queda localizable para la
	// disponibilidad de sus companeros y es el dueno de su pagina de citas.
	AddressHeader = "X-Mailbox-Address"
	// PublicBookingPrefix son las rutas de la pagina publica de citas: sin buzon, las pide el webmail en nombre
	// de un visitante.
	PublicBookingPrefix = InternalPrefix + "/booking/public"
)

// multipartOverhead es lo que se admite de mas sobre el fichero en una importacion: las cabeceras de la parte
// y los separadores del multipart.
const multipartOverhead = 64 << 10

// APILimits son los topes de la API estructurada; los sirve GET /internal/mail-dav/meta.
type APILimits struct {
	// MaxBodyBytes acota el cuerpo JSON de un alta o una actualizacion.
	MaxBodyBytes int64
	// MaxImportBytes y MaxImportCards acotan el fichero de una importacion y cuantas tarjetas trae.
	MaxImportBytes int64
	MaxImportCards int
	// MaxWindowDays es la ventana mas larga de una consulta de apariciones.
	MaxWindowDays int
	// MaxOccurrences es cuantas apariciones devuelve como mucho una consulta.
	MaxOccurrences int
	// DefaultPerPage y MaxPerPage son el tamano de pagina por omision y el maximo del listado de contactos.
	DefaultPerPage int
	MaxPerPage     int
	// MaxITIPBodyBytes acota el cuerpo JSON que lleva un iCalendar recibido por correo (invitaciones); nunca es
	// menor que MaxBodyBytes.
	MaxITIPBodyBytes int64
}

func (l APILimits) Validate() error {
	for name, v := range map[string]int64{
		"MaxBodyBytes": l.MaxBodyBytes, "MaxImportBytes": l.MaxImportBytes, "MaxImportCards": int64(l.MaxImportCards),
		"MaxWindowDays": int64(l.MaxWindowDays), "MaxOccurrences": int64(l.MaxOccurrences),
		"DefaultPerPage": int64(l.DefaultPerPage), "MaxPerPage": int64(l.MaxPerPage),
	} {
		if v < 1 {
			return fmt.Errorf("%s debe ser mayor que cero", name)
		}
	}
	if l.DefaultPerPage > l.MaxPerPage {
		return errors.New("DefaultPerPage no puede pasar de MaxPerPage")
	}
	return nil
}

type API struct {
	uc     *app.UseCase
	lim    APILimits
	logger *zap.Logger
}

func NewAPI(uc *app.UseCase, lim APILimits, logger *zap.Logger) (*API, error) {
	if err := lim.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	lim.MaxITIPBodyBytes = max(lim.MaxITIPBodyBytes, lim.MaxBodyBytes)
	return &API{uc: uc, lim: lim, logger: logger}, nil
}

// Routes sirve la API bajo InternalPrefix. Quien la monta pone delante el token interno y
// RequireInternalCaller.
func (a *API) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(noStore)
	r.Route(InternalPrefix, func(r chi.Router) {
		r.Get("/meta", a.meta)
		r.Group(func(r chi.Router) {
			r.Use(a.mailbox)
			r.Get("/contacts", a.listContacts)
			r.Post("/contacts", a.createContact)
			r.Get("/contacts/export", a.exportContacts)
			r.Post("/contacts/import", a.importContacts)
			r.Get("/contacts/{id}", a.getContact)
			r.Put("/contacts/{id}", a.updateContact)
			r.Delete("/contacts/{id}", a.deleteContact)
			r.Get("/calendar/events", a.listOccurrences)
			r.Post("/calendar/events", a.createEvent)
			r.Get("/calendar/events/{id}", a.getEvent)
			r.Put("/calendar/events/{id}", a.updateEvent)
			r.Delete("/calendar/events/{id}", a.deleteEvent)
			r.Put("/calendar/events/{id}/occurrences/{rid}", a.updateOccurrence)
			r.Delete("/calendar/events/{id}/occurrences/{rid}", a.deleteOccurrence)
			r.Post("/calendar/events/{id}/itip", a.eventInvitation)
			r.Post("/itip/inspect", a.inspectInvitation)
			r.Post("/itip/respond", a.respondInvitation)
			r.Post("/itip/apply", a.applyInvitation)
			r.Post("/availability", a.availability)
			r.Get("/booking", a.bookingSettings)
			r.Put("/booking", a.saveBookingSettings)
		})
		r.Get("/booking/public/{tenant}/{page}", a.publicBooking)
		r.Post("/booking/public/{tenant}/{page}/reservations", a.book)
	})
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) { apiresponse.ErrNotFound(w, "ruta desconocida") })
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		apiresponse.Err(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "metodo no admitido")
	})
	return r
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

type principalKey struct{}

// mailbox exige las dos cabeceras de identidad (UUID) y liga la peticion a la empresa y al buzon.
func (a *API) mailbox(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids := make([]uuid.UUID, 2)
		for i, h := range []string{TenantHeader, MailboxHeader} {
			id, err := uuid.Parse(strings.TrimSpace(r.Header.Get(h)))
			if err != nil || id == uuid.Nil {
				apiresponse.ErrWithDetails(w, http.StatusBadRequest, "BAD_REQUEST", "falta la cabecera o no es un UUID", map[string]string{"field": h})
				return
			}
			ids[i] = id
		}
		ctx, p, err := a.uc.BindMailbox(r.Context(), ids[0], ids[1])
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if raw := strings.TrimSpace(r.Header.Get(AddressHeader)); raw != "" {
			address, err := domain.NormalizeAddress(AddressHeader, raw)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			p.Username = address
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, principalKey{}, p)))
	})
}

func principalOf(r *http.Request) domain.Principal {
	p, _ := r.Context().Value(principalKey{}).(domain.Principal)
	return p
}

func limitExceeded(w http.ResponseWriter, status int, limit, message string) {
	apiresponse.ErrWithDetails(w, status, "LIMIT_EXCEEDED", message, map[string]string{"limit": limit})
}

// fail traduce un error del caso de uso a la respuesta JSON. Lo que no se reconoce es un 500 sin detalle.
func (a *API) fail(w http.ResponseWriter, r *http.Request, err error) {
	var field *domain.FieldError
	var bad *domain.VCardError
	var badICal *domain.ICalError
	var conflict *domain.UIDConflictError
	switch {
	case errors.As(err, &field):
		apiresponse.ErrWithDetails(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", field.Reason, map[string]string{"field": field.Field})
	case errors.Is(err, domain.ErrTenantUnknown), errors.Is(err, domain.ErrNotFound):
		apiresponse.ErrNotFound(w, "no existe")
	case errors.Is(err, domain.ErrPreconditionFailed):
		apiresponse.Err(w, http.StatusPreconditionFailed, "PRECONDITION_FAILED", "el recurso cambio: vuelva a leerlo")
	case errors.Is(err, domain.ErrContactLimit):
		limitExceeded(w, http.StatusInsufficientStorage, "contacts", err.Error())
	case errors.Is(err, domain.ErrEventLimit):
		limitExceeded(w, http.StatusInsufficientStorage, "events", err.Error())
	case errors.Is(err, domain.ErrAddressbookLimit):
		limitExceeded(w, http.StatusInsufficientStorage, "addressbooks", err.Error())
	case errors.Is(err, domain.ErrCalendarLimit):
		limitExceeded(w, http.StatusInsufficientStorage, "calendars", err.Error())
	case errors.Is(err, domain.ErrStorageLimit):
		limitExceeded(w, http.StatusInsufficientStorage, "storage", err.Error())
	case errors.Is(err, domain.ErrResultTooLarge):
		limitExceeded(w, http.StatusInsufficientStorage, "result", err.Error())
	case errors.Is(err, domain.ErrImportTooManyCards):
		limitExceeded(w, http.StatusRequestEntityTooLarge, "import_cards", err.Error())
	case errors.Is(err, domain.ErrBookingLimit):
		w.Header().Set("Retry-After", "3600")
		limitExceeded(w, http.StatusTooManyRequests, "bookings", err.Error())
	case errors.Is(err, domain.ErrSlotUnavailable):
		apiresponse.Err(w, http.StatusConflict, "SLOT_UNAVAILABLE", err.Error())
	case errors.As(err, &bad):
		if bad.TooLarge {
			limitExceeded(w, http.StatusRequestEntityTooLarge, "object", bad.Reason)
			return
		}
		apiresponse.ErrWithDetails(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", bad.Reason, map[string]string{"field": "contact"})
	case errors.As(err, &badICal):
		if badICal.Kind == domain.ICalTooLarge {
			limitExceeded(w, http.StatusRequestEntityTooLarge, "object", badICal.Reason)
			return
		}
		apiresponse.ErrWithDetails(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", badICal.Reason, map[string]string{"field": "event"})
	case errors.As(err, &conflict):
		apiresponse.ErrConflict(w, "el UID ya lo usa otro recurso")
	case errors.Is(err, domain.ErrInvalidMailbox):
		apiresponse.ErrBadRequest(w, err.Error())
	case errors.Is(err, domain.ErrUnavailable), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		w.Header().Set("Retry-After", "30")
		apiresponse.Err(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "servicio no disponible")
	default:
		a.logger.Error("mail-dav: error inesperado en la API interna", zap.String("request_id", middleware.GetRequestID(r.Context())),
			zap.String("method", r.Method), zap.String("path", r.URL.Path), zap.Error(err))
		apiresponse.ErrInternal(w)
	}
}

// decode lee un cuerpo JSON acotado. Responde el error y devuelve false si no se puede leer.
func (a *API) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, a.lim.MaxBodyBytes)
	err := json.NewDecoder(r.Body).Decode(v)
	var tooLarge *http.MaxBytesError
	var typeErr *json.UnmarshalTypeError
	switch {
	case err == nil:
		return true
	case errors.As(err, &tooLarge):
		limitExceeded(w, http.StatusRequestEntityTooLarge, "body", "el cuerpo supera el tamano maximo")
	case errors.As(err, &typeErr) && typeErr.Field != "":
		apiresponse.ErrWithDetails(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "tipo de dato no valido", map[string]string{"field": typeErr.Field})
	default:
		apiresponse.ErrBadRequest(w, "el cuerpo no es un JSON valido")
	}
	return false
}

func setETag(w http.ResponseWriter, etag string) { w.Header().Set("ETag", `"`+etag+`"`) }

// ifMatch es la condicion de una escritura: solo el If-Match del cliente (etag fuerte o *).
func ifMatch(r *http.Request) domain.Precondition {
	c := preconditionFrom(r.Header)
	return domain.Precondition{IfMatchAny: c.IfMatchAny, IfMatch: c.IfMatch}
}

type limitsJSON struct {
	MaxImportBytes        int64 `json:"max_import_bytes"`
	MaxImportCards        int   `json:"max_import_cards"`
	MaxWindowDays         int   `json:"max_window_days"`
	MaxOccurrences        int   `json:"max_occurrences"`
	MaxPerPage            int   `json:"max_per_page"`
	DefaultPerPage        int   `json:"default_per_page"`
	MaxContacts           int   `json:"max_contacts"`
	MaxEvents             int   `json:"max_events"`
	MaxEmails             int   `json:"max_emails"`
	MaxPhones             int   `json:"max_phones"`
	MaxTextLength         int   `json:"max_text_length"`
	MaxNotesLength        int   `json:"max_notes_length"`
	MaxTitleLength        int   `json:"max_title_length"`
	MaxLocationLength     int   `json:"max_location_length"`
	MaxDescriptionLength  int   `json:"max_description_length"`
	MaxReminderMinutes    int   `json:"max_reminder_minutes"`
	MaxRecurrenceCount    int   `json:"max_recurrence_count"`
	MaxRecurrenceInterval int   `json:"max_recurrence_interval"`
}

func (a *API) meta(w http.ResponseWriter, _ *http.Request) {
	apiresponse.JSON(w, http.StatusOK, map[string]limitsJSON{"limits": {
		MaxImportBytes: a.lim.MaxImportBytes, MaxImportCards: a.lim.MaxImportCards, MaxWindowDays: a.lim.MaxWindowDays,
		MaxOccurrences: a.lim.MaxOccurrences, MaxPerPage: a.lim.MaxPerPage, DefaultPerPage: a.lim.DefaultPerPage,
		MaxContacts: a.uc.Limits().MaxContactsPerMailbox, MaxEvents: a.uc.CalendarLimits().MaxEventsPerMailbox,
		MaxEmails: domain.MaxContactEmails, MaxPhones: domain.MaxContactPhones,
		MaxTextLength: domain.MaxContactTextRunes, MaxNotesLength: domain.MaxContactNotesRunes,
		MaxTitleLength: domain.MaxEventTitleRunes, MaxLocationLength: domain.MaxEventLocationRunes,
		MaxDescriptionLength: domain.MaxEventDescriptionRunes, MaxReminderMinutes: domain.MaxReminderMinutes,
		MaxRecurrenceCount: domain.MaxRecurrenceCount, MaxRecurrenceInterval: domain.MaxRecurrenceInterval,
	}})
}

type typedJSON struct {
	Value string `json:"value"`
	Type  string `json:"type"`
}

type contactInput struct {
	Name         string      `json:"name"`
	GivenName    string      `json:"given_name"`
	FamilyName   string      `json:"family_name"`
	Emails       []typedJSON `json:"emails"`
	Phones       []typedJSON `json:"phones"`
	Organization string      `json:"organization"`
	Title        string      `json:"title"`
	Notes        string      `json:"notes"`
	Birthday     string      `json:"birthday"`
}

type contactJSON struct {
	ID   string `json:"id"`
	ETag string `json:"etag"`
	contactInput
	UpdatedAt time.Time `json:"updated_at"`
}

func typedIn(list []typedJSON) []domain.TypedValue {
	out := make([]domain.TypedValue, len(list))
	for i, v := range list {
		out[i] = domain.TypedValue{Value: v.Value, Type: v.Type}
	}
	return out
}

func typedOut(list []domain.TypedValue) []typedJSON {
	out := make([]typedJSON, len(list))
	for i, v := range list {
		out[i] = typedJSON{Value: v.Value, Type: v.Type}
	}
	return out
}

func (c contactInput) fields() domain.ContactFields {
	return domain.ContactFields{
		Name: c.Name, GivenName: c.GivenName, FamilyName: c.FamilyName, Emails: typedIn(c.Emails), Phones: typedIn(c.Phones),
		Organization: c.Organization, Title: c.Title, Notes: c.Notes, Birthday: c.Birthday,
	}
}

func contactOut(v app.ContactView) contactJSON {
	f := v.Fields
	return contactJSON{ID: v.ID, ETag: v.ETag, UpdatedAt: v.UpdatedAt.UTC(), contactInput: contactInput{
		Name: f.Name, GivenName: f.GivenName, FamilyName: f.FamilyName, Emails: typedOut(f.Emails), Phones: typedOut(f.Phones),
		Organization: f.Organization, Title: f.Title, Notes: f.Notes, Birthday: f.Birthday,
	}}
}

// intParam lee un entero de la consulta entre lo y hi; vacio es def.
func intParam(r *http.Request, name string, def, lo, hi int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < lo || n > hi {
		return 0, &domain.FieldError{Field: name, Reason: fmt.Sprintf("se espera un entero entre %d y %d", lo, hi)}
	}
	return n, nil
}

func (a *API) listContacts(w http.ResponseWriter, r *http.Request) {
	page, err := intParam(r, "page", 1, 1, 1_000_000)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	perPage, err := intParam(r, "per_page", a.lim.DefaultPerPage, 1, a.lim.MaxPerPage)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	q := r.URL.Query().Get("q")
	if len([]rune(q)) > domain.MaxContactTextRunes {
		a.fail(w, r, &domain.FieldError{Field: "q", Reason: "supera el largo maximo"})
		return
	}
	views, total, err := a.uc.ContactPage(r.Context(), principalOf(r), q, page, perPage)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]contactJSON, len(views))
	for i, v := range views {
		out[i] = contactOut(v)
	}
	meta := apiresponse.PageMeta(int64(total), page, perPage)
	apiresponse.JSONWithMeta(w, http.StatusOK, out, meta)
}

func (a *API) getContact(w http.ResponseWriter, r *http.Request) {
	v, err := a.uc.ContactByID(r.Context(), principalOf(r), chi.URLParam(r, "id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	setETag(w, v.ETag)
	apiresponse.JSON(w, http.StatusOK, contactOut(v))
}

func (a *API) createContact(w http.ResponseWriter, r *http.Request) {
	var in contactInput
	if !a.decode(w, r, &in) {
		return
	}
	v, err := a.uc.CreateContact(r.Context(), principalOf(r), in.fields())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	setETag(w, v.ETag)
	apiresponse.JSON(w, http.StatusCreated, contactOut(v))
}

func (a *API) updateContact(w http.ResponseWriter, r *http.Request) {
	var in contactInput
	if !a.decode(w, r, &in) {
		return
	}
	v, err := a.uc.UpdateContact(r.Context(), principalOf(r), chi.URLParam(r, "id"), in.fields(), ifMatch(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	setETag(w, v.ETag)
	apiresponse.JSON(w, http.StatusOK, contactOut(v))
}

func (a *API) deleteContact(w http.ResponseWriter, r *http.Request) {
	if err := a.uc.DeleteContactByID(r.Context(), principalOf(r), chi.URLParam(r, "id"), ifMatch(r)); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// exportContacts escribe todos los vCard seguidos. La cabecera sale con la primera tarjeta: un error antes
// es una respuesta JSON; uno despues corta la conexion, para que el cliente no tome por completo un fichero
// a medias.
func (a *API) exportContacts(w http.ResponseWriter, r *http.Request) {
	started := false
	start := func() {
		if !started {
			started = true
			w.Header().Set("Content-Type", "text/vcard; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="contacts.vcf"`)
			w.WriteHeader(http.StatusOK)
		}
	}
	err := a.uc.ExportContacts(r.Context(), principalOf(r), func(card string) error {
		start()
		_, err := io.WriteString(w, card)
		return err
	})
	switch {
	case err == nil:
		start()
	case !started:
		a.fail(w, r, err)
	default:
		a.logger.Warn("mail-dav: exportacion de contactos interrumpida", zap.String("request_id", middleware.GetRequestID(r.Context())), zap.Error(err))
		panic(http.ErrAbortHandler)
	}
}

type importSkipJSON struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

type importJSON struct {
	Imported int              `json:"imported"`
	Updated  int              `json:"updated"`
	Skipped  []importSkipJSON `json:"skipped"`
}

// readImportFile lee la parte "file" de un multipart/form-data, acotada a MaxImportBytes.
func (a *API) readImportFile(w http.ResponseWriter, r *http.Request) (string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, a.lim.MaxImportBytes+multipartOverhead)
	mr, err := r.MultipartReader()
	if err != nil {
		a.fail(w, r, &domain.FieldError{Field: "file", Reason: "se espera multipart/form-data con el campo file"})
		return "", false
	}
	for {
		part, err := mr.NextPart()
		var tooLarge *http.MaxBytesError
		switch {
		case errors.Is(err, io.EOF):
			a.fail(w, r, &domain.FieldError{Field: "file", Reason: "falta el fichero"})
			return "", false
		case errors.As(err, &tooLarge):
			limitExceeded(w, http.StatusRequestEntityTooLarge, "import_bytes", "el fichero supera el tamano maximo")
			return "", false
		case err != nil:
			apiresponse.ErrBadRequest(w, "multipart mal formado")
			return "", false
		}
		if part.FormName() != "file" {
			_ = part.Close()
			continue
		}
		data, err := io.ReadAll(io.LimitReader(part, a.lim.MaxImportBytes+1))
		_ = part.Close()
		if errors.As(err, &tooLarge) || int64(len(data)) > a.lim.MaxImportBytes {
			limitExceeded(w, http.StatusRequestEntityTooLarge, "import_bytes", "el fichero supera el tamano maximo")
			return "", false
		}
		if err != nil {
			apiresponse.ErrBadRequest(w, "multipart mal formado")
			return "", false
		}
		return string(data), true
	}
}

func (a *API) importContacts(w http.ResponseWriter, r *http.Request) {
	raw, ok := a.readImportFile(w, r)
	if !ok {
		return
	}
	res, err := a.uc.ImportContacts(r.Context(), principalOf(r), raw, a.lim.MaxImportCards)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if res.Imported == 0 && res.Updated == 0 && len(res.Skipped) == 0 {
		a.fail(w, r, &domain.FieldError{Field: "file", Reason: "el fichero no contiene tarjetas vCard"})
		return
	}
	out := importJSON{Imported: res.Imported, Updated: res.Updated, Skipped: make([]importSkipJSON, len(res.Skipped))}
	for i, s := range res.Skipped {
		out.Skipped[i] = importSkipJSON{Index: s.Index, Reason: s.Reason}
	}
	apiresponse.JSON(w, http.StatusOK, out)
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

type eventInput struct {
	Title           string          `json:"title"`
	Start           string          `json:"start"`
	End             string          `json:"end"`
	AllDay          bool            `json:"all_day"`
	TimeZone        string          `json:"timezone"`
	Location        string          `json:"location"`
	Description     string          `json:"description"`
	Recurrence      *recurrenceJSON `json:"recurrence"`
	ReminderMinutes *int            `json:"reminder_minutes"`
	Organizer       *partyJSON      `json:"organizer"`
	Attendees       []attendeeJSON  `json:"attendees"`
}

type eventJSON struct {
	ID   string `json:"id"`
	ETag string `json:"etag"`
	eventInput
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

// formatTime escribe un instante en RFC 3339 y UTC; en un dia completo es la medianoche UTC de la fecha.
func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func (in eventInput) fields() (domain.EventFields, error) {
	f := domain.EventFields{Title: in.Title, AllDay: in.AllDay, TimeZone: in.TimeZone, Location: in.Location, Description: in.Description, ReminderMinutes: in.ReminderMinutes}
	if in.Organizer != nil {
		f.Organizer = &domain.Party{Email: in.Organizer.Email, Name: in.Organizer.Name}
	}
	for _, at := range in.Attendees {
		f.Attendees = append(f.Attendees, domain.Attendee{Email: at.Email, Name: at.Name, PartStat: at.PartStat})
	}
	var err error
	if f.Start, err = domain.ParseEventTime("start", in.Start, in.AllDay); err != nil {
		return f, err
	}
	if f.End, err = domain.ParseEventTime("end", in.End, in.AllDay); err != nil {
		return f, err
	}
	if rec := in.Recurrence; rec != nil {
		f.Recurrence = &domain.Recurrence{Freq: rec.Freq, Interval: rec.Interval, ByDay: rec.ByDay}
		if rec.Count != nil {
			if *rec.Count < 1 {
				return f, &domain.FieldError{Field: "recurrence.count", Reason: "fuera de rango"}
			}
			f.Recurrence.Count = *rec.Count
		}
		if rec.Until != nil && strings.TrimSpace(*rec.Until) != "" {
			u, err := domain.ParseEventTime("recurrence.until", *rec.Until, in.AllDay)
			if err != nil {
				return f, err
			}
			f.Recurrence.Until = &u
		}
	}
	return f, nil
}

func eventOut(v app.EventView) eventJSON {
	f := v.Fields
	out := eventJSON{ID: v.ID, ETag: v.ETag, eventInput: eventInput{
		Title: f.Title, Start: formatTime(f.Start), End: formatTime(f.End), AllDay: f.AllDay, TimeZone: f.TimeZone,
		Location: f.Location, Description: f.Description, ReminderMinutes: f.ReminderMinutes, Attendees: []attendeeJSON{},
	}}
	if f.Organizer != nil {
		out.Organizer = &partyJSON{Email: f.Organizer.Email, Name: f.Organizer.Name}
	}
	for _, at := range f.Attendees {
		out.Attendees = append(out.Attendees, attendeeJSON{Email: at.Email, Name: at.Name, PartStat: at.PartStat})
	}
	if rec := f.Recurrence; rec != nil {
		rj := &recurrenceJSON{Freq: rec.Freq, Interval: rec.Interval, ByDay: rec.ByDay}
		if rj.ByDay == nil {
			rj.ByDay = []string{}
		}
		if rec.Count > 0 {
			n := rec.Count
			rj.Count = &n
		}
		if rec.Until != nil {
			u := formatTime(*rec.Until)
			rj.Until = &u
		}
		out.Recurrence = rj
	}
	return out
}

func (a *API) listOccurrences(w http.ResponseWriter, r *http.Request) {
	start, err := domain.ParseEventTime("start", r.URL.Query().Get("start"), false)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	end, err := domain.ParseEventTime("end", r.URL.Query().Get("end"), false)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !end.After(start) {
		a.fail(w, r, &domain.FieldError{Field: "end", Reason: "debe ser posterior al inicio"})
		return
	}
	if end.Sub(start) > time.Duration(a.lim.MaxWindowDays)*24*time.Hour {
		apiresponse.ErrWithDetails(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "la ventana supera el maximo de dias",
			map[string]string{"field": "end", "max_days": strconv.Itoa(a.lim.MaxWindowDays)})
		return
	}
	occs, err := a.uc.EventOccurrences(r.Context(), principalOf(r), start, end, a.lim.MaxOccurrences)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]occurrenceJSON, len(occs))
	for i, o := range occs {
		out[i] = occurrenceJSON{ID: o.ID, Start: formatTime(o.Start), End: formatTime(o.End), AllDay: o.AllDay,
			Title: o.Title, Location: o.Location, Recurring: o.Recurring, RecurrenceID: formatTime(o.RecurrenceID)}
	}
	apiresponse.JSON(w, http.StatusOK, out)
}

func (a *API) getEvent(w http.ResponseWriter, r *http.Request) {
	v, err := a.uc.EventByID(r.Context(), principalOf(r), chi.URLParam(r, "id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	setETag(w, v.ETag)
	apiresponse.JSON(w, http.StatusOK, eventOut(v))
}

func (a *API) readEvent(w http.ResponseWriter, r *http.Request) (domain.EventFields, bool) {
	var in eventInput
	if !a.decode(w, r, &in) {
		return domain.EventFields{}, false
	}
	f, err := in.fields()
	if err != nil {
		a.fail(w, r, err)
		return domain.EventFields{}, false
	}
	return f, true
}

func (a *API) createEvent(w http.ResponseWriter, r *http.Request) {
	f, ok := a.readEvent(w, r)
	if !ok {
		return
	}
	v, err := a.uc.CreateEvent(r.Context(), principalOf(r), f)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	setETag(w, v.ETag)
	apiresponse.JSON(w, http.StatusCreated, eventOut(v))
}

func (a *API) updateEvent(w http.ResponseWriter, r *http.Request) {
	f, ok := a.readEvent(w, r)
	if !ok {
		return
	}
	v, err := a.uc.UpdateEvent(r.Context(), principalOf(r), chi.URLParam(r, "id"), f, ifMatch(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	setETag(w, v.ETag)
	apiresponse.JSON(w, http.StatusOK, eventOut(v))
}

func (a *API) deleteEvent(w http.ResponseWriter, r *http.Request) {
	if err := a.uc.DeleteEventByID(r.Context(), principalOf(r), chi.URLParam(r, "id"), ifMatch(r)); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

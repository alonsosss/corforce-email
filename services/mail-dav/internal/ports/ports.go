package ports

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
)

// Authenticator verifica una credencial de buzon contra mail-auth (service "dav"). Todo rechazo es
// domain.ErrInvalidCredentials, sin distinguir la causa; un fallo del verificador es
// domain.ErrUnavailable.
type Authenticator interface {
	Authenticate(ctx context.Context, username, password, remoteIP string) (domain.Principal, error)
}

// TenantBinder deja en el contexto lo que hace falta para trabajar en nombre del buzon: la base de su
// empresa y su identidad, que es lo que leen las politicas de fila.
type TenantBinder interface {
	Bind(ctx context.Context, p domain.Principal) (context.Context, error)
}

// Store es el almacen de libretas y contactos. Toda operacion se acota al buzon del Principal: un
// recurso de otro buzon o de otra empresa no existe para quien pregunta (domain.ErrNotFound).
type Store interface {
	ListAddressbooks(ctx context.Context, p domain.Principal) ([]domain.Addressbook, error)
	GetAddressbook(ctx context.Context, p domain.Principal, slug string) (domain.Addressbook, error)
	// CreateAddressbook devuelve domain.ErrAlreadyExists si el buzon ya tiene una con ese slug y
	// domain.ErrAddressbookLimit si alcanzo maxBooks.
	CreateAddressbook(ctx context.Context, p domain.Principal, book domain.Addressbook, maxBooks int) (domain.Addressbook, error)
	// DeleteAddressbook borra la libreta con sus contactos.
	DeleteAddressbook(ctx context.Context, p domain.Principal, slug string) error
	// DeleteMailboxData borra todas las libretas del buzon con sus contactos y su registro de cambios y
	// devuelve cuantas libretas borro. Un buzon sin datos no es un error.
	DeleteMailboxData(ctx context.Context, p domain.Principal) (int, error)

	// ListContacts devuelve la libreta y todos sus contactos; la libreta se lee primero, de modo que
	// su ctag nunca es posterior a lo listado. Sin opt.WithData los contactos llevan solo metadatos y
	// tamano; con datos, domain.ErrResultTooLarge si superan opt.MaxBytes.
	ListContacts(ctx context.Context, p domain.Principal, slug string, opt domain.ReadOptions) (domain.Addressbook, []domain.Contact, error)
	// EachContact recorre los contactos con sus vCard, en orden, sin acumularlos: fn devuelve false para
	// parar y un error para abortar. Devuelve la libreta, leida antes que los contactos.
	EachContact(ctx context.Context, p domain.Principal, slug string, fn func(domain.Contact) (bool, error)) (domain.Addressbook, error)
	GetContact(ctx context.Context, p domain.Principal, slug, resource string) (domain.Contact, error)
	// GetContacts devuelve solo los recursos que existen, en cualquier orden.
	GetContacts(ctx context.Context, p domain.Principal, slug string, resources []string, opt domain.ReadOptions) ([]domain.Contact, error)
	// PutContact crea o reemplaza el contacto de c.ResourceName. cond se evalua contra el etag actual
	// con la libreta bloqueada. Errores: domain.ErrPreconditionFailed, *domain.UIDConflictError,
	// domain.ErrContactLimit, domain.ErrStorageLimit y domain.ErrNotFound (libreta inexistente).
	PutContact(ctx context.Context, p domain.Principal, slug string, c domain.Contact, cond domain.Precondition, lim domain.WriteLimits) (created bool, err error)
	DeleteContact(ctx context.Context, p domain.Principal, slug, resource string, cond domain.Precondition, maxChanges int) error
	// ChangesSince devuelve, ademas de la libreta, los contactos que existen y cambiaron despues de
	// seq y los nombres de los borrados. domain.ErrInvalidSyncToken si seq ya no se puede resolver.
	ChangesSince(ctx context.Context, p domain.Principal, slug string, seq int64, opt domain.ReadOptions) (book domain.Addressbook, changed []domain.Contact, removed []string, err error)
}

// CalendarStore es el almacen de calendarios y eventos. Como Store, toda operacion se acota al buzon del
// Principal: un recurso de otro buzon o de otra empresa no existe para quien pregunta (domain.ErrNotFound).
type CalendarStore interface {
	ListCalendars(ctx context.Context, p domain.Principal) ([]domain.Calendar, error)
	GetCalendar(ctx context.Context, p domain.Principal, slug string) (domain.Calendar, error)
	// CreateCalendar devuelve domain.ErrAlreadyExists si el buzon ya tiene uno con ese slug y
	// domain.ErrCalendarLimit si alcanzo maxCalendars.
	CreateCalendar(ctx context.Context, p domain.Principal, cal domain.Calendar, maxCalendars int) (domain.Calendar, error)
	// DeleteCalendar borra el calendario con sus eventos.
	DeleteCalendar(ctx context.Context, p domain.Principal, slug string) error
	// DeleteMailboxCalendars borra todos los calendarios del buzon con sus eventos y su registro de cambios
	// y devuelve cuantos borro. Un buzon sin datos no es un error.
	DeleteMailboxCalendars(ctx context.Context, p domain.Principal) (int, error)

	// ListEvents devuelve el calendario y todos sus eventos (opt como en ListContacts); el calendario se lee
	// primero, de modo que su ctag nunca es posterior a lo listado.
	ListEvents(ctx context.Context, p domain.Principal, slug string, opt domain.ReadOptions) (domain.Calendar, []domain.Event, error)
	// EachEvent recorre, con su iCalendar y sin acumularlos, los eventos que pueden tener una aparicion
	// dentro de la ventana (todos con una ventana vacia). Como EachContact en lo demas.
	EachEvent(ctx context.Context, p domain.Principal, slug string, window domain.EventWindow, fn func(domain.Event) (bool, error)) (domain.Calendar, error)
	GetEvent(ctx context.Context, p domain.Principal, slug, resource string) (domain.Event, error)
	// GetEvents devuelve solo los recursos que existen, en cualquier orden.
	GetEvents(ctx context.Context, p domain.Principal, slug string, resources []string, opt domain.ReadOptions) ([]domain.Event, error)
	// PutEvent crea o reemplaza el evento de e.ResourceName. cond se evalua contra el etag actual con el
	// calendario bloqueado. Errores: domain.ErrPreconditionFailed, *domain.UIDConflictError,
	// domain.ErrEventLimit, domain.ErrStorageLimit y domain.ErrNotFound (calendario inexistente).
	PutEvent(ctx context.Context, p domain.Principal, slug string, e domain.Event, cond domain.Precondition, lim domain.WriteLimits) (created bool, err error)
	DeleteEvent(ctx context.Context, p domain.Principal, slug, resource string, cond domain.Precondition, maxChanges int) error
	// EventChangesSince devuelve, ademas del calendario, los eventos que existen y cambiaron despues de seq
	// y los nombres de los borrados. domain.ErrInvalidSyncToken si seq ya no se puede resolver.
	EventChangesSince(ctx context.Context, p domain.Principal, slug string, seq int64, opt domain.ReadOptions) (cal domain.Calendar, changed []domain.Event, removed []string, err error)
}

// MailboxIndex enumera de que buzones guarda datos la empresa, sin verlos: es lo que la conciliacion de
// buzones borrados necesita y las politicas de fila por buzon no dejan hacer con la identidad de una peticion.
type MailboxIndex interface {
	// StaleMailboxIDs devuelve hasta limit ids de buzon con libretas o calendarios, en orden ascendente y
	// mayores que after, cuyo elemento mas antiguo es anterior a before.
	StaleMailboxIDs(ctx context.Context, tenantID uuid.UUID, before time.Time, after uuid.UUID, limit int) ([]uuid.UUID, error)
}

// SchedulingStore es la planificacion sobre los calendarios (migracion 05_scheduling.sql): la ocupacion
// materializada de los eventos, la direccion de cada buzon y las paginas de citas. Lo que cruza buzones de la
// misma empresa (resolver direcciones, leer ocupacion, encontrar el dueno de una pagina) solo devuelve ids e
// intervalos; lo demas se acota al buzon del Principal como CalendarStore.
type SchedulingStore interface {
	// ReplaceBusy materializa de nuevo la ocupacion de un evento si su etag sigue siendo etag.
	ReplaceBusy(ctx context.Context, p domain.Principal, eventID uuid.UUID, etag string, busy []domain.Interval, until *time.Time) (bool, error)
	// EventsByID lee con su iCalendar los eventos del buzon con esos ids, de cualquiera de sus calendarios.
	EventsByID(ctx context.Context, p domain.Principal, ids []uuid.UUID) ([]domain.Event, error)
	// EventByUID lee el evento del calendario con ese UID; domain.ErrNotFound si no esta.
	EventByUID(ctx context.Context, p domain.Principal, slug, uid string) (domain.Event, error)
	// RegisterAddress anota la direccion del buzon del Principal.
	RegisterAddress(ctx context.Context, p domain.Principal, address string) error
	// ResolveMailboxes devuelve el buzon de la empresa de cada direccion registrada (hasta 50).
	ResolveMailboxes(ctx context.Context, p domain.Principal, addresses []string) (map[string]uuid.UUID, error)
	// BusyIntervals devuelve la ocupacion materializada de esos buzones en [from, to): inicio y fin.
	BusyIntervals(ctx context.Context, p domain.Principal, mailboxes []uuid.UUID, from, to time.Time) (map[uuid.UUID][]domain.Interval, error)
	// PendingBusy devuelve, por buzon, hasta limit eventos cuya ocupacion no esta materializada hasta until.
	PendingBusy(ctx context.Context, p domain.Principal, mailboxes []uuid.UUID, until time.Time, limit int) (map[uuid.UUID][]uuid.UUID, error)
	// BookingPage lee la pagina de citas del buzon; domain.ErrNotFound si no tiene.
	BookingPage(ctx context.Context, p domain.Principal) (domain.BookingPage, error)
	SaveBookingPage(ctx context.Context, p domain.Principal, page domain.BookingPage) (domain.BookingPage, error)
	// BookingPageOwner devuelve el buzon dueno de la pagina activa de la empresa de p con ese enlace, o uuid.Nil.
	BookingPageOwner(ctx context.Context, p domain.Principal, publicID string) (uuid.UUID, error)
	// ReserveBooking guarda la cita y su registro en una transaccion con el cerrojo del buzon. Errores:
	// domain.ErrBookingLimit, domain.ErrSlotUnavailable y los de PutEvent.
	ReserveBooking(ctx context.Context, p domain.Principal, slug string, e domain.Event, b domain.Booking, lim domain.BookingLimits) error
	// DeleteMailboxScheduling retira la direccion y la pagina de citas de un buzon dado de baja.
	DeleteMailboxScheduling(ctx context.Context, p domain.Principal) error
}

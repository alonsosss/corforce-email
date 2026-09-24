package ports

import (
	"context"
	"io"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// Authenticator verifica la credencial de un buzon contra mail-auth.
type Authenticator interface {
	// Verify devuelve la identidad del buzon si la credencial abre el webmail. Todo
	// rechazo es domain.ErrInvalidCredentials, sin distinguir la causa; un fallo del
	// verificador es domain.ErrUnavailable.
	Verify(ctx context.Context, username, password, remoteIP string) (domain.Identity, error)
}

// SessionStore guarda las sesiones del webmail. La clave es el hash del token: el token
// en claro solo existe en la cookie del navegador.
type SessionStore interface {
	Create(ctx context.Context, key string, s domain.Session, ttl time.Duration) error
	// Get devuelve domain.ErrSessionInvalid si la sesion no existe.
	Get(ctx context.Context, key string) (domain.Session, error)
	// Touch renueva la inactividad de una sesion existente.
	Touch(ctx context.Context, key string, ttl time.Duration) error
	Delete(ctx context.Context, key, username string) error
	// Revoke invalida todas las sesiones del buzon abiertas hasta at.
	Revoke(ctx context.Context, username string, at time.Time) error
	// RevokedAt es la ultima revocacion vigente del buzon (cero si no hay).
	RevokedAt(ctx context.Context, username string) (time.Time, error)
}

// MailStore abre el buzon en el servidor IMAP de la celda.
type MailStore interface {
	// Open devuelve una conexion autenticada como el buzon; la cierra quien la abre.
	Open(ctx context.Context, username string) (Mailbox, error)
}

// Mailbox es una conexion IMAP abierta sobre un buzon.
type Mailbox interface {
	Close() error
	// Folders lista las carpetas; withCounts pide totales y no leidos.
	Folders(ctx context.Context, withCounts bool) ([]domain.Folder, error)
	// Quota devuelve nil si el servidor no informa de cuota.
	Quota(ctx context.Context) (*domain.Quota, error)
	List(ctx context.Context, folder string, q domain.ListQuery) (domain.MessagePage, error)
	Read(ctx context.Context, folder string, uid uint32, opts domain.ReadOptions) (*domain.RawMessage, error)
	// OpenPart devuelve la parte decodificada. El lector es valido hasta que se cierra y
	// falla con domain.ErrPartTooLarge si la parte supera maxBytes.
	OpenPart(ctx context.Context, folder string, uid uint32, partID string, maxBytes int64) (domain.Part, io.ReadCloser, error)
	ReplyReference(ctx context.Context, folder string, uid uint32) (domain.ReplyReference, error)
	// SetFlags, Move y Expunge actuan sobre los UIDs que existen en la carpeta y devuelven cuantos
	// eran: un UID que no existe no cuenta y no es un error. Expunge borra de forma definitiva.
	SetFlags(ctx context.Context, folder string, uids []uint32, change domain.FlagChange) (int, error)
	Move(ctx context.Context, folder string, uids []uint32, dest string) (int, error)
	Expunge(ctx context.Context, folder string, uids []uint32) (int, error)
	// Empty borra de forma definitiva todos los mensajes de la carpeta y devuelve cuantos eran.
	Empty(ctx context.Context, folder string) (int, error)
	// Append guarda un mensaje y devuelve su referencia (UID 0 si el servidor no la informa).
	Append(ctx context.Context, folder string, raw []byte, flags []domain.Flag, date time.Time) (domain.AppendedMessage, error)
	// Stat identifica el mensaje sin leerlo: UIDVALIDITY de la carpeta, Message-ID y tamano. Un
	// UID que no existe es domain.ErrMessageNotFound.
	Stat(ctx context.Context, folder string, uid uint32) (domain.StoredMessage, error)
	// OpenRaw devuelve el mensaje entero tal como esta guardado. El lector es valido hasta que se
	// cierra; un mensaje mayor que maxBytes es domain.ErrMessageTooLarge.
	OpenRaw(ctx context.Context, folder string, uid uint32, maxBytes int64) (domain.StoredMessage, io.ReadCloser, error)
	// FindByMessageID busca en la carpeta el mensaje con esa cabecera Message-ID (sin corchetes) y
	// devuelve su UID, 0 si no esta.
	FindByMessageID(ctx context.Context, folder, messageID string) (uint32, error)
	// CreateFolder crea la carpeta y la suscribe. Una que ya existe es domain.ErrFolderExists.
	CreateFolder(ctx context.Context, name string) error
	// RenameFolder la renombra (con sus subcarpetas) y traslada la suscripcion.
	RenameFolder(ctx context.Context, name, newName string) error
	// DeleteFolder la borra con sus mensajes y retira la suscripcion.
	DeleteFolder(ctx context.Context, name string) error
	// ListThreads agrupa en conversaciones los mensajes de la carpeta que casan con q y devuelve la
	// pagina pedida, de la conversacion con actividad mas reciente a la mas antigua.
	ListThreads(ctx context.Context, folder string, q domain.ListQuery) (domain.ThreadPage, error)
	// Conversation devuelve los mensajes de la carpeta de la conversacion del UID, del mas reciente
	// al mas antiguo y como mucho max. Un UID que no existe es domain.ErrMessageNotFound.
	Conversation(ctx context.Context, folder string, uid uint32, max int) ([]domain.ConversationMessage, error)
	// Related busca en la carpeta los mensajes que son alguno de messageIDs o responden a uno de
	// ellos, del mas reciente al mas antiguo y como mucho max.
	Related(ctx context.Context, folder string, messageIDs []string, max int) ([]domain.ConversationMessage, error)
	// Insight lee el remitente y las cabeceras de la ficha sin marcar el mensaje como leido.
	Insight(ctx context.Context, folder string, uid uint32) (domain.InsightSource, error)
	// MoveTracked mueve un mensaje y devuelve su referencia en la carpeta de destino (COPYUID); UID 0
	// si el servidor no la informa. Un UID que no existe es domain.ErrMessageNotFound.
	MoveTracked(ctx context.Context, folder string, uid uint32, dest string) (domain.AppendedMessage, error)
	// HasReply dice si algun mensaje de la carpeta cita ese Message-ID (sin corchetes) en In-Reply-To o
	// References.
	HasReply(ctx context.Context, folder, messageID string) (bool, error)
}

// Unsubscriber hace la baja en un clic de RFC 8058: un POST a la URL https que declara el boletin.
// Solo conecta con direcciones publicas, comprobadas en cada conexion. Una URL o un destino que no
// admite es domain.ErrUnsubscribeRefused; una respuesta que no confirma la baja o un fallo de red,
// domain.ErrUnsubscribeFailed.
type Unsubscriber interface {
	OneClick(ctx context.Context, target string) error
}

// Sender entrega un mensaje por el submission de la celda autenticado como el buzon, de
// modo que Postfix aplique smtpd_sender_login_maps al remitente.
type Sender interface {
	Send(ctx context.Context, username, envelopeFrom string, recipients []string, raw []byte) error
}

// SenderDirectory dice con que direcciones concretas puede enviar un buzon segun el
// directorio de la celda, con la misma regla que aplica Postfix (smtpd_sender_login_maps).
// Un fallo es domain.ErrUnavailable.
type SenderDirectory interface {
	SenderIdentities(ctx context.Context, username string) ([]string, error)
}

// VacationDirectory lee y cambia la respuesta automatica del buzon en mail-directory, que es el
// dueno de la regla y de la validacion: el webmail no la copia. Un texto que el directorio rechaza es
// un *domain.ValidationError; cualquier otro fallo, domain.ErrUnavailable.
type VacationDirectory interface {
	Vacation(ctx context.Context, username string) (domain.Vacation, error)
	SetVacation(ctx context.Context, username string, in domain.VacationInput) (domain.Vacation, error)
}

// MailboxWatcher avisa de los cambios de la bandeja de entrada de un buzon. Comparte una sola conexion de
// vigilancia por buzon entre todos los que la piden, y limita cuantas admite por buzon y en total.
type MailboxWatcher interface {
	// Watch se suscribe. El canal emite un aviso por cambio (los que llegan juntos se funden en uno) y se
	// cierra al cancelar ctx. Devuelve domain.ErrTooManyStreams si se supera un tope.
	Watch(ctx context.Context, username string) (<-chan domain.MailboxChange, error)
}

// AddressBook busca en el directorio de correo de la empresa de quien pregunta (los buzones activos
// de su empresa). La empresa la resuelve mail-directory a partir del buzon; el webmail no la conoce.
// Un texto que el directorio rechaza es un *domain.ValidationError; cualquier otro fallo,
// domain.ErrUnavailable.
type AddressBook interface {
	Search(ctx context.Context, username, query string, limit int) ([]domain.AddressBookEntry, error)
}

// SendLedger recuerda cada envio por su clave de idempotencia para que un reintento del
// cliente nunca entregue el mensaje dos veces.
type SendLedger interface {
	// Reserve guarda rec si la clave no tenia registro (reserved true; current lleva el Token
	// nuevo). Si ya lo tenia, lo devuelve sin tocarlo.
	Reserve(ctx context.Context, key string, rec domain.SendRecord, ttl time.Duration) (current domain.SendRecord, reserved bool, err error)
	// Update sobrescribe el registro si sigue siendo de rec.Token; false si ya no lo es.
	Update(ctx context.Context, key string, rec domain.SendRecord, ttl time.Duration) (bool, error)
	// Release borra el registro si sigue siendo de token: el mensaje no salio y la clave
	// queda libre para reintentar.
	Release(ctx context.Context, key, token string) error
}

// Composer arma el mensaje RFC 5322. includeBcc solo para la copia que se guarda: el Bcc
// nunca viaja en el mensaje que se entrega.
type Composer interface {
	Compose(msg domain.Outgoing, includeBcc bool) ([]byte, error)
	// Finalize prepara para salir un mensaje ya compuesto y guardado (envio programado): fija la
	// fecha de envio, quita el Bcc de la version que viaja y saca el sobre de sus cabeceras. Un
	// mensaje ilegible o sin remitente es un *domain.ValidationError.
	Finalize(stored []byte, date time.Time) (domain.FinalizedMessage, error)
}

// HTMLSanitizer sanea el HTML de terceros y el que redacta el usuario.
type HTMLSanitizer interface {
	Incoming(html string, opts domain.SanitizeOptions) domain.SanitizedHTML
	// Outgoing devuelve el HTML saneado y su version en texto plano.
	Outgoing(html string) (clean, plain string)
}

// VirusScanner analiza un adjunto antes de guardarlo o enviarlo. Devuelve
// domain.ErrAttachmentInfected o domain.ErrScanUnavailable.
type VirusScanner interface {
	Scan(ctx context.Context, name string, data []byte) error
}

// PartURL construye la URL con la que el cliente pide una parte (imagenes cid:).
type PartURL func(folder string, uid uint32, partID string) string

// SignatureDirectory lee y guarda la firma del buzon en mail-directory. Un valor que el directorio
// rechaza es un *domain.ValidationError; cualquier otro fallo, domain.ErrUnavailable.
type SignatureDirectory interface {
	Signature(ctx context.Context, username string) (domain.Signature, error)
	SetSignature(ctx context.Context, username string, in domain.SignatureInput) (domain.Signature, error)
}

// FilterDirectory lee y reemplaza las reglas y el reenvio del buzon en mail-directory, que las
// valida y genera el script Sieve. Errores como SignatureDirectory.
type FilterDirectory interface {
	Filters(ctx context.Context, username string) (domain.MailFilters, error)
	SetFilters(ctx context.Context, username string, in domain.MailFiltersInput) (domain.MailFilters, error)
}

// PasswordDirectory cambia la contrasena del buzon con la politica y el evento del directorio.
// Una contrasena que la politica rechaza es un *domain.ValidationError.
type PasswordDirectory interface {
	SetPassword(ctx context.Context, username, password string) error
}

// ScheduledDirectory es el indice durable de los envios programados de la celda (mail-directory).
// Una fila que no existe o no es del buzon es domain.ErrScheduledNotFound; una que ya no esta
// pendiente, domain.ErrScheduledNotPending; cualquier otro fallo, domain.ErrUnavailable.
type ScheduledDirectory interface {
	CreateScheduled(ctx context.Context, in domain.NewScheduledSend) (string, error)
	ListScheduled(ctx context.Context, username string) ([]domain.ScheduledSend, error)
	RescheduleScheduled(ctx context.Context, username, id string, at time.Time) (domain.ScheduledSend, error)
	CancelScheduled(ctx context.Context, username, id string) error
	// ClaimScheduled reclama hasta limit filas vencidas de toda la celda con un arriendo de lease.
	ClaimScheduled(ctx context.Context, limit int, lease time.Duration) ([]domain.ScheduledClaim, error)
	FinishScheduled(ctx context.Context, id string, outcome domain.ScheduledOutcome) error
}

// ReminderDirectory es el indice durable de los recordatorios de la celda (mail-directory): posponer y
// seguimiento. Una fila que no existe o no es del buzon es domain.ErrReminderNotFound; una que ya no esta
// pendiente, domain.ErrReminderNotPending; un mismo mensaje con otro recordatorio activo del mismo tipo,
// domain.ErrReminderExists; cualquier otro fallo, domain.ErrUnavailable.
type ReminderDirectory interface {
	CreateReminder(ctx context.Context, in domain.NewReminder) (domain.Reminder, error)
	ListReminders(ctx context.Context, username string, kind domain.ReminderKind) ([]domain.Reminder, error)
	RescheduleReminder(ctx context.Context, username, id string, at time.Time) (domain.Reminder, error)
	CancelReminder(ctx context.Context, username, id string) error
	// ClaimReminders reclama hasta limit recordatorios vencidos de toda la celda con un arriendo de lease.
	ClaimReminders(ctx context.Context, limit int, lease time.Duration) ([]domain.ReminderClaim, error)
	FinishReminder(ctx context.Context, id string, outcome domain.ReminderOutcome) error
}

// QuickReplyDirectory guarda las respuestas rapidas del buzon en mail-directory, que aplica sus topes.
// Un valor que rechaza es un *domain.ValidationError; un nombre repetido, domain.ErrQuickReplyExists;
// el tope, domain.ErrQuickReplyLimit; una que no es del buzon, domain.ErrQuickReplyNotFound.
type QuickReplyDirectory interface {
	QuickReplies(ctx context.Context, username string) (domain.QuickReplyList, error)
	CreateQuickReply(ctx context.Context, username string, q domain.QuickReply) (domain.QuickReply, error)
	UpdateQuickReply(ctx context.Context, username string, q domain.QuickReply) (domain.QuickReply, error)
	DeleteQuickReply(ctx context.Context, username, id string) error
}

// ContactBook es la libreta personal del buzon en mail-dav (la misma que sirve CardDAV). Todo lo que
// mail-dav rechaza es un *domain.ServiceRejection con su codigo y sus detalles (un id que no existe,
// un If-Match que ya no casa, un dato invalido, una cuota, un cupo); un fallo de la llamada es
// domain.ErrUnavailable.
type ContactBook interface {
	ListContacts(ctx context.Context, mb domain.MailboxRef, q domain.ContactQuery) (domain.ContactPage, error)
	Contact(ctx context.Context, mb domain.MailboxRef, id string) (domain.Contact, error)
	CreateContact(ctx context.Context, mb domain.MailboxRef, in domain.ContactInput) (domain.Contact, error)
	UpdateContact(ctx context.Context, mb domain.MailboxRef, id string, in domain.ContactInput, ifMatch string) (domain.Contact, error)
	DeleteContact(ctx context.Context, mb domain.MailboxRef, id string) error
	// ExportContacts entrega todas las tarjetas en text/vcard; el lector lo cierra quien lo pide.
	ExportContacts(ctx context.Context, mb domain.MailboxRef) (io.ReadCloser, error)
	ImportContacts(ctx context.Context, mb domain.MailboxRef, filename string, data []byte) (domain.ImportResult, error)
	// Limits son los topes de la libreta y del calendario que sirve mail-dav, por nombre.
	Limits(ctx context.Context) (map[string]int64, error)
}

// Calendar es el calendario personal del buzon en mail-dav (el mismo que sirve CalDAV). Errores
// como ContactBook.
type Calendar interface {
	Occurrences(ctx context.Context, mb domain.MailboxRef, w domain.EventWindow) ([]domain.Occurrence, error)
	Event(ctx context.Context, mb domain.MailboxRef, id string) (domain.Event, error)
	CreateEvent(ctx context.Context, mb domain.MailboxRef, in domain.EventInput) (domain.Event, error)
	UpdateEvent(ctx context.Context, mb domain.MailboxRef, id string, in domain.EventInput, ifMatch string) (domain.Event, error)
	DeleteEvent(ctx context.Context, mb domain.MailboxRef, id string) error
}

// Scheduling es la planificacion de mail-dav sobre el calendario del buzon (docs/adr/0004): una aparicion de una
// serie, las invitaciones iTIP, la disponibilidad del equipo y la pagina de citas. Errores como ContactBook.
type Scheduling interface {
	UpdateOccurrence(ctx context.Context, mb domain.MailboxRef, id, recurrenceID string, in domain.EventInput, ifMatch string) (domain.Event, error)
	DeleteOccurrence(ctx context.Context, mb domain.MailboxRef, id, recurrenceID, ifMatch string) (domain.Event, error)
	// EventInvitation escribe la invitacion (REQUEST o CANCEL) de un evento del buzon.
	EventInvitation(ctx context.Context, mb domain.MailboxRef, id, method string) (domain.ITIPMessage, error)
	InspectInvitation(ctx context.Context, mb domain.MailboxRef, ical string, addresses []string) (domain.Invitation, error)
	RespondInvitation(ctx context.Context, mb domain.MailboxRef, ical string, addresses []string, response string) (domain.InvitationAnswer, error)
	ApplyInvitation(ctx context.Context, mb domain.MailboxRef, ical, from string, addresses []string) (domain.InvitationApplied, error)
	Availability(ctx context.Context, mb domain.MailboxRef, addresses []string, w domain.EventWindow) ([]domain.MailboxAvailability, error)
	BookingSettings(ctx context.Context, mb domain.MailboxRef) (domain.BookingPage, error)
	SaveBookingSettings(ctx context.Context, mb domain.MailboxRef, in domain.BookingSettings, ownerName string, regenerate bool) (domain.BookingPage, error)
	// PublicBooking y Book no llevan buzon: son la pagina publica de una empresa.
	PublicBooking(ctx context.Context, tenantID, publicID string, w domain.EventWindow) (domain.PublicBookingPage, error)
	Book(ctx context.Context, tenantID, publicID string, in domain.BookingRequest) (domain.BookingConfirmation, error)
}

// InvitationComposer arma el correo iMIP (RFC 6047) de una invitacion, una respuesta o una cancelacion: texto
// para personas y el iCalendar con su METHOD en una parte text/calendar.
type InvitationComposer interface {
	ComposeInvitation(msg domain.InvitationMail) ([]byte, error)
}

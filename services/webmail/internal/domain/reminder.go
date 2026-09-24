package domain

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// SnoozedFolderName es la carpeta donde espera un mensaje pospuesto. Como Scheduled, se reconoce siempre
// por el nombre y el webmail la crea al posponer el primer mensaje del buzon.
const SnoozedFolderName = "Snoozed"

// IsSnoozedFolderName dice si name es la carpeta de pospuestos.
func IsSnoozedFolderName(name string) bool {
	return strings.EqualFold(name, SnoozedFolderName)
}

// ReminderKind es el tipo de un recordatorio en mail-directory.
type ReminderKind string

const (
	ReminderSnooze   ReminderKind = "snooze"
	ReminderFollowUp ReminderKind = "follow_up"
)

// ReminderStatus es el estado de un recordatorio en mail-directory.
type ReminderStatus string

const (
	ReminderPending  ReminderStatus = "pending"
	ReminderRunning  ReminderStatus = "running"
	ReminderDone     ReminderStatus = "done"
	ReminderFailed   ReminderStatus = "failed"
	ReminderCanceled ReminderStatus = "canceled"
)

// ReminderResult es como termino un recordatorio hecho.
type ReminderResult string

const (
	// ReminderReturned: el pospuesto volvio a su carpeta sin leer.
	ReminderReturned ReminderResult = "returned"
	// ReminderMissing: el mensaje ya no estaba donde se dejo (el usuario lo movio o lo borro); no se toco.
	ReminderMissing ReminderResult = "missing"
	// ReminderReplied: el seguimiento encontro una respuesta y se cerro en silencio.
	ReminderReplied ReminderResult = "replied"
	// ReminderReminded: no habia respuesta y el enviado se copio a INBOX con \Flagged y sin \Seen.
	ReminderReminded ReminderResult = "reminded"
)

// Reminder es un recordatorio del buzon. El mensaje vive en IMAP: Folder, UIDValidity y UID lo
// localizan (cero en un seguimiento de un envio programado, que se busca en Enviados por su
// Message-ID). ReturnFolder es la carpeta a la que vuelve un pospuesto.
type Reminder struct {
	ID           string
	Kind         ReminderKind
	MessageID    string
	Folder       string
	UIDValidity  uint32
	UID          uint32
	ReturnFolder string
	Subject      string
	Addresses    []string
	DueAt        time.Time
	Status       ReminderStatus
	CreatedAt    time.Time
}

// NewReminder es la fila que se registra al posponer un mensaje o pedir seguimiento de uno enviado.
type NewReminder struct {
	Username     string
	Kind         ReminderKind
	MessageID    string
	Folder       string
	UIDValidity  uint32
	UID          uint32
	ReturnFolder string
	DueAt        time.Time
	Subject      string
	Addresses    []string
}

// ReminderClaim es un recordatorio vencido que el trabajador reclamo con arriendo.
type ReminderClaim struct {
	Reminder
	Username string
}

// ReminderOutcome es como termina el intento de un recordatorio. Result solo con Done; Retry solo con
// Failed: el fallo fue de infraestructura y mail-directory lo reprograma con espera creciente.
type ReminderOutcome struct {
	Status ReminderStatus
	Result ReminderResult
	Error  string
	Retry  bool
}

// MaxReminderAddresses es el tope de direcciones que mail-directory guarda con un recordatorio: solo se
// muestran, asi que las que sobran se descartan en vez de rechazar el recordatorio.
const MaxReminderAddresses = 100

// ValidateReminderAt exige una hora entre MinScheduleLead y maxDays dias desde now, como un envio
// programado.
func ValidateReminderAt(field string, at, now time.Time, maxDays int) error {
	if at.IsZero() {
		return invalid(field, "es obligatoria")
	}
	if at.Before(now.Add(MinScheduleLead)) {
		return invalid(field, "debe ser al menos un minuto en el futuro")
	}
	if at.After(now.AddDate(0, 0, maxDays)) {
		return invalid(field, "supera el plazo maximo de un recordatorio")
	}
	return nil
}

// ValidateFollowUpDays exige un plazo de seguimiento entero entre 1 y maxDays dias.
func ValidateFollowUpDays(days, maxDays int) error {
	if days < 1 || days > maxDays {
		return invalid("follow_up_days", "debe estar entre 1 y el plazo maximo de un recordatorio")
	}
	return nil
}

// ValidateReminderID exige el identificador de una fila (UUID): viaja en la ruta interna.
func ValidateReminderID(id string) error {
	if !ValidUUID(id) {
		return invalid("id", "identificador de recordatorio invalido")
	}
	return nil
}

// CheckSnoozable rechaza posponer desde las carpetas donde no tiene sentido: la de pospuestos (se cambia
// la hora) y la de envios programados (el mensaje saldria de ella y no se enviaria).
func CheckSnoozable(f Folder) error {
	if f.Role == RoleSnoozed || f.Role == RoleScheduled {
		return invalid("folder", "los mensajes de esta carpeta no se posponen")
	}
	return nil
}

// NormalizeUIDs exige de 1 a MaxBatchUIDs UIDs positivos y quita los repetidos.
func NormalizeUIDs(uids []uint32) ([]uint32, error) {
	if len(uids) == 0 {
		return nil, invalid("uids", "hace falta al menos un mensaje")
	}
	if len(uids) > MaxBatchUIDs {
		return nil, invalid("uids", "demasiados mensajes en una sola accion")
	}
	seen := make(map[uint32]bool, len(uids))
	out := make([]uint32, 0, len(uids))
	for _, uid := range uids {
		if uid == 0 {
			return nil, invalid("uids", "cada UID debe ser un entero positivo")
		}
		if !seen[uid] {
			seen[uid] = true
			out = append(out, uid)
		}
	}
	return out, nil
}

// ReminderAddresses recorta la lista a lo que guarda el directorio.
func ReminderAddresses(in []string) []string {
	out := make([]string, 0, min(len(in), MaxReminderAddresses))
	for _, a := range in {
		if len(out) == MaxReminderAddresses {
			break
		}
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// SnoozeResult es lo que queda de posponer un lote: los pospuestos y los UIDs que no se pudieron
// posponer (ya no estaban o fallo su registro).
type SnoozeResult struct {
	Snoozed []Reminder
	Failed  []uint32
}

var (
	ErrReminderNotFound   = errors.New("recordatorio no encontrado")
	ErrReminderNotPending = errors.New("el recordatorio ya no esta pendiente")
	ErrReminderNotClaimed = errors.New("el recordatorio no estaba reclamado")
	ErrReminderLimit      = errors.New("el buzon alcanzo el maximo de recordatorios activos")
	// ErrReminderExists: el mensaje ya tiene un recordatorio activo de ese tipo.
	ErrReminderExists = errors.New("el mensaje ya tiene un recordatorio activo de ese tipo")
)

// Respuestas rapidas.

// QuickReply es una respuesta rapida del buzon. Las variables ({nombre}, {empresa}, {fecha}...) se
// guardan tal cual: las resuelve la interfaz al insertarla con los datos del destinatario y del buzon.
type QuickReply struct {
	ID        string
	Name      string
	HTML      string
	Text      string
	UpdatedAt time.Time
}

// QuickReplyInput es lo que envia la interfaz: el HTML del editor, que el webmail sanea antes de
// guardarlo y del que saca el texto.
type QuickReplyInput struct {
	Name string
	HTML string
}

// QuickReplyLimits son los topes que sirve mail-directory.
type QuickReplyLimits struct {
	MaxItems     int
	MaxNameChars int
	MaxHTMLBytes int
	MaxTextBytes int
}

// QuickReplyList son las respuestas del buzon con sus topes.
type QuickReplyList struct {
	Items  []QuickReply
	Limits QuickReplyLimits
}

// Validate comprueba lo que el webmail puede comprobar antes de sanear: nombre presente y UTF-8. Los
// topes los aplica mail-directory, dueno del dato.
func (in QuickReplyInput) Validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return invalid("name", "es obligatorio")
	}
	if !utf8.ValidString(in.Name) {
		return invalid("name", "no es UTF-8 valido")
	}
	if !utf8.ValidString(in.HTML) {
		return invalid("html", "no es UTF-8 valido")
	}
	if strings.TrimSpace(in.HTML) == "" {
		return invalid("html", "la respuesta necesita contenido")
	}
	return nil
}

// ValidateQuickReplyID exige el identificador de una respuesta (UUID).
func ValidateQuickReplyID(id string) error {
	if !ValidUUID(id) {
		return invalid("id", "identificador de respuesta rapida invalido")
	}
	return nil
}

var (
	ErrQuickReplyNotFound = errors.New("respuesta rapida no encontrada")
	ErrQuickReplyExists   = errors.New("ya existe una respuesta rapida con ese nombre")
	ErrQuickReplyLimit    = errors.New("el buzon alcanzo el maximo de respuestas rapidas")
)

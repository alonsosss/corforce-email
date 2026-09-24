package domain

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Tipos de recordatorio del webmail. snooze devuelve un mensaje pospuesto a su carpeta; follow_up
// comprueba si alguien respondio a un mensaje enviado.
const (
	ReminderSnooze   = "snooze"
	ReminderFollowUp = "follow_up"
)

// Estados de un recordatorio. pending espera su hora; running esta reclamado por un trabajador con
// arriendo; done, failed y canceled son finales.
const (
	ReminderPending  = "pending"
	ReminderRunning  = "running"
	ReminderDone     = "done"
	ReminderFailed   = "failed"
	ReminderCanceled = "canceled"
)

// Como termino un recordatorio hecho: returned (el pospuesto volvio), missing (el mensaje ya no
// estaba donde se dejo: el usuario lo movio o lo borro, y no se toco nada), replied (el seguimiento
// encontro respuesta) y reminded (no la encontro y dejo el aviso en INBOX).
const (
	ReminderReturned = "returned"
	ReminderMissing  = "missing"
	ReminderReplied  = "replied"
	ReminderReminded = "reminded"
)

const (
	// MaxReminderDays es el techo del directorio para la hora de un recordatorio; el webmail puede
	// fijar uno menor (WEBMAIL_REMINDERS_MAX_DAYS), nunca mayor.
	MaxReminderDays = 366
	// MaxRemindersPerMailbox acota los recordatorios activos de un buzon, de todos los tipos.
	MaxRemindersPerMailbox = 1000
	// MaxReminderAttempts cuenta todas las reclamaciones, incluida la primera.
	MaxReminderAttempts = 5
	// MaxReminderAddresses acota las direcciones que se listan con el recordatorio (el remitente de un
	// pospuesto o los destinatarios de un seguimiento): son solo para mostrarlas.
	MaxReminderAddresses = 100
	// ReminderRetention: las filas terminadas se purgan pasado este tiempo desde su ultimo cambio.
	ReminderRetention = 30 * 24 * time.Hour
	// ReminderLeaseExpiredError es el error de una fila cuyo trabajador no la cerro antes de que
	// vencieran su arriendo y todos sus intentos.
	ReminderLeaseExpiredError = "el recordatorio no se cerro antes de vencer su arriendo"
)

// Reminder es el indice durable de un recordatorio del buzon. El mensaje vive en IMAP; aqui su
// referencia (carpeta, UIDVALIDITY, UID y Message-ID), la hora y lo que la interfaz lista. UID y
// UIDValidity son cero en un seguimiento que aun no tiene copia en Enviados (envio programado).
type Reminder struct {
	ID           uuid.UUID  `json:"id"`
	TenantID     uuid.UUID  `json:"tenant_id"`
	Username     string     `json:"username"`
	Kind         string     `json:"kind"`
	MessageID    string     `json:"message_id"`
	Folder       string     `json:"folder"`
	UIDValidity  uint32     `json:"uid_validity"`
	UID          uint32     `json:"uid"`
	ReturnFolder string     `json:"return_folder"`
	Subject      string     `json:"subject"`
	Addresses    []string   `json:"addresses"`
	DueAt        time.Time  `json:"due_at"`
	Status       string     `json:"status"`
	Result       string     `json:"result"`
	Attempts     int        `json:"attempts"`
	LeaseUntil   *time.Time `json:"lease_until"`
	LastError    string     `json:"last_error"`
	DoneAt       *time.Time `json:"done_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// ValidReminderKind dice si kind es un tipo de recordatorio.
func ValidReminderKind(kind string) bool {
	return kind == ReminderSnooze || kind == ReminderFollowUp
}

// Normalize valida una fila nueva contra la hora now.
func (r *Reminder) Normalize(now time.Time) error {
	if !ValidReminderKind(r.Kind) {
		return fieldErr("kind", "debe ser snooze o follow_up")
	}
	r.MessageID = strings.TrimSpace(r.MessageID)
	if len(r.MessageID) > MaxScheduledMessageID || (r.MessageID != "" && !printableASCII(r.MessageID)) {
		return fieldErr("message_id", "no es un Message-ID valido")
	}
	if !validReminderFolder(r.Folder) {
		return fieldErr("folder", "no es un nombre de carpeta valido")
	}
	if (r.UID == 0) != (r.UIDValidity == 0) {
		return fieldErr("uid", "uid y uid_validity van juntos")
	}
	switch r.Kind {
	case ReminderSnooze:
		if r.UID == 0 {
			return fieldErr("uid", "es obligatorio")
		}
		if !validReminderFolder(r.ReturnFolder) {
			return fieldErr("return_folder", "no es un nombre de carpeta valido")
		}
	case ReminderFollowUp:
		if r.MessageID == "" {
			return fieldErr("message_id", "es obligatorio")
		}
		if r.ReturnFolder != "" {
			return fieldErr("return_folder", "solo aplica a snooze")
		}
	}
	if err := ValidateReminderDue(r.DueAt, now); err != nil {
		return err
	}
	r.Subject = strings.TrimSpace(r.Subject)
	if !utf8.ValidString(r.Subject) || utf8.RuneCountInString(r.Subject) > MaxScheduledSubjectRunes || strings.ContainsFunc(r.Subject, unicode.IsControl) {
		return fieldErr("subject", "tiene caracteres no validos o es demasiado largo")
	}
	if len(r.Addresses) > MaxReminderAddresses {
		return fieldErr("addresses", "supera el maximo de direcciones")
	}
	addresses := make([]string, 0, len(r.Addresses))
	for i, a := range r.Addresses {
		a = strings.TrimSpace(a)
		if !validRecipientShape(a) {
			return fieldErr("addresses["+strconv.Itoa(i)+"]", "no es una direccion de correo")
		}
		addresses = append(addresses, a)
	}
	r.Addresses = addresses
	r.Status = ReminderPending
	r.Result = ""
	r.Attempts = 0
	r.DueAt = r.DueAt.UTC()
	return nil
}

// ValidateReminderDue exige una hora futura (con ScheduledPastTolerance) y dentro de MaxReminderDays.
func ValidateReminderDue(due, now time.Time) error {
	if due.IsZero() || due.Before(now.Add(-ScheduledPastTolerance)) {
		return fieldErr("due_at", "debe ser una hora futura")
	}
	if due.After(now.AddDate(0, 0, MaxReminderDays)) {
		return fieldErr("due_at", "supera el plazo maximo de un recordatorio")
	}
	return nil
}

func validReminderFolder(name string) bool {
	return name != "" && len(name) <= MaxScheduledFolderBytes && utf8.ValidString(name) && !strings.ContainsFunc(name, func(r rune) bool {
		return unicode.IsControl(r) || r == '*' || r == '%'
	})
}

// ReminderOutcome es como cierra el trabajador una fila reclamada.
type ReminderOutcome struct {
	Status string
	Result string
	Error  string
	// Retry pide reprogramar un failed: el fallo fue transitorio (IMAP no disponible).
	Retry bool
}

// ReminderTransition es lo que queda de la fila tras cerrarla: su estado y, si vuelve a pending,
// cuanto espera desde ahora.
type ReminderTransition struct {
	Status     string
	Result     string
	RetryAfter time.Duration
	Error      string
}

// NormalizeReminderOutcome valida el cierre: done lleva siempre su resultado, y el resultado tiene que
// ser de su tipo (lo comprueba Close, que conoce la fila). El error se guarda en una linea y recortado.
func NormalizeReminderOutcome(o ReminderOutcome) (ReminderOutcome, error) {
	switch o.Status {
	case ReminderDone:
		switch o.Result {
		case ReminderReturned, ReminderMissing, ReminderReplied, ReminderReminded:
		default:
			return o, fieldErr("result", "debe ser returned, missing, replied o reminded")
		}
	case ReminderFailed, ReminderCanceled:
		if o.Result != "" {
			return o, fieldErr("result", "solo aplica a done")
		}
	default:
		return o, fieldErr("status", "debe ser done, failed o canceled")
	}
	if o.Retry && o.Status != ReminderFailed {
		return o, fieldErr("retry", "solo aplica a failed")
	}
	o.Error = cleanScheduledError(o.Error)
	return o, nil
}

// Close decide el estado final de una fila reclamada. Un failed con reintento vuelve a pending con la
// misma espera creciente que un envio programado, mientras queden intentos.
func (r *Reminder) Close(o ReminderOutcome) (ReminderTransition, error) {
	if r.Status != ReminderRunning {
		return ReminderTransition{}, ErrReminderNotClaimed
	}
	if o.Status == ReminderDone && !resultFits(r.Kind, o.Result) {
		return ReminderTransition{}, fieldErr("result", "no corresponde al tipo del recordatorio")
	}
	t := ReminderTransition{Status: o.Status, Result: o.Result, Error: o.Error}
	if o.Status == ReminderFailed && o.Retry && r.Attempts < MaxReminderAttempts {
		t.Status = ReminderPending
		t.RetryAfter = ScheduledRetryDelay(r.Attempts)
	}
	return t, nil
}

func resultFits(kind, result string) bool {
	switch result {
	case ReminderMissing:
		return true
	case ReminderReturned:
		return kind == ReminderSnooze
	case ReminderReplied, ReminderReminded:
		return kind == ReminderFollowUp
	}
	return false
}

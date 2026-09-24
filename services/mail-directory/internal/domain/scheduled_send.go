package domain

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Estados de un envio programado. pending espera su hora; sending esta reclamado por un trabajador con
// arriendo; sent, failed y canceled son finales.
const (
	ScheduledPending  = "pending"
	ScheduledSending  = "sending"
	ScheduledSent     = "sent"
	ScheduledFailed   = "failed"
	ScheduledCanceled = "canceled"
)

const (
	// MaxScheduledDays es el techo del directorio para la hora de envio; el webmail puede fijar uno
	// menor (WEBMAIL_SCHEDULED_*), nunca mayor.
	MaxScheduledDays = 366
	// ScheduledPastTolerance admite una hora apenas vencida: entre que el webmail valida y guarda el
	// mensaje en IMAP pasa tiempo, y una fila vencida simplemente sale en la siguiente reclamacion.
	ScheduledPastTolerance = time.Minute
	// MaxScheduledAttempts cuenta todas las reclamaciones, incluida la primera.
	MaxScheduledAttempts = 5
	// MaxScheduledPerMailbox acota los envios pendientes de un buzon.
	MaxScheduledPerMailbox = 200
	// MaxScheduledRecipients es el recipient limit de Postfix, el techo de WEBMAIL_MAX_RECIPIENTS.
	MaxScheduledRecipients   = 1000
	MaxScheduledSubjectRunes = 998
	MaxScheduledMessageID    = 998
	MaxScheduledFolderBytes  = 512
	MaxScheduledAddressBytes = 320
	MaxScheduledErrorRunes   = 1000

	MinScheduledLease     = 30 * time.Second
	MaxScheduledLease     = time.Hour
	DefaultScheduledLease = 5 * time.Minute
	MaxScheduledClaim     = 100
	DefaultScheduledClaim = 10
	// ScheduledRetention: las filas terminadas se purgan pasado este tiempo desde su ultimo cambio.
	ScheduledRetention = 30 * 24 * time.Hour
	// ScheduledLeaseExpiredError es el error de una fila cuyo trabajador no la cerro antes de que vencieran
	// su arriendo y todos sus intentos.
	ScheduledLeaseExpiredError = "el envío no se cerró antes de vencer su arriendo"
)

// ScheduledSend es el indice durable de un mensaje ya compuesto que espera en la carpeta Scheduled del
// buzon. El mensaje vive en IMAP; aqui solo su referencia (carpeta, UIDVALIDITY, UID y Message-ID) y
// lo que la interfaz lista.
type ScheduledSend struct {
	ID          uuid.UUID  `json:"id"`
	TenantID    uuid.UUID  `json:"tenant_id"`
	Username    string     `json:"username"`
	MessageID   string     `json:"message_id"`
	Folder      string     `json:"folder"`
	UIDValidity uint32     `json:"uid_validity"`
	UID         uint32     `json:"uid"`
	SendAt      time.Time  `json:"send_at"`
	Subject     string     `json:"subject"`
	Recipients  []string   `json:"recipients"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	LeaseUntil  *time.Time `json:"lease_until"`
	LastError   string     `json:"last_error"`
	SentAt      *time.Time `json:"sent_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// Normalize valida una fila nueva contra la hora now.
func (s *ScheduledSend) Normalize(now time.Time) error {
	s.MessageID = strings.TrimSpace(s.MessageID)
	if s.MessageID == "" || len(s.MessageID) > MaxScheduledMessageID || !printableASCII(s.MessageID) {
		return fieldErr("message_id", "no es un Message-ID válido")
	}
	if s.Folder == "" || len(s.Folder) > MaxScheduledFolderBytes || !utf8.ValidString(s.Folder) || strings.ContainsFunc(s.Folder, func(r rune) bool {
		return unicode.IsControl(r) || r == '*' || r == '%'
	}) {
		return fieldErr("folder", "no es un nombre de carpeta válido")
	}
	if s.UIDValidity == 0 {
		return fieldErr("uid_validity", "es obligatorio")
	}
	if s.UID == 0 {
		return fieldErr("uid", "es obligatorio")
	}
	if err := ValidateSendAt(s.SendAt, now); err != nil {
		return err
	}
	s.Subject = strings.TrimSpace(s.Subject)
	if !utf8.ValidString(s.Subject) || utf8.RuneCountInString(s.Subject) > MaxScheduledSubjectRunes || strings.ContainsFunc(s.Subject, unicode.IsControl) {
		return fieldErr("subject", "tiene caracteres no válidos o es demasiado largo")
	}
	if len(s.Recipients) == 0 {
		return fieldErr("recipients", "hace falta al menos un destinatario")
	}
	if len(s.Recipients) > MaxScheduledRecipients {
		return fieldErr("recipients", "supera el máximo de destinatarios")
	}
	for i, r := range s.Recipients {
		r = strings.TrimSpace(r)
		if !validRecipientShape(r) {
			return fieldErr("recipients["+strconv.Itoa(i)+"]", "no es una dirección de correo")
		}
		s.Recipients[i] = r
	}
	s.Status = ScheduledPending
	s.Attempts = 0
	s.SendAt = s.SendAt.UTC()
	return nil
}

// ValidateSendAt exige una hora futura (con ScheduledPastTolerance) y dentro de MaxScheduledDays.
func ValidateSendAt(sendAt, now time.Time) error {
	if sendAt.IsZero() || sendAt.Before(now.Add(-ScheduledPastTolerance)) {
		return fieldErr("send_at", "debe ser una hora futura")
	}
	if sendAt.After(now.AddDate(0, 0, MaxScheduledDays)) {
		return fieldErr("send_at", "supera el plazo máximo de programación")
	}
	return nil
}

// ScheduledOutcome es como cierra el trabajador una fila reclamada.
type ScheduledOutcome struct {
	Status string
	Error  string
	// Retry pide reprogramar un failed: el fallo fue transitorio (red, SMTP 4xx).
	Retry bool
}

// ScheduledTransition es lo que queda de la fila tras cerrarla: su estado y, si vuelve a pending,
// cuanto espera desde ahora.
type ScheduledTransition struct {
	Status     string
	RetryAfter time.Duration
	Error      string
}

// NormalizeOutcome valida el cierre y limpia el error: es texto del trabajador (a veces la respuesta
// del servidor SMTP), se guarda en una linea y recortado.
func NormalizeOutcome(o ScheduledOutcome) (ScheduledOutcome, error) {
	switch o.Status {
	case ScheduledSent, ScheduledFailed, ScheduledCanceled:
	default:
		return o, fieldErr("status", "debe ser sent, failed o canceled")
	}
	if o.Retry && o.Status != ScheduledFailed {
		return o, fieldErr("retry", "solo aplica a failed")
	}
	o.Error = cleanScheduledError(o.Error)
	return o, nil
}

// Close decide el estado final de una fila reclamada. Un failed con reintento vuelve a pending con una
// espera que crece con cada intento, mientras queden intentos.
func (s *ScheduledSend) Close(o ScheduledOutcome) (ScheduledTransition, error) {
	if s.Status != ScheduledSending {
		return ScheduledTransition{}, ErrScheduledSendNotClaimed
	}
	t := ScheduledTransition{Status: o.Status, Error: o.Error}
	if o.Status == ScheduledFailed && o.Retry && s.Attempts < MaxScheduledAttempts {
		t.Status = ScheduledPending
		t.RetryAfter = ScheduledRetryDelay(s.Attempts)
	}
	return t, nil
}

// ScheduledRetryDelay es la espera tras el intento n (1, 4, 16, 64 minutos).
func ScheduledRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	return time.Duration(1<<(2*(attempt-1))) * time.Minute
}

// ClaimParams acota una reclamacion: cuantas filas y cuanto dura el arriendo.
type ClaimParams struct {
	Limit int
	Lease time.Duration
}

// NormalizeClaim pone los valores por defecto y rechaza los que salen de rango.
func NormalizeClaim(limit int, leaseSeconds int) (ClaimParams, error) {
	if limit == 0 {
		limit = DefaultScheduledClaim
	}
	if limit < 1 || limit > MaxScheduledClaim {
		return ClaimParams{}, fieldErr("limit", "debe estar entre 1 y "+strconv.Itoa(MaxScheduledClaim))
	}
	lease := DefaultScheduledLease
	if leaseSeconds != 0 {
		// Se compara antes de multiplicar: un valor enorme desbordaria la duracion.
		lease = -1
		if leaseSeconds > 0 && leaseSeconds <= int(MaxScheduledLease/time.Second) {
			lease = time.Duration(leaseSeconds) * time.Second
		}
	}
	if lease < MinScheduledLease || lease > MaxScheduledLease {
		return ClaimParams{}, fieldErr("lease_seconds", "debe estar entre "+strconv.Itoa(int(MinScheduledLease.Seconds()))+" y "+strconv.Itoa(int(MaxScheduledLease.Seconds())))
	}
	return ClaimParams{Limit: limit, Lease: lease}, nil
}

func cleanScheduledError(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > MaxScheduledErrorRunes {
		s = string([]rune(s)[:MaxScheduledErrorRunes])
	}
	return s
}

func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] <= ' ' || s[i] >= 0x7f {
			return false
		}
	}
	return true
}

// validRecipientShape es una comprobacion de forma, no de sintaxis completa: la direccion ya la valido
// el webmail al componer, y aqui solo se lista. Una parte local y un dominio, sin espacios, comas ni
// angulos que la conviertan en otra cosa.
func validRecipientShape(r string) bool {
	if r == "" || len(r) > MaxScheduledAddressBytes || !utf8.ValidString(r) {
		return false
	}
	at := strings.LastIndex(r, "@")
	if at <= 0 || at == len(r)-1 {
		return false
	}
	return !strings.ContainsFunc(r, func(c rune) bool {
		return unicode.IsSpace(c) || unicode.IsControl(c) || strings.ContainsRune(`,;<>"()`, c)
	})
}

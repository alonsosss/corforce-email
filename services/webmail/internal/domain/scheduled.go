package domain

import (
	"regexp"
	"strings"
	"time"
)

// MinScheduleLead es lo minimo que un envio programado puede estar en el futuro: por debajo es
// un envio normal, y el trabajador necesita margen para reclamarlo a su hora.
const MinScheduleLead = time.Minute

// ScheduledStatus es el estado de un envio programado en mail-directory.
type ScheduledStatus string

const (
	ScheduledPending  ScheduledStatus = "pending"
	ScheduledSending  ScheduledStatus = "sending"
	ScheduledSent     ScheduledStatus = "sent"
	ScheduledFailed   ScheduledStatus = "failed"
	ScheduledCanceled ScheduledStatus = "canceled"
)

// ScheduledSend es un envio programado del buzon. El mensaje vive en la carpeta Scheduled; la
// fila de mail-directory guarda la hora y la referencia IMAP (UIDValidity, UID y Message-ID).
type ScheduledSend struct {
	ID          string
	SendAt      time.Time
	Subject     string
	Recipients  []string
	CreatedAt   time.Time
	Status      ScheduledStatus
	MessageID   string
	Folder      string
	UIDValidity uint32
	UID         uint32
}

// NewScheduledSend es la fila que se registra al programar un envio.
type NewScheduledSend struct {
	Username    string
	MessageID   string
	Folder      string
	UIDValidity uint32
	UID         uint32
	SendAt      time.Time
	Subject     string
	Recipients  []string
}

// ScheduledClaim es una fila vencida que el trabajador reclamo con arriendo.
type ScheduledClaim struct {
	ID          string
	Username    string
	MessageID   string
	Folder      string
	UIDValidity uint32
	UID         uint32
	SendAt      time.Time
}

// ScheduledOutcome es como termina el intento de un envio programado. Retry solo con Failed: el
// fallo fue de infraestructura y mail-directory lo reprograma con espera creciente.
type ScheduledOutcome struct {
	Status ScheduledStatus
	Error  string
	Retry  bool
}

// ScheduledResult es la respuesta a programar un envio. Replayed indica que la peticion repetia
// una ya hecha con la misma clave de idempotencia y que no se programo nada nuevo.
type ScheduledResult struct {
	ID       string
	SendAt   time.Time
	Replayed bool
	// MessageID es el del mensaje programado: el seguimiento lo busca en Enviados cuando salga.
	MessageID string
}

// ValidateSendAt exige una hora de envio entre MinScheduleLead y maxDays dias desde now.
func ValidateSendAt(at, now time.Time, maxDays int) error {
	if at.IsZero() {
		return invalid("send_at", "es obligatoria")
	}
	if at.Before(now.Add(MinScheduleLead)) {
		return invalid("send_at", "debe ser al menos un minuto en el futuro")
	}
	if at.After(now.AddDate(0, 0, maxDays)) {
		return invalid("send_at", "supera el plazo máximo de programación")
	}
	return nil
}

// ParseSendAt interpreta la hora de envio (RFC 3339).
func ParseSendAt(raw string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, invalid("send_at", "debe ser una fecha y hora RFC 3339")
	}
	return t.UTC(), nil
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidUUID dice si v es un UUID en su forma canonica.
func ValidUUID(v string) bool { return uuidPattern.MatchString(v) }

// ValidateScheduledID exige el identificador de una fila (UUID): viaja en la ruta interna.
func ValidateScheduledID(id string) error {
	if !ValidUUID(id) {
		return invalid("id", "identificador de envío programado inválido")
	}
	return nil
}

// FinalizedMessage es un mensaje guardado ya preparado para salir: la version que viaja (sin
// Bcc), la copia que se guarda en Enviados (con Bcc) y el sobre SMTP que sale de sus cabeceras.
type FinalizedMessage struct {
	From       string
	Recipients []string
	Wire       []byte
	Stored     []byte
}

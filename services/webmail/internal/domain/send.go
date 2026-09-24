package domain

import "regexp"

// SendOptions acompanan a un envio.
type SendOptions struct {
	// IdempotencyKey identifica el intento del cliente (cabecera Idempotency-Key): repetir
	// la peticion con la misma clave nunca entrega el mensaje dos veces.
	IdempotencyKey string
	// ReplaceUID es el borrador que el envio retira de Borradores (0: ninguno).
	ReplaceUID uint32
}

// SendState es el punto en que esta un envio identificado por su clave.
type SendState string

const (
	// SendPending: reservado y en curso; otra peticion con la misma clave espera.
	SendPending SendState = "pending"
	// SendSent: Postfix acepto el mensaje. Repetir la peticion no lo vuelve a entregar.
	SendSent SendState = "sent"
	// SendUncertain: la conexion se corto esperando la respuesta al final de DATA y el
	// mensaje pudo quedar en cola. Con esa clave no se vuelve a intentar: reenviar es una
	// decision del usuario, con otra clave.
	SendUncertain SendState = "uncertain"
	// SendScheduled: el mensaje quedo programado (ScheduledID). Repetir la peticion no programa
	// otro.
	SendScheduled SendState = "scheduled"
)

// SendRecord es lo que se recuerda de un envio por su clave. Fingerprint resume el mensaje
// pedido: la misma clave con otro mensaje se rechaza. Token es la marca de quien reservo la
// clave: solo con ella se actualiza o se libera el registro.
type SendRecord struct {
	State        SendState
	Fingerprint  string
	MessageID    string
	SavedToSent  bool
	DraftRemoved bool
	ScheduledID  string
	Token        string
}

var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// ValidateIdempotencyKey exige la clave de un envio: sin ella un reintento del navegador
// tras un corte de red entregaria el mensaje otra vez.
func ValidateIdempotencyKey(key string) error {
	if key == "" {
		return invalid("idempotency_key", "es obligatoria (cabecera Idempotency-Key)")
	}
	if !idempotencyKeyPattern.MatchString(key) {
		return invalid("idempotency_key", "debe tener de 16 a 128 caracteres A-Z, a-z, 0-9, _ o -")
	}
	return nil
}

// SenderIdentity es una direccion con la que el buzon puede enviar. Primary es el propio
// buzon, que siempre puede.
type SenderIdentity struct {
	Address string
	Name    string
	Primary bool
}

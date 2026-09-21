package domain

import "regexp"

// QueueAction es lo unico que el gestor de cola puede hacer con un mensaje.
type QueueAction string

const (
	QueueRetry  QueueAction = "retry"
	QueueHold   QueueAction = "hold"
	QueueUnhold QueueAction = "unhold"
	QueueDelete QueueAction = "delete"
)

const (
	// MaxQueueListLimit es el tope de mensajes de una consulta de la cola.
	MaxQueueListLimit = 500
	// DefaultQueueListLimit es lo que devuelve una consulta sin limite.
	DefaultQueueListLimit = 100
)

// queueIDPattern es la forma de un identificador de cola de Postfix, corto o largo. El agente de la cola
// la vuelve a comprobar: es lo unico que llega a un proceso.
var queueIDPattern = regexp.MustCompile(`^[0-9A-Za-z]{5,25}$`)

// ValidateQueueID rechaza lo que no es un identificador de cola.
func ValidateQueueID(id string) error {
	if !queueIDPattern.MatchString(id) {
		return newValidation("identificador de cola no valido")
	}
	return nil
}

// ValidateQueueAction rechaza una accion desconocida.
func ValidateQueueAction(a QueueAction) error {
	switch a {
	case QueueRetry, QueueHold, QueueUnhold, QueueDelete:
		return nil
	}
	return newValidation("action debe ser retry, hold, unhold o delete")
}

// QueueRecipient es un destinatario pendiente de un mensaje y por que sigue en cola.
type QueueRecipient struct {
	Address     string `json:"address"`
	DelayReason string `json:"delay_reason,omitempty"`
}

// QueueMessage es un mensaje de la cola de Postfix de la celda, sin su contenido.
type QueueMessage struct {
	QueueID          string           `json:"queue_id"`
	QueueName        string           `json:"queue_name"`
	ArrivalTime      int64            `json:"arrival_time"`
	MessageSize      int64            `json:"message_size"`
	Sender           string           `json:"sender"`
	Recipients       []QueueRecipient `json:"recipients"`
	RecipientsTotal  int              `json:"recipients_total"`
	RecipientsCapped bool             `json:"recipients_capped,omitempty"`
}

// QueueListing es una consulta de la cola: Total cuenta todos los mensajes aunque Items traiga menos.
type QueueListing struct {
	Total     int  `json:"total"`
	Truncated bool `json:"truncated"`
	// Counts cuenta los mensajes de la cola entera por cola de Postfix y OldestArrival es el instante Unix
	// del mas antiguo sin contar los retenidos (0 si no hay): describen toda la cola aunque Items traiga menos.
	Counts        map[string]int `json:"counts"`
	OldestArrival int64          `json:"oldest_arrival"`
	Items         []QueueMessage `json:"items"`
}

// QueueNames son las colas de Postfix que se vigilan: una que no aparece en la consulta se anota a cero.
func QueueNames() []string { return []string{"incoming", "active", "deferred", "hold", "corrupt"} }

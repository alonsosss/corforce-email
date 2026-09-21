package domain

import "errors"

// ErrTooManyStreams: el buzon o el proceso ya tienen todas las conexiones de avisos que admiten.
var ErrTooManyStreams = errors.New("demasiadas conexiones de avisos abiertas")

// ErrEventsDisabled: los avisos en tiempo real estan desactivados en este despliegue.
var ErrEventsDisabled = errors.New("avisos en tiempo real desactivados")

// MailboxChange avisa de que cambio la bandeja de entrada de un buzon (mensaje nuevo, borrado o cambio de
// banderas). No dice que cambio: la interfaz vuelve a leer lo que muestra.
type MailboxChange struct {
	// Messages es el total de mensajes que Dovecot dio en el ultimo aviso, 0 si el aviso no lo traia.
	Messages uint32
}

package app

import (
	"sync"
	"time"
)

const (
	// loginDedupeWindow es cada cuanto deja rastro un cliente que verifica cada peticion.
	loginDedupeWindow = 5 * time.Minute
	// loginDedupeEntries acota los clientes recordados a la vez.
	loginDedupeEntries = 10000
)

// loginDedupe recuerda los clientes que ya dejaron rastro en la ventana. Vive en la memoria de cada
// replica: con varias, un cliente deja un registro por replica y ventana, no mas.
type loginDedupe struct {
	mu     sync.Mutex
	seen   map[string]time.Time
	max    int
	window time.Duration
}

func newLoginDedupe(max int, window time.Duration) *loginDedupe {
	return &loginDedupe{seen: map[string]time.Time{}, max: max, window: window}
}

// firstInWindow dice si hay que dejar rastro: verdadero la primera vez que se ve el cliente en la
// ventana. Con la memoria llena y nada vencido que purgar tambien es verdadero: registrar de mas es
// mejor que perder un inicio.
func (d *loginDedupe) firstInWindow(client string, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if at, ok := d.seen[client]; ok && now.Sub(at) < d.window {
		return false
	}
	if _, known := d.seen[client]; !known && len(d.seen) >= d.max {
		for k, at := range d.seen {
			if now.Sub(at) >= d.window {
				delete(d.seen, k)
			}
		}
		if len(d.seen) >= d.max {
			return true
		}
	}
	d.seen[client] = now
	return true
}

func (d *loginDedupe) size() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.seen)
}

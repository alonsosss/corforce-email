package http

import "sync"

// composeGate acota las operaciones que cargan un mensaje entero en memoria (enviar,
// programar y guardar un borrador): cada una puede ocupar varias veces MaxMessageBytes
// entre el formulario, las dos composiciones y la codificacion base64. Un tope global
// protege el proceso de quedarse sin memoria, y una sola operacion por buzon impide que
// una sesion acapare todo el cupo.
type composeGate struct {
	slots chan struct{}
	mu    sync.Mutex
	busy  map[string]bool
}

func newComposeGate(capacity int) *composeGate {
	return &composeGate{slots: make(chan struct{}, capacity), busy: map[string]bool{}}
}

// acquire reserva un hueco para mailbox sin esperar. Sin hueco devuelve ok=false.
func (g *composeGate) acquire(mailbox string) (release func(), ok bool) {
	g.mu.Lock()
	if g.busy[mailbox] {
		g.mu.Unlock()
		return nil, false
	}
	select {
	case g.slots <- struct{}{}:
	default:
		g.mu.Unlock()
		return nil, false
	}
	g.busy[mailbox] = true
	g.mu.Unlock()
	return func() {
		g.mu.Lock()
		delete(g.busy, mailbox)
		g.mu.Unlock()
		<-g.slots
	}, true
}

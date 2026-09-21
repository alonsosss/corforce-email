package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

const (
	defaultEventsHeartbeat    = 25 * time.Second
	defaultEventsSessionCheck = 10 * time.Second
	// defaultEventsMaxLifetime queda por debajo del WriteTimeout de 120 s del gateway: el flujo se cierra
	// solo y la interfaz reconecta, en vez de que el gateway lo corte a mitad.
	defaultEventsMaxLifetime = 100 * time.Second
	eventsWriteTimeout       = 15 * time.Second
	eventsRetry              = 3 * time.Second
)

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// requireSessionPeek es requireSession sin renovar la inactividad: el flujo de avisos reconecta solo y no
// debe mantener viva una sesion que nadie usa.
func (h *Handler) requireSessionPeek(next http.Handler) http.Handler {
	return h.sessionMiddleware(next, h.app.PeekSession)
}

// Events es un flujo Server-Sent Events con los cambios de la bandeja de entrada del buzon de la sesion:
//
//	event: ready           al abrir
//	event: mailbox         hay algo nuevo, borrado o con otras banderas: la interfaz vuelve a leer
//	event: session-expired la sesion caduco o se revoco (se cierra el flujo)
//	event: reconnect       tope de vida del flujo (se cierra para que el cliente reconecte)
//	: ping                 latido, para que ningun intermediario lo dé por muerto
//
// No lleva el contenido de ningun mensaje. Un GET desde otro origen se rechaza aunque SameSite=Strict ya
// impide que lleve la cookie.
func (h *Handler) Events(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" && !h.origins.Allowed(origin) {
		response.Err(w, http.StatusForbidden, "ORIGIN_NOT_ALLOWED", "origen no permitido")
		return
	}
	rc := http.NewResponseController(w)
	sess := sessionFrom(r)
	changes, err := h.app.WatchInbox(r.Context(), sess)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream")
	hd.Set("Cache-Control", "no-store")
	// Sin esto el proxy de borde acumularia el flujo y la interfaz no veria nada hasta que se llene.
	hd.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	write := func(format string, args ...any) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(eventsWriteTimeout))
		if _, err := fmt.Fprintf(w, format, args...); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !write("retry: %d\n\nevent: ready\ndata: {}\n\n", eventsRetry.Milliseconds()) {
		return
	}

	heartbeat := time.NewTicker(orDefault(h.cfg.EventsHeartbeat, defaultEventsHeartbeat))
	defer heartbeat.Stop()
	check := time.NewTicker(orDefault(h.cfg.EventsSessionCheck, defaultEventsSessionCheck))
	defer check.Stop()
	lifetime := time.NewTimer(orDefault(h.cfg.EventsMaxLifetime, defaultEventsMaxLifetime))
	defer lifetime.Stop()
	token := cookieValue(r)

	for {
		select {
		case <-r.Context().Done():
			return
		case change, ok := <-changes:
			if !ok {
				return
			}
			body, _ := json.Marshal(map[string]any{"folder": "INBOX", "messages": change.Messages})
			if !write("event: mailbox\ndata: %s\n\n", body) {
				return
			}
		case <-heartbeat.C:
			if !write(": ping\n\n") {
				return
			}
		case <-check.C:
			ctx, cancel := h.opContext(r)
			_, err := h.app.PeekSession(ctx, token)
			cancel()
			// Un fallo de Redis o de mail-auth no es una sesion caida: se sigue y se vuelve a comprobar.
			if errors.Is(err, domain.ErrSessionInvalid) {
				write("event: session-expired\ndata: {}\n\n")
				return
			}
		case <-lifetime.C:
			write("event: reconnect\ndata: {}\n\n")
			return
		}
	}
}

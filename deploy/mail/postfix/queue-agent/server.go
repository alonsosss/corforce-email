package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
)

const (
	defaultListLimit = 1000
	maxListLimit     = 5000
)

// operations es lo que el servidor necesita de la cola; Queue lo cumple y las pruebas lo sustituyen.
type operations interface {
	List(ctx context.Context, limit int) (Listing, error)
	Apply(ctx context.Context, action Action, id string) error
	Flush(ctx context.Context) error
}

// Server expone la cola de Postfix por HTTPS a un unico cliente de confianza (mail-security de la
// celda), autenticado por clave. No hay ruta que devuelva el contenido de un mensaje ni que ejecute
// otra cosa que las cuatro acciones y el vaciado de la cola diferida.
type Server struct {
	queue   operations
	keyHash [sha256.Size]byte
	log     *slog.Logger
	// slots limita a uno las operaciones que ejecutan procesos: no hay razon para lanzar varios
	// postsuper a la vez y un cliente con prisa no debe poder saturar el contenedor de Postfix.
	slots chan struct{}
}

func NewServer(queue operations, apiKey string, log *slog.Logger) *Server {
	return &Server{queue: queue, keyHash: sha256.Sum256([]byte(apiKey)), log: log, slots: make(chan struct{}, 1)}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/queue", s.list)
	mux.HandleFunc("POST /v1/queue/flush", s.flush)
	mux.HandleFunc("POST /v1/queue/{id}/{action}", s.apply)
	return s.authenticate(mux)
}

// authenticate compara la clave en tiempo constante, sobre su huella para no filtrar el largo.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		got := sha256.Sum256([]byte(token))
		if !ok || subtle.ConstantTimeCompare(got[:], s.keyHash[:]) != 1 {
			s.log.Warn("peticion sin clave valida", "remote", remoteHost(r), "path", r.URL.Path)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no autorizado"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	limit := defaultListLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxListLimit {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit debe estar entre 1 y " + strconv.Itoa(maxListLimit)})
			return
		}
		limit = n
	}
	if !s.acquire(w, r) {
		return
	}
	defer s.release()
	out, err := s.queue.List(r.Context(), limit)
	if err != nil {
		s.fail(w, r, "listar", "", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apply(w http.ResponseWriter, r *http.Request) {
	id, action := r.PathValue("id"), Action(r.PathValue("action"))
	if !ValidQueueID(id) || !ValidAction(action) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "identificador o accion no validos"})
		return
	}
	if !s.acquire(w, r) {
		return
	}
	defer s.release()
	if err := s.queue.Apply(r.Context(), action, id); err != nil {
		s.fail(w, r, string(action), id, err)
		return
	}
	s.log.Info("accion sobre la cola", "action", action, "queue_id", id, "remote", remoteHost(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) flush(w http.ResponseWriter, r *http.Request) {
	if !s.acquire(w, r) {
		return
	}
	defer s.release()
	if err := s.queue.Flush(r.Context()); err != nil {
		s.fail(w, r, "flush", "", err)
		return
	}
	s.log.Info("accion sobre la cola", "action", "flush", "remote", remoteHost(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) acquire(w http.ResponseWriter, r *http.Request) bool {
	select {
	case s.slots <- struct{}{}:
		return true
	case <-r.Context().Done():
		return false
	default:
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "hay otra operacion en curso"})
		return false
	}
}

func (s *Server) release() { <-s.slots }

func (s *Server) fail(w http.ResponseWriter, r *http.Request, action, id string, err error) {
	if errors.Is(err, ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": ErrNotFound.Error()})
		return
	}
	// El detalle (salida de Postfix) queda en el registro del contenedor; al cliente solo un mensaje fijo.
	s.log.Error("operacion de la cola fallida", "action", action, "queue_id", id, "remote", remoteHost(r), "error", err)
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Postfix no pudo completar la operacion"})
}

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// Package http sirve CardDAV (RFC 6352) y CalDAV (RFC 4791) sobre WebDAV (RFC 4918) para mail-dav: el
// subconjunto que usan los clientes de contactos y calendario (DAVx5, iOS, Thunderbird). Cada peticion se autentica con HTTP Basic contra
// mail-auth; Basic solo es admisible porque el borde termina TLS antes del gateway.
package http

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"go.uber.org/zap"
)

type Config struct {
	// BasePath es el prefijo publico del servicio (el del gateway), sin barra final.
	BasePath string
	Realm    string
	// MaxXMLBytes acota el cuerpo de un PROPFIND, REPORT o MKCOL.
	MaxXMLBytes int64
	Logger      *zap.Logger
}

var basePathRe = regexp.MustCompile(`^(/[a-z0-9][a-z0-9-]*)+$`)

type Handler struct {
	uc     *app.UseCase
	cfg    Config
	logger *zap.Logger
}

func NewHandler(uc *app.UseCase, cfg Config) (*Handler, error) {
	if !basePathRe.MatchString(cfg.BasePath) {
		return nil, fmt.Errorf("BasePath %q no es una ruta de segmentos en minúsculas sin barra final", cfg.BasePath)
	}
	if strings.TrimSpace(cfg.Realm) == "" || strings.ContainsAny(cfg.Realm, "\"\\\r\n") {
		return nil, errors.New("el realm de autenticación es obligatorio y no admite comillas ni saltos de línea")
	}
	if cfg.MaxXMLBytes < 1 {
		return nil, errors.New("MaxXMLBytes debe ser mayor que cero")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Handler{uc: uc, cfg: cfg, logger: logger}, nil
}

const allowedMethods = "OPTIONS, PROPFIND, REPORT, GET, HEAD, PUT, DELETE, MKCOL, MKCALENDAR"

// ServeHTTP autentica antes de mirar la ruta: sin credenciales validas ninguna URL distingue un
// recurso que existe de uno que no.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	switch r.Method {
	case http.MethodOptions:
		w.Header().Set("Allow", allowedMethods)
		w.Header().Set("DAV", "1, 3, addressbook, calendar-access")
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
		return
	case "PROPFIND", "REPORT", http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, "MKCOL", "MKCALENDAR":
	default:
		w.Header().Set("Allow", allowedMethods)
		http.Error(w, "metodo no admitido", http.StatusMethodNotAllowed)
		return
	}

	ctx, p, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	t, ok := h.route(r, p)
	if !ok {
		http.NotFound(w, r)
		return
	}
	r = r.WithContext(ctx)
	switch r.Method {
	case "PROPFIND":
		h.propfind(w, r, p, t)
	case "REPORT":
		h.report(w, r, p, t)
	case http.MethodGet, http.MethodHead:
		h.get(w, r, p, t)
	case http.MethodPut:
		h.put(w, r, p, t)
	case http.MethodDelete:
		h.delete(w, r, p, t)
	case "MKCOL":
		h.mkcol(w, r, p, t)
	case "MKCALENDAR":
		h.mkcalendar(w, r, p, t)
	}
}

func (h *Handler) challenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="`+h.cfg.Realm+`", charset="UTF-8"`)
	http.Error(w, "autenticacion requerida", http.StatusUnauthorized)
}

const maxCredentialLength = 1024

func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request) (context.Context, domain.Principal, bool) {
	username, password, ok := r.BasicAuth()
	if !ok || username == "" || password == "" || len(username) > maxCredentialLength || len(password) > maxCredentialLength {
		h.challenge(w)
		return nil, domain.Principal{}, false
	}
	ctx, p, err := h.uc.Authenticate(r.Context(), username, password, clientIP(r))
	switch {
	case err == nil:
		return ctx, p, true
	case errors.Is(err, domain.ErrInvalidCredentials):
		h.challenge(w)
	case errors.Is(err, domain.ErrUnavailable):
		h.logger.Error("mail-dav: autenticacion no disponible", zap.String("request_id", middleware.GetRequestID(r.Context())), zap.Error(err))
		w.Header().Set("Retry-After", "30")
		http.Error(w, "servicio no disponible", http.StatusServiceUnavailable)
	default:
		h.logger.Error("mail-dav: fallo inesperado al autenticar", zap.String("request_id", middleware.GetRequestID(r.Context())), zap.Error(err))
		http.Error(w, "error interno", http.StatusInternalServerError)
	}
	return nil, domain.Principal{}, false
}

// clientIP es la IP real del visitante: la pone el gateway en X-Real-IP tras validarla contra el proxy de
// borde, y el servicio solo acepta peticiones con el token del gateway, asi que el cliente no puede
// fijarla. Llega a mail-auth para el freno de fuerza bruta por (buzon, IP).
func clientIP(r *http.Request) string {
	if ip := net.ParseIP(r.Header.Get("X-Real-IP")); ip != nil {
		return ip.String()
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type kind int

const (
	kindRoot kind = iota
	kindPrincipals
	kindPrincipal
	kindHomes
	kindHome
	kindBook
	kindContact
	kindCalHomes
	kindCalHome
	kindCalendar
	kindEvent
)

// target es el recurso que nombra la URL. Solo el buzon autenticado tiene recursos: cualquier ruta
// con otro usuario no existe.
type target struct {
	kind     kind
	slug     string
	resource string
}

func (h *Handler) route(r *http.Request, p domain.Principal) (target, bool) {
	return h.routePath(r.URL.EscapedPath(), p)
}

// routePath resuelve una ruta con escapes: la de la peticion o la de un href de un informe.
func (h *Handler) routePath(escaped string, p domain.Principal) (target, bool) {
	rel, ok := strings.CutPrefix(escaped, h.cfg.BasePath)
	if !ok || (rel != "" && rel[0] != '/') {
		return target{}, false
	}
	segs, ok := splitPath(rel)
	if !ok || len(segs) > 4 {
		return target{}, false
	}
	trailing := strings.HasSuffix(rel, "/")
	own := func(user string) bool {
		name, ok := domain.NormalizeUsername(user)
		return ok && name == p.Username
	}
	switch {
	case len(segs) == 0:
		return target{kind: kindRoot}, true
	case segs[0] == "principals" && len(segs) == 1:
		return target{kind: kindPrincipals}, true
	case segs[0] == "principals" && len(segs) == 2 && own(segs[1]):
		return target{kind: kindPrincipal}, true
	case segs[0] == "addressbooks" && len(segs) == 1:
		return target{kind: kindHomes}, true
	case segs[0] == "addressbooks" && len(segs) == 2 && own(segs[1]):
		return target{kind: kindHome}, true
	case segs[0] == "addressbooks" && len(segs) == 3 && own(segs[1]):
		return target{kind: kindBook, slug: segs[2]}, true
	case segs[0] == "addressbooks" && len(segs) == 4 && own(segs[1]) && !trailing:
		return target{kind: kindContact, slug: segs[2], resource: segs[3]}, true
	case segs[0] == "calendars" && len(segs) == 1:
		return target{kind: kindCalHomes}, true
	case segs[0] == "calendars" && len(segs) == 2 && own(segs[1]):
		return target{kind: kindCalHome}, true
	case segs[0] == "calendars" && len(segs) == 3 && own(segs[1]):
		return target{kind: kindCalendar, slug: segs[2]}, true
	case segs[0] == "calendars" && len(segs) == 4 && own(segs[1]) && !trailing:
		return target{kind: kindEvent, slug: segs[2], resource: segs[3]}, true
	}
	return target{}, false
}

// path arma la ruta sin escapar; quien la escribe en una respuesta la escapa una sola vez (hrefText).
func (h *Handler) path(elems ...string) string {
	var b strings.Builder
	b.WriteString(h.cfg.BasePath)
	for _, e := range elems {
		b.WriteByte('/')
		b.WriteString(e)
	}
	return b.String()
}

func (h *Handler) principalPath(p domain.Principal) string {
	return h.path("principals", p.Username) + "/"
}

func (h *Handler) homePath(p domain.Principal) string {
	return h.path("addressbooks", p.Username) + "/"
}

func (h *Handler) bookPath(p domain.Principal, slug string) string {
	return h.path("addressbooks", p.Username, slug) + "/"
}

func (h *Handler) contactPath(p domain.Principal, slug, resource string) string {
	return h.path("addressbooks", p.Username, slug, resource)
}

// icalCondition es la precondicion de CalDAV (RFC 4791, 5.3.2.1) que incumple un iCalendar.
func icalCondition(kind domain.ICalErrorKind) xml.Name {
	switch kind {
	case domain.ICalObject:
		return calName("valid-calendar-object-resource")
	case domain.ICalComponent:
		return calName("supported-calendar-component")
	case domain.ICalTooLarge:
		return calName("max-resource-size")
	}
	return calName("valid-calendar-data")
}

// fail traduce un error del caso de uso a su respuesta. Lo que no se reconoce es un 500 sin detalle.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var bad *domain.VCardError
	var badICal *domain.ICalError
	switch {
	case errors.Is(err, domain.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, domain.ErrPreconditionFailed):
		http.Error(w, "la precondicion no se cumple", http.StatusPreconditionFailed)
	case errors.Is(err, domain.ErrAlreadyExists):
		w.Header().Set("Allow", allowedMethods)
		http.Error(w, "el recurso ya existe", http.StatusMethodNotAllowed)
	case errors.Is(err, domain.ErrContactLimit), errors.Is(err, domain.ErrAddressbookLimit),
		errors.Is(err, domain.ErrEventLimit), errors.Is(err, domain.ErrCalendarLimit), errors.Is(err, domain.ErrStorageLimit):
		writeDAVError(w, http.StatusInsufficientStorage, davName("quota-not-exceeded"))
	case errors.Is(err, domain.ErrResultTooLarge):
		writeDAVError(w, http.StatusInsufficientStorage, davName("number-of-matches-within-limits"))
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		w.Header().Set("Retry-After", "30")
		http.Error(w, "tiempo de espera agotado", http.StatusServiceUnavailable)
	case errors.Is(err, domain.ErrInvalidName):
		http.Error(w, "nombre no valido", http.StatusForbidden)
	case errors.Is(err, domain.ErrInvalidSyncToken):
		writeDAVError(w, http.StatusForbidden, davName("valid-sync-token"))
	case errors.As(err, &badICal):
		writeDAVError(w, http.StatusForbidden, icalCondition(badICal.Kind))
	case errors.As(err, &bad):
		if bad.TooLarge {
			http.Error(w, bad.Reason, http.StatusRequestEntityTooLarge)
			return
		}
		writeDAVError(w, http.StatusForbidden, cardName("valid-address-data"))
	case errors.Is(err, domain.ErrUnavailable):
		w.Header().Set("Retry-After", "30")
		http.Error(w, "servicio no disponible", http.StatusServiceUnavailable)
	default:
		h.logger.Error("mail-dav: error inesperado", zap.String("request_id", middleware.GetRequestID(r.Context())),
			zap.String("method", r.Method), zap.Error(err))
		http.Error(w, "error interno", http.StatusInternalServerError)
	}
}

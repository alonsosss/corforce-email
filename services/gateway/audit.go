package main

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"go.uber.org/zap"
)

// auditTrail publica un evento persistente por cada ESCRITURA autenticada del API y por cada
// peticion con celda destino explicita, tambien las lecturas y las rechazadas (target.go): quien
// (usuario/roles/IP), que (metodo/ruta/modulo, celda destino) y con que resultado (status
// HTTP). El servicio audit lo consume con un worker durable y lo persiste append-only en
// audit.audit_logs de la empresa de quien llama. Asincrono y best-effort: nunca bloquea ni
// falla la peticion; sin NATS el gateway sigue operando (solo se pierde el rastro API, no el
// trafico).
type auditTrail struct {
	bus     trailPublisher
	modules map[string]string
	logger  *zap.Logger

	// Deteccion de exfiltracion: un contador de LECTURAS por usuario en una ventana anclada
	// en la primera. Una cuenta comprometida que descarga la lista de contactos o los
	// buzones hace muchas mas lecturas que un humano; al alcanzar el umbral se emite una
	// alerta (no se bloquea, para no cortar a un usuario legitimo intensivo). El conteo es
	// comun a todas las replicas (Redis, exfilLimiterName): repartir las lecturas entre
	// ellas no lo esquiva. Con Redis caido cada replica cuenta lo suyo.
	reads       *middleware.RateLimiter
	exfilMax    int64
	exfilWindow time.Duration
}

// trailPublisher es la parte del bus que usa el rastro.
type trailPublisher interface {
	Publish(subject string, evt events.Event) error
	PublishPersistent(subject string, evt events.Event) error
}

// newAuditTrail arma el rastro con el contador de lecturas y el umbral y la ventana de
// exfiltracion ya validados (loadSettings).
func newAuditTrail(modules map[string]string, reads *middleware.RateLimiter, exfilMax int, exfilWindow time.Duration, logger *zap.Logger) *auditTrail {
	url := os.Getenv("NATS_URL")
	if url == "" {
		url = "nats://nats:4222"
	}
	at := &auditTrail{modules: modules, logger: logger, reads: reads, exfilMax: int64(exfilMax), exfilWindow: exfilWindow}
	bus, err := events.NewBus(url, logger)
	if err != nil {
		logger.Warn("audit trail sin NATS: escrituras no auditadas a nivel API", zap.Error(err))
		return at
	}
	if err := bus.EnsureStream("AUDIT_API", []string{"audit.api.>"}); err != nil {
		logger.Warn("ensure stream AUDIT_API", zap.Error(err))
	}
	at.bus = bus
	return at
}

// trackRead cuenta una lectura del usuario y devuelve el total de la ventana y true en la
// lectura que alcanza el umbral: una alerta por ventana y usuario, tambien entre replicas
// porque el total sale de un contador atomico. Si la respuesta de esa unica lectura se
// perdiera (timeout con el contador ya incrementado) la alerta de esa ventana no sale.
func (a *auditTrail) trackRead(ctx context.Context, userID string) (int64, bool) {
	if userID == "" {
		return 0, false
	}
	n := a.reads.CountPerUser(ctx, userID)
	return n, n == a.exfilMax
}

// publishExfil emite la alerta de posible extraccion masiva por la misma tuberia que el
// resto: el servicio audit la consume, el detector crea el evento de seguridad y notifica
// al usuario y a los administradores.
func (a *auditTrail) publishExfil(r *http.Request, count int64) {
	if a.bus == nil {
		return
	}
	tenantID := middleware.GetTenantID(r.Context())
	userID := middleware.GetUserID(r.Context())
	if tenantID == "" || userID == "" {
		return
	}
	evt := events.Event{
		Type:     "user.bulk_read",
		Source:   "gateway",
		TenantID: tenantID,
		UserID:   userID,
		Data: map[string]interface{}{
			"tenant_id": tenantID,
			"user_id":   userID,
			"ip":        middleware.GetClientIP(r.Context()),
			"count":     count,
			"window":    a.exfilWindow.String(),
		},
	}
	go func() {
		defer func() { _ = recover() }()
		if err := a.bus.Publish("gateway.security.exfiltration", evt); err != nil {
			a.logger.Warn("alerta de exfiltracion no publicada", zap.Error(err))
		}
	}()
}

// statusRecorder captura el codigo de respuesta que devuelve el proxy.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (a *auditTrail) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seg1, _ := pathSegments(r.URL.Path)
		esDatos := seg1 != "" && seg1 != "auth" && seg1 != "access"

		// Deteccion de exfiltracion: cuenta las LECTURAS de datos por usuario. Al alcanzar
		// el umbral en la ventana, emite una alerta (no bloquea). Fuera del camino de la
		// escritura, que se audita aparte abajo.
		if r.Method == http.MethodGet && esDatos {
			if n, alert := a.trackRead(r.Context(), middleware.GetUserID(r.Context())); alert {
				a.publishExfil(r, n)
			}
		}

		// auth tiene su propia bitacora en identity; access es consulta de permisos. Una
		// peticion con celda destino se audita siempre, sea cual sea su ruta y su metodo.
		target, targeted := requestedTargetFrom(r.Context())
		if a.bus == nil || !(targeted || (esDatos && rbacWriteMethods[r.Method])) {
			next.ServeHTTP(w, r)
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		tenantID := middleware.GetTenantID(r.Context())
		userID := middleware.GetUserID(r.Context())
		if tenantID == "" {
			return
		}
		module := a.modules[seg1]
		if module == "" {
			module = seg1
		}
		data := map[string]interface{}{
			"tenant_id":  tenantID,
			"user_id":    userID,
			"roles":      strings.Join(middleware.GetRoles(r.Context()), ","),
			"method":     r.Method,
			"path":       r.URL.Path,
			"module":     module,
			"status":     rec.status,
			"ip":         middleware.GetClientIP(r.Context()),
			"user_agent": r.UserAgent(),
			"request_id": middleware.GetRequestID(r.Context()),
		}
		if targeted {
			data["target_cell"] = target.auditValue()
		}
		if keyID := middleware.GetAPIKeyID(r.Context()); keyID != "" {
			data["api_key_id"] = keyID
		}
		evt := events.Event{
			Type:     "audit.api.write",
			Source:   "gateway",
			TenantID: tenantID,
			UserID:   userID,
			Data:     data,
		}
		// Fuera del camino de la peticion: publicar no debe anadir latencia ni fallo.
		go func() {
			defer func() { _ = recover() }()
			if err := a.bus.PublishPersistent("audit.api.write", evt); err != nil {
				a.logger.Warn("audit trail publish", zap.Error(err))
			}
		}()
	})
}

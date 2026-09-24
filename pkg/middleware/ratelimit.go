package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// RateLimiter es una ventana fija por identidad (IP o usuario) anclada en su primera
// peticion: admite rate peticiones hasta que la ventana vence y responde 429 con
// Retry-After hasta el final de esa ventana.
//
// Sin almacen compartido (NewRateLimiter) cuenta en la memoria del proceso: cada replica
// lleva su propio cupo. Con almacen (NewSharedRateLimiter) el cupo es uno solo para todas
// las replicas y la memoria queda como respaldo cuando el almacen no responde.
type RateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	rate     int
	window   time.Duration
	now      func() time.Time
	shared   *sharedLimit
}

type visitor struct {
	count int64
	start time.Time
}

// minWindow acota por abajo la ventana del limitador. Una ventana expresada como numero
// suelto (NewRateLimiter(100, 60)) no son 60 segundos sino 60 NANOSEGUNDOS: el bucle de
// limpieza pasa a girar millones de veces por segundo tomando el mutex, y el proceso se
// come un nucleo entero sin hacer nada. Ocurrio: un servicio estuvo 30 dias al 100% de CPU
// por ese unico caracter. Ademas una ventana asi vuelve inutil el limite, porque cada
// visitante caduca antes de la siguiente peticion.
const minWindow = time.Second

const (
	// sharedTimeout acota lo que una peticion espera al almacen: el limitador va delante
	// de cada llamada al API y un Redis lento no puede convertirse en la latencia de todos.
	sharedTimeout = 250 * time.Millisecond
	// sharedCooldown es cuanto se deja de consultar el almacen tras un fallo. Sin el, con
	// Redis caido cada peticion pagaria su timeout antes de caer a memoria.
	sharedCooldown = 5 * time.Second
	// degradedLogInterval espacia el aviso de degradacion: con Redis caido se decide en
	// memoria miles de veces por minuto y el registro no debe crecer al mismo ritmo.
	degradedLogInterval = time.Minute
)

// rateLimitDegraded cuenta las decisiones tomadas en memoria porque el almacen compartido
// no respondia: mientras crece, el cupo de cada limitador se multiplica por el numero de
// replicas. La etiqueta es el nombre del limitador, nunca la identidad limitada.
var rateLimitDegraded = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "rate_limit_degraded_total",
	Help: "Decisiones del limitador de peticiones tomadas en la memoria del proceso porque el almacen compartido no respondia.",
}, []string{"limiter"})

func init() { prometheus.MustRegister(rateLimitDegraded) }

// RateLimitStore es el contador compartido entre replicas. Hit suma una peticion a key y
// devuelve el total de la ventana en curso y cuanto le queda. La ventana la abre la primera
// peticion y no se prolonga con las siguientes; la caducidad la aplica el almacen, de modo
// que todas las replicas ven el mismo final aunque sus relojes difieran.
type RateLimitStore interface {
	Hit(ctx context.Context, key string, window time.Duration) (count int64, reset time.Duration, err error)
}

type sharedLimit struct {
	store     RateLimitStore
	name      string
	prefix    string
	logger    *zap.Logger
	degraded  prometheus.Counter
	downUntil atomic.Int64
	lastLog   atomic.Int64
	isDown    atomic.Bool
}

var limiterNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*(:[a-z0-9_-]+)*$`)

func NewRateLimiter(rate int, window time.Duration) *RateLimiter {
	return newRateLimiter(rate, window, time.Now)
}

// newRateLimiter fija el reloj antes de arrancar la limpieza, que tambien lo lee.
func newRateLimiter(rate int, window time.Duration, now func() time.Time) *RateLimiter {
	if window < minWindow {
		window = minWindow
	}
	rl := &RateLimiter{
		visitors: make(map[string]*visitor),
		rate:     rate,
		window:   window,
		now:      now,
	}
	go rl.cleanup()
	return rl
}

// NewSharedRateLimiter es NewRateLimiter con el cupo en store, comun a todas las replicas.
// name identifica al limitador (p. ej. "gateway:auth") y forma la clave de cada identidad
// (rl:<name>:ip:<ip> o rl:<name>:u:<sha256 del usuario>): dos limitadores con el mismo
// nombre comparten cupo aunque vivan en procesos distintos.
//
// Politica ante el almacen caido: la decision se toma en la memoria del proceso con el
// mismo cupo y la misma ventana, que siempre va contando en paralelo. Nunca se deja pasar
// sin limite y nunca se rechaza por no poder contar; lo que se pierde mientras dura es la
// suma entre replicas (cada una vuelve a su propio cupo). Se cuenta en
// rate_limit_degraded_total y se avisa en el registro como mucho una vez por minuto.
func NewSharedRateLimiter(store RateLimitStore, name string, rate int, window time.Duration, logger *zap.Logger) *RateLimiter {
	return newSharedRateLimiter(store, name, rate, window, logger, time.Now)
}

func newSharedRateLimiter(store RateLimitStore, name string, rate int, window time.Duration, logger *zap.Logger, now func() time.Time) *RateLimiter {
	if store == nil {
		panic("middleware: NewSharedRateLimiter sin almacen")
	}
	if !limiterNameRe.MatchString(name) {
		panic(fmt.Sprintf("middleware: nombre de limitador invalido %q", name))
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	rl := newRateLimiter(rate, window, now)
	rl.shared = &sharedLimit{
		store:    store,
		name:     name,
		prefix:   "rl:" + name + ":",
		logger:   logger,
		degraded: rateLimitDegraded.WithLabelValues(name),
	}
	return rl
}

// LimitPerUser acota por IDENTIDAD y no por direccion de salida.
//
// El limite por IP es correcto para lo que no esta autenticado, pero dentro de
// una oficina todo el mundo comparte una IP publica: el cupo se reparte entre
// todos y basta con que unas cuantas personas trabajen a la vez para que se
// devuelvan 429 sin que nadie haya hecho nada anomalo. Es una denegacion de
// servicio que se causa la propia empresa.
//
// Cuando no hay usuario en el contexto -- rutas publicas, servicio a servicio --
// se cae a la IP, que ahi si es la unica identidad disponible.
func (rl *RateLimiter) LimitPerUser(next http.Handler) http.Handler {
	return rl.middleware(next, func(r *http.Request) string {
		if uid := GetUserID(r.Context()); uid != "" {
			return "u:" + digest(uid)
		}
		if keyID := GetAPIKeyID(r.Context()); keyID != "" {
			return "k:" + digest(keyID)
		}
		return ipIdentity(extractIP(r))
	})
}

func (rl *RateLimiter) Limit(next http.Handler) http.Handler {
	return rl.middleware(next, func(r *http.Request) string {
		return ipIdentity(extractIP(r))
	})
}

func (rl *RateLimiter) middleware(next http.Handler, identity func(*http.Request) string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed, reset := rl.decide(r.Context(), identity(r))
		if !allowed {
			w.Header().Set("Retry-After", retryAfterSeconds(reset))
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// AllowIP cuenta una operacion de la IP y dice si cabe en el cupo, con cuanto falta para que
// la ventana se reabra. Es Limit para quien decide dentro del handler (una respuesta que no es
// JSON, un cupo que solo cuenta cierto tipo de peticion); la IP se agrupa igual (IPv6 por /64).
func (rl *RateLimiter) AllowIP(ctx context.Context, ip string) (bool, time.Duration) {
	return rl.decide(ctx, ipIdentity(ip))
}

// AllowKey es AllowIP para una identidad que no es una IP (un formulario, un token de un solo
// uso). La clave viaja resumida al almacen, como la de un usuario.
func (rl *RateLimiter) AllowKey(ctx context.Context, key string) (bool, time.Duration) {
	return rl.decide(ctx, "k:"+digest(key))
}

// decide es observe con el veredicto del cupo: permitida mientras el total de la ventana
// no lo supere.
func (rl *RateLimiter) decide(ctx context.Context, identity string) (bool, time.Duration) {
	count, reset := rl.observe(ctx, identity)
	return count <= int64(rl.rate), reset
}

// CountPerUser suma una operacion del usuario y devuelve cuantas lleva en la ventana en
// curso, sin limitar nada: es el contador de un detector, no una barrera. Comparte con
// LimitPerUser el almacen, la ventana anclada en la primera operacion y la degradacion a
// memoria (mismo nombre de limitador, misma metrica), de modo que con el almacen caido
// cada replica cuenta lo suyo en lugar de dejar de contar.
func (rl *RateLimiter) CountPerUser(ctx context.Context, userID string) int64 {
	count, _ := rl.observe(ctx, "u:"+digest(userID))
	return count
}

// observe cuenta la peticion en memoria siempre y, si hay almacen disponible, devuelve el
// total del almacen. Contar en memoria aunque cuente el almacen hace que, si este cae,
// cada replica arranque con lo que ya vio y no con un cupo nuevo.
func (rl *RateLimiter) observe(ctx context.Context, identity string) (int64, time.Duration) {
	now := rl.now()
	localCount, localReset := rl.countLocal(identity, now)
	s := rl.shared
	if s == nil {
		return localCount, localReset
	}
	if now.UnixNano() >= s.downUntil.Load() {
		// Sin la cancelacion del cliente: un cliente que corta no es un almacen caido.
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sharedTimeout)
		count, reset, err := s.store.Hit(sctx, s.prefix+identity, rl.window)
		cancel()
		if err == nil {
			if s.isDown.Swap(false) {
				s.logger.Info("limitador de peticiones: el almacen compartido vuelve a responder", zap.String("limiter", s.name))
			}
			if reset <= 0 || reset > rl.window {
				reset = rl.window
			}
			return count, reset
		}
		s.downUntil.Store(now.Add(sharedCooldown).UnixNano())
		s.isDown.Store(true)
		s.warn(now, err)
	}
	s.degraded.Inc()
	return localCount, localReset
}

func (s *sharedLimit) warn(now time.Time, err error) {
	last := s.lastLog.Load()
	if now.UnixNano()-last < int64(degradedLogInterval) || !s.lastLog.CompareAndSwap(last, now.UnixNano()) {
		return
	}
	s.logger.Warn("limitador de peticiones: almacen compartido no disponible, se decide en memoria del proceso (cupo por replica)",
		zap.String("limiter", s.name), zap.Duration("retry_in", sharedCooldown), zap.Error(err))
}

// countLocal suma la peticion en la memoria del proceso y devuelve el total de la ventana.
// No se detiene en el cupo: quien decide compara el total con el cupo.
func (rl *RateLimiter) countLocal(key string, now time.Time) (int64, time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	v, exists := rl.visitors[key]
	if !exists || now.Sub(v.start) >= rl.window {
		rl.visitors[key] = &visitor{count: 1, start: now}
		return 1, rl.window
	}
	v.count++
	return v.count, v.start.Add(rl.window).Sub(now)
}

func (rl *RateLimiter) cleanup() {
	for {
		time.Sleep(rl.window * 2)
		now := rl.now()
		rl.mu.Lock()
		for key, v := range rl.visitors {
			if now.Sub(v.start) > rl.window*2 {
				delete(rl.visitors, key)
			}
		}
		rl.mu.Unlock()
	}
}

// retryAfterSeconds redondea hacia arriba: con 0 el cliente reintentaria dentro de una
// ventana que todavia esta cerrada.
func retryAfterSeconds(reset time.Duration) string {
	secs := int64(math.Ceil(reset.Seconds()))
	if secs < 1 {
		secs = 1
	}
	return strconv.FormatInt(secs, 10)
}

// ipIdentity normaliza la IP para la clave. Lo que no es una IP (una cabecera de un
// servicio interno, un RemoteAddr raro) viaja resumido: una clave no puede llevar texto
// arbitrario elegido por quien hace la peticion.
func ipIdentity(raw string) string {
	if ip := net.ParseIP(raw); ip != nil {
		// Un cliente IPv6 suele disponer de un /64 entero: contar por direccion le daria un
		// cupo por cada una. IPv6 se agrupa por /64; IPv4 (tambien la mapeada en IPv6) va
		// por direccion.
		if v4 := ip.To4(); v4 != nil {
			return "ip:" + v4.String()
		}
		return "ip6:" + ip.Mask(net.CIDRMask(ipv6ClientPrefix, 128)).String() + "/64"
	}
	return "id:" + digest(raw)
}

// ipv6ClientPrefix es el prefijo que se asigna normalmente a un solo cliente IPv6.
const ipv6ClientPrefix = 64

// digest resume un identificador para que no quede en claro en el almacen compartido.
func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}

func extractIP(r *http.Request) string {
	// En el gateway CaptureClientIP ya resolvio la IP real del visitante (validando
	// que X-Real-IP venga del proxy de borde de confianza); esa es la autoridad.
	if ip := GetClientIP(r.Context()); ip != "" {
		return ip
	}
	// En los servicios internos X-Real-IP lo pone el gateway (el cliente no puede
	// inyectarlo: el gateway lo consume y lo re-emite desde el contexto).
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return xff[:i]
			}
		}
		return xff
	}
	for i := len(r.RemoteAddr) - 1; i >= 0; i-- {
		if r.RemoteAddr[i] == ':' {
			return r.RemoteAddr[:i]
		}
	}
	return r.RemoteAddr
}

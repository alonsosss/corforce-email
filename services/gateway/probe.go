package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// Deteccion de sondeo de endpoints (docs/Plan_Proteccion_Frente_a_Bots.md, capa 3.2). Una persona
// en la consola no produce decenas de 401, 403, 404 y 405 en pocos minutos; un escaner con una
// credencial valida, si. El guardia cuenta esas respuestas por identidad (el token que presenta la
// peticion, o su IP cuando no trae ninguno) en una ventana, y al pasar el umbral bloquea a esa
// identidad un rato con 429 PROBE_DETECTED, lo cuenta, lo registra y, si la peticion venia con
// sesion verificada, publica el evento que audit convierte en un evento de seguridad de la empresa.
//
// La identidad por token (y no por usuario) es deliberada: el guardia corre por delante de la
// autenticacion para ver tambien las rutas que no existen, que chi despacha sin pasar por los
// grupos autenticados. Un token de acceso dura cinco minutos, la ventana tambien: un escaner que
// renueve el token para esquivar el conteo sigue contando por su IP.
//
// El conteo y el bloqueo viven en el Redis de la plataforma (comunes a todas las replicas) y caen
// a la memoria del proceso si no responde: con Redis caido cada replica cuenta lo suyo, nunca se
// deja de contar ni se bloquea a nadie por error.

// Estados que cuentan como sondeo. Un 400 o un 422 son una peticion mal hecha a una ruta que
// existe; un 429 ya es un freno; un 5xx es nuestro.
var probeStatuses = map[int]struct{}{
	http.StatusUnauthorized: {}, http.StatusForbidden: {}, http.StatusNotFound: {}, http.StatusMethodNotAllowed: {},
}

// probeExempt son los prefijos bajo /api/v1 que no cuentan: el inicio de sesion y sus pasos tienen
// su propio cupo estricto y su bitacora en identity, y un 401 alli es una contrasena mal escrita.
var probeExempt = []string{"/api/v1/auth/"}

const (
	probeModeEnforce = "enforce"
	probeModeObserve = "observe"
)

// probeSettings son los umbrales del guardia, leidos del entorno (main.go).
type probeSettings struct {
	// Mode enforce bloquea; observe solo cuenta y registra (para calibrar sin cortar a nadie).
	Mode string
	// TokenThreshold e IPThreshold son los fallos en la ventana a partir de los cuales se bloquea.
	TokenThreshold int64
	IPThreshold    int64
	Window         time.Duration
	BlockFor       time.Duration
}

// probeStore cuenta fallos y guarda bloqueos. Las claves ya van resumidas.
type probeStore interface {
	// Fail suma un fallo de la identidad y devuelve el total de la ventana en curso.
	Fail(ctx context.Context, key string, window time.Duration) (int64, error)
	// Block deja la identidad bloqueada durante ttl.
	Block(ctx context.Context, key string, ttl time.Duration) error
	// Blocked devuelve cuanto le queda de bloqueo a la identidad, 0 si no esta bloqueada.
	Blocked(ctx context.Context, key string) (time.Duration, error)
}

var (
	probeBlocks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_probe_blocks_total",
		Help: "Identidades bloqueadas por sondeo de endpoints (decenas de 401/403/404/405 en la ventana), por tipo de identidad (token: una sesion o una clave de API; ip: sin credencial) y modo (enforce bloqueo con 429; observe solo se anoto).",
	}, []string{"identity", "mode"})
	probeRejections = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "gateway_probe_rejections_total",
		Help: "Peticiones respondidas con 429 PROBE_DETECTED porque su identidad estaba bloqueada por sondeo.",
	})
	probeStoreFailures = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "gateway_probe_store_failures_total",
		Help: "Operaciones del guardia de sondeo que no pudo hacer en Redis y resolvio en la memoria del proceso.",
	})
)

func init() {
	for _, id := range []string{"token", "ip"} {
		for _, mode := range []string{probeModeEnforce, probeModeObserve} {
			probeBlocks.WithLabelValues(id, mode)
		}
	}
	prometheus.MustRegister(probeBlocks, probeRejections, probeStoreFailures)
}

type probeGuard struct {
	cfg    probeSettings
	store  probeStore
	memory *memoryProbeStore
	bus    trailPublisher
	logger *zap.Logger
	now    func() time.Time
}

func newProbeGuard(cfg probeSettings, store probeStore, bus trailPublisher, logger *zap.Logger) *probeGuard {
	g := &probeGuard{cfg: cfg, store: store, memory: newMemoryProbeStore(time.Now), bus: bus, logger: logger, now: time.Now}
	if g.store == nil {
		g.store = g.memory
	}
	return g
}

// probeState viaja en el contexto de cada peticion: el guardia externo lo crea y el interno
// (identify, dentro del grupo autenticado) le pone la empresa y el usuario verificados, para que
// el evento de un bloqueo lleve a quien es y no lo que diga un token sin comprobar.
type probeState struct {
	identity     string
	kind         string
	ip           string
	tenantID     string
	userID       string
	apiKeyID     string
	countedAsIP  bool
	skipCounting bool
}

type probeStateKey struct{}

// identity resume el token de la peticion; sin token, la IP.
func (g *probeGuard) identity(r *http.Request) (key, kind, ip string) {
	ip = middleware.GetClientIP(r.Context())
	if tok := bearerToken(r); tok != "" {
		return "t:" + probeDigest(tok), "token", ip
	}
	return "ip:" + probeDigest(ip), "ip", ip
}

func probeDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}

func (g *probeGuard) threshold(kind string) int64 {
	if kind == "token" {
		return g.cfg.TokenThreshold
	}
	return g.cfg.IPThreshold
}

// middleware es el guardia externo: rechaza a las identidades bloqueadas y cuenta las
// respuestas de sondeo de las demas.
func (g *probeGuard) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, kind, ip := g.identity(r)
		st := &probeState{identity: key, kind: kind, ip: ip}
		for _, p := range probeExempt {
			if strings.HasPrefix(r.URL.Path, p) {
				st.skipCounting = true
			}
		}
		ctx := context.WithValue(r.Context(), probeStateKey{}, st)
		r = r.WithContext(ctx)

		if g.cfg.Mode == probeModeEnforce {
			// El token y, con token, tambien su IP: renovar el token no levanta el bloqueo de la IP.
			remaining := g.blocked(ctx, key)
			if kind == "token" {
				if byIP := g.blocked(ctx, "ip:"+probeDigest(ip)); byIP > remaining {
					remaining = byIP
				}
			}
			if remaining > 0 {
				probeRejections.Inc()
				w.Header().Set("Retry-After", probeRetryAfter(remaining))
				response.Err(w, http.StatusTooManyRequests, "PROBE_DETECTED",
					"demasiadas peticiones a recursos inexistentes o no permitidos; la credencial queda en pausa unos minutos")
				return
			}
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if st.skipCounting {
			return
		}
		if _, counts := probeStatuses[rec.status]; !counts {
			return
		}
		g.countFailure(ctx, st, r, rec.status)
	})
}

// identify es el guardia interno: anota en el estado la identidad verificada de la peticion.
func (g *probeGuard) identify(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if st, ok := r.Context().Value(probeStateKey{}).(*probeState); ok {
			st.tenantID = middleware.GetTenantID(r.Context())
			st.userID = middleware.GetUserID(r.Context())
			st.apiKeyID = middleware.GetAPIKeyID(r.Context())
		}
		next.ServeHTTP(w, r)
	})
}

// countFailure suma el fallo a la identidad y, con token, tambien a la IP: renovar el token no
// reinicia el conteo. Al alcanzar el umbral bloquea y avisa una sola vez por ventana.
func (g *probeGuard) countFailure(ctx context.Context, st *probeState, r *http.Request, status int) {
	keys := []struct{ key, kind string }{{st.identity, st.kind}}
	if st.kind == "token" {
		keys = append(keys, struct{ key, kind string }{"ip:" + probeDigest(st.ip), "ip"})
	}
	for _, k := range keys {
		n, err := g.store.Fail(ctx, k.key, g.cfg.Window)
		if err != nil {
			probeStoreFailures.Inc()
			n, _ = g.memory.Fail(ctx, k.key, g.cfg.Window)
		}
		if n != g.threshold(k.kind) {
			// Solo en el fallo exacto del umbral: los siguientes de la misma ventana ya estan
			// bloqueados (enforce) o ya avisaron (observe).
			continue
		}
		if g.cfg.Mode == probeModeEnforce {
			if err := g.store.Block(ctx, k.key, g.cfg.BlockFor); err != nil {
				probeStoreFailures.Inc()
				_ = g.memory.Block(ctx, k.key, g.cfg.BlockFor)
			}
		}
		probeBlocks.WithLabelValues(k.kind, g.cfg.Mode).Inc()
		g.logger.Warn("sondeo de endpoints detectado",
			zap.String("identity", k.kind), zap.String("mode", g.cfg.Mode), zap.String("ip", st.ip),
			zap.String("tenant_id", st.tenantID), zap.String("user_id", st.userID), zap.String("api_key_id", st.apiKeyID),
			zap.Int64("failures", n), zap.Duration("window", g.cfg.Window), zap.Duration("blocked_for", g.cfg.BlockFor),
			zap.String("last_path", r.URL.Path), zap.Int("last_status", status), zap.String("user_agent", truncateUA(r.UserAgent())))
		g.publish(st, k.kind, n, r)
	}
}

// probeRetryAfter redondea hacia arriba: con 0 el cliente reintentaria dentro del bloqueo.
func probeRetryAfter(remaining time.Duration) string {
	secs := int64(math.Ceil(remaining.Seconds()))
	if secs < 1 {
		secs = 1
	}
	return strconv.FormatInt(secs, 10)
}

func truncateUA(ua string) string {
	if len(ua) > 200 {
		return ua[:200]
	}
	return ua
}

// blocked consulta el bloqueo; con Redis caido, la memoria de esta replica.
func (g *probeGuard) blocked(ctx context.Context, key string) time.Duration {
	remaining, err := g.store.Blocked(ctx, key)
	if err != nil {
		probeStoreFailures.Inc()
		remaining, _ = g.memory.Blocked(ctx, key)
	}
	return remaining
}

// publish emite el evento con la identidad verificada; sin empresa no hay a quien atribuirlo y
// queda solo el registro y la metrica.
func (g *probeGuard) publish(st *probeState, kind string, failures int64, r *http.Request) {
	if g.bus == nil || st.tenantID == "" {
		return
	}
	data := map[string]interface{}{
		"tenant_id":   st.tenantID,
		"user_id":     st.userID,
		"ip":          st.ip,
		"identity":    kind,
		"mode":        g.cfg.Mode,
		"failures":    failures,
		"window":      g.cfg.Window.String(),
		"blocked_for": g.cfg.BlockFor.String(),
		"last_path":   r.URL.Path,
	}
	if st.apiKeyID != "" {
		data["api_key_id"] = st.apiKeyID
	}
	evt := events.Event{Type: "user.probe_detected", Source: "gateway", TenantID: st.tenantID, UserID: st.userID, Data: data}
	go func() {
		defer func() { _ = recover() }()
		if err := g.bus.Publish("gateway.security.probe", evt); err != nil {
			g.logger.Warn("evento de sondeo no publicado", zap.Error(err))
		}
	}()
}

// ── Almacenes ─────────────────────────────────────────────────────────────────

// probeFailScript suma el fallo y abre la ventana en una sola operacion (como hitScript del
// limitador): sin ventana no hay clave eterna, y las siguientes no la prolongan.
var probeFailScript = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
if redis.call('PTTL', KEYS[1]) < 0 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return n
`)

// redisProbeStore es el almacen comun a las replicas.
type redisProbeStore struct {
	rdb    redis.UniversalClient
	prefix string
}

func newRedisProbeStore(rdb redis.UniversalClient) *redisProbeStore {
	return &redisProbeStore{rdb: rdb, prefix: "probe:gateway:"}
}

func (s *redisProbeStore) Fail(ctx context.Context, key string, window time.Duration) (int64, error) {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sharedProbeTimeout)
	defer cancel()
	return probeFailScript.Run(sctx, s.rdb, []string{s.prefix + "f:" + key}, window.Milliseconds()).Int64()
}

func (s *redisProbeStore) Block(ctx context.Context, key string, ttl time.Duration) error {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sharedProbeTimeout)
	defer cancel()
	return s.rdb.Set(sctx, s.prefix+"b:"+key, "1", ttl).Err()
}

func (s *redisProbeStore) Blocked(ctx context.Context, key string) (time.Duration, error) {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sharedProbeTimeout)
	defer cancel()
	ttl, err := s.rdb.PTTL(sctx, s.prefix+"b:"+key).Result()
	if err != nil {
		return 0, err
	}
	if ttl <= 0 {
		return 0, nil
	}
	return ttl, nil
}

// sharedProbeTimeout acota lo que una peticion espera al Redis: el guardia va delante de todo.
const sharedProbeTimeout = 250 * time.Millisecond

// memoryProbeStore es el respaldo por replica y el almacen de las pruebas.
type memoryProbeStore struct {
	mu     sync.Mutex
	fails  map[string]*probeWindow
	blocks map[string]time.Time
	now    func() time.Time
}

type probeWindow struct {
	count int64
	until time.Time
}

func newMemoryProbeStore(now func() time.Time) *memoryProbeStore {
	return &memoryProbeStore{fails: map[string]*probeWindow{}, blocks: map[string]time.Time{}, now: now}
}

func (m *memoryProbeStore) Fail(_ context.Context, key string, window time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	w, ok := m.fails[key]
	if !ok || !now.Before(w.until) {
		m.fails[key] = &probeWindow{count: 1, until: now.Add(window)}
		m.sweep(now)
		return 1, nil
	}
	w.count++
	return w.count, nil
}

func (m *memoryProbeStore) Block(_ context.Context, key string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blocks[key] = m.now().Add(ttl)
	return nil
}

func (m *memoryProbeStore) Blocked(_ context.Context, key string) (time.Duration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	until, ok := m.blocks[key]
	if !ok {
		return 0, nil
	}
	if remaining := until.Sub(m.now()); remaining > 0 {
		return remaining, nil
	}
	delete(m.blocks, key)
	return 0, nil
}

// sweep retira lo caducado al abrir una ventana nueva: la memoria no crece con identidades que
// ya no vuelven. Se llama con el mutex tomado.
func (m *memoryProbeStore) sweep(now time.Time) {
	if len(m.fails)%256 != 0 {
		return
	}
	for k, w := range m.fails {
		if !now.Before(w.until) {
			delete(m.fails, k)
		}
	}
	for k, until := range m.blocks {
		if !now.Before(until) {
			delete(m.blocks, k)
		}
	}
}

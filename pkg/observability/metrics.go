// Package observability expone la instrumentacion comun de los servicios de la plataforma:
// metricas de proceso y de trafico HTTP en formato Prometheus.
//
// La plataforma corre mas de cien contenedores; sin series temporales la unica forma de
// diagnosticar una degradacion es leer logs contenedor por contenedor. Por eso la
// instrumentacion se engancha en un unico punto (pkg/server), no servicio por servicio:
// un microservicio nuevo queda medido por el solo hecho de arrancar con el servidor comun.
//
// El endpoint /metrics se sirve en el mismo puerto del servicio pero FUERA de su cadena de
// middlewares, de modo que el recolector no necesita token de gateway ni sesion. Los
// puertos de servicio solo se publican en la interfaz de loopback del host y en la red
// interna de Docker: quien puede leer /metrics ya tiene acceso al host.
package observability

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// MetricsPath y HealthPath son las rutas operativas que sirve pkg/server. Se declaran
// aqui para que el recolector, el healthcheck del contenedor y el servidor compartan una
// sola definicion.
const (
	MetricsPath = "/metrics"
	HealthPath  = "/healthz"
)

// La identidad del servicio NO viaja como etiqueta en la metrica: la aporta el recolector
// desde la definicion del objetivo (ops/observability/prometheus/targets.json). Es la
// convencion de Prometheus y ademas evita la colision que renombraria la etiqueta del
// servicio a "exported_service" al mezclarse con la del objetivo.
var (
	requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Peticiones HTTP atendidas, por metodo, patron de ruta y codigo de estado.",
	}, []string{"method", "route", "status"})

	duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "http_request_duration_seconds",
		Help: "Latencia de las peticiones HTTP atendidas.",
		// Cubre desde una lectura cacheada (5ms) hasta un reporte contable pesado (10s).
		Buckets: []float64{0.005, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"method", "route"})

	inFlight = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "http_requests_in_flight",
		Help: "Peticiones HTTP en curso.",
	})

	// Bytes servidos. Es la medida que factura el borde -Cloudflare Argo cobra por byte
	// que llega al origen- y la unica forma de saber que pantalla esta transfiriendo de
	// mas antes de verlo en la factura.
	//
	// Solo se cuenta el CUERPO de la respuesta, no las cabeceras: es lo que el
	// ResponseWriter sabe medir sin envolver la conexion entera, y la diferencia es
	// despreciable frente al cuerpo en cualquier respuesta que importe.
	responseBytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_response_bytes_total",
		Help: "Bytes de cuerpo servidos, por metodo y patron de ruta.",
	}, []string{"method", "route"})

	// Bytes recibidos. Sube con las importaciones de Excel y las subidas de archivo, que
	// es donde el consumo se dispara sin que nadie lo note.
	requestBytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_request_bytes_total",
		Help: "Bytes de cuerpo recibidos, por metodo y patron de ruta.",
	}, []string{"method", "route"})

	// Bytes de WebSocket. Van aparte porque el middleware HTTP no puede verlos: al hacer
	// upgrade se SECUESTRA la conexion, y desde ese momento los frames no pasan por el
	// ResponseWriter. La medida HTTP contaba el saludo inicial y ni un byte de la
	// conversacion, que en una plataforma con una conexion viva por usuario es justo el trafico
	// que se acumula sin aparecer en ningun sitio.
	//
	// Los cuenta quien maneja el socket, llamando a WSBytes.
	wsBytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ws_bytes_total",
		Help: "Bytes de WebSocket, por direccion (in/out).",
	}, []string{"direction"})
)

// WSBytes suma bytes de una conexion WebSocket ya establecida. direction es "in" o "out".
//
// Se expone como funcion y no como middleware a proposito: solo quien tiene el socket en la
// mano sabe cuanto escribio de verdad, y envolver la conexion entera para averiguarlo
// meteria una capa en el camino critico del tiempo real.
func WSBytes(direction string, n int) {
	if n <= 0 {
		return
	}
	wsBytes.WithLabelValues(direction).Add(float64(n))
}

func init() {
	prometheus.MustRegister(requests, duration, inFlight, responseBytes, requestBytes, wsBytes)
}

// ServiceName resuelve el nombre con el que el servicio se identifica en las metricas.
// Por defecto es el nombre del binario (identico al del servicio en Compose y ECR);
// SERVICE_NAME lo sobrescribe cuando un binario sirve a mas de un despliegue.
func ServiceName() string {
	if v := strings.TrimSpace(os.Getenv("SERVICE_NAME")); v != "" {
		return v
	}
	if len(os.Args) > 0 {
		if base := filepath.Base(os.Args[0]); base != "" && base != "." && base != "/" {
			return base
		}
	}
	return "unknown"
}

// Handler sirve el registro de metricas en formato de exposicion Prometheus.
func Handler() http.Handler {
	return promhttp.Handler()
}

// HTTPMetrics mide cada peticion atendida. Se coloca por fuera del router del servicio;
// para poder etiquetar por PATRON de ruta (y no por URL concreta, que dispararia la
// cardinalidad con un identificador distinto por peticion) siembra el contexto de ruteo
// de chi antes de delegar y lee el patron ya resuelto al volver.
func HTTPMetrics() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			// Sembrar el contexto de ruteo: chi lo reutiliza en vez de crear el suyo, asi
			// que al volver trae el patron que resolvio (p. ej. "/loans/{id}/payments").
			rctx := chi.NewRouteContext()
			r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			inFlight.Inc()
			defer inFlight.Dec()

			next.ServeHTTP(ww, r)

			route := routeLabel(rctx, r.URL.Path)
			requests.WithLabelValues(r.Method, route, strconv.Itoa(status(ww))).Inc()
			// Una conexion WebSocket no es una peticion que tarda: es una conversacion que
			// dura, y el handler no vuelve hasta que se cierra. Medirla como latencia mete
			// horas en el histograma y deja al servicio pegado al ultimo bucket mientras
			// haya un equipo conectado: en el servicio de balanzas el 100% de las
			// observaciones caia en +Inf, la p95 marcaba 10s con el servicio sano y
			// LatenciaAlta no podia apagarse. Una alerta que no puede apagarse se aprende
			// a ignorar, y con ella las de al lado.
			//
			// La conexion SI se cuenta -saber cuantas se abren es util-; lo que no se
			// cuenta es su duracion. Los bytes de la conversacion los lleva WSBytes, que
			// existe porque el upgrade secuestra la conexion y este envoltorio ya no la ve.
			if esUpgradeDeWebSocket(r) {
				return
			}
			// Un flujo de eventos (SSE) es lo mismo sin secuestrar la conexion: el handler
			// escribe hasta que el servicio lo cierra a proposito. El canal en tiempo real del
			// webmail dejo LatenciaAlta encendida en webmail y en el gateway con el servicio
			// sano. Sus bytes si pasan por el ResponseWriter y se siguen contando.
			if !esFlujoDeEventos(ww) {
				duration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
			}
			responseBytes.WithLabelValues(r.Method, route).Add(float64(ww.BytesWritten()))
			// ContentLength es -1 cuando el cuerpo llega troceado (subidas grandes); en ese
			// caso se omite en vez de sumar basura, que falsearia el total hacia abajo de
			// forma silenciosa.
			if r.ContentLength > 0 {
				requestBytes.WithLabelValues(r.Method, route).Add(float64(r.ContentLength))
			}
		})
	}
}

// esUpgradeDeWebSocket reconoce el handshake por las cabeceras de la PETICION, no por el
// codigo de respuesta: cuando la libreria toma la conexion, el envoltorio deja de ver lo
// que se escribe y el estado que reporta ya no es de fiar. Las dos cabeceras se comparan
// sin distinguir mayusculas y "Connection" puede traer varios valores separados por comas
// (por ejemplo "keep-alive, Upgrade"), que es como las manda algun proxy.
func esUpgradeDeWebSocket(r *http.Request) bool {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	for _, v := range strings.Split(r.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(v), "upgrade") {
			return true
		}
	}
	return false
}

// esFlujoDeEventos mira el Content-Type de la RESPUESTA: lo fija quien sirve el flujo, y el
// gateway lo copia al reenviarlo, asi que vale igual en el servicio y en el borde sin depender
// de lo que el cliente anuncie en Accept.
func esFlujoDeEventos(ww chimw.WrapResponseWriter) bool {
	media, _, _ := strings.Cut(ww.Header().Get("Content-Type"), ";")
	return strings.EqualFold(strings.TrimSpace(media), "text/event-stream")
}

func status(ww chimw.WrapResponseWriter) int {
	if s := ww.Status(); s != 0 {
		return s
	}
	// Un handler que escribe cuerpo sin fijar codigo responde 200 implicito.
	return http.StatusOK
}

func routeLabel(rctx *chi.Context, path string) string {
	if rctx != nil {
		if p := rctx.RoutePattern(); p != "" && p != "/*" {
			return p
		}
	}
	return sanitizePath(path)
}

var (
	uuidSegment    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	numericSegment = regexp.MustCompile(`^\d+$`)
)

// sanitizePath es el respaldo para handlers que no son un router chi: colapsa los
// segmentos que son identificadores para que la etiqueta siga siendo un patron acotado.
func sanitizePath(path string) string {
	segments := strings.Split(path, "/")
	// Un path con demasiados segmentos no es una ruta de la API; se agrupa para no crear
	// una serie por cada URL rara que llegue desde internet.
	if len(segments) > 12 {
		return "/other"
	}
	for i, s := range segments {
		if uuidSegment.MatchString(s) || numericSegment.MatchString(s) {
			segments[i] = ":id"
		}
	}
	return strings.Join(segments, "/")
}

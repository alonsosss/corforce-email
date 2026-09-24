package middleware

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/auth"
	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type contextKey string

const (
	CtxUserID contextKey = "user_id"
	// CtxCSPNonce lleva el nonce de la respuesta en curso hasta quien sirve el HTML.
	CtxCSPNonce  contextKey = "csp_nonce"
	CtxTenantID  contextKey = "tenant_id"
	CtxRoles     contextKey = "roles"
	CtxRequestID contextKey = "request_id"
	CtxClientIP  contextKey = "client_ip"
	// CtxTokenIssuedAt lleva el iat (epoch unix) del access token, para que el gateway
	// pueda rechazar tokens emitidos antes del epoch de revocacion del usuario.
	CtxTokenIssuedAt contextKey = "token_iat"
)

// HeaderOperatorCell lleva a un servicio de celda la celda destino que el gateway valido para
// un operador de la plataforma (tenantcell.Membership). Solo la escribe el gateway.
const HeaderOperatorCell = "X-Operator-Cell"

var internalHeaders = []string{
	"X-User-ID",
	"X-Tenant-ID",
	"X-User-Roles",
	"X-Gateway-Token",
	HeaderOperatorCell,
	HeaderAPIKeyID,
	HeaderAPIKeyScopes,
	// Token servicio-a-servicio: jamas debe llegar desde un cliente externo.
	"X-Internal-Token",
	// X-Real-IP y X-Forwarded-For NO se borran aqui: los evalua y consume
	// CaptureClientIP, que distingue si vienen del proxy de borde de confianza
	// (IP real del visitante) o de un cliente que intenta suplantar su IP.
}

// StripInternalHeaders removes trusted internal headers from client requests
// before any processing. This prevents header injection from external clients.
func StripInternalHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, h := range internalHeaders {
			r.Header.Del(h)
		}
		next.ServeHTTP(w, r)
	})
}

// BodyLimit caps the request body size to guard against memory-exhaustion DoS
// from oversized payloads. Handlers that need a stricter per-route limit can
// still wrap r.Body again; the tighter limit wins.
func BodyLimit(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CaptureClientIP stores the real client IP in the request context so downstream
// handlers and proxy directors can retrieve it.
//
// En produccion el gateway vive detras del edge-proxy (nginx), asi que RemoteAddr es
// la IP interna del contenedor del edge, no la del visitante. El edge propaga la IP
// real en X-Real-IP; ese header solo se acepta cuando la conexion entra desde un CIDR
// de confianza (la red interna donde vive el edge). Un cliente externo que envie
// X-Real-IP no puede suplantar su IP: su RemoteAddr es publica y el header se ignora
// y se descarta. Tras evaluar, el middleware consume X-Real-IP/X-Forwarded-For del
// request; el director del proxy re-emite X-Real-IP desde el contexto.
func CaptureClientIP(trustedProxies []*net.IPNet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := remoteHost(r.RemoteAddr)
			if fromTrustedProxy(ip, trustedProxies) {
				if real := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); real != nil {
					ip = real.String()
				}
			}
			r.Header.Del("X-Real-IP")
			r.Header.Del("X-Forwarded-For")
			ctx := context.WithValue(r.Context(), CtxClientIP, ip)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// remoteHost extrae el host de un RemoteAddr "host:puerto" (soporta IPv6 entre corchetes).
func remoteHost(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

func fromTrustedProxy(ip string, trusted []*net.IPNet) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, cidr := range trusted {
		if cidr.Contains(parsed) {
			return true
		}
	}
	return false
}

// TrustedProxyCIDRs interpreta la env TRUSTED_PROXY_CIDRS (lista separada por comas).
// Sin configurar, el default son los rangos privados RFC1918 + loopback: cubren la red
// interna de Docker donde corre el edge-proxy y no incluyen ninguna IP publica, de modo
// que un cliente de internet nunca califica como proxy de confianza.
func TrustedProxyCIDRs(env string) []*net.IPNet {
	spec := env
	if strings.TrimSpace(spec) == "" {
		spec = "10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,127.0.0.0/8,::1/128"
	}
	var nets []*net.IPNet
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, cidr, err := net.ParseCIDR(part); err == nil {
			nets = append(nets, cidr)
		}
	}
	return nets
}

func GetClientIP(ctx context.Context) string {
	v, _ := ctx.Value(CtxClientIP).(string)
	return v
}

// maxRequestIDLen: el identificador llega al rastro de auditoria, cuya columna es varchar(100).
const maxRequestIDLen = 64

// validRequestID admite lo que producen un UUID o un proxy (letras, digitos, '-', '_', '.'): el
// identificador lo elige el cliente y viaja al registro de acceso, a la cabecera de respuesta y al
// apunte de auditoria de cada escritura.
func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !validRequestID(id) {
			id = uuid.New().String()
		}
		ctx := context.WithValue(r.Context(), CtxRequestID, id)
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func Logger(logger *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			logger.Info("request",
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.Int("status", ww.Status()),
				zap.Duration("duration", time.Since(start)),
				zap.String("request_id", GetRequestID(r.Context())),
			)
		})
	}
}

// CSPNonce es el nonce de la respuesta en curso: quien sirve HTML lo necesita para
// marcar los <script> propios. Vacio cuando la respuesta no lleva nonce.
func CSPNonce(ctx context.Context) string {
	if v, ok := ctx.Value(CtxCSPNonce).(string); ok {
		return v
	}
	return ""
}

// newCSPNonce genera el valor de un solo uso. Con nonce, un script solo se ejecuta si
// lleva el valor que el servidor acaba de sortear: un script inyectado por un atacante no
// puede adivinarlo, y ya no basta con conseguir que el codigo salga del propio dominio.
func newCSPNonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Sin aleatoriedad no hay nonce que valga: se responde sin el, y la politica
		// cae a la lista de origenes (mas debil, pero nunca a 'unsafe-inline').
		return ""
	}
	return base64.RawStdEncoding.EncodeToString(b[:])
}

func SecureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "0")
		connectSrc := "connect-src 'self' blob:"
		if apiOrigin := strings.TrimSpace(os.Getenv("API_ORIGIN")); apiOrigin != "" {
			connectSrc += " " + apiOrigin
		}
		// script-src con nonce y 'strict-dynamic': la forma estricta de CSP.
		//
		// Sin 'unsafe-inline' ni 'unsafe-eval' —son justo lo que convierte un XSS en
		// ejecucion de codigo— y ademas SIN confiar en el origen: con 'strict-dynamic' el
		// navegador ignora la lista de origenes y solo ejecuta lo que lleve el nonce de
		// esta respuesta, mas lo que esos scripts carguen (los chunks que la aplicacion web
		// carga con import()). Un archivo .js subido al propio dominio deja de ejecutarse solo.
		//
		// Un CDN delante que inyecte scripts (la deteccion de bots de Cloudflare, por
		// ejemplo) tiene que marcarlos con este mismo nonce; ningun otro inline se admite.
		//
		// style-src conserva 'unsafe-inline' a proposito: el sistema de diseño inyecta
		// estilos y la alternativa (nonce en cada regla) no compensa; un estilo inyectado
		// no ejecuta codigo.
		//
		// object-src, base-uri y form-action cierran los tres desvios clasicos que quedan
		// cuando script-src ya esta cerrado: plugins, reescritura de la base de URLs
		// relativas y envio de formularios a un tercero.
		nonce := newCSPNonce()
		scriptSrc := "script-src 'self'; "
		if nonce != "" {
			r = r.WithContext(context.WithValue(r.Context(), CtxCSPNonce, nonce))
			scriptSrc = "script-src 'nonce-" + nonce + "' 'strict-dynamic' https: 'self'; "
		}
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; "+
				scriptSrc+
				"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
				"font-src 'self' https://fonts.gstatic.com; "+
				// blob: lo necesitan los adjuntos e imagenes que el navegador arma en
				// memoria a partir de una respuesta autenticada: no se pueden pedir
				// por <img src> porque llevan cabecera de sesion. Un blob solo existe
				// dentro de la propia pagina, asi que no amplia la exposicion.
				"img-src 'self' data: blob: https:; "+
				"media-src 'self' data: https:; "+
				connectSrc+"; "+
				"frame-src 'self' https:; "+
				"object-src 'none'; "+
				"base-uri 'self'; "+
				"form-action 'self'; "+
				"frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		// Cámara y geolocalización habilitadas solo para el propio origen (self):
		// las usa la asistencia por QR (escaneo + geocerca GPS). El navegador
		// igualmente pide permiso explícito al usuario.
		w.Header().Set("Permissions-Policy", "camera=(self), microphone=(self), geolocation=(self)")
		next.ServeHTTP(w, r)
	})
}

// JWTAuth autentica el token de acceso con las claves PUBLICAS de identity: quien verifica
// no tiene con que firmar, asi que un servicio comprometido no puede forjar una sesion.
type JWTAuth struct {
	verifier *auth.Verifier
}

func NewJWTAuth(verifier *auth.Verifier) *JWTAuth {
	return &JWTAuth{verifier: verifier}
}

func (j *JWTAuth) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if header == "" {
			http.Error(w, `{"error":"missing authorization header"}`, http.StatusUnauthorized)
			return
		}

		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
			http.Error(w, `{"error":"invalid authorization format"}`, http.StatusUnauthorized)
			return
		}

		claims, err := j.verifier.ParseAccess(parts[1])
		if err != nil {
			http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
			return
		}

		ctx := r.Context()
		ctx = context.WithValue(ctx, CtxUserID, claims.UserID)
		ctx = context.WithValue(ctx, CtxTenantID, claims.TenantID)
		ctx = context.WithValue(ctx, CtxRoles, claims.Roles)
		if claims.IssuedAt != nil {
			ctx = context.WithValue(ctx, CtxTokenIssuedAt, claims.IssuedAt.Unix())
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// WithIdentity fija la identidad de un contexto SIN pasar por una petición HTTP.
//
// Es para los trabajos de fondo que actúan en nombre de alguien concreto -- por
// ejemplo el generador de tareas recurrentes, que crea cada tarea con la
// identidad de quien configuró la regla. Importa porque de este contexto salen
// los GUC que leen las políticas RLS: un proceso de fondo sin identidad no ve
// nada y, peor, no falla, simplemente no hace nada.
//
// No inventa roles: quien la use actúa con los permisos de esa persona, no con
// los de un usuario de sistema.
func WithIdentity(ctx context.Context, userID, tenantID string) context.Context {
	return WithTenantID(context.WithValue(ctx, CtxUserID, userID), tenantID)
}

// WithTenantID marca a qué empresa pertenece un contexto sin atribuirlo a nadie.
// Es lo que necesita un barrido por empresas: la identidad de la persona llega
// después, si la operación actúa en nombre de alguien.
func WithTenantID(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, CtxTenantID, tenantID)
}

func GetUserID(ctx context.Context) string {
	v, _ := ctx.Value(CtxUserID).(string)
	return v
}

func GetTenantID(ctx context.Context) string {
	v, _ := ctx.Value(CtxTenantID).(string)
	return v
}

func GetRoles(ctx context.Context) []string {
	v, _ := ctx.Value(CtxRoles).([]string)
	return v
}

func GetRequestID(ctx context.Context) string {
	v, _ := ctx.Value(CtxRequestID).(string)
	return v
}

// GetTokenIssuedAt devuelve el iat (epoch unix) del access token, o 0 si no consta.
func GetTokenIssuedAt(ctx context.Context) int64 {
	v, _ := ctx.Value(CtxTokenIssuedAt).(int64)
	return v
}

// ErrGatewayTokenRequired: sin INTERNAL_GATEWAY_TOKEN fuera de un ENVIRONMENT declarado de
// desarrollo o de prueba.
var ErrGatewayTokenRequired = errors.New(
	"INTERNAL_GATEWAY_TOKEN is required: services only run without it when ENVIRONMENT is development or test")

// InternalGatewayToken lee INTERNAL_GATEWAY_TOKEN, el secreto que el gateway presenta a los
// servicios y con el que estos se llaman entre si. Vacio solo se admite donde
// config.DeclaredDevelopmentOrTest lo permite; en cualquier otro entorno el proceso que lo
// necesita no debe arrancar.
func InternalGatewayToken() (string, error) {
	token := os.Getenv("INTERNAL_GATEWAY_TOKEN")
	if token == "" && !config.DeclaredDevelopmentOrTest() {
		return "", ErrGatewayTokenRequired
	}
	return token, nil
}

// RequireGatewayToken exige el token del gateway en X-Gateway-Token. Sin token configurado
// deja pasar solo con InternalGatewayToken en desarrollo o prueba; fuera, responde 503.
func RequireGatewayToken(next http.Handler) http.Handler {
	expected, err := InternalGatewayToken()
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":"gateway token not configured"}`, http.StatusServiceUnavailable)
		})
	}
	if expected == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actual := r.Header.Get("X-Gateway-Token")
		if subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) != 1 {
			http.Error(w, `{"error":"unauthorized gateway"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireStepUp exige que la peticion traiga un token de step-up valido (cabecera
// X-Step-Up) del MISMO usuario de la sesion. Es para las operaciones criticas: una
// sesion robada no basta, hay que re-probar identidad (contrasena + MFA) para
// obtener el token. Stateless: se valida por firma, sin consultar ningun store, asi
// que no anade latencia ni depende de infraestructura extra.
//
// Debe ir DESPUES de InjectFromGateway (necesita X-User-ID en el contexto). El verificador
// es el del emisor (auth.IssuerKeySet); sin el, en enforce, ninguna peticion pasa.
func RequireStepUp(verifier *auth.Verifier) func(http.Handler) http.Handler {
	// STEP_UP_MODE: off (default) | enforce. Arranca en off para no romper a los
	// clientes que aun no piden el token de step-up; se sube a enforce cuando el
	// frontend ya intercepta STEP_UP_REQUIRED y reenvia la cabecera. Mismo patron
	// escalonado que el acotado de lecturas del RBAC.
	enforce := strings.EqualFold(os.Getenv("STEP_UP_MODE"), "enforce")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !enforce {
				next.ServeHTTP(w, r)
				return
			}
			tok := r.Header.Get("X-Step-Up")
			if tok == "" {
				stepUpRequired(w)
				return
			}
			uid, _, err := verifier.ParseStepUp(tok)
			if err != nil {
				stepUpRequired(w)
				return
			}
			if reqUID := GetUserID(r.Context()); reqUID == "" || reqUID != uid {
				stepUpRequired(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func stepUpRequired(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":{"code":"STEP_UP_REQUIRED","message":"esta operacion requiere reconfirmar tu identidad"}}`))
}

// InjectFromGateway reads X-User-ID, X-Tenant-ID and X-User-Roles headers
// forwarded by the gateway and injects them into the request context.
// Use this middleware in every downstream service.
func InjectFromGateway(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if uid := r.Header.Get("X-User-ID"); uid != "" {
			ctx = context.WithValue(ctx, CtxUserID, uid)
		}
		if tid := r.Header.Get("X-Tenant-ID"); tid != "" {
			ctx = context.WithValue(ctx, CtxTenantID, tid)
		}
		if rolesHeader := r.Header.Get("X-User-Roles"); rolesHeader != "" {
			roles := strings.Split(rolesHeader, ",")
			ctx = context.WithValue(ctx, CtxRoles, roles)
		}
		// Un alcance ilegible deja la clave sin permisos, nunca sin marca: la peticion sigue
		// siendo de una clave y pkg/authz la deniega.
		if keyID := r.Header.Get(HeaderAPIKeyID); keyID != "" {
			scopes, _ := ParseAPIKeyScopes(r.Header.Get(HeaderAPIKeyScopes))
			ctx = WithAPIKey(ctx, keyID, scopes)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

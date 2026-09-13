package main

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/auth"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/objectstore"
	"github.com/alonsosss/corforce-email/pkg/observability"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"
	"go.uber.org/zap"
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	jwtAuth, kids, err := jwtAuthFromEnv()
	if err != nil {
		log.Fatalf("token keys: %v", err)
	}
	logger.Info("claves publicas del token de acceso", zap.Strings("kid", kids))
	internalToken, err := middleware.InternalGatewayToken()
	if err != nil {
		log.Fatal(err)
	}

	table, err := loadRouteTable()
	if err != nil {
		log.Fatal(err)
	}

	corsOrigins := os.Getenv("CORS_ALLOWED_ORIGINS")
	var allowedOrigins []string
	if corsOrigins != "" {
		allowedOrigins = strings.Split(corsOrigins, ",")
	} else {
		allowedOrigins = []string{"http://localhost:3000"}
	}

	r := chi.NewRouter()
	// Strip trusted internal headers first to prevent client header injection.
	r.Use(middleware.StripInternalHeaders)
	// La IP real del visitante llega en X-Real-IP desde el proxy de borde; solo se
	// acepta ese header cuando la conexion entra por un proxy de confianza.
	r.Use(middleware.CaptureClientIP(middleware.TrustedProxyCIDRs(os.Getenv("TRUSTED_PROXY_CIDRS"))))
	r.Use(middleware.RequestID)
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   allowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Request-ID", "X-Auth-Mode", "X-Step-Up"},
		ExposedHeaders:   []string{"X-Request-ID"},
		AllowCredentials: true,
		MaxAge:           86400,
	}))

	// Los cupos son por IP y comunes a todas las replicas (Redis de la plataforma): con
	// N replicas un cliente no obtiene N veces su cupo.
	//
	// El de autenticacion es dedicado y estricto (login, reto MFA, recuperacion de
	// contrasena, inicio de sesion del webmail). El general es demasiado holgado para
	// frenar fuerza bruta o credential spraying lanzado desde un fetch en la consola del
	// navegador. El bloqueo por cuenta ya frena el ataque a UNA cuenta; esto ademas
	// frena el barrido de MUCHAS cuentas desde una misma IP. El valor por defecto
	// aguanta el pico de una oficina tras NAT.
	rateStore, err := newRateLimitStore(logger)
	if err != nil {
		log.Fatalf("redis: %v", err)
	}
	limiter, authLimiter := newRateLimiters(rateStore,
		envInt("API_RATE_LIMIT_PER_MIN", 600), envInt("AUTH_RATE_LIMIT_PER_MIN", 30), logger)

	identity := reverseProxy(table.serviceURL("identity"), internalToken)

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Media del almacen de objetos (S3/MinIO). El bucket es privado: el gateway
	// genera una URL prefirmada de corta vida y redirige al cliente. Solo los
	// objetos public/ son legibles sin sesion; los private/ los entrega cada
	// servicio acotados al tenant autenticado.
	if mediaStore, mErr := objectstore.FromEnv(); mErr != nil {
		logger.Warn("media store init failed; /media disabled", zap.Error(mErr))
	} else if mediaStore != nil {
		r.Get("/media/*", mediaHandler(mediaStore, logger))
	}

	if table.Frontend != "" {
		r.Handle("/*", reverseProxy(table.serviceURL(table.Frontend), internalToken))
	}

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(limiter.Limit)
		// Auth publico: login, refresh y el reto MFA (paso del propio login).
		// Rutas explicitas (no subrouter) para no solapar con las MFA autenticadas.
		// Limitador estricto en las rutas atacables por fuerza bruta. /auth/refresh
		// se queda en el general: los clientes legitimos lo llaman a menudo y no es
		// un objetivo de adivinacion (el refresh es un opaco de 256 bits).
		r.With(authLimiter.Limit).Post("/auth/login", identity.ServeHTTP)
		r.Post("/auth/refresh", identity.ServeHTTP)
		r.With(authLimiter.Limit).Post("/auth/mfa/challenge", identity.ServeHTTP)
		r.With(authLimiter.Limit).Post("/auth/forgot-password", identity.ServeHTTP)
		r.With(authLimiter.Limit).Post("/auth/reset-password", identity.ServeHTTP)
		// Reglas de contrasena de la empresa del enlace: la pantalla de reinicio las
		// muestra antes de que el usuario escriba. Publica como el reinicio, con el
		// mismo limitador; no revela nada del usuario.
		r.With(authLimiter.Limit).Get("/auth/reset-password/policy", identity.ServeHTTP)

		// Rutas publicas declaradas en la tabla: webhooks de proveedores y enlaces que
		// llegan por correo. Sin JWT; el servicio verifica la firma o el enlace.
		for _, p := range table.Public {
			r.Method(p.Method, p.Path, reverseProxy(table.serviceURL(p.Service), internalToken))
		}

		// Prefijos que autentica el propio servicio con su sesion (el webmail): sin JWT
		// ni RBAC, con el limitador general y el estricto en su inicio de sesion.
		mountSelfAuthenticated(r, table.SelfAuthenticated, table.serviceURL, authLimiter.Limit, internalToken)

		// MFA self-service: requiere autenticacion (inyecta X-User-ID) pero NO pasa
		// por RBAC: es gestion de la propia cuenta, no un recurso protegido por modulo.
		r.Group(func(r chi.Router) {
			r.Use(jwtAuth.Authenticate)
			r.Post("/auth/mfa/setup", identity.ServeHTTP)
			r.Post("/auth/mfa/activate", identity.ServeHTTP)
			r.Delete("/auth/mfa/disable", identity.ServeHTTP)
			// Step-up: re-autenticacion para acciones criticas. Requiere sesion valida.
			r.Post("/auth/step-up", identity.ServeHTTP)
		})

		r.Group(func(r chi.Router) {
			r.Use(jwtAuth.Authenticate)
			modules := table.moduleIndex()
			enforcer := newRBACEnforcer(
				table.serviceURL("access-control"), internalToken,
				os.Getenv("RBAC_ENFORCE_MODE"), os.Getenv("RBAC_FAIL_MODE"), os.Getenv("RBAC_READ_MODE"),
				modules, table.readPostIndex(), logger,
			)
			r.Use(enforcer.middleware)
			// Rastro de auditoria de escrituras: publica un evento por cada
			// mutacion autenticada, que persiste el servicio audit. Corre despues
			// del RBAC: solo audita lo permitido (las denegaciones ya se registran
			// en access-control).
			trail := newAuditTrail(modules, logger)
			r.Use(trail.middleware)

			for _, rt := range table.Routes {
				proxy := reverseProxy(table.serviceURL(rt.Service), internalToken)
				r.Route("/"+rt.Prefix, func(r chi.Router) {
					r.Handle("/*", proxy)
				})
			}
		})
	})

	port := os.Getenv("GATEWAY_PORT")
	if port == "" {
		port = "8080"
	}
	logger.Info("gateway starting", zap.String("port", port), zap.Int("routes", len(table.Routes)))

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           observability.WithOps(r),
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		// El gateway es un proxy: el tiempo de respuesta no lo pone el, sino el
		// servicio de detras. Con 30s, toda respuesta que tardara mas se perdia de
		// la peor manera posible: la conexion se cerraba sin cuerpo, sin codigo de
		// estado y sin rastro en los registros (exportaciones, informes). Se
		// alinea con el tope del borde. La proteccion contra clientes que abren
		// conexiones y no hablan la sigue dando ReadHeaderTimeout.
		WriteTimeout:   120 * time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	if err := srv.ListenAndServe(); err != nil {
		logger.Fatal("gateway failed", zap.Error(err))
	}
}

// jwtAuthFromEnv arma la autenticacion del token de acceso con las claves PUBLICAS de
// JWT_PUBLIC_KEYS. El gateway no tiene con que firmar, y sin claves no arranca.
func jwtAuthFromEnv() (*middleware.JWTAuth, []string, error) {
	keys, err := auth.KeySetFromEnv()
	if err != nil {
		return nil, nil, err
	}
	verifier, err := auth.NewVerifier(keys)
	if err != nil {
		return nil, nil, err
	}
	return middleware.NewJWTAuth(verifier), keys.KIDs(), nil
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

// stampCSPNonce marca los <script> del HTML con el nonce de esta respuesta.
//
// La politica solo ejecuta scripts que lleven el nonce sorteado para la peticion, asi que
// el HTML servido tiene que llevarlo: sin esto la aplicacion no arrancaria. Solo toca
// documentos HTML (los assets se sirven intactos) y solo cuando el cuerpo no viene
// comprimido, porque reescribirlo exigiria descomprimir y volver a comprimir por cada
// peticion sin ninguna ganancia (el HTML de la aplicacion son dos kilobytes).
func stampCSPNonce(resp *http.Response) error {
	nonce := middleware.CSPNonce(resp.Request.Context())
	if nonce == "" || resp.Body == nil {
		return nil
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		return nil
	}
	if resp.Header.Get("Content-Encoding") != "" {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}
	// Solo se marcan las etiquetas que aun no traen nonce, y nunca dentro de texto: la
	// sustitucion es sobre el literal de apertura de la etiqueta.
	marcado := bytes.ReplaceAll(body, []byte("<script "), []byte("<script nonce=\""+nonce+"\" "))
	marcado = bytes.ReplaceAll(marcado, []byte("<script>"), []byte("<script nonce=\""+nonce+"\">"))

	resp.Body = io.NopCloser(bytes.NewReader(marcado))
	resp.ContentLength = int64(len(marcado))
	resp.Header.Set("Content-Length", strconv.Itoa(len(marcado)))
	return nil
}

func reverseProxy(target, internalToken string) http.Handler {
	return reverseProxyWith(target, internalToken, false)
}

// reverseProxyWith es reverseProxy con la opcion de conservar la CSP del servicio. Solo
// la piden los prefijos autenticados por el servicio (self_authenticated): sus respuestas
// son datos y adjuntos, nunca la aplicacion, y su politica es mas estricta que la del
// borde. Con las dos cabeceras el navegador aplica la interseccion, que es la del
// servicio; el resto de rutas sigue con una sola politica.
func reverseProxyWith(target, internalToken string, keepUpstreamCSP bool) http.Handler {
	u, err := url.Parse(target)
	if err != nil {
		log.Fatalf("upstream invalido %q: %v", target, err)
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	// Cada servicio pone sus propias cabeceras de seguridad y el gateway pone las suyas:
	// con dos politicas CSP el navegador aplica la INTERSECCION, asi que una cabecera
	// repetida puede bloquear algo sin explicacion. Manda la del borde, que es una sola.
	stripped := []string{
		"X-Frame-Options", "X-Content-Type-Options",
		"X-XSS-Protection", "Referrer-Policy", "Permissions-Policy",
		"Strict-Transport-Security",
	}
	if !keepUpstreamCSP {
		stripped = append(stripped, "Content-Security-Policy")
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		for _, h := range stripped {
			resp.Header.Del(h)
		}
		return stampCSPNonce(resp)
	}
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		// El host que pidio el cliente se pierde al apuntar al servicio interno, asi
		// que se conserva antes de pisarlo: lo necesita cualquier servicio que tenga
		// que decir "esta es mi direccion" (enlaces de baja, de reinicio de contrasena).
		//
		// Se ESCRIBE, no se anade: un cliente puede mandar estas cabeceras, y si se
		// respetaran podria hacer que la plataforma se anunciara en un dominio ajeno.
		if req.Host != "" {
			req.Header.Set("X-Forwarded-Host", req.Host)
		}
		esquema := "https"
		if req.TLS == nil && req.Header.Get("X-Forwarded-Proto") == "" &&
			(strings.HasPrefix(req.Host, "localhost") || strings.HasPrefix(req.Host, "127.0.0.1")) {
			esquema = "http"
		} else if p := req.Header.Get("X-Forwarded-Proto"); p != "" {
			esquema = p
		}
		req.Header.Set("X-Forwarded-Proto", esquema)

		originalDirector(req)
		req.Host = u.Host
		// El documento HTML hay que marcarlo con el nonce, y no se puede reescribir
		// comprimido. Solo se pide sin comprimir el DOCUMENTO (el navegador manda
		// text/html en Accept); los assets siguen viajando comprimidos.
		if strings.Contains(req.Header.Get("Accept"), "text/html") {
			req.Header.Set("Accept-Encoding", "identity")
		}
		if uid := middleware.GetUserID(req.Context()); uid != "" {
			req.Header.Set("X-User-ID", uid)
		}
		if tid := middleware.GetTenantID(req.Context()); tid != "" {
			req.Header.Set("X-Tenant-ID", tid)
		}
		if roles := middleware.GetRoles(req.Context()); len(roles) > 0 {
			req.Header.Set("X-User-Roles", strings.Join(roles, ","))
		}
		if internalToken != "" {
			req.Header.Set("X-Gateway-Token", internalToken)
		}
		if ip := middleware.GetClientIP(req.Context()); ip != "" {
			req.Header.Set("X-Real-IP", ip)
		}
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		response.Err(w, http.StatusBadGateway, "SERVICE_UNAVAILABLE", "downstream service unavailable")
	}
	return proxy
}

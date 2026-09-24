package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// rbacDenials permite vigilar el control de acceso como una serie temporal y no solo como
// filas en una tabla: un pico de denegaciones tras un despliegue delata un permiso mal
// sembrado, y un pico sostenido en un modulo delata un intento de acceso indebido. Sin
// user_id ni ruta a proposito: son datos personales y dispararian la cardinalidad; el
// detalle nominal ya queda en access-control y en el rastro de auditoria.
var rbacDenials = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "rbac_denials_total",
	Help: "Accesos denegados por el control de acceso del gateway.",
}, []string{"module", "action", "enforced"})

func init() { prometheus.MustRegister(rbacDenials) }

// rbacEnforcer aplica control de acceso por modulo en el gateway. Las ESCRITURAS
// (POST/PUT/PATCH/DELETE) se gatean por accion: DELETE exige 'delete', PUT/PATCH exige
// 'update' y POST exige cualquier permiso de escritura del modulo (POST sirve para muchas
// acciones de dominio: crear, aprobar, anular). Las LECTURAS se gatean por modulo con su
// propio modo, porque el RBAC fino de cada lectura vive en el servicio y, sin esta
// barrera, cualquiera con cuenta lee cualquier modulo.
//
// Es la segunda capa: la primera es el menu (que solo muestra los modulos donde el
// usuario tiene algun permiso) y la tercera es cada handler, que exige el permiso de
// accion concreto.
//
// Modos (RBAC_ENFORCE_MODE / RBAC_READ_MODE): off | audit (solo registra) | enforce (403).
//
// Resiliencia: cachea el acceso del usuario (TTL 60s) y, si access-control no responde,
// usa el ultimo valor conocido en vez de fallar. Solo si nunca se consulto al usuario
// aplica el modo de fallo (RBAC_FAIL_MODE): open (permite) o closed (bloquea). Asi la
// barrera no se vuelve un punto unico de caida del producto.
type rbacEnforcer struct {
	accessURL string
	token     string
	mode      string // off | audit | enforce
	failMode  string // open | closed
	readMode  string // off | audit | enforce
	modules   map[string]string
	readPosts map[string]map[string]bool
	logger    *zap.Logger
	client    *http.Client

	mu    sync.Mutex
	cache map[string]rbacEntry
}

type rbacEntry struct {
	// writeActions: modulo -> conjunto de acciones de escritura del usuario.
	writeActions map[string]map[string]bool
	// modules: modulos donde el usuario tiene ALGUN permiso, tambien de solo lectura.
	modules map[string]bool
	isAdmin bool
	// disabledModules: modulos que la empresa no tiene contratados o habilitados.
	// Bloquean escrituras incluso de sus administradores.
	disabledModules map[string]bool
	// tokensValidFrom: epoch unix de revocacion del usuario. Un access token con iat
	// anterior esta revocado. 0 = sin revocacion.
	tokensValidFrom int64
	// closedAccount: access-control respondio de forma definitiva que la cuenta no existe
	// (accountMissing) o no esta activa (accountInactive). Vacio si esta activa.
	closedAccount string
	// checkedAt: epoch unix de la respuesta de access-control.
	checkedAt int64
	expires   time.Time
}

const (
	rbacCacheTTL    = 60 * time.Second
	accountMissing  = "missing"
	accountInactive = "inactive"
)

var rbacWriteMethods = map[string]bool{
	http.MethodPost: true, http.MethodPut: true, http.MethodPatch: true, http.MethodDelete: true,
}

func newRBACEnforcer(accessURL, token, mode, failMode, readMode string, modules map[string]string, readPosts map[string]map[string]bool, logger *zap.Logger) *rbacEnforcer {
	if mode == "" {
		mode = "enforce"
	}
	if failMode == "" {
		failMode = "closed"
	}
	if readMode == "" {
		readMode = "enforce"
	}
	return &rbacEnforcer{
		accessURL: accessURL,
		token:     token,
		mode:      mode,
		failMode:  failMode,
		readMode:  readMode,
		modules:   modules,
		readPosts: readPosts,
		logger:    logger,
		client:    &http.Client{Timeout: 3 * time.Second},
		cache:     make(map[string]rbacEntry),
	}
}

// requiredAction traduce el metodo HTTP a la accion de escritura exigida. POST se deja
// abierto ("") porque sirve para muchas acciones (crear, aprobar, anular) y exigir
// 'create' bloquearia, p. ej., a quien solo puede aprobar. DELETE y PUT/PATCH si se
// exigen estrictos para proteger borrado y edicion.
func requiredAction(method string) string {
	switch method {
	case http.MethodDelete:
		return "delete"
	case http.MethodPut, http.MethodPatch:
		return "update"
	default:
		return ""
	}
}

// autoservicio son las rutas que un usuario necesita para su propia sesion y que el
// servicio resuelve con su JWT: entrar, refrescar, leer y cambiar lo suyo, y consultar su
// acceso. El directorio de usuarios NO esta aqui a proposito.
func autoservicio(seg1, seg2, userID string) bool {
	switch seg1 {
	case "auth", "sessions":
		return true
	case "access":
		return seg2 == "my-modules"
	case "users":
		return seg2 == "me" || seg2 == "change-password" || seg2 == "password-policy" || seg2 == userID
	}
	return false
}

// gatearLectura acota las LECTURAS por modulo. Devuelve false cuando ya respondio.
func (e *rbacEnforcer) gatearLectura(w http.ResponseWriter, r *http.Request) bool {
	if e.readMode == "off" {
		return true
	}
	userID := middleware.GetUserID(r.Context())
	if userID == "" {
		return true
	}
	seg1, seg2 := pathSegments(r.URL.Path)
	if autoservicio(seg1, seg2, userID) {
		return true
	}
	module := e.modules[seg1]
	if module == "" {
		return true
	}
	if middleware.IsPrivileged(r.Context()) {
		return true
	}
	tenantID := middleware.GetTenantID(r.Context())
	entry, err := e.modulesFor(r.Context(), userID, tenantID, middleware.GetTokenIssuedAt(r.Context()))
	if err != nil {
		if e.readMode == "enforce" && e.failMode == "closed" {
			e.logger.Warn("rbac: sin acceso consultable, se bloquea la lectura (fail-closed)",
				zap.String("user_id", userID), zap.Error(err))
			e.deny(w)
			return false
		}
		return true
	}
	if entry.modules[module] {
		return true
	}
	if e.readMode != "enforce" {
		e.logger.Warn("rbac-audit: bloquearia esta lectura",
			zap.String("user_id", userID), zap.String("module", module),
			zap.String("method", r.Method), zap.String("path", r.URL.Path))
		e.recordDenial(userID, tenantID, module, "read", r.Method, r.URL.Path, false)
		return true
	}
	e.logger.Info("rbac: lectura fuera de los modulos del usuario",
		zap.String("user_id", userID), zap.String("module", module),
		zap.String("method", r.Method), zap.String("path", r.URL.Path))
	e.recordDenial(userID, tenantID, module, "read", r.Method, r.URL.Path, true)
	e.deny(w)
	return false
}

// sessionCheck rechaza con 401 el access token que ya no vale aunque su firma y su vida
// sigan en regla: el de una cuenta que access-control da por inexistente o no activa
// (borrada, desactivada, bloqueada, pendiente) y el emitido antes del epoch de revocacion
// del usuario (sesion cerrada, contrasena cambiada). Va en toda ruta con sesion, antes del
// RBAC y sea cual sea su modo.
//
// Solo decide con una respuesta definitiva de access-control, reciente o en cache. Si no
// responde y no hay una aplicable, deja pasar: el token sigue acotado por su vida corta, y
// bloquear ahi convertiria una caida de access-control en una caida de toda la plataforma.
// Las rutas con modulo siguen despues con RBAC_FAIL_MODE.
func (e *rbacEnforcer) sessionCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if e.sessionRejected(r) {
			e.denyRevoked(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sessionRejected deja un margen de 5 s al epoch de revocacion por el desfase de reloj
// entre identity y la base. Un token sin iat solo se juzga por el estado de la cuenta.
func (e *rbacEnforcer) sessionRejected(r *http.Request) bool {
	userID := middleware.GetUserID(r.Context())
	if userID == "" {
		return false
	}
	iat := middleware.GetTokenIssuedAt(r.Context())
	entry, err := e.modulesFor(r.Context(), userID, middleware.GetTenantID(r.Context()), iat)
	if err != nil {
		return false
	}
	if entry.closedAccount != "" {
		return true
	}
	return iat != 0 && entry.tokensValidFrom > 0 && iat < entry.tokensValidFrom-5
}

func (e *rbacEnforcer) denyRevoked(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":{"code":"SESSION_REVOKED","message":"tu sesión fue cerrada; vuelve a iniciar sesión"}}`))
}

func (e *rbacEnforcer) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if middleware.GetAPIKeyID(r.Context()) != "" {
			if e.allowAPIKey(w, r) {
				next.ServeHTTP(w, r)
			}
			return
		}
		if e.mode == "off" {
			next.ServeHTTP(w, r)
			return
		}

		if !rbacWriteMethods[r.Method] || e.isReadPost(r) {
			if !e.gatearLectura(w, r) {
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		// Autoservicio (autoservicio): lo propio de la sesion, que el servicio resuelve con el JWT.
		// No se gatea por modulo.
		seg1, seg2 := pathSegments(r.URL.Path)
		if autoservicio(seg1, seg2, middleware.GetUserID(r.Context())) {
			next.ServeHTTP(w, r)
			return
		}

		module := e.modules[seg1]
		if module == "" {
			next.ServeHTTP(w, r)
			return
		}

		isAdmin := middleware.IsPrivileged(r.Context())
		userID := middleware.GetUserID(r.Context())
		tenantID := middleware.GetTenantID(r.Context())
		if userID == "" {
			next.ServeHTTP(w, r)
			return
		}

		entry, err := e.modulesFor(r.Context(), userID, tenantID, middleware.GetTokenIssuedAt(r.Context()))
		if err != nil {
			// Los administradores nunca se bloquean por indisponibilidad de access-control.
			if isAdmin {
				next.ServeHTTP(w, r)
				return
			}
			if e.mode == "enforce" && e.failMode == "closed" {
				e.logger.Warn("rbac: sin acceso consultable, se bloquea (fail-closed)",
					zap.String("user_id", userID), zap.Error(err))
				e.deny(w)
				return
			}
			e.logger.Warn("rbac: sin acceso consultable, se permite (fail-open)",
				zap.String("user_id", userID), zap.Error(err))
			next.ServeHTTP(w, r)
			return
		}

		action := requiredAction(r.Method)
		// Un modulo deshabilitado para la empresa no admite escrituras de NADIE, ni
		// siquiera de sus administradores.
		disabled := entry.disabledModules[module]

		var allowed bool
		if isAdmin {
			allowed = !disabled
		} else {
			acts := entry.writeActions[module]
			allowed = !disabled &&
				((action == "" && len(acts) > 0) ||
					(action != "" && acts[action]))
		}
		if allowed {
			next.ServeHTTP(w, r)
			return
		}

		if e.mode == "enforce" {
			e.logger.Info("rbac: acceso denegado",
				zap.String("user_id", userID), zap.String("module", module),
				zap.String("action", action), zap.String("method", r.Method),
				zap.String("path", r.URL.Path), zap.Bool("module_disabled", disabled))
			e.recordDenial(userID, tenantID, module, action, r.Method, r.URL.Path, true)
			e.deny(w)
			return
		}
		e.logger.Warn("rbac-audit: bloquearia esta escritura",
			zap.String("user_id", userID), zap.String("module", module),
			zap.String("action", action), zap.String("method", r.Method),
			zap.String("path", r.URL.Path), zap.Bool("module_disabled", disabled))
		e.recordDenial(userID, tenantID, module, action, r.Method, r.URL.Path, false)
		next.ServeHTTP(w, r)
	})
}

// isReadPost reconoce las consultas con cuerpo declaradas en la tabla de rutas.
func (e *rbacEnforcer) isReadPost(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	seg1, _ := pathSegments(r.URL.Path)
	return e.readPosts[seg1][lastSegment(r.URL.Path)]
}

// recordDenial persiste el acceso denegado en access-control para las metricas de
// administracion. Best-effort y asincrono: nunca debe afectar la peticion original.
func (e *rbacEnforcer) recordDenial(userID, tenantID, module, action, method, path string, enforced bool) {
	rbacDenials.WithLabelValues(module, action, strconv.FormatBool(enforced)).Inc()

	body, err := json.Marshal(map[string]interface{}{
		"tenant_id": tenantID, "user_id": userID, "module": module,
		"action": action, "method": method, "path": path, "enforced": enforced,
	})
	if err != nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.accessURL+"/api/v1/access/denials", bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Gateway-Token", e.token)
		resp, err := e.client.Do(req)
		if err != nil {
			return
		}
		_ = resp.Body.Close()
	}()
}

func (e *rbacEnforcer) deny(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":{"code":"FORBIDDEN","message":"su rol no tiene permiso para esta operación"}}`))
}

// modulesFor resuelve el acceso del usuario desde la cache (rbacCacheTTL) o access-control.
// Una cuenta cerrada solo se da por cerrada para los tokens emitidos hasta esa respuesta: uno
// posterior lo emitio identity con la cuenta activa otra vez (renovar exige cuenta activa, y
// una cuenta borrada ya no renueva), asi que obliga a preguntar de nuevo.
func (e *rbacEnforcer) modulesFor(ctx context.Context, userID, tenantID string, iat int64) (rbacEntry, error) {
	key := userID + ":" + tenantID
	e.mu.Lock()
	cached, hasCached := e.cache[key]
	e.mu.Unlock()
	applies := hasCached && (cached.closedAccount == "" || iat <= cached.checkedAt)
	if applies && time.Now().Before(cached.expires) {
		return cached, nil
	}

	ent, err := e.fetch(ctx, userID, tenantID)
	if err != nil {
		// Resiliencia: si access-control no responde pero teniamos un valor previo que
		// aplica al token, se usa ese en vez de fallar. Si no, se devuelve el error y
		// decide quien pregunta: la sesion deja pasar y el RBAC aplica su modo de fallo.
		if applies {
			e.logger.Warn("rbac: usando acceso en cache por error de access-control",
				zap.String("user_id", userID), zap.Error(err))
			return cached, nil
		}
		return rbacEntry{}, err
	}
	if ent.closedAccount != "" {
		e.logger.Info("rbac: la cuenta no existe o no esta activa; se rechazan sus tokens",
			zap.String("user_id", userID), zap.String("account", ent.closedAccount))
	}

	e.mu.Lock()
	e.cache[key] = ent
	e.mu.Unlock()
	return ent, nil
}

// myModulesPayload es el contrato de GET /api/v1/access/my-modules de access-control.
type myModulesPayload struct {
	Data struct {
		IsAdmin         bool                `json:"is_admin"`
		Modules         []string            `json:"modules"`
		WriteActions    map[string][]string `json:"write_actions"`
		DisabledModules []string            `json:"disabled_modules"`
		TokensValidFrom time.Time           `json:"tokens_valid_from"`
	} `json:"data"`
}

func (e *rbacEnforcer) fetch(ctx context.Context, userID, tenantID string) (rbacEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.accessURL+"/api/v1/access/my-modules", nil)
	if err != nil {
		return rbacEntry{}, err
	}
	req.Header.Set("X-Gateway-Token", e.token)
	req.Header.Set("X-User-ID", userID)
	req.Header.Set("X-Tenant-ID", tenantID)

	resp, err := e.client.Do(req)
	if err != nil {
		return rbacEntry{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		if reason := closedAccountReason(resp); reason != "" {
			now := time.Now()
			return rbacEntry{closedAccount: reason, checkedAt: now.Unix(), expires: now.Add(rbacCacheTTL)}, nil
		}
		return rbacEntry{}, errStatus(resp.StatusCode)
	}

	var payload myModulesPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return rbacEntry{}, err
	}
	return entryFromPayload(payload), nil
}

// closedAccountReason reconoce las dos respuestas definitivas del contrato de
// GET /access/my-modules: 404 USER_NOT_FOUND y 403 USER_NOT_ACTIVE. Cualquier otra, tambien
// un 404 de ruta, un 403 por otro motivo o un 401 del token interno, es "no se pudo
// determinar": tomarla por cuenta cerrada expulsaria a todos ante un fallo de despliegue.
func closedAccountReason(resp *http.Response) string {
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusForbidden {
		return ""
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&body) != nil {
		return ""
	}
	switch {
	case resp.StatusCode == http.StatusNotFound && body.Error.Code == "USER_NOT_FOUND":
		return accountMissing
	case resp.StatusCode == http.StatusForbidden && body.Error.Code == "USER_NOT_ACTIVE":
		return accountInactive
	}
	return ""
}

func entryFromPayload(payload myModulesPayload) rbacEntry {
	wa := make(map[string]map[string]bool, len(payload.Data.WriteActions))
	for module, actions := range payload.Data.WriteActions {
		set := make(map[string]bool, len(actions))
		for _, a := range actions {
			set[a] = true
		}
		wa[module] = set
	}
	mods := make(map[string]bool, len(payload.Data.Modules))
	for _, m := range payload.Data.Modules {
		mods[m] = true
	}
	disabled := make(map[string]bool, len(payload.Data.DisabledModules))
	for _, m := range payload.Data.DisabledModules {
		disabled[m] = true
	}
	var validFrom int64
	if !payload.Data.TokensValidFrom.IsZero() {
		validFrom = payload.Data.TokensValidFrom.Unix()
	}
	now := time.Now()
	return rbacEntry{
		writeActions:    wa,
		modules:         mods,
		isAdmin:         payload.Data.IsAdmin,
		disabledModules: disabled,
		tokensValidFrom: validFrom,
		checkedAt:       now.Unix(),
		expires:         now.Add(rbacCacheTTL),
	}
}

type errStatus int

func (e errStatus) Error() string { return "access-control status " + http.StatusText(int(e)) }

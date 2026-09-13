package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/alonsosss/corforce-email/pkg/auth"
	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/identity/internal/app"
	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/alonsosss/corforce-email/services/identity/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const (
	defaultPage     = 1
	defaultPageSize = 20
	maxPageSize     = 100

	// defaultMFAIssuer es el nombre que muestra la app de autenticacion cuando el
	// despliegue no fija MFA_ISSUER.
	defaultMFAIssuer = "Core Force Mail"

	permModule = "identity"
)

// Config es lo que el handler necesita del entorno; lo resuelve main, no el adaptador.
type Config struct {
	// StepUp valida los tokens de step-up en las rutas criticas.
	StepUp *auth.Verifier
	// MFAIssuer es el emisor que ve el usuario en su app TOTP. Vacio: la marca del producto.
	MFAIssuer string
}

type Handler struct {
	auth      *app.AuthUseCase
	user      *app.UserUseCase
	reset     *app.PasswordResetUseCase
	authz     *authz.Checker
	stepUp    *auth.Verifier
	mfaIssuer string
}

func NewHandler(auth *app.AuthUseCase, user *app.UserUseCase, reset *app.PasswordResetUseCase, checker *authz.Checker, cfg Config) *Handler {
	issuer := strings.TrimSpace(cfg.MFAIssuer)
	if issuer == "" {
		issuer = defaultMFAIssuer
	}
	return &Handler{auth: auth, user: user, reset: reset, authz: checker, stepUp: cfg.StepUp, mfaIssuer: issuer}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()

	// step-up: re-autenticacion reciente para las acciones mas peligrosas (borrar
	// usuarios, reset de contrasena ajena y politica de sesion).
	stepUp := middleware.RequireStepUp(h.stepUp)

	r.Route("/api/v1", func(r chi.Router) {
		r.Route("/auth", func(r chi.Router) {
			r.Post("/login", h.Login)
			r.Post("/refresh", h.RefreshToken)
			// Recuperacion self-service de contrasena (rutas publicas via gateway).
			r.Post("/forgot-password", h.ForgotPassword)
			r.Post("/reset-password", h.ResetPasswordPublic)
			// Reglas de contrasena de la empresa del enlace, para explicarlas antes de
			// que el usuario escriba. Publica, como el reinicio: no revela nada del usuario.
			r.Get("/reset-password/policy", h.GetPasswordResetPolicy)
			// Step-up: re-autenticacion para desbloquear acciones criticas. Requiere
			// sesion (el gateway inyecta X-User-ID) y ademas contrasena + MFA.
			r.Post("/step-up", h.StepUp)
			r.Route("/mfa", func(r chi.Router) {
				r.Post("/challenge", h.MFAChallenge)
				r.Post("/setup", h.MFASetup)
				r.Post("/activate", h.MFAActivate)
				r.Delete("/disable", h.MFADisable)
			})
		})

		// El permiso va antes que el step-up: no se pide reconfirmar la identidad a quien
		// de todos modos no puede hacer la operacion.
		r.Route("/users", func(r chi.Router) {
			r.With(h.perm("users", "read")).Get("/", h.ListUsers)
			r.With(h.perm("users", "create")).Post("/", h.CreateUser)
			// La ficha propia es autoservicio; la de otro usuario exige el permiso.
			r.With(h.selfOrPerm("users", "read")).Get("/{id}", h.GetUser)
			// UpdateUser decide dentro: el perfil propio es autoservicio y el de otro no.
			r.Patch("/{id}", h.UpdateUser)
			// users/delete es "Desactivar usuarios": cubre la baja logica y el borrado.
			r.With(h.perm("users", "delete")).Post("/{id}/deactivate", h.DeactivateUser)
			r.With(h.perm("users", "delete"), stepUp).Delete("/{id}", h.DeleteUser)
			r.With(h.perm("users", "reset_password"), stepUp).Post("/{id}/reset-password", h.ResetPassword)
			// Autoservicio: la propia contrasena y las reglas que debe cumplir.
			r.Post("/change-password", h.ChangePassword)
			r.Get("/password-policy", h.GetPasswordPolicy)
		})

		r.Route("/sessions", func(r chi.Router) {
			r.Post("/logout", h.Logout)
			r.Post("/logout-all", h.LogoutAll)
			// Dispositivos del propio usuario: cualquier sesion autenticada.
			r.Get("/mine", h.MySessions)
			// Vista de plataforma: sesiones de todas las empresas, solo superadmin.
			r.With(middleware.RequireRoles(middleware.RoleSuperadmin), h.perm("platform_sessions", "read")).
				Get("/platform", h.ListPlatformSessions)
			// Vista por empresa, politica y revocacion. El gateway trata /sessions como
			// autoservicio y no lo gatea por modulo: el permiso de aqui es la unica barrera.
			r.With(h.perm("sessions", "read")).Get("/", h.ListSessions)
			r.With(h.perm("session_policies", "read")).Get("/policy", h.GetSessionPolicy)
			r.With(h.perm("session_policies", "update"), stepUp).Put("/policy", h.SaveSessionPolicy)
			r.With(h.perm("sessions", "revoke")).Delete("/{id}", h.RevokeSession)
		})
	})

	return r
}

type loginRequest struct {
	TenantSlug string `json:"tenant_slug"`
	Email      string `json:"email"`
	Password   string `json:"password"`
	// CookieAuth pide que el refresh token viaje como cookie HttpOnly en vez de en el
	// JSON. Lo usa el navegador; los clientes que no son navegador lo omiten.
	CookieAuth bool `json:"cookie_auth"`
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}

	v := validate.New()
	v.Required("email", req.Email)
	v.Email("email", req.Email)
	v.Required("password", req.Password)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}

	res, err := h.auth.Login(r.Context(), ports.LoginRequest{
		TenantSlug: req.TenantSlug,
		Email:      req.Email,
		Password:   req.Password,
		IPAddress:  extractClientIP(r),
		UserAgent:  r.UserAgent(),
	})
	if err != nil {
		switch err {
		case domain.ErrInvalidCredentials, domain.ErrTenantNotFound:
			response.ErrUnauthorized(w, "invalid credentials")
		case domain.ErrAccountLocked:
			response.Err(w, http.StatusForbidden, "ACCOUNT_LOCKED", "account is temporarily locked")
		case domain.ErrAccountInactive:
			response.Err(w, http.StatusForbidden, "ACCOUNT_INACTIVE", "account is inactive")
		default:
			response.ErrInternal(w)
		}
		return
	}

	response.JSON(w, http.StatusOK, issueSession(w, r, res, req.CookieAuth))
}

type forgotPasswordRequest struct {
	Email string `json:"email"`
}

// ForgotPassword inicia la recuperacion self-service. Responde siempre 200 con
// el mismo mensaje, exista o no la cuenta (anti-enumeracion de usuarios).
func (h *Handler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req forgotPasswordRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	v.Required("email", req.Email)
	v.Email("email", req.Email)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	h.reset.RequestReset(r.Context(), req.Email, extractClientIP(r))
	response.JSON(w, http.StatusOK, map[string]string{
		"message": "Si el correo esta registrado, recibiras un enlace para restablecer tu contrasena.",
	})
}

type resetPasswordPublicRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

// ResetPasswordPublic completa la recuperacion con el token del correo.
func (h *Handler) ResetPasswordPublic(w http.ResponseWriter, r *http.Request) {
	var req resetPasswordPublicRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	v.Required("token", req.Token)
	v.Required("new_password", req.NewPassword)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	if err := h.reset.ConfirmReset(r.Context(), req.Token, req.NewPassword); err != nil {
		switch err {
		case domain.ErrResetTokenInvalid:
			response.Err(w, http.StatusBadRequest, "RESET_TOKEN_INVALID", "el enlace es invalido, expiro o ya fue usado")
		case domain.ErrPasswordPolicyFail, domain.ErrPasswordBreached:
			respondPasswordRejected(w, err)
		default:
			response.ErrInternal(w)
		}
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"message": "Contrasena actualizada. Ya puedes iniciar sesion."})
}

// respondPasswordRejected es la unica voz para una contrasena rechazada: mismo codigo y
// mismo texto en las cuatro pantallas que la piden, para que el frontend lo trate una vez.
func respondPasswordRejected(w http.ResponseWriter, err error) {
	switch err {
	case domain.ErrPasswordBreached:
		response.Err(w, http.StatusUnprocessableEntity, "PASSWORD_BREACHED", "esa contrasena aparece en filtraciones publicas; elige otra distinta")
	case domain.ErrPasswordReused:
		response.Err(w, http.StatusUnprocessableEntity, "PASSWORD_REUSED", "esa contrasena ya se uso hace poco; elige otra distinta")
	default:
		response.Err(w, http.StatusUnprocessableEntity, "PASSWORD_POLICY", "la contrasena no cumple las reglas de tu empresa")
	}
}

// GetPasswordResetPolicy devuelve las reglas de contrasena de la empresa del enlace de
// reinicio (?token=). Invalido o gastado: 400, igual que confirmar con el.
func (h *Handler) GetPasswordResetPolicy(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		response.ErrBadRequest(w, "token requerido")
		return
	}
	rules, err := h.reset.RulesForToken(r.Context(), token)
	if err != nil {
		if err == domain.ErrResetTokenInvalid {
			response.Err(w, http.StatusBadRequest, "RESET_TOKEN_INVALID", "el enlace es invalido, expiro o ya fue usado")
			return
		}
		response.ErrInternal(w)
		return
	}
	response.JSON(w, http.StatusOK, rules)
}

// GetPasswordPolicy devuelve las reglas de contrasena de la empresa del usuario en sesion.
func (h *Handler) GetPasswordPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	rules, err := h.user.PasswordRules(r.Context(), tenantID)
	if err != nil {
		response.ErrInternal(w)
		return
	}
	response.JSON(w, http.StatusOK, rules)
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
	CookieAuth   bool   `json:"cookie_auth"`
}

func (h *Handler) RefreshToken(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	// El JSON es opcional en modo cookie: el token va en la cookie HttpOnly.
	if r.ContentLength > 0 {
		if err := validate.DecodeJSON(r, &req); err != nil {
			response.ErrBadRequest(w, err.Error())
			return
		}
	}

	// La cookie manda cuando existe: es la sesion que el navegador tiene abierta, y el
	// JSON nunca deberia poder suplantarla.
	fromCookie := refreshCookieValue(r)
	cookieMode := req.CookieAuth || fromCookie != ""
	token := fromCookie
	if token == "" {
		token = req.RefreshToken
	}
	if token == "" {
		response.ErrUnauthorized(w, "invalid or expired refresh token")
		return
	}

	res, err := h.auth.RefreshToken(r.Context(), token)
	if err != nil {
		// Sesion revocada o vencida: la cookie ya no sirve para nada y debe irse, o el
		// navegador la seguiria reenviando en cada intento.
		if fromCookie != "" {
			clearRefreshCookie(w)
		}
		// El cierre por inactividad se distingue para que el cliente pueda explicar
		// por que se cerro la sesion en vez de mostrar un fallo generico.
		if err == domain.ErrSessionIdle {
			response.ErrUnauthorized(w, "la sesion se cerro por inactividad")
			return
		}
		response.ErrUnauthorized(w, "invalid or expired refresh token")
		return
	}

	response.JSON(w, http.StatusOK, issueSession(w, r, res, cookieMode))
}

type logoutRequest struct {
	// RefreshToken lo envian los clientes que no usan cookie, para que el servidor
	// revoque su sesion y no solo el access token.
	RefreshToken string `json:"refresh_token"`
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	uid, err := uuid.Parse(middleware.GetUserID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "invalid user")
		return
	}
	tid, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}

	var req logoutRequest
	if r.ContentLength > 0 {
		if err := validate.DecodeJSON(r, &req); err != nil {
			response.ErrBadRequest(w, err.Error())
			return
		}
	}
	// La cookie manda sobre el JSON, igual que en la renovacion.
	refreshToken := refreshCookieValue(r)
	if refreshToken == "" {
		refreshToken = req.RefreshToken
	}

	h.auth.Logout(r.Context(), tid, uid, bearerToken(r), refreshToken)
	clearRefreshCookie(w)
	response.JSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

func (h *Handler) LogoutAll(w http.ResponseWriter, r *http.Request) {
	uid, err := uuid.Parse(middleware.GetUserID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "invalid user")
		return
	}

	h.auth.LogoutAll(r.Context(), uid)
	clearRefreshCookie(w)
	response.JSON(w, http.StatusOK, map[string]string{"status": "all sessions revoked"})
}

// bearerToken extrae el access token de la cabecera Authorization, o "" si no viene.
func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// pagination lee page y per_page de la query con sus valores por defecto. Pedir mas de
// lo permitido se RECORTA al maximo: caer al valor por defecto hacia que el cliente
// recibiera menos filas creyendo que pedia mas.
func pagination(r *http.Request) (page, pageSize int) {
	page, _ = strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ = strconv.Atoi(r.URL.Query().Get("per_page"))
	if page < 1 {
		page = defaultPage
	}
	if pageSize < 1 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	return page, pageSize
}

// sessionFilterFromQuery arma el filtro comun del listado de sesiones a partir de la
// query: user_id, active (por defecto solo activas) y paginacion.
func sessionFilterFromQuery(r *http.Request) (domain.SessionFilter, int, int) {
	page, pageSize := pagination(r)
	f := domain.SessionFilter{
		ActiveOnly: r.URL.Query().Get("active") != "false",
		Offset:     (page - 1) * pageSize,
		Limit:      pageSize,
	}
	if uid, err := uuid.Parse(r.URL.Query().Get("user_id")); err == nil {
		f.UserID = &uid
	}
	return f, page, pageSize
}

func (h *Handler) respondSessions(w http.ResponseWriter, r *http.Request, f domain.SessionFilter, page, pageSize int) {
	sessions, total, err := h.auth.ListSessions(r.Context(), f)
	if err != nil {
		response.ErrInternal(w)
		return
	}
	if sessions == nil {
		sessions = []*domain.SessionInfo{}
	}
	response.JSONWithMeta(w, http.StatusOK, sessions, response.PageMeta(total, page, pageSize))
}

// ListSessions lista los dispositivos/sesiones de los usuarios del tenant del actor.
func (h *Handler) ListSessions(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	f, page, pageSize := sessionFilterFromQuery(r)
	f.TenantID = &tenantID
	h.respondSessions(w, r, f, page, pageSize)
}

// MySessions lista las sesiones del propio usuario (sus dispositivos).
func (h *Handler) MySessions(w http.ResponseWriter, r *http.Request) {
	uid, err := uuid.Parse(middleware.GetUserID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "invalid user")
		return
	}
	f, page, pageSize := sessionFilterFromQuery(r)
	f.UserID = &uid
	h.respondSessions(w, r, f, page, pageSize)
}

// ListPlatformSessions es la vista cross-tenant del superadmin de plataforma.
// Acepta tenant_id como filtro opcional.
func (h *Handler) ListPlatformSessions(w http.ResponseWriter, r *http.Request) {
	f, page, pageSize := sessionFilterFromQuery(r)
	if tid, err := uuid.Parse(r.URL.Query().Get("tenant_id")); err == nil {
		f.TenantID = &tid
	}
	h.respondSessions(w, r, f, page, pageSize)
}

type sessionPolicyRequest struct {
	RefreshTTLHours       int `json:"refresh_ttl_hours"`
	MaxConcurrentSessions int `json:"max_concurrent_sessions"`
	IdleTimeoutMinutes    int `json:"idle_timeout_minutes"`
}

// GetSessionPolicy devuelve la politica vigente: la configurada por la empresa o la
// por defecto si nunca la toco.
func (h *Handler) GetSessionPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	policy, err := h.auth.GetSessionPolicy(r.Context(), tenantID)
	if err != nil {
		response.ErrInternal(w)
		return
	}
	response.JSON(w, http.StatusOK, policy)
}

// SaveSessionPolicy guarda los controles de sesion de la empresa. El tenant sale del
// token, nunca del JSON: nadie configura la politica de otra empresa.
func (h *Handler) SaveSessionPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	actorID, err := uuid.Parse(middleware.GetUserID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "invalid user")
		return
	}

	var req sessionPolicyRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}

	policy := &domain.SessionPolicy{
		TenantID:              tenantID,
		RefreshTTLHours:       req.RefreshTTLHours,
		MaxConcurrentSessions: req.MaxConcurrentSessions,
		IdleTimeoutMinutes:    req.IdleTimeoutMinutes,
	}
	if err := h.auth.SaveSessionPolicy(r.Context(), policy, actorID); err != nil {
		if err == domain.ErrInvalidSessionPolicy {
			response.ErrValidation(w, "valores fuera de rango: la duracion va de 1 a 8760 horas, el maximo de sesiones de 0 a 100 y la inactividad de 0 a 43200 minutos")
			return
		}
		response.ErrInternal(w)
		return
	}
	response.JSON(w, http.StatusOK, policy)
}

// callerIsSuperadmin distingue la cuenta de plataforma, que opera todas las empresas.
func callerIsSuperadmin(r *http.Request) bool {
	for _, role := range middleware.GetRoles(r.Context()) {
		if strings.TrimSpace(role) == middleware.RoleSuperadmin {
			return true
		}
	}
	return false
}

// RevokeSession cierra remotamente una sesion. El admin de empresa solo alcanza
// sesiones de usuarios de su tenant; el superadmin de plataforma, cualquiera.
func (h *Handler) RevokeSession(w http.ResponseWriter, r *http.Request) {
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.ErrBadRequest(w, "invalid session id")
		return
	}
	actorID, err := uuid.Parse(middleware.GetUserID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "invalid user")
		return
	}

	var actorTenant *uuid.UUID
	if !callerIsSuperadmin(r) {
		tenantID, err := uuid.Parse(middleware.GetTenantID(r.Context()))
		if err != nil {
			response.ErrUnauthorized(w, "invalid tenant")
			return
		}
		actorTenant = &tenantID
	}

	if err := h.auth.RevokeSessionScoped(r.Context(), sessionID, actorID, actorTenant, extractClientIP(r)); err != nil {
		if err == domain.ErrSessionNotFound {
			response.ErrNotFound(w, "session not found")
			return
		}
		response.ErrInternal(w)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "session revoked"})
}

func (h *Handler) perm(resource, action string) func(http.Handler) http.Handler {
	return h.authz.RequirePermission(permModule, resource, action)
}

// selfOrPerm deja pasar al usuario sobre su propia ficha ({id} de la ruta) y exige el
// permiso cuando la ficha es de otro.
func (h *Handler) selfOrPerm(resource, action string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		guarded := h.perm(resource, action)(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if chi.URLParam(r, "id") == middleware.GetUserID(r.Context()) {
				next.ServeHTTP(w, r)
				return
			}
			guarded.ServeHTTP(w, r)
		})
	}
}

// permitted es la comprobacion de permiso cuando depende del cuerpo de la peticion.
// Responde lo mismo que RequirePermission: 403 sin permiso, 503 si no se pudo comprobar.
func (h *Handler) permitted(w http.ResponseWriter, r *http.Request, resource, action string) bool {
	ok, err := h.authz.Allowed(r.Context(), permModule, resource, action)
	if err != nil {
		response.Err(w, http.StatusServiceUnavailable, "AUTHZ_UNAVAILABLE", "no se pudo comprobar el permiso")
		return false
	}
	if !ok {
		response.ErrForbidden(w, "su rol no tiene permiso para esta operacion")
		return false
	}
	return true
}

// canManage impide que un rol de empresa con permisos sobre usuarios actue sobre una
// cuenta con rol del sistema: fijarle la contrasena al tenant_admin, desactivarlo o
// editarlo seria hacerse administrador por la puerta de atras. Los roles del sistema no
// tienen ese limite. Si los roles del objetivo no se pueden leer, no se concede.
func (h *Handler) canManage(w http.ResponseWriter, r *http.Request, target *domain.User) bool {
	ctx := r.Context()
	if middleware.IsPrivileged(ctx) {
		return true
	}
	roles, err := h.auth.RoleNamesOf(ctx, target.ID)
	if err != nil {
		response.Err(w, http.StatusServiceUnavailable, "AUTHZ_UNAVAILABLE", "no se pudo comprobar el permiso")
		return false
	}
	// El mismo criterio unico que se aplica a quien llama, sobre los roles del objetivo.
	if middleware.IsPrivileged(context.WithValue(ctx, middleware.CtxRoles, roles)) {
		response.ErrForbidden(w, "solo un rol del sistema puede administrar a un administrador")
		return false
	}
	return true
}

// tenantUser carga el usuario de la ruta comprobando que pertenece al tenant del actor.
// Un usuario de otra empresa responde 404, no 403: no se revela que existe.
func (h *Handler) tenantUser(w http.ResponseWriter, r *http.Request) (*domain.User, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.ErrBadRequest(w, "invalid user id")
		return nil, false
	}
	callerTenant, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return nil, false
	}
	user, err := h.user.GetByID(r.Context(), id)
	if err != nil || user.TenantID != callerTenant {
		response.ErrNotFound(w, "user not found")
		return nil, false
	}
	return user, true
}

type createUserRequest struct {
	Email     string `json:"email"`
	Password  string `json:"password"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}

	v := validate.New()
	v.Required("email", req.Email)
	v.Email("email", req.Email)
	v.Required("password", req.Password)
	v.MinLength("password", req.Password, 8)
	v.MaxLength("password", req.Password, 128)
	v.Required("first_name", req.FirstName)
	v.MaxLength("first_name", req.FirstName, 100)
	v.Required("last_name", req.LastName)
	v.MaxLength("last_name", req.LastName, 100)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}

	tenantID, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}

	createdBy, _ := uuid.Parse(middleware.GetUserID(r.Context()))

	user, err := h.user.Create(r.Context(), ports.CreateUserRequest{
		TenantID:  tenantID,
		Email:     req.Email,
		Password:  req.Password,
		FirstName: req.FirstName,
		LastName:  req.LastName,
		CreatedBy: createdBy,
	})
	if err != nil {
		switch err {
		case domain.ErrUserAlreadyExists:
			response.ErrConflict(w, "user with this email already exists")
		case domain.ErrPasswordPolicyFail, domain.ErrPasswordBreached:
			respondPasswordRejected(w, err)
		default:
			// Con constancia del motivo: un 500 mudo obliga a reproducir el fallo para
			// saber que se quejo por debajo.
			response.Unexpected(w, fmt.Errorf("crear usuario: %w", err))
		}
		return
	}

	response.JSON(w, http.StatusCreated, userResponse(user))
}

func (h *Handler) GetUser(w http.ResponseWriter, r *http.Request) {
	user, ok := h.tenantUser(w, r)
	if !ok {
		return
	}
	response.JSON(w, http.StatusOK, userResponse(user))
}

func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}

	page, pageSize := pagination(r)
	users, total, err := h.user.List(r.Context(), tenantID, page, pageSize, r.URL.Query().Get("search"))
	if err != nil {
		response.ErrInternal(w)
		return
	}

	items := make([]map[string]interface{}, 0, len(users))
	for _, u := range users {
		items = append(items, userResponse(u))
	}
	response.JSONWithMeta(w, http.StatusOK, items, response.PageMeta(total, page, pageSize))
}

type updateUserRequest struct {
	FirstName *string `json:"first_name,omitempty"`
	LastName  *string `json:"last_name,omitempty"`
	Status    *string `json:"status,omitempty"`
	AvatarURL *string `json:"avatar_url,omitempty"`
}

func (h *Handler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	existing, ok := h.tenantUser(w, r)
	if !ok {
		return
	}

	var req updateUserRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}

	if req.Status != nil {
		v := validate.New()
		v.OneOf("status", *req.Status, []string{string(domain.UserStatusActive), string(domain.UserStatusInactive)})
		if !v.Valid() {
			response.ErrValidation(w, v.Error())
			return
		}
	}

	// El propio perfil (nombre, foto) es autoservicio; el propio estado solo lo cambia un
	// rol del sistema. Editar a otro exige users/update, y cambiarle el estado es activarlo
	// o desactivarlo, que exige ademas users/delete.
	if existing.ID.String() == middleware.GetUserID(r.Context()) {
		if req.Status != nil && !middleware.IsPrivileged(r.Context()) {
			response.ErrForbidden(w, "no puedes cambiar tu propio estado")
			return
		}
	} else {
		if !h.permitted(w, r, "users", "update") {
			return
		}
		if req.Status != nil && !h.permitted(w, r, "users", "delete") {
			return
		}
		if !h.canManage(w, r, existing) {
			return
		}
	}

	user, err := h.user.Update(r.Context(), existing.ID, ports.UpdateUserRequest{
		FirstName: req.FirstName,
		LastName:  req.LastName,
		Status:    req.Status,
		AvatarURL: req.AvatarURL,
	})
	if err != nil {
		response.ErrNotFound(w, "user not found")
		return
	}

	response.JSON(w, http.StatusOK, userResponse(user))
}

func (h *Handler) DeactivateUser(w http.ResponseWriter, r *http.Request) {
	existing, ok := h.tenantUser(w, r)
	if !ok || !h.canManage(w, r, existing) {
		return
	}
	if err := h.user.Deactivate(r.Context(), existing.ID); err != nil {
		response.ErrNotFound(w, "user not found")
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "deactivated"})
}

func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	existing, ok := h.tenantUser(w, r)
	if !ok || !h.canManage(w, r, existing) {
		return
	}
	actorID, _ := uuid.Parse(middleware.GetUserID(r.Context()))
	if err := h.user.Delete(r.Context(), existing.ID, actorID); err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			response.ErrNotFound(w, "user not found")
			return
		}
		response.Unexpected(w, fmt.Errorf("borrar usuario: %w", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	var req changePasswordRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}

	v := validate.New()
	v.Required("current_password", req.CurrentPassword)
	v.Required("new_password", req.NewPassword)
	v.MinLength("new_password", req.NewPassword, 8)
	v.MaxLength("new_password", req.NewPassword, 128)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}

	userID, err := uuid.Parse(middleware.GetUserID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "invalid user")
		return
	}

	err = h.user.ChangePassword(r.Context(), userID, ports.ChangePasswordRequest{
		CurrentPassword: req.CurrentPassword,
		NewPassword:     req.NewPassword,
	})
	if err != nil {
		switch err {
		case domain.ErrInvalidCredentials:
			response.ErrUnauthorized(w, "current password is incorrect")
		case domain.ErrPasswordPolicyFail, domain.ErrPasswordBreached, domain.ErrPasswordReused:
			respondPasswordRejected(w, err)
		default:
			response.ErrInternal(w)
		}
		return
	}

	// Cambiar la contrasena cierra las demas sesiones: si un atacante tenia una
	// sesion robada, cambiar la clave la mata. Best-effort: no revierte el cambio.
	_ = h.auth.LogoutAll(r.Context(), userID)

	response.JSON(w, http.StatusOK, map[string]string{"status": "password changed"})
}

type resetPasswordRequest struct {
	NewPassword string `json:"new_password"`
}

// ResetPassword permite a un administrador fijar una contrasena nueva para otro
// usuario sin conocer la actual (recuperacion de acceso). Requiere users/reset_password
// (la ruta lo exige), que el objetivo sea de la misma empresa y que no sea un
// administrador si quien llama no lo es.
func (h *Handler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	existing, ok := h.tenantUser(w, r)
	if !ok || !h.canManage(w, r, existing) {
		return
	}

	var req resetPasswordRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}

	v := validate.New()
	v.Required("new_password", req.NewPassword)
	v.MinLength("new_password", req.NewPassword, 8)
	v.MaxLength("new_password", req.NewPassword, 128)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}

	if err := h.user.ResetPassword(r.Context(), existing.ID, req.NewPassword); err != nil {
		switch err {
		case domain.ErrUserNotFound:
			response.ErrNotFound(w, "user not found")
		case domain.ErrPasswordPolicyFail, domain.ErrPasswordBreached:
			respondPasswordRejected(w, err)
		default:
			response.ErrInternal(w)
		}
		return
	}

	// Un reset de admin invalida las sesiones del usuario objetivo: si su cuenta
	// estaba comprometida, el reset expulsa al atacante.
	_ = h.auth.LogoutAll(r.Context(), existing.ID)

	response.JSON(w, http.StatusOK, map[string]string{"status": "password reset"})
}

func userResponse(u *domain.User) map[string]interface{} {
	return map[string]interface{}{
		"id":            u.ID.String(),
		"email":         u.Email,
		"first_name":    u.FirstName,
		"last_name":     u.LastName,
		"avatar_url":    u.AvatarURL,
		"status":        string(u.Status),
		"mfa_enabled":   u.MFAEnabled,
		"last_login_at": u.LastLoginAt,
		"created_at":    u.CreatedAt,
		"updated_at":    u.UpdatedAt,
	}
}

// extractClientIP devuelve la IP del cliente desde X-Real-IP, que fija el gateway.
// X-Forwarded-For no se considera porque el cliente puede falsificarla.
func extractClientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	for i := len(r.RemoteAddr) - 1; i >= 0; i-- {
		if r.RemoteAddr[i] == ':' {
			return r.RemoteAddr[:i]
		}
	}
	return r.RemoteAddr
}

type mfaChallengeRequest struct {
	MFAToken   string `json:"mfa_token"`
	Code       string `json:"code"`
	CookieAuth bool   `json:"cookie_auth"`
}

func (h *Handler) MFAChallenge(w http.ResponseWriter, r *http.Request) {
	var req mfaChallengeRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}

	v := validate.New()
	v.Required("mfa_token", req.MFAToken)
	v.Required("code", req.Code)
	v.MinLength("code", req.Code, 6)
	v.MaxLength("code", req.Code, 6)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}

	res, err := h.auth.VerifyMFAChallenge(r.Context(), req.MFAToken, req.Code, extractClientIP(r), r.UserAgent())
	if err != nil {
		// Quien llega aqui ya probo la contrasena: el estado de su cuenta no revela nada.
		switch err {
		case domain.ErrAccountLocked:
			response.Err(w, http.StatusForbidden, "ACCOUNT_LOCKED", "account is temporarily locked")
		case domain.ErrAccountInactive:
			response.Err(w, http.StatusForbidden, "ACCOUNT_INACTIVE", "account is inactive")
		default:
			response.ErrUnauthorized(w, "invalid MFA code")
		}
		return
	}

	response.JSON(w, http.StatusOK, issueSession(w, r, res, req.CookieAuth))
}

type mfaSetupResponse struct {
	Secret          string `json:"secret"`
	ProvisioningURI string `json:"provisioning_uri"`
}

func (h *Handler) MFASetup(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(middleware.GetUserID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "invalid user")
		return
	}

	user, err := h.user.GetByID(r.Context(), userID)
	if err != nil {
		response.ErrNotFound(w, "user not found")
		return
	}

	secret, uri, err := h.auth.SetupMFA(r.Context(), userID, user.Email, h.mfaIssuer)
	if err != nil {
		response.ErrInternal(w)
		return
	}

	response.JSON(w, http.StatusOK, mfaSetupResponse{Secret: secret, ProvisioningURI: uri})
}

type mfaActivateRequest struct {
	Secret string `json:"secret"`
	Code   string `json:"code"`
}

func (h *Handler) MFAActivate(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(middleware.GetUserID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "invalid user")
		return
	}

	var req mfaActivateRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}

	v := validate.New()
	v.Required("secret", req.Secret)
	v.Required("code", req.Code)
	v.MinLength("code", req.Code, 6)
	v.MaxLength("code", req.Code, 6)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}

	if err := h.auth.ActivateMFA(r.Context(), userID, req.Secret, req.Code); err != nil {
		response.ErrUnauthorized(w, "invalid MFA code")
		return
	}

	response.JSON(w, http.StatusOK, map[string]string{"status": "mfa enabled"})
}

type stepUpRequest struct {
	CurrentPassword string `json:"current_password"`
	Code            string `json:"code"`
}

func (h *Handler) StepUp(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(middleware.GetUserID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "invalid user")
		return
	}
	var req stepUpRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	v.Required("current_password", req.CurrentPassword)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	token, err := h.auth.StepUp(r.Context(), userID, req.CurrentPassword, req.Code)
	if err != nil {
		switch err {
		case domain.ErrInvalidCredentials:
			response.ErrUnauthorized(w, "current password is incorrect")
		case domain.ErrInvalidMFACode:
			response.ErrUnauthorized(w, "invalid mfa code")
		default:
			response.ErrInternal(w)
		}
		return
	}
	response.JSON(w, http.StatusOK, map[string]interface{}{
		"step_up_token": token,
		"expires_in":    int(auth.StepUpTTL.Seconds()),
	})
}

type mfaDisableRequest struct {
	CurrentPassword string `json:"current_password"`
	Code            string `json:"code"`
}

func (h *Handler) MFADisable(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(middleware.GetUserID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "invalid user")
		return
	}

	var req mfaDisableRequest
	if err := validate.DecodeJSON(r, &req); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	v := validate.New()
	v.Required("current_password", req.CurrentPassword)
	v.Required("code", req.Code)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}

	if err := h.auth.DisableMFA(r.Context(), userID, req.CurrentPassword, req.Code); err != nil {
		switch err {
		case domain.ErrInvalidCredentials:
			response.ErrUnauthorized(w, "current password is incorrect")
		case domain.ErrInvalidMFACode:
			response.ErrUnauthorized(w, "invalid mfa code")
		default:
			response.ErrInternal(w)
		}
		return
	}

	response.JSON(w, http.StatusOK, map[string]string{"status": "mfa disabled"})
}

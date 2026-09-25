package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/mail-auth/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const (
	// maxVerifyBody acota el JSON de passwd-verify.lua: cuatro campos cortos.
	maxVerifyBody = 8 << 10

	defaultLoginsLimit = 20
	maxLoginsLimit     = 200
)

// Verifier es lo que el adaptador necesita del caso de uso.
type Verifier interface {
	Authenticate(ctx context.Context, req domain.VerifyRequest) domain.Verification
	RecentLogins(ctx context.Context, tenantID uuid.UUID, username string, limit int) ([]domain.Login, error)
}

type Handler struct {
	uc Verifier
}

func NewHandler(uc Verifier) *Handler { return &Handler{uc: uc} }

// VerifyRoutes es el router del listener TLS que consulta Dovecot. Sin token de
// gateway: passwd-verify.lua no envia ninguno, y el listener solo es alcanzable desde
// la red de los motores.
func (h *Handler) VerifyRoutes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.BodyLimit(maxVerifyBody))
	r.Post("/", h.Verify)
	r.Post("/auth", h.Verify)
	return r
}

// InternalRoutes es el router del listener HTTP interno (tras RequireGatewayToken).
func (h *Handler) InternalRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/internal/mail-auth/logins", h.RecentLogins)
	return r
}

// verifyRequest es el cuerpo exacto que compone passwd-verify.lua.
type verifyRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	RealRIP  string `json:"real_rip"`
	Service  string `json:"service"`
}

type verifyResponse struct {
	Success bool `json:"success"`
	// DisplayName solo viaja al webmail (service "webmail"), que lo usa como nombre del
	// remitente. passwd-verify.lua solo lee success e ignora el resto.
	DisplayName *string `json:"display_name,omitempty"`
	// TenantID y MailboxID viajan a mail-dav (service "dav") y al webmail, que no tienen otra forma
	// de saber a que empresa y a que buzon pertenece una credencial verificada: el webmail los guarda
	// en su sesion para hablar con mail-dav. Username solo viaja a mail-dav.
	Username  *string `json:"username,omitempty"`
	TenantID  *string `json:"tenant_id,omitempty"`
	MailboxID *string `json:"mailbox_id,omitempty"`
	// MFARequired solo viaja al webmail: la contrasena es correcta pero el buzon tiene verificacion en
	// dos pasos, y el webmail no abre sesion hasta el segundo paso.
	MFARequired *bool `json:"mfa_required,omitempty"`
}

// Verify responde 200 {"success":true}, 401 {"success":false} o 400 si el cuerpo esta
// incompleto. El cuerpo NO usa el envelope de la plataforma: el lua lee `success` en
// la raiz y cualquier otra forma la trata como contrasena incorrecta.
func (h *Handler) Verify(w http.ResponseWriter, r *http.Request) {
	var body verifyRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeVerify(w, http.StatusBadRequest, verifyResponse{})
		return
	}
	if body.Username == "" || body.Password == "" || body.RealRIP == "" {
		writeVerify(w, http.StatusBadRequest, verifyResponse{})
		return
	}
	v := h.uc.Authenticate(r.Context(), domain.VerifyRequest{
		Username: body.Username,
		Password: body.Password,
		RemoteIP: body.RealRIP,
		Service:  body.Service,
	})
	if !v.Result.Authorized() {
		writeVerify(w, http.StatusUnauthorized, verifyResponse{})
		return
	}
	resp := verifyResponse{Success: true}
	switch p, _ := domain.ProtocolFromService(body.Service); p {
	case domain.ProtocolWebmail:
		tenantID, mailboxID, mfa := v.TenantID.String(), v.MailboxID.String(), v.MFARequired
		resp.DisplayName, resp.TenantID, resp.MailboxID, resp.MFARequired = &v.DisplayName, &tenantID, &mailboxID, &mfa
	case domain.ProtocolDAV:
		tenantID, mailboxID := v.TenantID.String(), v.MailboxID.String()
		resp.Username, resp.TenantID, resp.MailboxID = &v.Username, &tenantID, &mailboxID
	}
	writeVerify(w, http.StatusOK, resp)
}

func writeVerify(w http.ResponseWriter, status int, resp verifyResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

// RecentLogins lista los ultimos inicios de un buzon de la empresa de la peticion.
func (h *Handler) RecentLogins(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(middleware.GetTenantID(r.Context()))
	if err != nil {
		response.ErrUnauthorized(w, "invalid tenant")
		return
	}
	username := r.URL.Query().Get("username")
	if username == "" {
		response.ErrBadRequest(w, "username es obligatorio")
		return
	}
	limit := defaultLoginsLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 {
			response.ErrBadRequest(w, "limit invalido")
			return
		}
		limit = min(v, maxLoginsLimit)
	}
	logins, err := h.uc.RecentLogins(r.Context(), tenantID, username, limit)
	if err != nil {
		response.Unexpected(w, err)
		return
	}
	out := make([]loginDTO, 0, len(logins))
	for _, l := range logins {
		out = append(out, toLoginDTO(l))
	}
	response.JSON(w, http.StatusOK, out)
}

type loginDTO struct {
	ID            uuid.UUID  `json:"id"`
	Username      string     `json:"username"`
	Service       string     `json:"service"`
	AppPasswordID *uuid.UUID `json:"app_password_id"`
	RemoteIP      string     `json:"remote_ip"`
	LoggedAt      string     `json:"logged_at"`
}

func toLoginDTO(l domain.Login) loginDTO {
	return loginDTO{
		ID:            l.ID,
		Username:      l.Username,
		Service:       l.Service,
		AppPasswordID: l.AppPasswordID,
		RemoteIP:      l.RemoteIP,
		LoggedAt:      l.LoggedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

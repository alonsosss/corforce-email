package http

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Seguridad del buzon (docs/Plan_Webmail_Seguridad.md, 3.2): la verificacion en dos pasos y las
// contrasenas de aplicacion del buzon del webmail por las rutas internas, y la politica de correo y el
// restablecimiento de la verificacion por el API del panel.

const (
	resourceMailPolicy = "mail_policy"
	resourceMailboxMFA = "mailbox_mfa"

	codeInvalidMFACode              = "INVALID_MFA_CODE"
	codeMFAAlreadyEnabled           = "MFA_ALREADY_ENABLED"
	codeMFANotEnabled               = "MFA_NOT_ENABLED"
	codeReauthRequired              = "REAUTH_REQUIRED"
	codeExternalForwardingDisabled  = "EXTERNAL_FORWARDING_DISABLED"
	maxSecurityBody                 = 4096
	appPasswordNameMax              = 100
	forwardingDetailsAddressesField = "addresses"
)

// writeSecurityError traduce los errores de la seguridad del buzon; false si no es uno de ellos.
// details.addresses lleva las direcciones separadas por coma: error.details es un mapa de textos en
// toda la plataforma, y una direccion ya validada no lleva comas.
func writeSecurityError(w http.ResponseWriter, err error) bool {
	var fwd *domain.ForwardingError
	switch {
	case errors.As(err, &fwd):
		status, code := http.StatusForbidden, codeReauthRequired
		if errors.Is(fwd.Kind, domain.ErrExternalForwardingDisabled) {
			status, code = http.StatusUnprocessableEntity, codeExternalForwardingDisabled
		}
		response.ErrWithDetails(w, status, code, fwd.Kind.Error(),
			map[string]string{forwardingDetailsAddressesField: strings.Join(fwd.Addresses, ",")})
	case errors.Is(err, domain.ErrInvalidMFACode):
		response.Err(w, http.StatusUnprocessableEntity, codeInvalidMFACode, err.Error())
	case errors.Is(err, domain.ErrMFAAlreadyEnabled):
		response.Err(w, http.StatusConflict, codeMFAAlreadyEnabled, err.Error())
	case errors.Is(err, domain.ErrMFANotEnabled):
		response.Err(w, http.StatusConflict, codeMFANotEnabled, err.Error())
	default:
		return false
	}
	return true
}

// ── Verificacion en dos pasos del buzon del webmail ──────────────────────────

type mfaStatusResponse struct {
	Enabled           bool       `json:"enabled"`
	EnabledAt         *time.Time `json:"enabled_at"`
	RecoveryRemaining int        `json:"recovery_remaining"`
}

type mfaActivateRequest struct {
	Secret string `json:"secret"`
	Code   string `json:"code"`
}

type mfaCodeRequest struct {
	Code string `json:"code"`
}

type mfaVerifyResponse struct {
	Method            string `json:"method"`
	RecoveryRemaining int    `json:"recovery_remaining"`
}

type recoveryCodesResponse struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

func decodeSmall(w http.ResponseWriter, r *http.Request, dst interface{}) bool {
	if err := validate.DecodeJSONLimit(w, r, dst, maxSecurityBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return false
	}
	return true
}

func (h *Handler) InternalMFAStatus(w http.ResponseWriter, r *http.Request) {
	s, err := h.uc.MFAStatusByUsername(r.Context(), r.URL.Query().Get("username"))
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, mfaStatusResponse{Enabled: s.Enabled, EnabledAt: s.EnabledAt, RecoveryRemaining: s.RecoveryRemaining})
}

func (h *Handler) InternalActivateMFA(w http.ResponseWriter, r *http.Request) {
	var req mfaActivateRequest
	if !decodeSmall(w, r, &req) {
		return
	}
	codes, err := h.uc.ActivateMFAByUsername(r.Context(), r.URL.Query().Get("username"), req.Secret, req.Code)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, recoveryCodesResponse{RecoveryCodes: codes})
}

func (h *Handler) InternalVerifyMFA(w http.ResponseWriter, r *http.Request) {
	var req mfaCodeRequest
	if !decodeSmall(w, r, &req) {
		return
	}
	v, err := h.uc.VerifyMFAByUsername(r.Context(), r.URL.Query().Get("username"), req.Code)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, mfaVerifyResponse{Method: v.Method, RecoveryRemaining: v.RecoveryRemaining})
}

func (h *Handler) InternalRegenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	var req mfaCodeRequest
	if !decodeSmall(w, r, &req) {
		return
	}
	codes, err := h.uc.RegenerateRecoveryCodesByUsername(r.Context(), r.URL.Query().Get("username"), req.Code)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, recoveryCodesResponse{RecoveryCodes: codes})
}

func (h *Handler) InternalDisableMFA(w http.ResponseWriter, r *http.Request) {
	var req mfaCodeRequest
	if !decodeSmall(w, r, &req) {
		return
	}
	if err := h.uc.DisableMFAByUsername(r.Context(), r.URL.Query().Get("username"), req.Code); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Contrasenas de aplicacion del buzon del webmail ──────────────────────────

type internalAppPasswordRequest struct {
	Name  string `json:"name"`
	IMAP  *bool  `json:"imap"`
	POP3  *bool  `json:"pop3"`
	SMTP  *bool  `json:"smtp"`
	Sieve *bool  `json:"sieve"`
	DAV   *bool  `json:"dav"`
}

// internalAppPasswordCreated es el registro con la contrasena generada, que solo viaja en esta
// respuesta.
type internalAppPasswordCreated struct {
	domain.AppPassword
	Password string `json:"password"`
}

func (h *Handler) InternalListAppPasswords(w http.ResponseWriter, r *http.Request) {
	items, err := h.uc.ListAppPasswordsByUsername(r.Context(), r.URL.Query().Get("username"))
	if err != nil {
		writeError(w, err)
		return
	}
	// El tope viaja con la lista: el webmail lo ensena y no lo copia.
	response.JSON(w, http.StatusOK, internalAppPasswordList{Items: items, Max: domain.MaxAppPasswordsPerMailbox})
}

type internalAppPasswordList struct {
	Items []domain.AppPassword `json:"items"`
	Max   int                  `json:"max"`
}

func (h *Handler) InternalCreateAppPassword(w http.ResponseWriter, r *http.Request) {
	var req internalAppPasswordRequest
	if !decodeSmall(w, r, &req) {
		return
	}
	v := validate.New()
	v.Required("name", req.Name)
	v.MaxLength("name", req.Name, appPasswordNameMax)
	if !v.Valid() {
		response.ErrValidation(w, v.Error())
		return
	}
	p, plain, err := h.uc.CreateAppPasswordByUsername(r.Context(), r.URL.Query().Get("username"), app.CreateAppPasswordRequest{
		Name: req.Name, IMAPAccess: req.IMAP, POP3Access: req.POP3, SMTPAccess: req.SMTP, SieveAccess: req.Sieve, DAVAccess: req.DAV,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, internalAppPasswordCreated{AppPassword: *p, Password: plain})
}

func (h *Handler) InternalDeleteAppPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.uc.DeleteAppPasswordByUsername(r.Context(), r.URL.Query().Get("username"), id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Politica de correo de la empresa ──────────────────────────────────────────

type mailPolicyRequest struct {
	ExternalForwardingAllowed *bool `json:"external_forwarding_allowed"`
}

type mailPolicyResponse struct {
	ExternalForwardingAllowed bool       `json:"external_forwarding_allowed"`
	UpdatedAt                 *time.Time `json:"updated_at"`
}

// mailPolicyUpdateResponse anade cuantos buzones perdieron reenvios externos con el cambio.
type mailPolicyUpdateResponse struct {
	mailPolicyResponse
	RemovedMailboxes int `json:"removed_mailboxes"`
}

func toMailPolicyResponse(p *domain.MailPolicy) mailPolicyResponse {
	return mailPolicyResponse{ExternalForwardingAllowed: p.ExternalForwardingAllowed, UpdatedAt: updatedAt(p.UpdatedAt)}
}

func (h *Handler) mailPolicyRoutes(r chi.Router) {
	r.With(h.require(moduleMailboxes, resourceMailPolicy, actionRead)).Get("/", h.GetMailPolicy)
	r.With(h.require(moduleMailboxes, resourceMailPolicy, actionUpdate)).Put("/", h.SetMailPolicy)
}

func (h *Handler) GetMailPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	p, err := h.uc.MailPolicy(r.Context(), tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toMailPolicyResponse(p))
}

// SetMailPolicy exige el valor explicito: un cuerpo vacio no cambia la politica por omision.
func (h *Handler) SetMailPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	var req mailPolicyRequest
	if !decode(w, r, &req) {
		return
	}
	if req.ExternalForwardingAllowed == nil {
		response.ErrWithDetails(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "external_forwarding_allowed es obligatorio",
			map[string]string{"field": "external_forwarding_allowed"})
		return
	}
	by, _ := uuid.Parse(middleware.GetUserID(r.Context()))
	p, removed, err := h.uc.SetMailPolicy(r.Context(), tenantID, by, *req.ExternalForwardingAllowed)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, mailPolicyUpdateResponse{mailPolicyResponse: toMailPolicyResponse(p), RemovedMailboxes: removed})
}

// ResetMailboxMFA restablece la verificacion en dos pasos del buzon: 204, o 409 MFA_NOT_ENABLED si no
// la tenia.
func (h *Handler) ResetMailboxMFA(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	actor, _ := uuid.Parse(middleware.GetUserID(r.Context()))
	if err := h.uc.ResetMailboxMFA(r.Context(), tenantID, id, actor); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

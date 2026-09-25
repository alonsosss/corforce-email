package http

import (
	"net/http"
	"time"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/go-chi/chi/v5"
)

// maxSecurityBody acota los cuerpos de la seguridad del buzon: contrasena, codigo, secreto y nombre.
const maxSecurityBody = 8 << 10

type mfaStatusDTO struct {
	Enabled           bool       `json:"enabled"`
	EnabledAt         *time.Time `json:"enabled_at"`
	RecoveryRemaining int        `json:"recovery_remaining"`
}

// appPasswordDTO nombra los protocolos como el listado de administracion (*_access), que es
// el que ya conoce la interfaz.
type appPasswordDTO struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	IMAP       bool       `json:"imap_access"`
	POP3       bool       `json:"pop3_access"`
	SMTP       bool       `json:"smtp_access"`
	Sieve      bool       `json:"sieve_access"`
	DAV        bool       `json:"dav_access"`
	Active     bool       `json:"active"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

type createdAppPasswordDTO struct {
	appPasswordDTO
	Password string `json:"password"`
}

// securityDTO: app_passwords_max es null si mail-directory no informa el tope.
type securityDTO struct {
	MFA             mfaStatusDTO     `json:"mfa"`
	AppPasswords    []appPasswordDTO `json:"app_passwords"`
	AppPasswordsMax *int             `json:"app_passwords_max"`
}

type mfaSetupDTO struct {
	Secret          string `json:"secret"`
	ProvisioningURI string `json:"provisioning_uri"`
}

type recoveryCodesDTO struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

type currentPasswordRequest struct {
	CurrentPassword string `json:"current_password"`
}

type activateMFARequest struct {
	Secret string `json:"secret"`
	Code   string `json:"code"`
}

type codeRequest struct {
	Code string `json:"code"`
}

type reauthRequest struct {
	CurrentPassword string `json:"current_password"`
	Code            string `json:"code"`
}

type createAppPasswordRequest struct {
	Name            string `json:"name"`
	IMAP            bool   `json:"imap"`
	POP3            bool   `json:"pop3"`
	SMTP            bool   `json:"smtp"`
	Sieve           bool   `json:"sieve"`
	DAV             bool   `json:"dav"`
	CurrentPassword string `json:"current_password"`
	Code            string `json:"code"`
}

func toAppPasswordDTO(p domain.AppPassword) appPasswordDTO {
	return appPasswordDTO{
		ID: p.ID, Name: p.Name, IMAP: p.Access.IMAP, POP3: p.Access.POP3, SMTP: p.Access.SMTP, Sieve: p.Access.Sieve,
		DAV: p.Access.DAV, Active: p.Active, LastUsedAt: p.LastUsedAt, CreatedAt: p.CreatedAt,
	}
}

func toSecurityDTO(o domain.SecurityOverview) securityDTO {
	out := securityDTO{
		MFA:          mfaStatusDTO{Enabled: o.MFA.Enabled, EnabledAt: o.MFA.EnabledAt, RecoveryRemaining: o.MFA.RecoveryRemaining},
		AppPasswords: make([]appPasswordDTO, len(o.AppPasswords.Items)),
	}
	for i, p := range o.AppPasswords.Items {
		out.AppPasswords[i] = toAppPasswordDTO(p)
	}
	if o.AppPasswords.Max > 0 {
		limit := o.AppPasswords.Max
		out.AppPasswordsMax = &limit
	}
	return out
}

func (h *Handler) securityRoutes(r chi.Router) {
	r.Get("/security", h.Security)
	r.Post("/security/mfa/setup", h.PrepareMFA)
	r.Post("/security/mfa/activate", h.ActivateMFA)
	r.Post("/security/mfa/recovery-codes", h.RegenerateRecoveryCodes)
	r.Delete("/security/mfa", h.DisableMFA)
	r.Post("/security/app-passwords", h.CreateAppPassword)
	r.Delete("/security/app-passwords/{id}", h.DeleteAppPassword)
}

func (h *Handler) Security(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	o, err := h.app.Security(ctx, sessionFrom(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toSecurityDTO(o))
}

// PrepareMFA exige la contrasena actual y devuelve un secreto nuevo con su URI para el QR.
func (h *Handler) PrepareMFA(w http.ResponseWriter, r *http.Request) {
	var req currentPasswordRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxSecurityBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	setup, err := h.app.PrepareMFA(ctx, sessionFrom(r), req.CurrentPassword, clientIP(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, mfaSetupDTO{Secret: setup.Secret, ProvisioningURI: setup.ProvisioningURI})
}

// ActivateMFA activa con el secreto preparado y un codigo; responde los codigos de recuperacion.
func (h *Handler) ActivateMFA(w http.ResponseWriter, r *http.Request) {
	var req activateMFARequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxSecurityBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	codes, err := h.app.ActivateMFA(ctx, sessionFrom(r), req.Secret, req.Code)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, recoveryCodesDTO{RecoveryCodes: nonNil(codes)})
}

func (h *Handler) RegenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	var req codeRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxSecurityBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	codes, err := h.app.RegenerateRecoveryCodes(ctx, sessionFrom(r), req.Code)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, recoveryCodesDTO{RecoveryCodes: nonNil(codes)})
}

func (h *Handler) DisableMFA(w http.ResponseWriter, r *http.Request) {
	var req reauthRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxSecurityBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	reauth := domain.Reauthentication{CurrentPassword: req.CurrentPassword, Code: req.Code}
	if err := h.app.DisableMFA(ctx, sessionFrom(r), reauth, clientIP(r)); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// CreateAppPassword responde la contrasena generada una unica vez, junto al registro.
func (h *Handler) CreateAppPassword(w http.ResponseWriter, r *http.Request) {
	var req createAppPasswordRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxSecurityBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	in := domain.AppPasswordInput{Name: req.Name, Access: domain.AppPasswordAccess{
		IMAP: req.IMAP, POP3: req.POP3, SMTP: req.SMTP, Sieve: req.Sieve, DAV: req.DAV,
	}}
	reauth := domain.Reauthentication{CurrentPassword: req.CurrentPassword, Code: req.Code}
	created, err := h.app.CreateAppPassword(ctx, sessionFrom(r), in, reauth, clientIP(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusCreated, createdAppPasswordDTO{appPasswordDTO: toAppPasswordDTO(created.AppPassword), Password: created.Password})
}

func (h *Handler) DeleteAppPassword(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	if err := h.app.DeleteAppPassword(ctx, sessionFrom(r), chi.URLParam(r, "id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

package http

import (
	"context"
	"errors"
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/go-chi/chi/v5"
)

// maxVerifyBody: un token de 123 caracteres y un nombre de buzon.
const maxVerifyBody = 4 << 10

// CredentialVerifier es lo que el adaptador necesita del caso de uso.
type CredentialVerifier interface {
	VerifyDestinationCredential(ctx context.Context, token, username string) (app.VerifiedCredential, error)
}

// CredentialHandler atiende a mail-auth: le dice si una credencial de destino abre un buzon. Es una ruta
// entre servicios, detras del token de gateway y sin sesion de persona; el gateway no la enruta.
type CredentialHandler struct {
	uc CredentialVerifier
}

func NewCredentialHandler(uc CredentialVerifier) *CredentialHandler {
	return &CredentialHandler{uc: uc}
}

// CredentialsPath es donde se monta: la ruta completa es CredentialsPath + "/verify".
const CredentialsPath = "/internal/mail-migration/credentials"

func (h *CredentialHandler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Post("/verify", h.Verify)
	return r
}

type verifyRequest struct {
	Token    string `json:"token"`
	Username string `json:"username"`
}

type verifyDTO struct {
	TenantID  string `json:"tenant_id"`
	JobID     string `json:"job_id"`
	MailboxID string `json:"mailbox_id"`
	Username  string `json:"username"`
}

// Verify responde 200 con el buzon y el trabajo si la credencial abre el buzon, 401 si no (sin decir por
// que) y un error de servidor si no pudo decidir, que quien llama no debe tomar por un rechazo.
func (h *CredentialHandler) Verify(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var req verifyRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxVerifyBody); err != nil {
		response.ErrBadRequest(w, "cuerpo no válido")
		return
	}
	if req.Token == "" || req.Username == "" {
		response.ErrBadRequest(w, "token y username son obligatorios")
		return
	}
	v, err := h.uc.VerifyDestinationCredential(r.Context(), req.Token, req.Username)
	switch {
	case err == nil:
		response.JSON(w, http.StatusOK, verifyDTO{
			TenantID: v.TenantID.String(), JobID: v.JobID.String(), MailboxID: v.MailboxID.String(), Username: v.Username,
		})
	case errors.Is(err, domain.ErrCredentialInvalid):
		response.ErrUnauthorized(w, "credencial no válida")
	default:
		response.Unexpected(w, err)
	}
}

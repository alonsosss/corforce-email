package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
)

// SpamCheckPath es la ruta interna con la que templates puntua un correo antes de publicarlo.
const SpamCheckPath = "/internal/mail-security/spam-check"

// spamCheckMaxBody acota el JSON: el MIME llega como cadena JSON y en el peor caso cada byte se escapa
// como \u00XX (seis bytes). El tope del mensaje se comprueba despues de decodificarlo.
const spamCheckMaxBody = 6*domain.MaxSpamCheckMessageBytes + 4096

// WithSpamCheck conecta la puntuacion antispam. Sin ella, la ruta responde 503 NOT_CONFIGURED.
func (h *Handler) WithSpamCheck(spamCheck *app.SpamCheckUseCase) *Handler {
	h.spamCheck = spamCheck
	return h
}

// CellFreeRequest dice si la peticion es de una ruta que no lee ni escribe datos de ninguna empresa y que
// por eso no pasa por el filtro de celda (tenantcell.Membership): la puntuacion antispam la pide el
// templates de una empresa de cualquier celda al mail-security de la celda base, y la empresa que viaje en
// X-Tenant-ID no es de esta. Solo sin usuario: una peticion con sesion sigue el filtro normal.
func CellFreeRequest(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == SpamCheckPath && middleware.GetUserID(r.Context()) == ""
}

type spamCheckRequest struct {
	Message *string `json:"message"`
}

type spamCheckSymbolResponse struct {
	Name        string      `json:"name"`
	Score       json.Number `json:"score"`
	Description string      `json:"description"`
}

// spamCheckResponse escribe las puntuaciones como numeros JSON, como las da Rspamd.
type spamCheckResponse struct {
	Score    json.Number               `json:"score"`
	Required json.Number               `json:"required"`
	Action   string                    `json:"action"`
	Symbols  []spamCheckSymbolResponse `json:"symbols"`
}

func newSpamCheckResponse(in domain.SpamCheckResult) spamCheckResponse {
	out := spamCheckResponse{
		Score: json.Number(in.Score.String()), Required: json.Number(in.Required.String()), Action: in.Action,
		Symbols: make([]spamCheckSymbolResponse, 0, len(in.Symbols)),
	}
	for _, s := range in.Symbols {
		out.Symbols = append(out.Symbols, spamCheckSymbolResponse{Name: s.Name, Score: json.Number(s.Score.String()), Description: s.Description})
	}
	return out
}

// SpamCheck puntua con Rspamd un MIME completo sin entregarlo (docs/Plan_Editor_Correos.md, seccion 4).
func (h *Handler) SpamCheck(w http.ResponseWriter, r *http.Request) {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, spamCheckMaxBody))
	dec.DisallowUnknownFields()
	var in spamCheckRequest
	if err := dec.Decode(&in); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeSpamCheckError(w, domain.ErrSpamCheckTooLarge)
			return
		}
		response.ErrBadRequest(w, "cuerpo JSON inválido: se espera {\"message\": \"<MIME>\"}")
		return
	}
	if in.Message == nil {
		writeSpamCheckError(w, domain.ErrSpamCheckEmpty)
		return
	}
	if h.spamCheck == nil {
		writeSpamCheckError(w, domain.ErrNotConfigured)
		return
	}
	out, err := h.spamCheck.Check(r.Context(), []byte(*in.Message))
	if err != nil {
		writeSpamCheckError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, newSpamCheckResponse(out))
}

func writeSpamCheckError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrSpamCheckTooLarge):
		response.Err(w, http.StatusRequestEntityTooLarge, "MESSAGE_TOO_LARGE", err.Error())
	case errors.Is(err, domain.ErrSpamCheckEmpty):
		response.Err(w, http.StatusBadRequest, "MESSAGE_REQUIRED", err.Error())
	case errors.Is(err, domain.ErrNotConfigured):
		response.Err(w, http.StatusServiceUnavailable, "NOT_CONFIGURED", "la puntuación antispam no está configurada en esta celda")
	case errors.Is(err, domain.ErrEngineUnreachable), errors.Is(err, domain.ErrEngineCommand):
		response.Err(w, http.StatusServiceUnavailable, "SPAM_CHECK_UNAVAILABLE", "rspamd no pudo puntuar el mensaje")
	default:
		response.Unexpected(w, err)
	}
}

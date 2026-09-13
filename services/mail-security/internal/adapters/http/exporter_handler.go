package http

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

// Cuerpo maximo de /pipe_rl: es un JSON pequeno.
const rateLimitMaxBody = 256 * 1024

// ExporterHandler sirve el puerto 9081 (metadata_exporter de Rspamd). El tope de
// cuerpo de /pipe es el techo fisico del proceso; el limite por empresa se aplica
// despues con sus ajustes.
type ExporterHandler struct {
	uc          *app.EngineUseCase
	pipeMaxBody int64
	logger      *zap.Logger
}

func NewExporterHandler(uc *app.EngineUseCase, pipeMaxBody int64, logger *zap.Logger) *ExporterHandler {
	return &ExporterHandler{uc: uc, pipeMaxBody: pipeMaxBody, logger: logger}
}

func (h *ExporterHandler) Routes(allowedCIDRs string) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(EngineNetworkGuard(allowedCIDRs))
	r.With(middleware.BodyLimit(h.pipeMaxBody)).Post("/pipe", h.Pipe)
	r.With(middleware.BodyLimit(rateLimitMaxBody)).Post("/pipe_rl", h.PipeRateLimit)
	return r
}

// Pipe responde como documenta el contrato: 200 guardado, 400 partes ausentes o JSON
// invalido, 505 mensaje mayor que el limite, 502 error resolviendo destinatarios, 503
// error al insertar.
func (h *ExporterHandler) Pipe(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(h.pipeMaxBody); err != nil {
		plain(w, http.StatusBadRequest, "multipart invalido")
		return
	}
	metaField := r.FormValue("metadata")
	if metaField == "" {
		plain(w, http.StatusBadRequest, "falta metadata")
		return
	}
	var meta domain.QuarantineMetadata
	if err := json.Unmarshal([]byte(metaField), &meta); err != nil {
		plain(w, http.StatusBadRequest, "metadata invalido")
		return
	}
	file, _, err := r.FormFile("message")
	if err != nil {
		plain(w, http.StatusBadRequest, "falta message")
		return
	}
	defer file.Close()
	msg, err := io.ReadAll(file)
	if err != nil || len(msg) == 0 {
		plain(w, http.StatusBadRequest, "message vacio")
		return
	}

	out, err := h.uc.Pipe(r.Context(), meta, msg)
	if err != nil {
		var storeErr *app.StoreError
		h.logger.Error("cuarentena", zap.String("qid", meta.QID), zap.Error(err))
		if errors.As(err, &storeErr) {
			plain(w, http.StatusServiceUnavailable, "")
			return
		}
		plain(w, http.StatusBadGateway, "")
		return
	}
	if out.Stored == 0 && out.SkippedSize > 0 {
		plain(w, http.StatusHTTPVersionNotSupported, "message too large")
		return
	}
	plain(w, http.StatusOK, "")
}

func (h *ExporterHandler) PipeRateLimit(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Rcpt      []string `json:"rcpt"`
		From      string   `json:"from"`
		User      string   `json:"user"`
		QID       string   `json:"qid"`
		IP        string   `json:"ip"`
		MessageID string   `json:"message_id"`
		Subject   []string `json:"header_subject"`
		HdrFrom   []string `json:"header_from"`
		Symbols   []struct {
			Name    string   `json:"name"`
			Options []string `json:"options"`
		} `json:"symbols"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		plain(w, http.StatusBadRequest, "json invalido")
		return
	}
	entry := domain.RateLimitLog{
		Rcpt: payload.Rcpt, From: payload.From, User: payload.User, QID: payload.QID, IP: payload.IP,
		MessageID: payload.MessageID, HeaderSubject: payload.Subject, HeaderFrom: payload.HdrFrom,
	}
	for _, s := range payload.Symbols {
		if s.Name == "RATELIMITED" && len(s.Options) > 0 {
			entry.RLInfo = s.Options[0]
		}
	}
	if err := h.uc.PipeRateLimit(r.Context(), entry); err != nil {
		h.logger.Error("registro de ratelimit", zap.Error(err))
		if errors.Is(err, domain.ErrRedisUnavailable) {
			plain(w, http.StatusGatewayTimeout, "")
			return
		}
		plain(w, http.StatusBadGateway, "")
		return
	}
	plain(w, http.StatusOK, "")
}

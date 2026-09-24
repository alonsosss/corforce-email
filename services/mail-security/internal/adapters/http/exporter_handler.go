package http

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

// Cuerpo maximo de /pipe_rl: es un JSON pequeno.
const rateLimitMaxBody = 256 * 1024

// pipeMetadata es el campo metadata que manda metadata_exporter de Rspamd (formatter
// multipart). Rspamd 4 manda cada simbolo como objeto (name, score, options, groups) y rcpt
// como la cadena "unknown" si el mensaje no trae destinatarios SMTP; tambien se admiten los
// simbolos como cadenas. La cuarentena guarda el nombre de cada simbolo.
type pipeMetadata struct {
	QID       string          `json:"qid"`
	Subject   string          `json:"subject"`
	Score     decimal.Decimal `json:"score"`
	Rcpt      addressList     `json:"rcpt"`
	User      string          `json:"user"`
	IP        string          `json:"ip"`
	Action    string          `json:"action"`
	From      string          `json:"from"`
	Symbols   symbolNames     `json:"symbols"`
	Fuzzy     []string        `json:"fuzzy"`
	MessageID string          `json:"message_id"`
}

func (m pipeMetadata) toDomain() domain.QuarantineMetadata {
	return domain.QuarantineMetadata{
		QID: m.QID, Subject: m.Subject, Score: m.Score, Rcpt: m.Rcpt, User: m.User, IP: m.IP,
		Action: m.Action, From: m.From, Symbols: m.Symbols, Fuzzy: m.Fuzzy, MessageID: m.MessageID,
	}
}

// addressList admite la lista de Rspamd o una cadena: "unknown" (sin destinatarios) no es
// una direccion, y el formato aplanado separa por comas.
type addressList []string

func (l *addressList) UnmarshalJSON(data []byte) error {
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		*l = list
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	*l = nil
	for _, a := range strings.Split(s, ",") {
		if a = strings.TrimSpace(a); a != "" && a != "unknown" {
			*l = append(*l, a)
		}
	}
	return nil
}

// symbolNames admite cada simbolo como objeto con name o como cadena.
type symbolNames []string

func (n *symbolNames) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		var name string
		if err := json.Unmarshal(item, &name); err != nil {
			var sym struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(item, &sym); err != nil {
				return err
			}
			name = sym.Name
		}
		if name != "" {
			out = append(out, name)
		}
	}
	*n = out
	return nil
}

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
		plain(w, http.StatusBadRequest, "multipart inválido")
		return
	}
	metaField := r.FormValue("metadata")
	if metaField == "" {
		plain(w, http.StatusBadRequest, "falta metadata")
		return
	}
	var wire pipeMetadata
	if err := json.Unmarshal([]byte(metaField), &wire); err != nil {
		plain(w, http.StatusBadRequest, "metadata inválido")
		return
	}
	meta := wire.toDomain()
	file, _, err := r.FormFile("message")
	if err != nil {
		plain(w, http.StatusBadRequest, "falta message")
		return
	}
	defer file.Close()
	msg, err := io.ReadAll(file)
	if err != nil || len(msg) == 0 {
		plain(w, http.StatusBadRequest, "message vacío")
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
		plain(w, http.StatusBadRequest, "json inválido")
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

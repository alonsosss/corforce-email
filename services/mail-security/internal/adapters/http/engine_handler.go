package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

// Cuerpo maximo que admiten los mapas dinamicos: llegan sin cuerpo (todo va en
// cabeceras), asi que cualquier cosa mayor es un error o un abuso.
const mapsMaxBody = 16 * 1024

// EngineHandler sirve el puerto 8081 (mapas dinamicos de Rspamd y Postfix). Todo es
// texto plano salvo /footer, y los codigos siguen deploy/mail/README.md: 502 error de
// base, 504 sin Redis.
type EngineHandler struct {
	uc     *app.EngineUseCase
	logger *zap.Logger
}

func NewEngineHandler(uc *app.EngineUseCase, logger *zap.Logger) *EngineHandler {
	return &EngineHandler{uc: uc, logger: logger}
}

func (h *EngineHandler) Routes(allowedCIDRs string) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(EngineNetworkGuard(allowedCIDRs))
	r.Use(middleware.BodyLimit(mapsMaxBody))
	r.Post("/aliasexp", h.AliasExp)
	r.Post("/bcc", h.BCC)
	r.Post("/footer", h.Footer)
	r.Get("/forwardinghosts", h.ForwardingHosts)
	r.Get("/settings", h.Settings)
	// Rspamd 4 pide cada mapa HTTP con HEAD antes del GET y, si el HEAD no responde, da la
	// carga por fallida y no reintenta en un cuarto de hora: sin esto nunca aplicaba settings.
	r.Head("/forwardinghosts", h.ForwardingHosts)
	r.Head("/settings", h.Settings)
	return r
}

func plain(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func (h *EngineHandler) engineError(w http.ResponseWriter, what string, err error) {
	h.logger.Error("mapa dinamico", zap.String("endpoint", what), zap.Error(err))
	if errors.Is(err, domain.ErrRedisUnavailable) {
		plain(w, http.StatusGatewayTimeout, "")
		return
	}
	plain(w, http.StatusBadGateway, "")
}

func (h *EngineHandler) AliasExp(w http.ResponseWriter, r *http.Request) {
	username, err := h.uc.AliasExpand(r.Context(), r.Header.Get("Rcpt"))
	if err != nil {
		h.engineError(w, "aliasexp", err)
		return
	}
	plain(w, http.StatusOK, username)
}

// BCC: Rspamd manda Rcpt: para las copias por destinatario y From: para las copias
// por remitente, en llamadas distintas. Solo actua con 201.
func (h *EngineHandler) BCC(w http.ResponseWriter, r *http.Request) {
	kind, value := "rcpt", r.Header.Get("Rcpt")
	if value == "" {
		kind, value = "sender", r.Header.Get("From")
	}
	if value == "" {
		plain(w, http.StatusOK, "")
		return
	}
	dest, found, err := h.uc.BCC(r.Context(), kind, value)
	if err != nil {
		h.engineError(w, "bcc", err)
		return
	}
	if !found {
		plain(w, http.StatusOK, "")
		return
	}
	plain(w, http.StatusCreated, dest)
}

func (h *EngineHandler) Footer(w http.ResponseWriter, r *http.Request) {
	resp, err := h.uc.Footer(r.Context(), r.Header.Get("Domain"), r.Header.Get("Username"), r.Header.Get("From"))
	if err != nil {
		h.engineError(w, "footer", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	// El consumidor es Rspamd, no un navegador: el HTML del pie va tal cual.
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(resp)
}

const (
	tcpTablePermit = "200 PERMIT"
	tcpTableDunno  = "200 DUNNO"
)

// ForwardingHosts con ?host= sigue el protocolo tcp_table de Postfix: siempre HTTP 200
// y un cuerpo "200 PERMIT" o "200 DUNNO". whitelist_forwardinghosts.sh lo reenvia tal cual
// a Postfix, que sin el codigo de estado lo descarta como respuesta mal formada. Sin host devuelve el mapa de CIDR para Rspamd.
func (h *EngineHandler) ForwardingHosts(w http.ResponseWriter, r *http.Request) {
	if host := r.URL.Query().Get("host"); host != "" {
		ok, err := h.uc.ForwardingHostPermits(r.Context(), host)
		if err != nil {
			h.logger.Error("mapa dinamico", zap.String("endpoint", "forwardinghosts"), zap.Error(err))
			plain(w, http.StatusOK, tcpTableDunno)
			return
		}
		if ok {
			plain(w, http.StatusOK, tcpTablePermit)
			return
		}
		plain(w, http.StatusOK, tcpTableDunno)
		return
	}
	list, err := h.uc.ForwardingHostList(r.Context())
	if err != nil {
		h.engineError(w, "forwardinghosts", err)
		return
	}
	plain(w, http.StatusOK, strings.Join(list, "\n")+"\n")
}

func (h *EngineHandler) Settings(w http.ResponseWriter, r *http.Request) {
	var since time.Time
	if ims := r.Header.Get("If-Modified-Since"); ims != "" {
		if t, err := http.ParseTime(ims); err == nil {
			since = t
		}
	}
	doc, notModified, err := h.uc.Settings(r.Context(), since)
	if err != nil {
		h.engineError(w, "settings", err)
		return
	}
	w.Header().Set("Last-Modified", doc.LastModified.UTC().Format(http.TimeFormat))
	if notModified {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	plain(w, http.StatusOK, doc.Body)
}

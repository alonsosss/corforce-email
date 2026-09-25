package http

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/response"
)

const rateLimitedMessage = "demasiadas peticiones; vuelve a intentarlo en unos segundos"

// limitByIP acota por la IP real del cliente (X-Real-IP del gateway) lo que se sirve sin sesion.
func (h *Handler) limitByIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.overIPLimit(w, r) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// overIPLimit cuenta la peticion contra el cupo de su IP y, si lo supera, responde 429.
func (h *Handler) overIPLimit(w http.ResponseWriter, r *http.Request) bool {
	allowed, reset := h.cfg.IPRateLimiter.AllowIP(r.Context(), clientIP(r))
	if allowed {
		return false
	}
	writeRateLimited(w, reset)
	return true
}

// limitByMailbox acota por el buzon de la sesion; va siempre detras de requireSession o
// requireSessionPeek.
func (h *Handler) limitByMailbox(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed, reset := h.cfg.MailboxRateLimiter.AllowKey(r.Context(), sessionFrom(r).Username)
		if !allowed {
			writeRateLimited(w, reset)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeRateLimited responde 429 RATE_LIMITED con Retry-After redondeado hacia arriba: con 0 el
// cliente reintentaria dentro de una ventana que sigue cerrada.
func writeRateLimited(w http.ResponseWriter, reset time.Duration) {
	secs := int64(math.Ceil(reset.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(secs, 10))
	response.Err(w, http.StatusTooManyRequests, "RATE_LIMITED", rateLimitedMessage)
}

// addressesErrorEnvelope es el envelope de error de pkg/response con una lista en details, que
// response.APIError no admite (sus detalles son texto).
type addressesErrorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details struct {
			Addresses []string `json:"addresses"`
		} `json:"details"`
	} `json:"error"`
}

// writeAddressesError responde un rechazo con las direcciones que lo causan en details.addresses.
func writeAddressesError(w http.ResponseWriter, status int, code, message string, addresses []string) {
	var env addressesErrorEnvelope
	env.Error.Code, env.Error.Message = code, message
	env.Error.Details.Addresses = nonNil(addresses)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}

package http

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
)

// deliveryEventResponse es una fila del registro de entregas tal como la ve la empresa.
type deliveryEventResponse struct {
	ID           string    `json:"id"`
	Direction    string    `json:"direction"`
	QueueID      string    `json:"queue_id"`
	MessageID    string    `json:"message_id"`
	Sender       string    `json:"sender"`
	Recipient    string    `json:"recipient"`
	Status       string    `json:"status"`
	DSN          string    `json:"dsn"`
	Relay        string    `json:"relay"`
	Reason       string    `json:"reason"`
	DelaySeconds *string   `json:"delay_seconds"`
	SASLUsername string    `json:"sasl_username"`
	OccurredAt   time.Time `json:"occurred_at"`
}

// ListDeliveryLog: GET /delivery-log?direction&status&address&date_from&date_to&page&per_page.
func (h *Handler) ListDeliveryLog(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := tenantFrom(w, r)
	if !ok {
		return
	}
	if h.delivery == nil {
		response.Err(w, http.StatusServiceUnavailable, "NOT_CONFIGURED", domain.ErrNotConfigured.Error())
		return
	}
	q := r.URL.Query()
	f := domain.DeliveryFilter{
		Direction: strings.TrimSpace(q.Get("direction")),
		Status:    strings.TrimSpace(q.Get("status")),
		Address:   strings.ToLower(strings.TrimSpace(q.Get("address"))),
	}
	if len(f.Address) > domain.MaxDeliveryAddress {
		response.ErrValidation(w, "address demasiado larga")
		return
	}
	f.Page, _ = strconv.Atoi(q.Get("page"))
	f.PerPage, _ = strconv.Atoi(q.Get("per_page"))
	f.Normalize()
	for _, p := range []struct {
		name string
		dst  **time.Time
	}{{"date_from", &f.DateFrom}, {"date_to", &f.DateTo}} {
		raw := strings.TrimSpace(q.Get(p.name))
		if raw == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			response.ErrValidation(w, p.name+" debe ser una fecha RFC 3339")
			return
		}
		*p.dst = &t
	}
	items, total, err := h.delivery.List(r.Context(), tenantID, f)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]deliveryEventResponse, 0, len(items))
	for _, e := range items {
		var delay *string
		if e.DelaySeconds != "" {
			d := e.DelaySeconds
			delay = &d
		}
		out = append(out, deliveryEventResponse{
			ID: e.ID.String(), Direction: e.Direction, QueueID: e.QueueID, MessageID: e.MessageID, Sender: e.Sender,
			Recipient: e.Recipient, Status: e.Status, DSN: e.DSN, Relay: e.Relay, Reason: e.Reason, DelaySeconds: delay,
			SASLUsername: e.SASLUsername, OccurredAt: e.OccurredAt,
		})
	}
	response.JSONWithMeta(w, http.StatusOK, out, response.PageMeta(total, f.Page, f.PerPage))
}

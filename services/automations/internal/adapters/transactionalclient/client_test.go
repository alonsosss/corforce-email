package transactionalclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/alonsosss/corforce-email/services/automations/internal/ports"
	"github.com/google/uuid"
)

func TestSendDOIRespetaElContrato(t *testing.T) {
	tenant, tpl, msg := uuid.New(), uuid.New(), uuid.New()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != messagesPath || r.Header.Get("Idempotency-Key") != "doi:e1" ||
			r.Header.Get("X-Tenant-ID") != tenant.String() || r.Header.Get("X-Gateway-Token") != "tok" {
			t.Errorf("peticion: %s %v", r.URL.Path, r.Header)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"data":{"messages":[{"id":"` + msg.String() + `","status":"queued"}],"suppressed":[]}}`))
	}))
	defer srv.Close()

	res, err := New(srv.URL, "tok").SendDOI(context.Background(), tenant, ports.DOIMessage{
		IdempotencyKey: "doi:e1", FromEmail: "hola@shop.example.com", ToEmail: "ana@example.com", ToName: "Ana",
		TemplateID: tpl, Variables: map[string]any{"confirm_url": "https://x"},
	})
	if err != nil || res.MessageID == nil || *res.MessageID != msg || res.Status != "queued" {
		t.Fatalf("%v %+v", err, res)
	}
	if got["purpose"] != "double_opt_in" || got["template_id"] != tpl.String() {
		t.Fatalf("cuerpo: %v", got)
	}
}

func TestSendMarketingEsUnLoteDeUnDestinatarioConLaCampanaDelFlujo(t *testing.T) {
	workflow, contact := uuid.New(), uuid.New()
	var got batchRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"data":{"accepted":1,"suppressed":[],"message_ids":["` + uuid.NewString() + `"]}}`))
	}))
	defer srv.Close()
	res, err := New(srv.URL, "tok").SendMarketing(context.Background(), uuid.New(), ports.MarketingMessage{
		WorkflowID: workflow, IdempotencyKey: "automation:r:step:0", FromEmail: "news@shop.example.com",
		TemplateID: uuid.New(), TemplateVersion: 3,
		Contact: domain.Contact{ID: contact, Email: "ana@example.com", FirstName: "Ana"},
		Tags:    map[string]string{"source": "automation"},
	})
	if err != nil || res.Accepted != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	if got.Class != "marketing" || got.CampaignID != workflow || len(got.Recipients) != 1 || got.Recipients[0].ContactID != contact ||
		got.IdempotencyKey != "automation:r:step:0" || got.TemplateVersion != 3 {
		t.Fatalf("lote: %+v", got)
	}
}

func TestErroresDeTransactionalSeClasifican(t *testing.T) {
	cases := []struct {
		status int
		header string
		code   string
		check  func(error) bool
	}{
		{429, "7", "RATE_LIMITED", func(err error) bool {
			var e *ports.RateLimitedError
			return errors.As(err, &e) && e.RetryAfter == 7*time.Second
		}},
		{403, "", "SENDING_RESTRICTED", func(err error) bool {
			var e *ports.BlockedError
			return errors.As(err, &e) && e.Code == "SENDING_RESTRICTED"
		}},
		{422, "", "SENDING_DOMAIN_NOT_VERIFIED", func(err error) bool {
			var e *ports.RejectedError
			return errors.As(err, &e) && e.Code == "SENDING_DOMAIN_NOT_VERIFIED"
		}},
		{503, "", "SUPPRESSION_UNAVAILABLE", func(err error) bool { return errors.Is(err, ports.ErrUnavailable) }},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tc.header != "" {
				w.Header().Set("Retry-After", tc.header)
			}
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`{"error":{"code":"` + tc.code + `","message":"x"}}`))
		}))
		_, err := New(srv.URL, "tok").SendDOI(context.Background(), uuid.New(), ports.DOIMessage{IdempotencyKey: "k", TemplateID: uuid.New()})
		srv.Close()
		if !tc.check(err) {
			t.Errorf("%d: %v", tc.status, err)
		}
	}
}

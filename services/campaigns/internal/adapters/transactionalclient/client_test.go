package transactionalclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/ports"
	"github.com/google/uuid"
)

func request(campaignID uuid.UUID) ports.BatchRequest {
	contactID := uuid.New()
	return ports.BatchRequest{
		CampaignID: campaignID, CampaignName: "Otono 2026", IdempotencyKey: domain.BatchIdempotencyKey(campaignID, 1),
		FromEmail: "news@shop.example.com", FromName: "Tienda", TemplateID: uuid.New(), TemplateVersion: 3,
		Recipients: []domain.Recipient{
			{Email: "a@example.com", Name: "Ana", ContactID: &contactID, Variables: map[string]json.RawMessage{"first_name": json.RawMessage(`"Ana"`)}},
			{Email: "b@example.com"},
		},
	}
}

func TestSendBatchContract(t *testing.T) {
	tenant, campaign := uuid.New(), uuid.New()
	msg := uuid.New()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/transactional/batch" ||
			r.Header.Get("X-Gateway-Token") != "tok" || r.Header.Get("X-Tenant-ID") != tenant.String() {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"data":{"accepted":1,"suppressed":[{"email":"b@example.com","reason":"unsubscribe"}],"message_ids":["` + msg.String() + `"]}}`))
	}))
	defer srv.Close()

	res, err := New(srv.URL, "tok").SendBatch(context.Background(), tenant, request(campaign))
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 1 || len(res.Suppressed) != 1 || res.Suppressed[0].Reason != "unsubscribe" || len(res.MessageIDs) != 1 || res.MessageIDs[0] != msg {
		t.Fatalf("resultado: %+v", res)
	}
	if body["class"] != "marketing" || body["campaign_id"] != campaign.String() ||
		body["idempotency_key"] != domain.BatchIdempotencyKey(campaign, 1) || body["template_version"] != float64(3) {
		t.Fatalf("cuerpo: %v", body)
	}
	if from := body["from"].(map[string]any); from["email"] != "news@shop.example.com" || from["name"] != "Tienda" {
		t.Fatalf("from: %v", from)
	}
	if _, ok := body["reply_to"]; ok {
		t.Fatal("reply_to vacio no se envia")
	}
	if _, ok := body["tags"]; ok {
		t.Fatal("sin etiquetas no se envia tags")
	}
	if utm, ok := body["utm"].(map[string]any); !ok || utm["campaign"] != "Otono 2026" || len(utm) != 1 {
		t.Fatalf("utm lleva el nombre de la campana y deja el resto a transactional: %v", body["utm"])
	}
	if _, ok := body["subject"]; ok {
		t.Fatal("sin asunto propio no se envia subject: manda el de la plantilla")
	}
	recipients := body["recipients"].([]any)
	first, second := recipients[0].(map[string]any), recipients[1].(map[string]any)
	if first["contact_id"] == nil || first["variables"].(map[string]any)["first_name"] != "Ana" {
		t.Fatalf("primer destinatario: %v", first)
	}
	if _, ok := second["contact_id"]; ok {
		t.Fatalf("sin contacto no hay contact_id: %v", second)
	}
	if vars, ok := second["variables"].(map[string]any); !ok || len(vars) != 0 {
		t.Fatalf("variables vacias como objeto: %v", second["variables"])
	}
}

func TestSendBatchErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		header string
		body   string
		check  func(error) bool
	}{
		{"429", 429, "30", `{"error":{"code":"RATE_LIMITED","message":"espere"}}`, func(err error) bool {
			var e *ports.RateLimitedError
			return errors.As(err, &e) && e.RetryAfter == 30*time.Second
		}},
		{"403 reputacion", 403, "", `{"error":{"code":"SENDING_RESTRICTED","message":"quejas"}}`, func(err error) bool {
			var e *ports.BlockedError
			return errors.As(err, &e) && e.Code == "SENDING_RESTRICTED"
		}},
		{"403 plan", 403, "", `{"error":{"code":"PLAN_LIMIT_REACHED","message":"cupo"}}`, func(err error) bool {
			var e *ports.BlockedError
			return errors.As(err, &e) && e.Code == "PLAN_LIMIT_REACHED"
		}},
		{"422", 422, "", `{"error":{"code":"VALIDATION_ERROR","message":"la plantilla no es de marketing"}}`, func(err error) bool {
			var e *ports.RejectedError
			return errors.As(err, &e) && e.Message == "la plantilla no es de marketing"
		}},
		{"422 sin baja", 422, "", `{"error":{"code":"TEMPLATE_MISSING_UNSUBSCRIBE","message":"la plantilla no tiene enlace de baja"}}`, func(err error) bool {
			var e *ports.RejectedError
			return errors.As(err, &e) && e.Code == "TEMPLATE_MISSING_UNSUBSCRIBE"
		}},
		{"422 no marketing", 422, "", `{"error":{"code":"TEMPLATE_NOT_MARKETING","message":"la plantilla es transaccional"}}`, func(err error) bool {
			var e *ports.RejectedError
			return errors.As(err, &e) && e.Code == "TEMPLATE_NOT_MARKETING"
		}},
		{"503", 503, "", `{"error":{"code":"UNAVAILABLE","message":"x"}}`, func(err error) bool {
			return errors.Is(err, ports.ErrUnavailable)
		}},
		{"503 templates", 503, "", `{"error":{"code":"TEMPLATES_UNAVAILABLE","message":"templates sin kind"}}`, func(err error) bool {
			return errors.Is(err, ports.ErrUnavailable)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			_, err := New(srv.URL, "tok").SendBatch(context.Background(), uuid.New(), request(uuid.New()))
			if !tc.check(err) {
				t.Fatalf("error mal clasificado: %v", err)
			}
		})
	}
}

func TestSendBatchRepeatReturnsSameBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"accepted":2}}`))
	}))
	defer srv.Close()
	res, err := New(srv.URL, "tok").SendBatch(context.Background(), uuid.New(), request(uuid.New()))
	if err != nil || res.Accepted != 2 || res.Suppressed == nil || res.MessageIDs == nil {
		t.Fatalf("200 de una repeticion: %+v %v", res, err)
	}
}

func TestSendBatchSubjectOverride(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"data":{"accepted":0,"suppressed":[],"message_ids":[]}}`))
	}))
	defer srv.Close()
	req := request(uuid.New())
	req.Subject = "Variante B"
	req.CampaignName = "Otono"
	req.UTMContent = "ab-b"
	if _, err := New(srv.URL, "tok").SendBatch(context.Background(), uuid.New(), req); err != nil {
		t.Fatal(err)
	}
	if body["subject"] != "Variante B" {
		t.Fatalf("el asunto de la variante viaja en el lote: %v", body["subject"])
	}
	if utm := body["utm"].(map[string]any); utm["campaign"] != "Otono" || utm["content"] != "ab-b" {
		t.Fatalf("utm_content identifica la variante: %v", utm)
	}
}

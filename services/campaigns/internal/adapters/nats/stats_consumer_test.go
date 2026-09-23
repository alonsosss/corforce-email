package nats

import (
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/google/uuid"
)

func TestParseDeliveryEvent(t *testing.T) {
	tenant, campaign, contact, msg := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	ts := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	base := func() events.Event {
		return events.Event{
			ID: "evt-1", Type: "transactional.email.opened", TenantID: tenant.String(), Timestamp: ts,
			Data: map[string]interface{}{
				"tenant_id": tenant.String(), "message_id": msg.String(), "class": "marketing",
				"campaign_id": campaign.String(), "contact_id": contact.String(), "email": "a@example.com",
			},
		}
	}

	ev, ok := parseDeliveryEvent(base())
	if !ok || ev.Kind != domain.KindOpened || ev.CampaignID != campaign || ev.TenantID != tenant ||
		ev.MessageID == nil || *ev.MessageID != msg || ev.EventID != "evt-1" || !ev.OccurredAt.Equal(ts) ||
		ev.ContactID == nil || *ev.ContactID != contact {
		t.Fatalf("evento valido: %+v ok=%v", ev, ok)
	}

	fromEnvelope := base()
	delete(fromEnvelope.Data.(map[string]interface{}), "tenant_id")
	if ev, ok := parseDeliveryEvent(fromEnvelope); !ok || ev.TenantID != tenant {
		t.Fatalf("la empresa sale del envelope si falta en data: %+v", ev)
	}

	discard := map[string]func(e *events.Event){
		"sin campana":  func(e *events.Event) { e.Data.(map[string]interface{})["campaign_id"] = nil },
		"sin contacto": func(e *events.Event) { delete(e.Data.(map[string]interface{}), "contact_id") },
		"envio de prueba": func(e *events.Event) {
			e.Data.(map[string]interface{})["contact_id"] = domain.TestContactID(campaign, "a@example.com").String()
		},
		"transaccional":     func(e *events.Event) { e.Data.(map[string]interface{})["class"] = "transactional" },
		"tipo sin contador": func(e *events.Event) { e.Type = "transactional.message.queued" },
		"sin empresa":       func(e *events.Event) { delete(e.Data.(map[string]interface{}), "tenant_id"); e.TenantID = "" },
		"payload roto":      func(e *events.Event) { e.Data = "no-es-un-mapa" },
	}
	for name, mutate := range discard {
		e := base()
		mutate(&e)
		if _, ok := parseDeliveryEvent(e); ok {
			t.Errorf("%s: debe descartarse", name)
		}
	}

	// sent y failed no traen email sino to[]: la prueba se reconoce por el unico destinatario.
	sent := base()
	sent.Type = "transactional.email.sent"
	data := sent.Data.(map[string]interface{})
	delete(data, "email")
	data["to"] = []interface{}{"a@example.com"}
	data["occurred_at"] = "2026-09-01T12:00:05.123456789Z"
	ev, ok = parseDeliveryEvent(sent)
	if !ok || ev.Kind != domain.KindSent || !ev.OccurredAt.Equal(time.Date(2026, 9, 1, 12, 0, 5, 123456789, time.UTC)) {
		t.Fatalf("sent de un contacto real, con su occurred_at: %+v ok=%v", ev, ok)
	}
	data["contact_id"] = domain.TestContactID(campaign, "a@example.com").String()
	if _, ok := parseDeliveryEvent(sent); ok {
		t.Fatal("el sent de un envio de prueba no cuenta")
	}
}

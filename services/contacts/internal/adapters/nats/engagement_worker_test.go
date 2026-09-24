package nats

import (
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

func eventOf(evt events.Event, kind domain.EngagementKind) (domain.EngagementEvent, bool) {
	data, _ := evt.Data.(map[string]interface{})
	return engagementEvent(evt, data, kind)
}

func TestEventoDeInteraccion(t *testing.T) {
	tenant, contact, campaign := uuid.New(), uuid.New(), uuid.New()
	payload := func(overrides map[string]interface{}) events.Event {
		data := map[string]interface{}{
			"tenant_id":   tenant.String(),
			"contact_id":  contact.String(),
			"campaign_id": campaign.String(),
			"class":       "marketing",
			"test":        false,
			"email":       "ana@example.com",
			"occurred_at": "2026-09-23T10:00:00Z",
		}
		for k, v := range overrides {
			data[k] = v
		}
		return events.Event{ID: uuid.NewString(), Timestamp: time.Date(2026, 9, 23, 11, 0, 0, 0, time.UTC), Data: data}
	}

	ev, ok := eventOf(payload(nil), domain.EngagementOpened)
	if !ok || ev.TenantID != tenant || ev.ContactID != contact || ev.CampaignID != campaign || ev.Kind != domain.EngagementOpened ||
		!ev.OccurredAt.Equal(time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("evento: %+v %v", ev, ok)
	}
	if ev, ok := eventOf(payload(map[string]interface{}{"occurred_at": "ayer"}), domain.EngagementClicked); !ok || !ev.OccurredAt.Equal(time.Date(2026, 9, 23, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("sin hora legible vale la del envelope: %+v", ev)
	}
	if ev, ok := eventOf(payload(map[string]interface{}{"tenant_id": nil}), domain.EngagementClicked); ok {
		t.Fatalf("sin empresa en el payload ni en el envelope: %+v", ev)
	}

	for name, o := range map[string]map[string]interface{}{
		"transaccional":  {"class": "transactional"},
		"sin clase":      {"class": nil},
		"prueba":         {"test": true},
		"sin contacto":   {"contact_id": nil},
		"sin campana":    {"campaign_id": nil},
		"campana rota":   {"campaign_id": "no-es-un-id"},
		"contacto vacio": {"contact_id": uuid.Nil.String()},
	} {
		if _, ok := eventOf(payload(o), domain.EngagementDelivered); ok {
			t.Errorf("%s: se esperaba descartar", name)
		}
	}
}

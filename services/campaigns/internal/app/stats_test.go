package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/google/uuid"
)

func deliveryEvent(h *harness, id string, campaignID uuid.UUID, kind domain.DeliveryKind, message *uuid.UUID) domain.DeliveryEvent {
	return domain.DeliveryEvent{EventID: id, TenantID: h.tenantID, CampaignID: campaignID, Kind: kind, MessageID: message}
}

func TestRecordDeliveryEventIsIdempotent(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	c := h.sending("Stats")
	msg := uuid.New()

	counted, err := h.uc.RecordDeliveryEvent(ctx, deliveryEvent(h, "evt-1", c.ID, domain.KindDelivered, &msg))
	if err != nil || !counted {
		t.Fatalf("primera entrega: counted=%v err=%v", counted, err)
	}
	counted, err = h.uc.RecordDeliveryEvent(ctx, deliveryEvent(h, "evt-1", c.ID, domain.KindDelivered, &msg))
	if err != nil || counted {
		t.Fatalf("la reentrega no suma: counted=%v err=%v", counted, err)
	}
	if got := h.campaign(c.ID).Counters.Delivered; got != 1 {
		t.Fatalf("delivered=%d", got)
	}
	if _, err := h.uc.RecordDeliveryEvent(ctx, deliveryEvent(h, "evt-2", c.ID, domain.KindBounced, nil)); err != nil {
		t.Fatal(err)
	}
	if got := h.campaign(c.ID).Counters.Bounced; got != 1 {
		t.Fatalf("bounced=%d", got)
	}
}

func TestOpensAndClicksCountUniqueMessages(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	c := h.sending("Aperturas")
	m1, m2 := uuid.New(), uuid.New()

	for _, ev := range []domain.DeliveryEvent{
		deliveryEvent(h, "o1", c.ID, domain.KindOpened, &m1),
		deliveryEvent(h, "o2", c.ID, domain.KindOpened, &m1),
		deliveryEvent(h, "o3", c.ID, domain.KindOpened, &m2),
		deliveryEvent(h, "o1", c.ID, domain.KindOpened, &m1),
		deliveryEvent(h, "c1", c.ID, domain.KindClicked, &m1),
		deliveryEvent(h, "c2", c.ID, domain.KindClicked, &m1),
	} {
		if _, err := h.uc.RecordDeliveryEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	got := h.campaign(c.ID).Counters
	if got.Opened != 2 || got.Clicked != 1 {
		t.Fatalf("aperturas y clics unicos: opened=%d clicked=%d", got.Opened, got.Clicked)
	}
}

func TestDeliveryEventForUnknownCampaignIsNotRecorded(t *testing.T) {
	h := newHarness()
	msg := uuid.New()
	for _, kind := range []domain.DeliveryKind{domain.KindDelivered, domain.KindOpened} {
		_, err := h.uc.RecordDeliveryEvent(context.Background(), deliveryEvent(h, "x-"+string(kind), uuid.New(), kind, &msg))
		if !errors.Is(err, domain.ErrCampaignNotFound) {
			t.Fatalf("%s: %v", kind, err)
		}
		if _, ok := h.store.processed["x-"+string(kind)]; ok {
			t.Fatalf("%s: el evento no debe quedar como procesado", kind)
		}
	}
}

func TestDeliveryEventValidation(t *testing.T) {
	h := newHarness()
	c := h.sending("Validacion")
	if _, err := h.uc.RecordDeliveryEvent(context.Background(), deliveryEvent(h, "o", c.ID, domain.KindOpened, nil)); !errors.Is(err, domain.ErrInvalidCampaign) {
		t.Fatalf("apertura sin mensaje: %v", err)
	}
	if _, err := h.uc.RecordDeliveryEvent(context.Background(), deliveryEvent(h, "", c.ID, domain.KindSent, nil)); !errors.Is(err, domain.ErrInvalidCampaign) {
		t.Fatalf("evento sin id: %v", err)
	}
}

func TestPruneProcessedEvents(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	c := h.sending("Poda")
	if _, err := h.uc.RecordDeliveryEvent(ctx, deliveryEvent(h, "viejo", c.ID, domain.KindSent, nil)); err != nil {
		t.Fatal(err)
	}
	h.clock.advance(domain.ProcessedEventRetention + 1)
	if _, err := h.uc.RecordDeliveryEvent(ctx, deliveryEvent(h, "nuevo", c.ID, domain.KindSent, nil)); err != nil {
		t.Fatal(err)
	}
	n, err := h.uc.PruneProcessedEvents(ctx, h.tenantID)
	if err != nil || n != 1 {
		t.Fatalf("podados=%d err=%v", n, err)
	}
	if _, ok := h.store.processed["nuevo"]; !ok {
		t.Fatal("el evento reciente se conserva")
	}
}

package sns

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/google/uuid"
)

func sesJSON(eventType, tenant, message, extra string) []byte {
	return []byte(`{"eventType":"` + eventType + `","mail":{"timestamp":"2026-09-12T10:00:00.000Z",` +
		`"messageId":"0100018f-ses","source":"no-reply@example.com","destination":["ana@example.com","eva@example.com"],` +
		`"tags":{"tenant_id":["` + tenant + `"],"message_id":["` + message + `"],"ses:configuration-set":["cfm-transactional"]}}` +
		extra + `}`)
}

func TestParsePermanentBounce(t *testing.T) {
	tenant, message := uuid.New(), uuid.New()
	raw := sesJSON("Bounce", tenant.String(), message.String(),
		`,"bounce":{"bounceType":"Permanent","bounceSubType":"General","timestamp":"2026-09-12T10:00:05.000Z",`+
			`"bouncedRecipients":[{"emailAddress":"ana@example.com","status":"5.1.1","diagnosticCode":"smtp; 550 5.1.1 user unknown"}]}`)
	ev, err := ParseSESEvent(raw, "sns-1")
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != domain.EventBounce || ev.BounceType != domain.BounceTypePermanent {
		t.Fatalf("tipo %q / %q", ev.Type, ev.BounceType)
	}
	if ev.TenantID != tenant || ev.MessageID != message || ev.SNSMessageID != "sns-1" {
		t.Fatalf("atribucion incorrecta: %+v", ev)
	}
	if len(ev.Recipients) != 1 || ev.Recipients[0] != "ana@example.com" {
		t.Fatalf("solo el destinatario rebotado: %v", ev.Recipients)
	}
	if !ev.OccurredAt.Equal(time.Date(2026, 9, 12, 10, 0, 5, 0, time.UTC)) {
		t.Fatalf("occurred_at = %v", ev.OccurredAt)
	}
	if !strings.Contains(ev.Detail["diagnostic_code"].(string), "5.1.1") {
		t.Fatalf("falta el diagnostico: %v", ev.Detail)
	}
}

func TestParseTransientBounceAndComplaint(t *testing.T) {
	tenant, message := uuid.New().String(), uuid.New().String()
	ev, err := ParseSESEvent(sesJSON("Bounce", tenant, message,
		`,"bounce":{"bounceType":"Transient","bounceSubType":"MailboxFull","bouncedRecipients":[{"emailAddress":"eva@example.com"}]}`), "s")
	if err != nil || ev.BounceType != domain.BounceTypeTransient {
		t.Fatalf("rebote transitorio: %+v, %v", ev, err)
	}
	ev, err = ParseSESEvent(sesJSON("Complaint", tenant, message,
		`,"complaint":{"complaintFeedbackType":"abuse","complainedRecipients":[{"emailAddress":"eva@example.com"}]}`), "s")
	if err != nil || ev.Type != domain.EventComplaint || ev.Recipients[0] != "eva@example.com" {
		t.Fatalf("queja: %+v, %v", ev, err)
	}
	ev, err = ParseSESEvent(sesJSON("DeliveryDelay", tenant, message, ""), "s")
	if err != nil || ev.Type != domain.EventDeliveryDelay || len(ev.Recipients) != 2 {
		t.Fatalf("retraso sin detalle cae a los destinatarios del correo: %+v, %v", ev, err)
	}
}

func TestParseRejectsForeignOrUnknown(t *testing.T) {
	if _, err := ParseSESEvent([]byte(`{"eventType":"Delivery","mail":{"tags":{}}}`), "s"); !errors.Is(err, ErrNotOurs) {
		t.Errorf("sin etiquetas: err = %v", err)
	}
	if _, err := ParseSESEvent(sesJSON("Delivery", "no-uuid", uuid.New().String(), ""), "s"); !errors.Is(err, ErrNotOurs) {
		t.Errorf("tenant_id invalido: err = %v", err)
	}
	if _, err := ParseSESEvent(sesJSON("Otro", uuid.New().String(), uuid.New().String(), ""), "s"); err == nil {
		t.Error("eventType desconocido debe rechazarse")
	}
	if _, err := ParseSESEvent([]byte(`no es json`), "s"); err == nil {
		t.Error("JSON ilegible debe rechazarse")
	}
}

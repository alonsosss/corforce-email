package outbox

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// captureExecer guarda el payload que outbox.Enqueue escribe en platform.event_outbox
// (cuarto argumento del INSERT).
type captureExecer struct {
	payloads [][]byte
}

func (c *captureExecer) Exec(_ context.Context, _ string, args ...interface{}) (pgconn.CommandTag, error) {
	if len(args) == 4 {
		if p, ok := args[3].([]byte); ok {
			c.payloads = append(c.payloads, p)
		}
	}
	return pgconn.CommandTag{}, nil
}

// Contrato de contacts.contact.resubscribed del lado del emisor: exactamente tenant_id,
// email y consented_at, este en RFC 3339 con fraccion y en UTC, que es lo que lee
// suppression (services/suppression/internal/adapters/nats, consentedAt).
func TestContactResubscribedLlevaLaHoraDelConsentimiento(t *testing.T) {
	q := &captureExecer{}
	pub := NewPublisher(q)
	tenant := uuid.New()
	c := &domain.Contact{ID: uuid.New(), TenantID: tenant, Email: "ana@example.com"}
	consentedAt := time.Date(2026, 9, 13, 5, 0, 0, 123456000, time.FixedZone("PET", -5*3600))

	if err := pub.ContactResubscribed(context.Background(), c, consentedAt); err != nil {
		t.Fatal(err)
	}
	if len(q.payloads) != 1 {
		t.Fatalf("un evento encolado: %d", len(q.payloads))
	}
	var evt struct {
		Type string                 `json:"type"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(q.payloads[0], &evt); err != nil {
		t.Fatal(err)
	}
	want := map[string]interface{}{
		"tenant_id":    tenant.String(),
		"email":        "ana@example.com",
		"consented_at": "2026-09-13T10:00:00.123456Z",
	}
	if evt.Type != SubjectContactResubscribed || !reflect.DeepEqual(evt.Data, want) {
		t.Fatalf("payload: %s %#v", evt.Type, evt.Data)
	}
	got, err := time.Parse(time.RFC3339Nano, evt.Data["consented_at"].(string))
	if err != nil || !got.Equal(consentedAt) {
		t.Fatalf("la hora vuelve al mismo instante: %v err=%v", got, err)
	}
}

// Sin la hora, suppression levantaria cualquier baja: el publicador se niega y la
// transaccion del consentimiento se revierte.
func TestContactResubscribedSinHoraEsUnError(t *testing.T) {
	q := &captureExecer{}
	c := &domain.Contact{ID: uuid.New(), TenantID: uuid.New(), Email: "ana@example.com"}
	if err := NewPublisher(q).ContactResubscribed(context.Background(), c, time.Time{}); err == nil {
		t.Fatal("se esperaba error")
	}
	if len(q.payloads) != 0 {
		t.Fatal("no se encola nada")
	}
}

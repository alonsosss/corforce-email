package nats

import (
	"encoding/json"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/billing/internal/domain"
)

func TestTranslateEmailSent(t *testing.T) {
	three := []interface{}{"ana@acme.test", "eva@acme.test", "luis@acme.test"}
	cases := []struct {
		name      string
		class     interface{}
		to        interface{}
		known     bool
		resource  domain.Resource
		units     int64
		defaulted bool
	}{
		{"marketing cuenta en su contador", "marketing", []interface{}{"ana@acme.test"}, true, domain.ResourceMarketingMessages, 1, false},
		{"transaccional por destinatarios", "transactional", three, true, domain.ResourceTransactionalMessages, 3, false},
		{"sin clase es transaccional", nil, three, true, domain.ResourceTransactionalMessages, 3, false},
		{"clase vacia es transaccional", "", three, true, domain.ResourceTransactionalMessages, 3, false},
		{"sin to cuenta uno", "marketing", nil, true, domain.ResourceMarketingMessages, 1, true},
		{"to vacio cuenta uno", "transactional", []interface{}{}, true, domain.ResourceTransactionalMessages, 1, true},
		{"to que no es lista cuenta uno", "transactional", "ana@acme.test", true, domain.ResourceTransactionalMessages, 1, true},
		{"clase desconocida", "promo", three, false, "", 0, false},
		{"clase en mayusculas", "MARKETING", three, false, "", 0, false},
		{"clase que no es texto", float64(1), three, false, "", 0, false},
	}
	for _, tc := range cases {
		got := translateEmailSent(tc.class, tc.to)
		if got.known != tc.known || got.resource != tc.resource || got.units != tc.units || got.defaulted != tc.defaulted {
			t.Errorf("%s: %+v", tc.name, got)
		}
	}
}

// El evento llega como JSON: los destinatarios se decodifican como []interface{} y la
// traduccion debe leerlos asi, con el payload que publica transactional.
func TestTranslateEmailSentFromWirePayload(t *testing.T) {
	raw := `{"id":"e-1","data":{"tenant_id":"4b6c7a8e-1f2d-4c3b-9a8e-7d6c5b4a3f21","message_id":"m-1",
		"class":"marketing","campaign_id":"c-1","contact_id":"k-1","occurred_at":"2026-09-12T12:00:00Z",
		"ses_message_id":"ses-1","to":["ana@acme.test","eva@acme.test"]}}`
	var evt events.Event
	if err := json.Unmarshal([]byte(raw), &evt); err != nil {
		t.Fatal(err)
	}
	data, ok := evt.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("data: %T", evt.Data)
	}
	got := translateEmailSent(data["class"], data["to"])
	if !got.known || got.resource != domain.ResourceMarketingMessages || got.units != 2 || got.defaulted {
		t.Fatalf("traduccion del payload real: %+v", got)
	}
}

func TestTestSendsAreNotBilled(t *testing.T) {
	cases := map[string]struct {
		test interface{}
		want bool
	}{
		"prueba":             {true, true},
		"envio real":         {false, false},
		"sin campo":          {nil, false},
		"texto no es prueba": {"true", false},
	}
	for name, tc := range cases {
		if got := isTestSend(tc.test); got != tc.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

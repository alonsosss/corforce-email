package nats

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
)

// Contrato de contacts.contact.resubscribed del lado del consumidor: consented_at llega
// como lo escribe contacts (RFC 3339 con fraccion, UTC) despues de pasar por JSON, y se lee
// sin perder el microsegundo con que la base fecha las filas.
func TestConsentedAtLeeElFormatoDeContacts(t *testing.T) {
	want := time.Date(2026, 9, 13, 10, 0, 0, 123456000, time.UTC)
	raw, err := json.Marshal(events.Event{Data: map[string]interface{}{
		"tenant_id":    "00000000-0000-0000-0000-000000000001",
		"email":        "ana@example.com",
		"consented_at": want.UTC().Format(time.RFC3339Nano),
	}})
	if err != nil {
		t.Fatal(err)
	}
	var evt events.Event
	if err := json.Unmarshal(raw, &evt); err != nil {
		t.Fatal(err)
	}
	data, _ := evt.Data.(map[string]interface{})
	got, err := consentedAt(data["consented_at"])
	if err != nil || got == nil || !got.Equal(want) {
		t.Fatalf("consented_at: %v err=%v", got, err)
	}
}

func TestConsentedAtSinCampoOInvalido(t *testing.T) {
	data := map[string]interface{}{"email": "ana@example.com", "nulo": nil}
	for _, key := range []string{"consented_at", "nulo"} {
		if got, err := consentedAt(data[key]); got != nil || err != nil {
			t.Fatalf("%s: un productor sin la hora da nil: %v err=%v", key, got, err)
		}
	}
	for _, bad := range []interface{}{"", "ayer", "2026-09-13", 1757757600, true} {
		if got, err := consentedAt(bad); got != nil || !errors.Is(err, errInvalidConsentedAt) {
			t.Fatalf("%v: se esperaba errInvalidConsentedAt, hubo %v err=%v", bad, got, err)
		}
	}
}

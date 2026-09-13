package outbox

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/suppression/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// captureExecer guarda el subject y el payload que outbox.Enqueue escribe en
// platform.event_outbox (segundo y cuarto argumento del INSERT).
type captureExecer struct {
	subjects []string
	payloads [][]byte
}

func (c *captureExecer) Exec(_ context.Context, _ string, args ...interface{}) (pgconn.CommandTag, error) {
	if len(args) == 4 {
		s, _ := args[1].(string)
		p, _ := args[3].([]byte)
		c.subjects = append(c.subjects, s)
		c.payloads = append(c.payloads, p)
	}
	return pgconn.CommandTag{}, nil
}

type envelope struct {
	Type     string                 `json:"type"`
	Source   string                 `json:"source"`
	TenantID string                 `json:"tenant_id"`
	UserID   string                 `json:"user_id"`
	Data     map[string]interface{} `json:"data"`
}

func decode(t *testing.T, raw []byte) envelope {
	t.Helper()
	var evt envelope
	if err := json.Unmarshal(raw, &evt); err != nil {
		t.Fatal(err)
	}
	return evt
}

func keys(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Contrato de suppression.entry.expired del lado del emisor: el payload de added y removed
// mas expires_at, la caducidad anunciada en RFC 3339 con fraccion y en UTC. contacts lee
// tenant_id, email, reason, source y reasons (services/contacts/internal/adapters/nats).
func TestEntryExpiredLlevaLaCaducidad(t *testing.T) {
	q := &captureExecer{}
	pub := NewPublisher(q)
	tenant := uuid.New()
	expiresAt := time.Date(2026, 9, 13, 5, 0, 0, 250000000, time.FixedZone("PET", -5*3600))
	e := &domain.Entry{ID: uuid.New(), TenantID: tenant, Email: "ana@example.com", Reason: domain.ReasonManual, Source: "api", ExpiresAt: &expiresAt}

	if err := pub.EntryExpired(context.Background(), e, nil); err != nil {
		t.Fatal(err)
	}
	if err := pub.EntryRemoved(context.Background(), e, []domain.Reason{domain.ReasonUnsubscribe}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(q.subjects, []string{SubjectEntryExpired, SubjectEntryRemoved}) {
		t.Fatalf("subjects: %v", q.subjects)
	}
	expired := decode(t, q.payloads[0])
	want := map[string]interface{}{
		"tenant_id":  tenant.String(),
		"email":      "ana@example.com",
		"reason":     "manual",
		"source":     "api",
		"reasons":    []interface{}{},
		"expires_at": "2026-09-13T10:00:00.25Z",
	}
	if expired.Type != SubjectEntryExpired || expired.Source != source || expired.TenantID != tenant.String() || expired.UserID != "" {
		t.Fatalf("envelope: %+v", expired)
	}
	if !reflect.DeepEqual(expired.Data, want) {
		t.Fatalf("payload: %#v", expired.Data)
	}
	got, err := time.Parse(time.RFC3339Nano, expired.Data["expires_at"].(string))
	if err != nil || !got.Equal(expiresAt) {
		t.Fatalf("la caducidad vuelve al mismo instante: %v err=%v", got, err)
	}

	// Mismas claves que removed (y added, que comparte payload) mas expires_at: el
	// consumidor lee las tres con el mismo manejador.
	removed := decode(t, q.payloads[1])
	if got, want := append(keys(removed.Data), "expires_at"), keys(expired.Data); !reflect.DeepEqual(sortedCopy(got), want) {
		t.Fatalf("claves de removed + expires_at = %v, expired = %v", got, want)
	}
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

// Sin la caducidad el anuncio no se distingue del de una renovacion posterior: el
// publicador se niega y la transaccion que lo anunciaba se revierte.
func TestEntryExpiredSinCaducidadEsUnError(t *testing.T) {
	q := &captureExecer{}
	e := &domain.Entry{ID: uuid.New(), TenantID: uuid.New(), Email: "ana@example.com", Reason: domain.ReasonManual}
	if err := NewPublisher(q).EntryExpired(context.Background(), e, nil); err == nil {
		t.Fatal("se esperaba error")
	}
	if len(q.payloads) != 0 {
		t.Fatal("no se encola nada")
	}
}

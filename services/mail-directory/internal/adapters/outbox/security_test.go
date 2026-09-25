package outbox

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

// sobre devuelve el unico evento encolado entero, con su empresa y su usuario.
func sobre(t *testing.T, exec *execCapturado, subject string) events.Event {
	t.Helper()
	datos(t, exec, subject)
	var evt events.Event
	if err := json.Unmarshal(exec.payloads[0], &evt); err != nil {
		t.Fatal(err)
	}
	return evt
}

func TestEventosDeVerificacionEnDosPasos(t *testing.T) {
	m := &domain.Mailbox{ID: uuid.New(), TenantID: uuid.New(), Username: "ana@acme.test"}
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	ctx := context.Background()

	exec := &execCapturado{}
	if err := NewPublisher(exec).MailboxMFAEnabled(ctx, m, at); err != nil {
		t.Fatal(err)
	}
	evt := sobre(t, exec, SubjectMailboxMFAEnabled)
	want := map[string]interface{}{"tenant_id": m.TenantID.String(), "id": m.ID.String(), "username": m.Username, "at": "2030-01-02T03:04:05Z"}
	if !reflect.DeepEqual(evt.Data, want) || evt.TenantID != m.TenantID.String() {
		t.Fatalf("activada: %+v", evt)
	}

	exec = &execCapturado{}
	if err := NewPublisher(exec).MailboxMFADisabled(ctx, m, at, domain.MFADisabledByUser, nil); err != nil {
		t.Fatal(err)
	}
	evt = sobre(t, exec, SubjectMailboxMFADisabled)
	data := evt.Data.(map[string]interface{})
	if data["by"] != "user" || data["actor_id"] != nil || evt.UserID != "" {
		t.Fatalf("apagada por el usuario: %+v", evt)
	}

	actor := uuid.New()
	exec = &execCapturado{}
	if err := NewPublisher(exec).MailboxMFADisabled(ctx, m, at, domain.MFADisabledByAdmin, &actor); err != nil {
		t.Fatal(err)
	}
	evt = sobre(t, exec, SubjectMailboxMFADisabled)
	data = evt.Data.(map[string]interface{})
	if data["by"] != "admin" || data["actor_id"] != actor.String() || evt.UserID != actor.String() {
		t.Fatalf("restablecida por el administrador: %+v", evt)
	}

	exec = &execCapturado{}
	if err := NewPublisher(exec).MailboxMFADisabled(ctx, m, at, "otro", nil); err == nil || len(exec.subjects) != 0 {
		t.Fatalf("un autor desconocido no se encola: %v %v", err, exec.subjects)
	}
}

func TestEventoDeReenvioExternoConListasNuncaNulas(t *testing.T) {
	m := &domain.Mailbox{ID: uuid.New(), TenantID: uuid.New(), Username: "ana@acme.test"}
	exec := &execCapturado{}
	err := NewPublisher(exec).MailboxForwardingChanged(context.Background(), m, time.Now(), domain.ForwardingChange{
		ExternalAdded: []string{"fuera@gmail.test"}, ForwardingEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	data := datos(t, exec, SubjectMailboxForwardingChanged)
	if !reflect.DeepEqual(data["external_added"], []interface{}{"fuera@gmail.test"}) ||
		!reflect.DeepEqual(data["external_removed"], []interface{}{}) || data["forwarding_enabled"] != true ||
		data["username"] != m.Username || data["id"] != m.ID.String() {
		t.Fatalf("payload %v", data)
	}
	if _, err := time.Parse(time.RFC3339, data["at"].(string)); err != nil {
		t.Fatalf("at: %v", err)
	}
}

func TestEventoDePoliticaDeCorreo(t *testing.T) {
	by := uuid.New()
	p := &domain.MailPolicy{TenantID: uuid.New(), ExternalForwardingAllowed: false, UpdatedBy: &by}
	exec := &execCapturado{}
	if err := NewPublisher(exec).MailPolicyUpdated(context.Background(), p, 3); err != nil {
		t.Fatal(err)
	}
	evt := sobre(t, exec, SubjectMailPolicyUpdated)
	want := map[string]interface{}{
		"tenant_id": p.TenantID.String(), "external_forwarding_allowed": false, "updated_by": by.String(), "removed_mailboxes": float64(3),
	}
	if !reflect.DeepEqual(evt.Data, want) || evt.UserID != by.String() || evt.TenantID != p.TenantID.String() {
		t.Fatalf("evento %+v", evt)
	}
}

package outbox

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// execCapturado hace de transaccion: guarda lo que outbox.Enqueue inserta.
type execCapturado struct {
	subjects []string
	payloads [][]byte
}

func (e *execCapturado) Exec(_ context.Context, _ string, args ...interface{}) (pgconn.CommandTag, error) {
	subject, _ := args[1].(string)
	payload, _ := args[3].([]byte)
	e.subjects = append(e.subjects, subject)
	e.payloads = append(e.payloads, payload)
	return pgconn.CommandTag{}, nil
}

// El aviso de credencial lleva lo que leen sus consumidores: username (mail-security echa al buzon
// de Dovecot), changed_at y credential (el webmail solo revoca con la principal).
func TestAvisoDeCredencialConSuCredencial(t *testing.T) {
	m := &domain.Mailbox{ID: uuid.New(), TenantID: uuid.New(), Username: "ana@acme.test"}
	for _, c := range []domain.Credential{domain.CredentialPassword, domain.CredentialAppPassword} {
		exec := &execCapturado{}
		if err := NewPublisher(exec).MailboxCredentialsChanged(context.Background(), m, c); err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		if len(exec.subjects) != 1 || exec.subjects[0] != SubjectMailboxCredentialsChanged {
			t.Fatalf("%s: encolado %v", c, exec.subjects)
		}
		var evt events.Event
		if err := json.Unmarshal(exec.payloads[0], &evt); err != nil {
			t.Fatalf("%s: payload: %v", c, err)
		}
		data, _ := evt.Data.(map[string]interface{})
		want := map[string]interface{}{"tenant_id": m.TenantID.String(), "id": m.ID.String(), "username": m.Username, "credential": string(c)}
		for k, v := range want {
			if data[k] != v {
				t.Errorf("%s: %s = %v; want %v", c, k, data[k], v)
			}
		}
		if raw, _ := data["changed_at"].(string); raw == "" {
			t.Errorf("%s: sin changed_at", c)
		} else if _, err := time.Parse(time.RFC3339, raw); err != nil {
			t.Errorf("%s: changed_at %q: %v", c, raw, err)
		}
		if len(data) != len(want)+1 || evt.Type != SubjectMailboxCredentialsChanged || evt.Source != source || evt.TenantID != m.TenantID.String() {
			t.Errorf("%s: sobre %+v", c, evt)
		}
	}
}

func TestAvisoDeCredencialDesconocidaNoSeEncola(t *testing.T) {
	exec := &execCapturado{}
	m := &domain.Mailbox{ID: uuid.New(), TenantID: uuid.New(), Username: "ana@acme.test"}
	if err := NewPublisher(exec).MailboxCredentialsChanged(context.Background(), m, "dav"); err == nil {
		t.Fatal("una credencial desconocida debe fallar")
	}
	if len(exec.subjects) != 0 {
		t.Fatalf("encolado %v", exec.subjects)
	}
}

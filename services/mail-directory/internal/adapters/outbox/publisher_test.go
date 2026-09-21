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

// datos devuelve el payload del unico evento encolado.
func datos(t *testing.T, exec *execCapturado, subject string) map[string]interface{} {
	t.Helper()
	if len(exec.subjects) != 1 || exec.subjects[0] != subject {
		t.Fatalf("encolado %v; want %s", exec.subjects, subject)
	}
	var evt events.Event
	if err := json.Unmarshal(exec.payloads[0], &evt); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if evt.Type != subject || evt.Source != source {
		t.Fatalf("sobre %+v", evt)
	}
	data, _ := evt.Data.(map[string]interface{})
	return data
}

// El aviso de credencial lleva lo que leen sus consumidores: username (mail-security echa al buzon
// de Dovecot), changed_at, credential (el webmail solo revoca con la principal) y changed (con el
// que el webmail sabe si el cambio invalida su sesion).
func TestAvisoDeCredencialConSuCredencial(t *testing.T) {
	m := &domain.Mailbox{ID: uuid.New(), TenantID: uuid.New(), Username: "ana@acme.test"}
	for _, c := range []domain.Credential{domain.CredentialPassword, domain.CredentialAppPassword} {
		exec := &execCapturado{}
		if err := NewPublisher(exec).MailboxCredentialsChanged(context.Background(), m, c, []domain.MailboxAttr{domain.AttrIMAPAccess}); err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		data := datos(t, exec, SubjectMailboxCredentialsChanged)
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
		if got := data["changed"]; !reflect.DeepEqual(got, []interface{}{"imap_access"}) {
			t.Errorf("%s: changed = %v", c, got)
		}
		// changed_at y changed, ademas de los cuatro comparables.
		if len(data) != len(want)+2 {
			t.Errorf("%s: payload %v", c, data)
		}
	}
}

// El cambio del buzon dice que atributos cambio, y una lista vacia viaja como [] y no como null:
// el consumidor distingue "no cambio nada" de "el evento no lo dice", y solo con lo segundo revoca.
func TestElCambioDelBuzonDiceQueCambio(t *testing.T) {
	m := &domain.Mailbox{ID: uuid.New(), TenantID: uuid.New(), Username: "ana@acme.test", Domain: "acme.test"}
	cases := map[string]struct {
		changed []domain.MailboxAttr
		want    []interface{}
	}{
		"cuota y nombre": {[]domain.MailboxAttr{domain.AttrDisplayName, domain.AttrQuotaBytes}, []interface{}{"display_name", "quota_bytes"}},
		"apagado":        {[]domain.MailboxAttr{domain.AttrActive}, []interface{}{"active"}},
		"nada":           {nil, []interface{}{}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			exec := &execCapturado{}
			if err := NewPublisher(exec).MailboxUpdated(context.Background(), m, c.changed); err != nil {
				t.Fatalf("%v", err)
			}
			data := datos(t, exec, SubjectMailboxUpdated)
			if got := data["changed"]; !reflect.DeepEqual(got, c.want) {
				t.Fatalf("changed = %#v; want %#v", got, c.want)
			}
			if data["username"] != m.Username {
				t.Fatalf("payload %v", data)
			}
		})
	}
}

func TestAvisoDeCredencialDesconocidaNoSeEncola(t *testing.T) {
	exec := &execCapturado{}
	m := &domain.Mailbox{ID: uuid.New(), TenantID: uuid.New(), Username: "ana@acme.test"}
	if err := NewPublisher(exec).MailboxCredentialsChanged(context.Background(), m, "dav", nil); err == nil {
		t.Fatal("una credencial desconocida debe fallar")
	}
	if len(exec.subjects) != 0 {
		t.Fatalf("encolado %v", exec.subjects)
	}
}

// Un atributo que el contrato no reconoce no se encola: el consumidor que no sepa leerlo revocaria
// de mas para siempre, y el fallo revierte la escritura que lo origino (la outbox va en su
// transaccion), que es donde se ve.
func TestAtributoDesconocidoNoSeEncola(t *testing.T) {
	m := &domain.Mailbox{ID: uuid.New(), TenantID: uuid.New(), Username: "ana@acme.test"}
	for _, changed := range [][]domain.MailboxAttr{{"caldav_access"}, {domain.AttrActive, "inventado"}} {
		exec := &execCapturado{}
		if err := NewPublisher(exec).MailboxUpdated(context.Background(), m, changed); err == nil {
			t.Fatalf("%v deberia fallar", changed)
		}
		if err := NewPublisher(exec).MailboxCredentialsChanged(context.Background(), m, domain.CredentialPassword, changed); err == nil {
			t.Fatalf("%v deberia fallar tambien en el aviso de credencial", changed)
		}
		if len(exec.subjects) != 0 {
			t.Fatalf("encolado %v", exec.subjects)
		}
	}
}

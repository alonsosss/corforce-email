package outbox

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
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

func decode(t *testing.T, raw []byte) map[string]interface{} {
	t.Helper()
	var evt map[string]interface{}
	if err := json.Unmarshal(raw, &evt); err != nil {
		t.Fatal(err)
	}
	return evt
}

// Contrato de identity.user.deleted del lado del emisor. access-control lee tenant_id y
// user_id del payload (services/access-control/internal/adapters/nats); audit guarda el sobre
// con quien borro en user_id.
func TestUserDeletedContrato(t *testing.T) {
	q := &captureExecer{}
	tenant, user, actor := uuid.New(), uuid.New(), uuid.New()
	deletedAt := time.Date(2026, 3, 1, 5, 0, 0, 250000000, time.FixedZone("PET", -5*3600))

	err := NewPublisher(q).UserDeleted(context.Background(), domain.UserDeletion{
		UserID: user, TenantID: tenant, ActorID: actor, DeletedAt: deletedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(q.subjects, []string{SubjectUserDeleted}) || SubjectUserDeleted != "identity.user.deleted" {
		t.Fatalf("subjects: %v", q.subjects)
	}
	evt := decode(t, q.payloads[0])
	if evt["type"] != "user.deleted" || evt["source"] != "identity-service" ||
		evt["tenant_id"] != tenant.String() || evt["user_id"] != actor.String() {
		t.Fatalf("sobre: %+v", evt)
	}
	if id, _ := evt["id"].(string); id == "" {
		t.Fatal("el evento sale sin id: es la clave de deduplicacion del rele y del consumidor")
	}
	want := map[string]interface{}{
		"tenant_id":  tenant.String(),
		"user_id":    user.String(),
		"deleted_at": "2026-03-01T10:00:00.25Z",
	}
	if !reflect.DeepEqual(evt["data"], want) {
		t.Fatalf("payload: %#v", evt["data"])
	}
}

// Una baja sin persona detras deja el user_id del sobre vacio; el del payload sigue siendo el
// de la cuenta borrada.
func TestUserDeletedSinActor(t *testing.T) {
	q := &captureExecer{}
	user := uuid.New()
	err := NewPublisher(q).UserDeleted(context.Background(), domain.UserDeletion{
		UserID: user, TenantID: uuid.New(), DeletedAt: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	evt := decode(t, q.payloads[0])
	if _, ok := evt["user_id"]; ok {
		t.Fatalf("user_id del sobre sin actor: %v", evt["user_id"])
	}
	if data, _ := evt["data"].(map[string]interface{}); data["user_id"] != user.String() {
		t.Fatalf("payload: %#v", evt["data"])
	}
}

func TestUserDeletedIncompletaEsUnError(t *testing.T) {
	for _, d := range []domain.UserDeletion{
		{TenantID: uuid.New()},
		{UserID: uuid.New()},
	} {
		q := &captureExecer{}
		if err := NewPublisher(q).UserDeleted(context.Background(), d); err == nil {
			t.Fatalf("%+v: se esperaba error", d)
		}
		if len(q.payloads) != 0 {
			t.Fatalf("%+v: no se encola nada", d)
		}
	}
}

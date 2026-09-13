//go:build integration

package outbox

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CONTACTS_TEST_DSN apunta a una base con platform/00_outbox.sql aplicada. Comprueba que
// los eventos quedan en la outbox de la transaccion, con el payload exacto que leen los
// consumidores, y que se pierden si la transaccion se revierte.
func TestPublicadorEncolaElContrato(t *testing.T) {
	dsn := os.Getenv("CONTACTS_TEST_DSN")
	if dsn == "" {
		t.Skip("CONTACTS_TEST_DSN no definido")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	tenant, user := uuid.New(), uuid.New()
	ctx = middleware.WithIdentity(db.WithPool(ctx, pool), user.String(), tenant.String())
	cp := &db.ContextPool{}
	pub := NewPublisher(cp)
	c := &domain.Contact{ID: uuid.New(), TenantID: tenant, Email: "ana@example.com", Status: domain.StatusActive,
		Attributes: map[string]any{"plan": "pro"}}

	err = cp.Transact(ctx, func(ctx context.Context) error {
		if err := pub.ContactResubscribed(ctx, c); err != nil {
			return err
		}
		if err := pub.ContactDeleted(ctx, tenant, c.ID); err != nil {
			return err
		}
		if err := pub.ContactUpdated(ctx, c, []string{"attributes.plan"}); err != nil {
			return err
		}
		return pub.ConsentRequested(ctx, c, "https://app.example.com/api/v1/public/contacts/confirm?k=x&t="+tenant.String())
	})
	if err != nil {
		t.Fatal(err)
	}

	rows, err := pool.Query(ctx, `SELECT subject, payload FROM platform.event_outbox WHERE tenant_id = $1`, tenant)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]map[string]any{}
	for rows.Next() {
		var (
			subject string
			payload []byte
		)
		if err := rows.Scan(&subject, &payload); err != nil {
			t.Fatal(err)
		}
		var evt struct {
			Type     string         `json:"type"`
			Source   string         `json:"source"`
			TenantID string         `json:"tenant_id"`
			UserID   string         `json:"user_id"`
			Data     map[string]any `json:"data"`
		}
		if err := json.Unmarshal(payload, &evt); err != nil {
			t.Fatal(err)
		}
		if evt.Type != subject || evt.Source != source || evt.TenantID != tenant.String() || evt.UserID != user.String() {
			t.Fatalf("envelope de %s: %+v", subject, evt)
		}
		got[subject] = evt.Data
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	// suppression lee exactamente {tenant_id, email} de la resuscripcion.
	if want := map[string]any{"tenant_id": tenant.String(), "email": "ana@example.com"}; !reflect.DeepEqual(got[SubjectContactResubscribed], want) {
		t.Fatalf("resubscribed: %#v", got[SubjectContactResubscribed])
	}
	if want := map[string]any{"tenant_id": tenant.String(), "contact_id": c.ID.String()}; !reflect.DeepEqual(got[SubjectContactDeleted], want) {
		t.Fatalf("el borrado no lleva la direccion: %#v", got[SubjectContactDeleted])
	}
	if upd := got[SubjectContactUpdated]; upd["attributes"] != nil || upd["plan"] != nil || !reflect.DeepEqual(upd["changed"], []any{"attributes.plan"}) {
		t.Fatalf("updated lleva nombres, no valores: %#v", upd)
	}
	if req := got[SubjectConsentRequested]; req["confirm_url"] == nil || req["email"] != "ana@example.com" || req["contact_id"] != c.ID.String() {
		t.Fatalf("requested: %#v", req)
	}

	other := uuid.New()
	c.TenantID = other
	_ = cp.Transact(ctx, func(ctx context.Context) error {
		if err := pub.ContactCreated(ctx, c); err != nil {
			return err
		}
		return context.Canceled
	})
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.event_outbox WHERE tenant_id = $1`, other).Scan(&n); err != nil || n != 0 {
		t.Fatalf("una transaccion revertida no deja evento: n=%d err=%v", n, err)
	}
}

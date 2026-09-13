package nats

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type fakeForgetter struct {
	remaining map[uuid.UUID]int64
	users     []uuid.UUID
	tenants   []uuid.UUID
	removed   []int64
	err       error
}

func (f *fakeForgetter) ForgetDeletedUser(_ context.Context, tenantID, userID uuid.UUID) (int64, error) {
	f.users, f.tenants = append(f.users, userID), append(f.tenants, tenantID)
	if f.err != nil {
		return 0, f.err
	}
	n := f.remaining[userID]
	delete(f.remaining, userID)
	f.removed = append(f.removed, n)
	return n, nil
}

func deliver(c *IdentityConsumer, evt events.Event) bool {
	acked := false
	c.Handle(evt, func() { acked = true })
	return acked
}

// deletedEvent es identity.user.deleted tal como llega del bus: el payload deserializado es
// un map[string]interface{}.
func deletedEvent(tenant, user uuid.UUID) events.Event {
	return events.Event{
		ID: uuid.NewString(), Type: "user.deleted", Source: "identity-service", TenantID: tenant.String(),
		Data: map[string]interface{}{"tenant_id": tenant.String(), "user_id": user.String(), "deleted_at": "2026-03-01T10:00:00Z"},
	}
}

func TestLaBajaRetiraLosRolesYSeReentregaSinEfecto(t *testing.T) {
	tenant, user := uuid.New(), uuid.New()
	f := &fakeForgetter{remaining: map[uuid.UUID]int64{user: 2}}
	c := NewIdentityConsumer(nil, f, zap.NewNop())
	evt := deletedEvent(tenant, user)

	if !deliver(c, evt) {
		t.Fatal("la baja aplicada se confirma")
	}
	if !deliver(c, evt) {
		t.Fatal("la reentrega del mismo evento se confirma")
	}
	if len(f.users) != 2 || f.users[0] != user || f.tenants[0] != tenant || f.removed[0] != 2 || f.removed[1] != 0 {
		t.Fatalf("usuarios=%v empresas=%v retiradas=%v", f.users, f.tenants, f.removed)
	}
}

// Una cuenta sin roles (o cuyos roles ya retiro la baja de su empresa) se confirma igual.
func TestLaBajaDeUnaCuentaSinRolesSeConfirma(t *testing.T) {
	f := &fakeForgetter{remaining: map[uuid.UUID]int64{}}
	if !deliver(NewIdentityConsumer(nil, f, zap.NewNop()), deletedEvent(uuid.New(), uuid.New())) {
		t.Fatal("una cuenta desconocida se confirma")
	}
	if len(f.removed) != 1 || f.removed[0] != 0 {
		t.Fatalf("retiradas %v", f.removed)
	}
}

func TestUnEventoIlegibleSeConfirmaSinTocarNada(t *testing.T) {
	valid := uuid.NewString()
	for name, data := range map[string]interface{}{
		"sin payload":        nil,
		"payload sin mapa":   "no es un mapa",
		"sin user_id":        map[string]interface{}{"tenant_id": valid},
		"sin tenant_id":      map[string]interface{}{"user_id": valid},
		"user_id ilegible":   map[string]interface{}{"tenant_id": valid, "user_id": "ana"},
		"user_id nulo":       map[string]interface{}{"tenant_id": valid, "user_id": uuid.Nil.String()},
		"tenant_id nulo":     map[string]interface{}{"tenant_id": uuid.Nil.String(), "user_id": valid},
		"user_id sin cadena": map[string]interface{}{"tenant_id": valid, "user_id": 42},
	} {
		f := &fakeForgetter{}
		if !deliver(NewIdentityConsumer(nil, f, zap.NewNop()), events.Event{Type: "user.deleted", Data: data}) {
			t.Errorf("%s: un evento ilegible se confirma para no reentregarse sin fin", name)
		}
		if len(f.users) != 0 {
			t.Errorf("%s: no se retira nada", name)
		}
	}
}

// Un fallo de la base no se confirma: JetStream lo reentrega y, agotadas las entregas,
// pkg/events lo guarda en EVENTS_DLQ.
func TestUnFalloDeLaBaseSeReentrega(t *testing.T) {
	f := &fakeForgetter{err: errors.New("registro no disponible")}
	if deliver(NewIdentityConsumer(nil, f, zap.NewNop()), deletedEvent(uuid.New(), uuid.New())) {
		t.Fatal("una baja no aplicada no se confirma")
	}
}

func TestSinBusNoSeSuscribe(t *testing.T) {
	NewIdentityConsumer(nil, &fakeForgetter{}, zap.NewNop()).Run(context.Background())
}

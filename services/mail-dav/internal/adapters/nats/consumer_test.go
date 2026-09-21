package nats

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type purgeCall struct{ tenant, mailbox uuid.UUID }

type fakePurger struct {
	calls   []purgeCall
	removed domain.PurgeResult
	err     error
}

func (f *fakePurger) PurgeMailbox(_ context.Context, tenantID, mailboxID uuid.UUID) (domain.PurgeResult, error) {
	f.calls = append(f.calls, purgeCall{tenantID, mailboxID})
	return f.removed, f.err
}

func handle(c *Consumer, evt events.Event) bool {
	acked := false
	c.Handle(evt, func() { acked = true })
	return acked
}

var (
	tenant  = uuid.New()
	mailbox = uuid.New()
)

func deleted(tenantID, mailboxID string) events.Event {
	return events.Event{ID: "e1", Type: SubjectMailboxDeleted, TenantID: tenantID, Data: map[string]any{
		"tenant_id": tenantID, "id": mailboxID, "username": "ana@acme.test", "domain": "acme.test", "active": true, "kind": "user",
	}}
}

func TestBuzonBorradoRetiraLosDatosDeEsaEmpresaYEseBuzon(t *testing.T) {
	p := &fakePurger{removed: domain.PurgeResult{Addressbooks: 2, Calendars: 1}}
	c := NewConsumer(nil, p, zap.NewNop())
	if !handle(c, deleted(tenant.String(), mailbox.String())) {
		t.Fatal("un evento aplicado se confirma")
	}
	if len(p.calls) != 1 || p.calls[0] != (purgeCall{tenant, mailbox}) {
		t.Fatalf("llamadas: %+v", p.calls)
	}
}

// El mismo evento entregado dos veces se aplica y se confirma las dos: la idempotencia es del caso de uso.
func TestEventoRepetidoSeConfirmaSiempre(t *testing.T) {
	p := &fakePurger{}
	c := NewConsumer(nil, p, zap.NewNop())
	evt := deleted(tenant.String(), mailbox.String())
	if !handle(c, evt) || !handle(c, evt) || len(p.calls) != 2 {
		t.Fatalf("llamadas: %+v", p.calls)
	}
}

// Se borra por el id del evento; el nombre no participa: un buzon recreado con el mismo nombre no lo lleva.
func TestSoloElIdDelEventoDecideQueSeBorra(t *testing.T) {
	p := &fakePurger{}
	c := NewConsumer(nil, p, zap.NewNop())
	recreated := uuid.New()
	handle(c, deleted(tenant.String(), mailbox.String()))
	handle(c, deleted(tenant.String(), recreated.String()))
	if len(p.calls) != 2 || p.calls[0].mailbox != mailbox || p.calls[1].mailbox != recreated {
		t.Fatalf("mismo nombre, dos ids: %+v", p.calls)
	}
	noID := deleted(tenant.String(), "")
	delete(noID.Data.(map[string]any), "id")
	if handle(c, noID) || len(p.calls) != 2 {
		t.Fatalf("un evento con solo el nombre no borra nada: %+v", p.calls)
	}
}

func TestEventosIncoherentesNoSeAplicanNiSeConfirman(t *testing.T) {
	other := uuid.New().String()
	casos := map[string]events.Event{
		"sin empresa":            deleted("", mailbox.String()),
		"empresa ilegible":       deleted("no-es-uuid", mailbox.String()),
		"empresa nula":           deleted(uuid.Nil.String(), mailbox.String()),
		"buzon ilegible":         deleted(tenant.String(), "no-es-uuid"),
		"buzon nulo":             deleted(tenant.String(), uuid.Nil.String()),
		"sin datos":              {ID: "e2", Type: SubjectMailboxDeleted},
		"datos que no son mapa":  {ID: "e3", Type: SubjectMailboxDeleted, Data: "x"},
		"sobre de otra empresa":  {ID: "e4", Type: SubjectMailboxDeleted, TenantID: other, Data: map[string]any{"tenant_id": tenant.String(), "id": mailbox.String()}},
		"id de otro tipo":        {ID: "e5", Type: SubjectMailboxDeleted, Data: map[string]any{"tenant_id": tenant.String(), "id": 7}},
		"empresa de otro tipo":   {ID: "e6", Type: SubjectMailboxDeleted, Data: map[string]any{"tenant_id": 7, "id": mailbox.String()}},
		"identificadores vacios": deleted("", ""),
	}
	for name, evt := range casos {
		p := &fakePurger{}
		c := NewConsumer(nil, p, zap.NewNop())
		if handle(c, evt) || len(p.calls) != 0 {
			t.Errorf("%s: ack o llamada inesperados (llamadas %+v)", name, p.calls)
		}
	}
}

func TestUnFalloTransitorioNoConfirmaYUnaEmpresaDesconocidaSi(t *testing.T) {
	transient := &fakePurger{err: fmt.Errorf("%w: base caida", domain.ErrUnavailable)}
	if handle(NewConsumer(nil, transient, zap.NewNop()), deleted(tenant.String(), mailbox.String())) {
		t.Fatal("un fallo transitorio se reentrega")
	}
	other := &fakePurger{err: errors.New("fallo de la base")}
	if handle(NewConsumer(nil, other, zap.NewNop()), deleted(tenant.String(), mailbox.String())) {
		t.Fatal("un error cualquiera se reentrega")
	}
	unknown := &fakePurger{err: fmt.Errorf("%w: %w", domain.ErrUnavailable, domain.ErrTenantUnknown)}
	if !handle(NewConsumer(nil, unknown, zap.NewNop()), deleted(tenant.String(), mailbox.String())) {
		t.Fatal("una empresa que ya no existe no tiene nada que borrar: se confirma")
	}
}

func TestRunSinBusNoHaceNada(t *testing.T) {
	NewConsumer(nil, &fakePurger{}, zap.NewNop()).Run(context.Background())
}

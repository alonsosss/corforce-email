package domain

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Las consultas SQL eligen la causa principal por la posicion en Reasons(): tiene que ser
// exactamente el orden de Severity.
func TestReasonsVanDeMasAMenosGrave(t *testing.T) {
	reasons := Reasons()
	for i := 1; i < len(reasons); i++ {
		if reasons[i-1].Severity() <= reasons[i].Severity() {
			t.Fatalf("%s (%d) debe ser mas grave que %s (%d)", reasons[i-1], reasons[i-1].Severity(), reasons[i], reasons[i].Severity())
		}
	}
}

func TestAggregate(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	tenant := uuid.New()
	e := func(email string, r Reason, exp *time.Time) Entry {
		return Entry{ID: uuid.New(), TenantID: tenant, Email: email, Reason: r, ExpiresAt: exp}
	}
	got := Aggregate([]Entry{
		e("ana@example.com", ReasonManual, &future),
		e("eva@example.com", ReasonManual, &past),
		e("ana@example.com", ReasonUnsubscribe, nil),
		e("luis@example.com", ReasonManual, &past),
		e("luis@example.com", ReasonInvalid, nil),
	}, now)

	if len(got) != 3 || got[0].Email != "ana@example.com" || got[1].Email != "eva@example.com" || got[2].Email != "luis@example.com" {
		t.Fatalf("una direccion por grupo, en orden de aparicion: %+v", got)
	}
	ana := got[0]
	if ana.Reason != ReasonUnsubscribe || !reflect.DeepEqual(ana.Reasons, []Reason{ReasonUnsubscribe, ReasonManual}) || len(ana.Causes) != 2 || !ana.Active() {
		t.Fatalf("ana: %+v", ana)
	}
	// Sin causas vigentes la principal es la que hay, y reasons queda vacio (no nil).
	eva := got[1]
	if eva.Reason != ReasonManual || eva.Reasons == nil || len(eva.Reasons) != 0 || eva.Active() {
		t.Fatalf("eva: %+v", eva)
	}
	// Una causa vigente menos grave gana a una caducada.
	luis := got[2]
	if luis.Reason != ReasonInvalid || !reflect.DeepEqual(luis.Reasons, []Reason{ReasonInvalid}) {
		t.Fatalf("luis: %+v", luis)
	}
}

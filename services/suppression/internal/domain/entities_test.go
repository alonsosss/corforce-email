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

// Solo la baja se vuelve a registrar al repetirse: es la unica causa que contacts compara
// con un reconsentimiento.
func TestSoloLaBajaSeVuelveARegistrar(t *testing.T) {
	for _, r := range Reasons() {
		if got, want := r.RenewedOnRepeat(), r == ReasonUnsubscribe; got != want {
			t.Errorf("%s: RenewedOnRepeat=%v, se esperaba %v", r, got, want)
		}
	}
}

// Un consentimiento solo levanta la baja registrada antes que el. En empate la baja
// sigue, como en contacts; sin hora (productor anterior) se levanta como antes.
func TestLiftedByConsentAt(t *testing.T) {
	baja := time.Date(2026, 9, 13, 12, 0, 0, 123456000, time.UTC)
	e := Entry{Reason: ReasonUnsubscribe, CreatedAt: baja}
	at := func(d time.Duration) *time.Time { t := baja.Add(d); return &t }
	for _, tc := range []struct {
		name        string
		consentedAt *time.Time
		want        bool
	}{
		{"consentimiento posterior", at(time.Microsecond), true},
		{"empate", at(0), false},
		{"baja posterior al consentimiento", at(-time.Microsecond), false},
		{"sin hora", nil, true},
	} {
		if got := e.LiftedByConsentAt(tc.consentedAt); got != tc.want {
			t.Errorf("%s: %v, se esperaba %v", tc.name, got, tc.want)
		}
	}
	// La zona horaria no cuenta: se comparan instantes.
	lima := baja.In(time.FixedZone("PET", -5*3600)).Add(time.Second)
	if !e.LiftedByConsentAt(&lima) {
		t.Fatal("un consentimiento posterior en otra zona levanta la baja")
	}
}

// causes de la consulta previa lista exactamente las causas de reasons, en su orden, cada
// una con la hora de alta de su fila; una manual caducada no aparece.
func TestSuppressedLlevaLaHoraDeCadaCausaVigente(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	tenant := uuid.New()
	baja := now.Add(-72 * time.Hour)
	rebote := now.Add(-2 * time.Hour)
	got := Aggregate([]Entry{
		{ID: uuid.New(), TenantID: tenant, Email: "ana@example.com", Reason: ReasonUnsubscribe, CreatedAt: baja},
		{ID: uuid.New(), TenantID: tenant, Email: "ana@example.com", Reason: ReasonManual, ExpiresAt: &past, CreatedAt: now.Add(-3 * time.Hour)},
		{ID: uuid.New(), TenantID: tenant, Email: "ana@example.com", Reason: ReasonHardBounce, CreatedAt: rebote},
	}, now)
	if len(got) != 1 {
		t.Fatalf("una direccion: %+v", got)
	}
	s := got[0].Suppressed(now)
	want := []ActiveCause{{Reason: ReasonHardBounce, CreatedAt: rebote}, {Reason: ReasonUnsubscribe, CreatedAt: baja}}
	if s.Email != "ana@example.com" || s.Reason != ReasonHardBounce || !reflect.DeepEqual(s.Causes, want) ||
		!reflect.DeepEqual(s.Reasons, []Reason{ReasonHardBounce, ReasonUnsubscribe}) {
		t.Fatalf("consulta previa: %+v", s)
	}
}

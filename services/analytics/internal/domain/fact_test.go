package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func at(day, hour int) time.Time { return time.Date(2026, 9, day, hour, 0, 0, 0, time.UTC) }

func newEvent(m Milestone, t time.Time) MessageEvent {
	return MessageEvent{
		EventID: uuid.New(), TenantID: uuid.New(), MessageID: uuid.New(),
		Milestone: m, Class: ClassTransactional, OccurredAt: t,
	}
}

func total(changes ...Change) Counters {
	var c Counters
	for _, ch := range changes {
		for _, d := range ch.Deltas {
			c.Add(d.Counter, d.Delta)
		}
	}
	return c
}

func TestAperturaUnica(t *testing.T) {
	f := NewMessageFact(newEvent(MilestoneSent, at(1, 9)))
	first := f.Apply(newEvent(MilestoneOpened, at(1, 10)))
	second := f.Apply(newEvent(MilestoneOpened, at(1, 12)))
	if !first.Changed || second.Changed {
		t.Fatalf("la primera apertura cambia y la segunda no: %+v %+v", first, second)
	}
	if got := total(first, second); got.OpenedUnique != 1 {
		t.Fatalf("dos aperturas del mismo mensaje suman 1, hubo %d", got.OpenedUnique)
	}
	if !f.FirstOpenedAt.Equal(at(1, 10)) {
		t.Fatalf("first_opened_at debe quedar en la primera: %v", f.FirstOpenedAt)
	}
}

func TestHitoAnteriorQueLlegaTardeMueveElDia(t *testing.T) {
	f := NewMessageFact(newEvent(MilestoneSent, at(2, 1)))
	f.Apply(newEvent(MilestoneOpened, at(2, 1)))
	moved := f.Apply(newEvent(MilestoneOpened, at(1, 23)))
	if !moved.Changed || len(moved.Deltas) != 2 {
		t.Fatalf("una apertura anterior de otro dia mueve la cuenta: %+v", moved)
	}
	days := GroupByDay(moved.Deltas)
	if len(days) != 2 || !days[0].Day.Equal(Day(at(1, 0))) || days[0].Counters.OpenedUnique != 1 || days[1].Counters.OpenedUnique != -1 {
		t.Fatalf("dias: %+v", days)
	}
	sameDay := f.Apply(newEvent(MilestoneOpened, at(1, 20)))
	if !sameDay.Changed || len(sameDay.Deltas) != 0 {
		t.Fatalf("mismo dia: cambia el instante sin mover contadores: %+v", sameDay)
	}
	if !f.FirstOpenedAt.Equal(at(1, 20)) {
		t.Fatalf("first_opened_at: %v", f.FirstOpenedAt)
	}
}

func TestReboteDuroFrenteABlando(t *testing.T) {
	soft := newEvent(MilestoneBounced, at(3, 8))
	soft.BounceKind = BounceSoft
	hard := newEvent(MilestoneBounced, at(3, 9))
	hard.BounceKind = BounceHard

	f := NewMessageFact(soft)
	c1 := f.Apply(soft)
	c2 := f.Apply(soft)
	if got := total(c1, c2); got.BouncedSoft != 1 || got.BouncedHard != 0 {
		t.Fatalf("dos rebotes blandos cuentan uno: %+v", got)
	}
	c3 := f.Apply(hard)
	if got := total(c1, c2, c3); got.BouncedSoft != 0 || got.BouncedHard != 1 {
		t.Fatalf("un duro posterior reclasifica: %+v", got)
	}
	if f.BounceKind != BounceHard || !f.BouncedAt.Equal(at(3, 8)) {
		t.Fatalf("queda duro en el instante del primer rebote: %s %v", f.BounceKind, f.BouncedAt)
	}
	if c4 := f.Apply(soft); c4.Changed {
		t.Fatalf("un blando no degrada un duro: %+v", c4)
	}

	g := NewMessageFact(hard)
	if got := total(g.Apply(hard)); got.BouncedHard != 1 || got.BouncedSoft != 0 {
		t.Fatalf("rebote duro directo: %+v", got)
	}
}

func TestTipoDeReboteDelProveedor(t *testing.T) {
	cases := map[string]BounceKind{
		"Permanent": BounceHard, "permanent": BounceHard, " PERMANENT ": BounceHard,
		"Transient": BounceSoft, "transient": BounceSoft, "Undetermined": BounceSoft, "": BounceSoft,
	}
	for in, want := range cases {
		if got := BounceKindFromProvider(in); got != want {
			t.Errorf("%q: %s, se esperaba %s", in, got, want)
		}
	}
}

func TestHitosIndependientes(t *testing.T) {
	f := NewMessageFact(newEvent(MilestoneSent, at(4, 1)))
	var changes []Change
	for _, m := range []Milestone{MilestoneSent, MilestoneDelivered, MilestoneOpened, MilestoneClicked,
		MilestoneComplained, MilestoneUnsubscribed, MilestoneFailed, MilestoneSent, MilestoneClicked} {
		changes = append(changes, f.Apply(newEvent(m, at(4, 2))))
	}
	want := Counters{Sent: 1, Delivered: 1, OpenedUnique: 1, ClickedUnique: 1, Complained: 1, Unsubscribed: 1, Failed: 1}
	if got := total(changes...); got != want {
		t.Fatalf("cada hito cuenta una vez: %+v", got)
	}
}

func TestGroupByDayOrdenaYDescartaCeros(t *testing.T) {
	deltas := []CounterDelta{
		{Day: at(5, 10), Counter: CounterSent, Delta: 1},
		{Day: at(3, 10), Counter: CounterSent, Delta: 1},
		{Day: at(4, 10), Counter: CounterOpenedUnique, Delta: 1},
		{Day: at(4, 11), Counter: CounterOpenedUnique, Delta: -1},
	}
	days := GroupByDay(deltas)
	if len(days) != 2 || !days[0].Day.Equal(Day(at(3, 0))) || !days[1].Day.Equal(Day(at(5, 0))) {
		t.Fatalf("orden ascendente y sin dias en cero: %+v", days)
	}
}

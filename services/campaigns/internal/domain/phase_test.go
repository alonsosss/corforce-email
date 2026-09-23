package domain

import (
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
)

var phaseNow = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

func draftCampaign(t *testing.T) *Campaign {
	t.Helper()
	c, err := NewCampaign(uuid.New(), NewCampaignInput{
		Name: "Otono", TemplateID: uuid.New(), FromEmail: "news@shop.example.com",
		Audience: Audience{ListIDs: []uuid.UUID{uuid.New()}}, CreatedBy: uuid.New(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func abCampaign(t *testing.T) *Campaign {
	t.Helper()
	c := draftCampaign(t)
	c.ABTest = validAB()
	if err := c.PinVariants([]int{3, 3}); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(3, phaseNow); err != nil {
		t.Fatal(err)
	}
	return c
}

func contactIn(zone string) Contact {
	return Contact{ID: uuid.New(), Email: uuid.NewString()[:8] + "@example.com", Timezone: zone}
}

func TestInitialPhases(t *testing.T) {
	c := draftCampaign(t)
	main := c.InitialPhases(phaseNow)
	if len(main) != 1 || main[0].Kind != PhaseMain || main[0].Key != "main" || main[0].Status != PhasePending || main[0].NotBefore != nil {
		t.Fatalf("sin opciones, una fase principal: %+v", main)
	}

	c = abCampaign(t)
	phases := c.InitialPhases(phaseNow)
	if len(phases) != 3 {
		t.Fatalf("dos muestras y la ganadora: %d", len(phases))
	}
	for i := 0; i < 2; i++ {
		if phases[i].Kind != PhaseSample || *phases[i].Variant != i || phases[i].Ordinal != i || phases[i].Key != "sample:"+string(rune('0'+i)) {
			t.Fatalf("muestra %d: %+v", i, phases[i])
		}
	}
	w := phases[2]
	if w.Kind != PhaseWinner || w.Variant != nil || w.NotBefore != nil || w.Ordinal <= phases[1].Ordinal {
		t.Fatalf("la ganadora va despues y sin fecha hasta que la muestra termina: %+v", w)
	}

	c = draftCampaign(t)
	c.TimezoneDelivery = &TimezoneDelivery{LocalSendAt: mustLocal(t, "2026-10-01T09:00"), FallbackTimezone: "UTC"}
	zone := c.InitialPhases(phaseNow)
	if len(zone) != 1 || zone[0].Kind != PhaseZone || !zone[0].SlotAt.Equal(phaseNow) || !zone[0].NotBefore.Equal(phaseNow) {
		t.Fatalf("envio por zona: un primer tramo al arrancar: %+v", zone)
	}
	if zone[0].Round() != RoundInitial || !zone[0].Filtered() || !zone[0].NeedsSent() {
		t.Fatal("el tramo es de la ronda inicial y excluye a quien ya recibio")
	}
}

func TestZoneAndResendPhases(t *testing.T) {
	c := draftCampaign(t)
	slot := time.Date(2026, 10, 1, 7, 0, 0, 0, time.FixedZone("CEST", 7200))
	p := c.ZonePhase(slot)
	if p.Key != "zone:2026-10-01T05:00:00Z" || p.SlotAt.Location() != time.UTC || !p.NotBefore.Equal(slot) {
		t.Fatalf("la clave del tramo es su instante en UTC: %+v", p)
	}
	if c.ResendPhase(phaseNow) != nil {
		t.Fatal("sin reenvio configurado no hay fase de reenvio")
	}
	c.Resend = &Resend{Subject: "Te lo perdiste", DelayMinutes: 48 * 60}
	r := c.ResendPhase(phaseNow)
	if r.Kind != PhaseResend || r.Key != "resend" || !r.NotBefore.Equal(phaseNow.Add(48*time.Hour)) || r.Round() != RoundResend {
		t.Fatalf("reenvio: %+v", r)
	}
	if r.Due(phaseNow) || !r.Due(phaseNow.Add(48*time.Hour)) {
		t.Fatal("el reenvio espera su retraso")
	}
	if r.NeedsSent() || !r.NeedsResendEligibility() {
		t.Fatal("el reenvio consulta quien cumple, no quien recibio")
	}
	r.Complete(phaseNow)
	if r.Status != PhaseDone || r.CompletedAt == nil || r.StartedAt == nil {
		t.Fatalf("una fase cerrada tiene fechas: %+v", r)
	}
}

func TestContentFor(t *testing.T) {
	c := abCampaign(t)
	other := uuid.New()
	c.ABTest.Variants[1].TemplateID = &other
	five := 5
	c.ABTest.Variants[1].PinnedVersion = &five
	c.Resend = &Resend{Subject: "Ultimo aviso", DelayMinutes: 24 * 60}

	base, err := c.ContentFor(nil)
	if err != nil || base != (Content{TemplateID: c.TemplateID, TemplateVersion: 3}) {
		t.Fatalf("un lote sin fase es de la principal: %+v %v", base, err)
	}
	phases := c.InitialPhases(phaseNow)
	a, _ := c.ContentFor(phases[0])
	b, _ := c.ContentFor(phases[1])
	if a != (Content{TemplateID: c.TemplateID, TemplateVersion: 3, Subject: "Oferta de otono", UTMContent: "ab-a"}) ||
		b != (Content{TemplateID: other, TemplateVersion: 5, Subject: "Solo hoy: 20 % menos", UTMContent: "ab-b"}) {
		t.Fatalf("contenido de las variantes: %+v %+v", a, b)
	}
	if _, err := c.ContentFor(phases[2]); !errors.Is(err, ErrInvalidCampaign) {
		t.Fatalf("sin ganadora no hay contenido de ganadora: %v", err)
	}
	if err := c.DecideWinner(ABDecision{Winner: 1, Criterion: CriterionOpens, Reason: ReasonCriterion}, phaseNow); err != nil {
		t.Fatal(err)
	}
	w, _ := c.ContentFor(phases[2])
	if w != b {
		t.Fatalf("la ganadora envia el contenido de su variante: %+v", w)
	}
	resend, _ := c.ContentFor(c.ResendPhase(phaseNow))
	if resend != (Content{TemplateID: other, TemplateVersion: 5, Subject: "Ultimo aviso", UTMContent: UTMContentResend}) {
		t.Fatalf("el reenvio usa la ganadora con su propio asunto: %+v", resend)
	}

	plain := draftCampaign(t)
	if _, err := plain.ContentFor(nil); !errors.Is(err, ErrInvalidCampaign) {
		t.Fatal("sin version fijada no hay contenido")
	}
	if err := plain.Start(2, phaseNow); err != nil {
		t.Fatal(err)
	}
	plain.Resend = &Resend{Subject: "Otra vez", DelayMinutes: 24 * 60}
	if got, _ := plain.ContentFor(plain.ResendPhase(phaseNow)); got != (Content{TemplateID: plain.TemplateID, TemplateVersion: 2, Subject: "Otra vez", UTMContent: UTMContentResend}) {
		t.Fatalf("reenvio sin A/B: %+v", got)
	}
	bad := &Phase{Kind: PhaseSample, Variant: func() *int { v := 3; return &v }()}
	if _, err := c.ContentFor(bad); !errors.Is(err, ErrInvalidCampaign) {
		t.Fatal("una variante que no existe no tiene contenido")
	}
}

func TestSelectSample(t *testing.T) {
	c := abCampaign(t)
	contacts := make([]Contact, 2000)
	for i := range contacts {
		contacts[i] = contactIn("")
	}
	phases := c.InitialPhases(phaseNow)
	seen := map[string]int{}
	total := 0
	for v := 0; v < 2; v++ {
		sel := c.Select(phases[v], contacts, SelectionLookup{})
		for _, r := range sel.Recipients {
			seen[r.Email]++
			if got, in := c.ABTest.Assign(c.ID, *r.ContactID); !in || got != v {
				t.Fatalf("%s no es de la variante %d", r.Email, v)
			}
		}
		total += len(sel.Recipients)
	}
	for email, n := range seen {
		if n != 1 {
			t.Fatalf("%s aparece en %d variantes", email, n)
		}
	}
	if total < 300 || total > 500 {
		t.Fatalf("una muestra del 20 %% de 2000 ronda los 400: %d", total)
	}
}

func TestSelectWinnerAndResend(t *testing.T) {
	c := abCampaign(t)
	contacts := []Contact{contactIn(""), contactIn(""), contactIn(""), {ID: uuid.New()}}
	winner := c.InitialPhases(phaseNow)[2]
	sel := c.Select(winner, contacts, SelectionLookup{Sent: map[uuid.UUID]bool{contacts[0].ID: true}})
	if len(sel.Recipients) != 2 || sel.Recipients[0].Email != contacts[1].Email {
		t.Fatalf("la ganadora va a quien no la recibio y tiene direccion: %+v", sel.Recipients)
	}
	c.Resend = &Resend{Subject: "x", DelayMinutes: 24 * 60}
	sel = c.Select(c.ResendPhase(phaseNow), contacts, SelectionLookup{ResendEligible: map[uuid.UUID]bool{contacts[2].ID: true}})
	if len(sel.Recipients) != 1 || sel.Recipients[0].Email != contacts[2].Email {
		t.Fatalf("el reenvio va solo a quien cumple: %+v", sel.Recipients)
	}
	main := draftCampaign(t).InitialPhases(phaseNow)[0]
	if got := c.Select(main, contacts, SelectionLookup{}); len(got.Recipients) != 3 {
		t.Fatalf("la principal envia a todos los que tienen direccion: %d", len(got.Recipients))
	}
	if got := c.Select(nil, contacts, SelectionLookup{}); len(got.Recipients) != 3 {
		t.Fatalf("un lote sin fase es de la principal: %d", len(got.Recipients))
	}
}

// Un tramo envia a quien ya alcanzo su hora y no la recibio, y descubre los tramos
// siguientes (uno por instante distinto, sin repetir).
func TestSelectZoneSlots(t *testing.T) {
	c := draftCampaign(t)
	c.TimezoneDelivery = &TimezoneDelivery{LocalSendAt: mustLocal(t, "2026-10-01T09:00"), FallbackTimezone: "America/New_York"}
	madrid := contactIn("Europe/Madrid")
	madrid2 := contactIn("Europe/Madrid")
	lima := contactIn("America/Lima")
	sinZona := contactIn("")
	invalida := contactIn("Mars/Olympus")
	yaEnviado := contactIn("Europe/Madrid")
	sinDireccion := Contact{ID: uuid.New(), Timezone: "Asia/Tokyo"}
	contacts := []Contact{madrid, madrid2, lima, sinZona, invalida, yaEnviado, sinDireccion}
	sent := map[uuid.UUID]bool{yaEnviado.ID: true}

	first := c.ZonePhase(phaseNow)
	sel := c.Select(first, contacts, SelectionLookup{Sent: sent})
	if len(sel.Recipients) != 0 {
		t.Fatalf("una semana antes nadie alcanzo su hora: %+v", sel.Recipients)
	}
	slots := append([]time.Time(nil), sel.FutureSlots...)
	sort.Slice(slots, func(i, j int) bool { return slots[i].Before(slots[j]) })
	want := []time.Time{
		time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 1, 14, 0, 0, 0, time.UTC),
	}
	if len(slots) != len(want) {
		t.Fatalf("tramos: %v", slots)
	}
	for i := range want {
		if !slots[i].Equal(want[i]) {
			t.Fatalf("tramo %d = %s, quiero %s", i, slots[i], want[i])
		}
	}

	madridSlot := c.ZonePhase(want[0])
	sel = c.Select(madridSlot, contacts, SelectionLookup{Sent: sent})
	if len(sel.Recipients) != 2 || len(sel.FutureSlots) != 2 {
		t.Fatalf("el tramo de Madrid envia a sus dos contactos y ve los dos siguientes: %+v", sel)
	}
	sent[madrid.ID], sent[madrid2.ID] = true, true
	nySlot := c.ZonePhase(want[1])
	sel = c.Select(nySlot, contacts, SelectionLookup{Sent: sent})
	if len(sel.Recipients) != 2 {
		t.Fatalf("sin zona y con zona invalida reciben a la hora de respaldo: %+v", sel.Recipients)
	}
	sent[sinZona.ID], sent[invalida.ID] = true, true
	// Un tramo que arranca tarde recoge tambien a quien ya debia haber recibido.
	late := c.ZonePhase(want[2].Add(3 * time.Hour))
	sel = c.Select(late, contacts, SelectionLookup{Sent: sent})
	if len(sel.Recipients) != 1 || sel.Recipients[0].Email != lima.Email || len(sel.FutureSlots) != 0 {
		t.Fatalf("tramo tardio: %+v", sel)
	}
}

// En un cambio de hora el tramo es el instante resuelto por In: Nueva York el dia del
// salto de primavera y el dia siguiente caen en tramos distintos de su desfase habitual.
func TestSelectZoneAcrossDST(t *testing.T) {
	c := draftCampaign(t)
	c.TimezoneDelivery = &TimezoneDelivery{LocalSendAt: mustLocal(t, "2026-03-08T02:30"), FallbackTimezone: "UTC"}
	ny := contactIn("America/New_York")
	sel := c.Select(c.ZonePhase(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)), []Contact{ny}, SelectionLookup{})
	if len(sel.FutureSlots) != 1 || !sel.FutureSlots[0].Equal(time.Date(2026, 3, 8, 7, 30, 0, 0, time.UTC)) {
		t.Fatalf("la hora inexistente se desplaza el salto: %v", sel.FutureSlots)
	}
}

func TestNextPhaseBatch(t *testing.T) {
	c := abCampaign(t)
	phases := c.InitialPhases(phaseNow)
	cursor := "c1"
	first := NextPhaseBatch(c, nil, phases[0], phaseNow)
	if first.Seq != 1 || first.CursorIn != nil || first.PhaseID == nil || *first.PhaseID != phases[0].ID || first.LeaseToken == nil {
		t.Fatalf("primer lote: %+v", first)
	}
	first.CursorOut = &cursor
	first.Status = BatchDelivered
	same := NextPhaseBatch(c, first, phases[0], phaseNow)
	if same.Seq != 2 || same.CursorIn == nil || *same.CursorIn != "c1" {
		t.Fatalf("en la misma fase sigue el cursor: %+v", same)
	}
	next := NextPhaseBatch(c, first, phases[1], phaseNow)
	if next.Seq != 2 || next.CursorIn != nil || *next.PhaseID != phases[1].ID {
		t.Fatalf("una fase nueva empieza la audiencia con el numero siguiente: %+v", next)
	}
	if next.IdempotencyKey() != BatchIdempotencyKey(c.ID, 2) {
		t.Fatal("la clave depende solo de campana y numero")
	}

	legacy := &Batch{Seq: 7, CursorOut: &cursor, Status: BatchDelivered}
	mainPhase := draftCampaign(t).InitialPhases(phaseNow)[0]
	if !legacy.InPhase(mainPhase) || legacy.InPhase(phases[0]) || !legacy.InPhase(nil) {
		t.Fatal("un lote anterior a las fases es de la principal")
	}
	resumed := NextPhaseBatch(c, legacy, mainPhase, phaseNow)
	if resumed.Seq != 8 || resumed.CursorIn == nil || *resumed.CursorIn != "c1" {
		t.Fatalf("la principal continua el cursor de un lote anterior a las fases: %+v", resumed)
	}
	if first.InPhase(nil) {
		t.Fatal("un lote con fase no es de la fase nil")
	}
}

func TestSampleResults(t *testing.T) {
	zero, one := 0, 1
	rows := []PhaseEngagement{
		{Kind: PhaseSample, Variant: &zero, Accepted: 10, Delivered: 9, Opened: 3, Clicked: 1},
		{Kind: PhaseSample, Variant: &one, Accepted: 11, Delivered: 10, Opened: 5, Clicked: 0},
		{Kind: PhaseWinner, Accepted: 100, Delivered: 90, Opened: 40},
		{Kind: PhaseResend, Accepted: 50, Delivered: 45, Opened: 5},
	}
	got := SampleResults(rows)
	if len(got) != 2 || got[1] != (VariantResult{Variant: 1, Accepted: 11, Delivered: 10, Opened: 5}) {
		t.Fatalf("solo cuentan las muestras: %+v", got)
	}
}

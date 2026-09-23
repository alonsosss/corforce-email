package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewCampaignWithOptions(t *testing.T) {
	ab := validAB()
	ab.Variants[0].Subject = "  Con espacios  "
	c, err := NewCampaign(uuid.New(), NewCampaignInput{
		Name: "AB", TemplateID: uuid.New(), FromEmail: "news@shop.example.com",
		Audience: Audience{ListIDs: []uuid.UUID{uuid.New()}}, CreatedBy: uuid.New(),
		ABTest: ab, Resend: &Resend{Subject: " Otra vez ", DelayMinutes: 24 * 60},
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.ABTest == ab || c.ABTest.Variants[0].Subject != "Con espacios" || c.Resend.Subject != "Otra vez" {
		t.Fatalf("la campana guarda una copia normalizada: %+v %+v", c.ABTest, c.Resend)
	}
	ab.Variants[1].Subject = "cambiado fuera"
	if c.ABTest.Variants[1].Subject == "cambiado fuera" {
		t.Fatal("la campana no comparte las variantes con quien la creo")
	}
	bad := validAB()
	bad.SamplePercent = 5
	if _, err := NewCampaign(uuid.New(), NewCampaignInput{
		Name: "AB", TemplateID: uuid.New(), FromEmail: "news@shop.example.com",
		Audience: Audience{ListIDs: []uuid.UUID{uuid.New()}}, CreatedBy: uuid.New(), ABTest: bad,
	}); !errors.Is(err, ErrInvalidCampaign) {
		t.Fatalf("una prueba A/B invalida no se crea: %v", err)
	}
}

func TestPatchOptions(t *testing.T) {
	c := draftCampaign(t)
	if err := c.ApplyPatch(Patch{ABTest: Optional[ABTest]{Set: true, Value: validAB()}}); err != nil || c.ABTest == nil {
		t.Fatalf("anadir prueba A/B: %v", err)
	}
	if err := c.ApplyPatch(Patch{Resend: Optional[Resend]{Set: true, Value: &Resend{Subject: "x", DelayMinutes: 1440}}}); err != nil || c.Resend == nil {
		t.Fatalf("anadir reenvio: %v", err)
	}
	if err := c.ApplyPatch(Patch{Name: strPtr("Otro nombre")}); err != nil || c.ABTest == nil || c.Resend == nil {
		t.Fatal("un PATCH sin las opciones no las toca")
	}
	if err := c.ApplyPatch(Patch{ABTest: Optional[ABTest]{Set: true}}); err != nil || c.ABTest != nil {
		t.Fatalf("null quita la prueba: %v", err)
	}
	bad := &Resend{Subject: "x", DelayMinutes: 10}
	if err := c.ApplyPatch(Patch{Resend: Optional[Resend]{Set: true, Value: bad}}); !errors.Is(err, ErrInvalidCampaign) || c.Resend.DelayMinutes != 1440 {
		t.Fatalf("un cambio invalido no se aplica: %v", err)
	}

	c.Status = StatusPaused
	for name, p := range map[string]Patch{
		"ab":     {ABTest: Optional[ABTest]{Set: true}},
		"resend": {Resend: Optional[Resend]{Set: true}},
	} {
		if err := c.ApplyPatch(p); !errors.Is(err, ErrLockedWhilePaused) {
			t.Errorf("%s: en pausa no se cambia: %v", name, err)
		}
	}
}

func TestPinVariantsAndReadiness(t *testing.T) {
	c := draftCampaign(t)
	if err := c.PinVariants(nil); err != nil {
		t.Fatal("sin prueba no hay nada que fijar")
	}
	if err := c.PinVariants([]int{1}); !errors.Is(err, ErrInvalidCampaign) {
		t.Fatal("versiones sin prueba A/B")
	}
	c.ABTest = validAB()
	if err := c.Start(1, phaseNow); !errors.Is(err, ErrInvalidCampaign) {
		t.Fatalf("no arranca con variantes sin version: %v", err)
	}
	if err := c.PinVariants([]int{1}); !errors.Is(err, ErrInvalidCampaign) {
		t.Fatal("una version por variante")
	}
	if err := c.PinVariants([]int{1, 0}); !errors.Is(err, ErrInvalidCampaign) {
		t.Fatal("versiones positivas")
	}
	if err := c.PinVariants([]int{1, 4}); err != nil || *c.ABTest.Variants[1].PinnedVersion != 4 {
		t.Fatalf("fijar: %v", err)
	}
	if err := c.Start(1, phaseNow); err != nil {
		t.Fatal(err)
	}
}

func TestDecideWinnerOnce(t *testing.T) {
	c := abCampaign(t)
	d := ABDecision{Winner: 1, Criterion: CriterionOpens, Reason: ReasonCriterion}
	if err := c.DecideWinner(ABDecision{Winner: 2}, phaseNow); !errors.Is(err, ErrInvalidCampaign) {
		t.Fatal("una variante inexistente no gana")
	}
	if err := c.DecideWinner(d, phaseNow); err != nil || *c.ABWinner != 1 || !c.ABDecidedAt.Equal(phaseNow) || c.ABDecision.Winner != 1 {
		t.Fatalf("decidir: %v", err)
	}
	if err := c.DecideWinner(ABDecision{Winner: 0}, phaseNow); !errors.Is(err, ErrABAlreadyDecided) || *c.ABWinner != 1 {
		t.Fatal("la ganadora se decide una sola vez")
	}
	plain := draftCampaign(t)
	if err := plain.DecideWinner(d, phaseNow); !errors.Is(err, ErrInvalidCampaign) {
		t.Fatal("sin prueba no hay ganadora")
	}
}

func TestSameContent(t *testing.T) {
	a := draftCampaign(t)
	b := *a
	if !a.SameContent(&b) {
		t.Fatal("igual a si misma")
	}
	b.ABTest = validAB()
	if a.SameContent(&b) {
		t.Fatal("anadir una prueba cambia el contenido")
	}
	a.ABTest = validAB()
	if !a.SameContent(&b) {
		t.Fatal("misma prueba, mismo contenido")
	}
	b.ABTest.Variants[1].Subject = "otro"
	if a.SameContent(&b) {
		t.Fatal("otro asunto, otro contenido")
	}
	b.ABTest = validAB()
	b.ABTest.Variants = append(b.ABTest.Variants, ABVariant{Subject: "c"})
	if a.SameContent(&b) {
		t.Fatal("otra variante, otro contenido")
	}
	b = *a
	b.TemplateID = uuid.New()
	if a.SameContent(&b) {
		t.Fatal("otra plantilla, otro contenido")
	}
}

func TestScheduleLocal(t *testing.T) {
	local := mustLocal(t, "2026-10-01T09:00")
	c := draftCampaign(t)
	if err := c.ScheduleLocal(local, "America/Lima", 2, phaseNow); err != nil {
		t.Fatal(err)
	}
	wantStart := time.Date(2026, 9, 30, 19, 0, 0, 0, time.UTC)
	if c.Status != StatusScheduled || !c.ScheduledAt.Equal(wantStart) || *c.TemplateVersion != 2 {
		t.Fatalf("arranca cuando la primera zona marca la hora: %+v", c.ScheduledAt)
	}
	if c.TimezoneDelivery == nil || c.TimezoneDelivery.FallbackTimezone != "America/Lima" || c.TimezoneDelivery.LocalSendAt != local {
		t.Fatalf("guarda hora y zona de respaldo: %+v", c.TimezoneDelivery)
	}

	soon := draftCampaign(t)
	now := time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC)
	if err := soon.ScheduleLocal(local, "America/Lima", 1, now); err != nil {
		t.Fatal(err)
	}
	if !soon.ScheduledAt.Equal(now.Add(MinScheduleLead)) {
		t.Fatalf("si la primera zona ya paso, arranca en cuanto se puede: %s", soon.ScheduledAt)
	}

	cases := map[string]struct {
		c        func() *Campaign
		local    LocalDateTime
		fallback string
		version  int
		err      error
	}{
		"zona de respaldo invalida": {func() *Campaign { return draftCampaign(t) }, local, "Mars/Olympus", 1, ErrInvalidCampaign},
		"sin zona de respaldo":      {func() *Campaign { return draftCampaign(t) }, local, "", 1, ErrInvalidCampaign},
		"sin hora":                  {func() *Campaign { return draftCampaign(t) }, LocalDateTime{}, "UTC", 1, ErrInvalidCampaign},
		"hora pasada en respaldo":   {func() *Campaign { return draftCampaign(t) }, mustLocal(t, "2026-09-23T05:00"), "America/Lima", 1, ErrScheduleInPast},
		"mas alla del horizonte":    {func() *Campaign { return draftCampaign(t) }, mustLocal(t, "2028-01-01T09:00"), "UTC", 1, ErrScheduleTooFar},
		"version cero":              {func() *Campaign { return draftCampaign(t) }, local, "UTC", 0, ErrInvalidCampaign},
		"en envio":                  {func() *Campaign { x := draftCampaign(t); x.Status = StatusSending; return x }, local, "UTC", 1, ErrInvalidTransition},
		"con prueba A/B": {func() *Campaign {
			x := draftCampaign(t)
			x.ABTest = validAB()
			_ = x.PinVariants([]int{1, 1})
			return x
		}, local, "UTC", 1, ErrABWithTimezone},
	}
	for name, tc := range cases {
		x := tc.c()
		before := x.Status
		if err := x.ScheduleLocal(tc.local, tc.fallback, tc.version, phaseNow); !errors.Is(err, tc.err) {
			t.Errorf("%s: err = %v, quiero %v", name, err, tc.err)
		}
		if x.Status != before || (x.TimezoneDelivery != nil && before == StatusDraft) {
			t.Errorf("%s: un rechazo no cambia la campana", name)
		}
	}

	if err := c.Schedule(phaseNow.Add(time.Hour), 2, phaseNow); err != nil || c.TimezoneDelivery != nil {
		t.Fatalf("reprogramar a un instante quita el envio por zona: %v", err)
	}
	if err := c.ScheduleLocal(local, "UTC", 2, phaseNow); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(2, phaseNow); err != nil || c.TimezoneDelivery != nil {
		t.Fatalf("iniciar ya quita el envio por zona: %v", err)
	}
}

// La prueba A/B y la zona horaria no se combinan ni por PATCH.
func TestABWithTimezoneRejected(t *testing.T) {
	c := draftCampaign(t)
	c.TimezoneDelivery = &TimezoneDelivery{LocalSendAt: mustLocal(t, "2026-10-01T09:00"), FallbackTimezone: "UTC"}
	if err := c.ApplyPatch(Patch{ABTest: Optional[ABTest]{Set: true, Value: validAB()}}); !errors.Is(err, ErrABWithTimezone) || c.ABTest != nil {
		t.Fatalf("anadir A/B a una campana por zona: %v", err)
	}
}

func strPtr(s string) *string { return &s }

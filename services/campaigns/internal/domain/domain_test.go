package domain

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func newDraft(t *testing.T) *Campaign {
	t.Helper()
	c, err := NewCampaign(uuid.New(), NewCampaignInput{
		Name: "  Otono  ", TemplateID: uuid.New(), FromEmail: "news@shop.example.com",
		Audience: Audience{ListIDs: []uuid.UUID{uuid.New()}}, CreatedBy: uuid.New(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewCampaignNormalizesAndValidates(t *testing.T) {
	c := newDraft(t)
	if c.Name != "Otono" || c.Status != StatusDraft || c.Audience.SegmentIDs == nil || c.Audience.ExcludeSegmentIDs == nil {
		t.Fatalf("borrador: %+v", c)
	}
	base := NewCampaignInput{Name: "x", TemplateID: uuid.New(), FromEmail: "a@example.com",
		Audience: Audience{ListIDs: []uuid.UUID{uuid.New()}}, CreatedBy: uuid.New()}
	cases := map[string]func(in *NewCampaignInput){
		"sin nombre":            func(in *NewCampaignInput) { in.Name = " " },
		"sin plantilla":         func(in *NewCampaignInput) { in.TemplateID = uuid.Nil },
		"remitente invalido":    func(in *NewCampaignInput) { in.FromEmail = "a@b" },
		"remitente con nombre":  func(in *NewCampaignInput) { in.FromEmail = "Ana <a@example.com>" },
		"inyeccion de cabecera": func(in *NewCampaignInput) { in.FromEmail = "a@example.com\r\nBcc: x@example.com" },
		"nombre con salto":      func(in *NewCampaignInput) { in.FromName = "Tienda\nBcc: x" },
		"reply_to invalido":     func(in *NewCampaignInput) { in.ReplyTo = "no-es-direccion" },
		"audiencia vacia":       func(in *NewCampaignInput) { in.Audience = Audience{} },
		"sin usuario":           func(in *NewCampaignInput) { in.CreatedBy = uuid.Nil },
	}
	for name, mutate := range cases {
		in := base
		mutate(&in)
		if _, err := NewCampaign(uuid.New(), in); !errors.Is(err, ErrInvalidCampaign) {
			t.Errorf("%s: se esperaba ErrInvalidCampaign, hubo %v", name, err)
		}
	}
}

func TestAudienceValidate(t *testing.T) {
	seg := uuid.New()
	cases := []struct {
		name string
		a    Audience
		ok   bool
	}{
		{"lista", Audience{ListIDs: []uuid.UUID{uuid.New()}}, true},
		{"segmento con exclusion", Audience{SegmentIDs: []uuid.UUID{uuid.New()}, ExcludeSegmentIDs: []uuid.UUID{uuid.New()}}, true},
		{"solo exclusion", Audience{ExcludeSegmentIDs: []uuid.UUID{uuid.New()}}, false},
		{"id vacio", Audience{ListIDs: []uuid.UUID{uuid.Nil}}, false},
		{"repetido", Audience{ListIDs: []uuid.UUID{seg, seg}}, false},
		{"incluido y excluido", Audience{SegmentIDs: []uuid.UUID{seg}, ExcludeSegmentIDs: []uuid.UUID{seg}}, false},
		{"demasiados", Audience{ListIDs: make([]uuid.UUID, MaxAudienceIDs+1)}, false},
	}
	for _, tc := range cases {
		if err := tc.a.Validate(); (err == nil) != tc.ok {
			t.Errorf("%s: err=%v", tc.name, err)
		}
	}
}

func TestTransitions(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	type action func(c *Campaign) error
	schedule := func(c *Campaign) error { return c.Schedule(future, 1, now) }
	start := func(c *Campaign) error { return c.Start(1, now) }
	pause := func(c *Campaign) error { return c.Pause(PauseReasonManual) }
	resume := func(c *Campaign) error { _, err := c.Resume(now); return err }
	cancel := func(c *Campaign) error { return c.Cancel() }
	complete := func(c *Campaign) error { return c.Complete(now) }
	fail := func(c *Campaign) error { return c.Fail("rechazo") }

	allowed := map[Status]map[string]bool{
		StatusDraft:     {"schedule": true, "start": true},
		StatusScheduled: {"schedule": true, "start": true, "pause": true, "cancel": true},
		StatusSending:   {"pause": true, "cancel": true, "complete": true, "fail": true},
		StatusPaused:    {"resume": true, "cancel": true},
		StatusCompleted: {},
		StatusCancelled: {},
		StatusFailed:    {},
	}
	actions := map[string]action{"schedule": schedule, "start": start, "pause": pause, "resume": resume,
		"cancel": cancel, "complete": complete, "fail": fail}

	for from, ok := range allowed {
		for name, act := range actions {
			c := newDraft(t)
			version := 1
			c.Status, c.TemplateVersion = from, &version
			err := act(c)
			if ok[name] && err != nil {
				t.Errorf("%s -> %s deberia permitirse: %v", from, name, err)
			}
			if !ok[name] && !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("%s -> %s deberia rechazarse, hubo %v", from, name, err)
			}
		}
	}
}

func TestApplyPatchRules(t *testing.T) {
	c := newDraft(t)
	name := "Nueva"
	if err := c.ApplyPatch(Patch{Name: &name}); err != nil || c.Name != "Nueva" {
		t.Fatalf("patch en borrador: %v", err)
	}
	if err := c.ApplyPatch(Patch{}); !errors.Is(err, ErrNothingToUpdate) {
		t.Fatalf("patch vacio: %v", err)
	}
	bad := ""
	if err := c.ApplyPatch(Patch{FromEmail: &bad}); !errors.Is(err, ErrInvalidCampaign) || c.FromEmail == "" {
		t.Fatalf("un patch invalido no deja cambios a medias: %v from=%q", err, c.FromEmail)
	}
	c.Status = StatusPaused
	aud := Audience{ListIDs: []uuid.UUID{uuid.New()}}
	if err := c.ApplyPatch(Patch{Audience: &aud}); !errors.Is(err, ErrLockedWhilePaused) {
		t.Fatalf("audiencia en pausa: %v", err)
	}
	c.Status = StatusCompleted
	if err := c.ApplyPatch(Patch{Name: &name}); !errors.Is(err, ErrNotEditable) {
		t.Fatalf("patch en completada: %v", err)
	}
}

func TestResumeTarget(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	c := newDraft(t)
	if err := c.Schedule(now.Add(time.Hour), 1, now); err != nil {
		t.Fatal(err)
	}
	if err := c.Pause(PauseReasonManual); err != nil {
		t.Fatal(err)
	}
	if first, err := c.Resume(now); err != nil || first || c.Status != StatusScheduled {
		t.Fatalf("vuelve a programada: first=%v status=%s err=%v", first, c.Status, err)
	}
	_ = c.Pause(PauseReasonManual)
	if first, err := c.Resume(now.Add(2 * time.Hour)); err != nil || !first || c.Status != StatusSending || c.StartedAt == nil {
		t.Fatalf("fecha vencida: first=%v status=%s err=%v", first, c.Status, err)
	}
	if !c.Deletable() == (c.Status == StatusDraft) {
		t.Fatal("solo borrador y cancelada se borran")
	}
}

func TestRetryBackoffAndClamp(t *testing.T) {
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	for i, w := range want {
		if got := RetryBackoff(i + 1); got != w {
			t.Errorf("intento %d: %s, se esperaba %s", i+1, got, w)
		}
	}
	if RetryBackoff(0) != 30*time.Second {
		t.Error("intento 0 = primera espera")
	}
	if ClampRetryAfter(0) != DefaultRetryAfter || ClampRetryAfter(5*time.Hour) != MaxRetryAfter || ClampRetryAfter(time.Minute) != time.Minute {
		t.Error("ClampRetryAfter")
	}
}

func TestRates(t *testing.T) {
	r := Counters{Sent: 4, Delivered: 3, Opened: 1, Bounced: 1, Unsubscribed: 3}.Rates()
	if r.DeliveryRate != "0.7500" || r.OpenRate != "0.3333" || r.BounceRate != "0.2500" || r.ClickRate != "0.0000" || r.UnsubscribeRate != "1.0000" {
		t.Fatalf("tasas: %+v", r)
	}
	if z := (Counters{}).Rates(); z.DeliveryRate != "0.0000" || z.ComplaintRate != "0.0000" {
		t.Fatalf("sin envios: %+v", z)
	}
}

func TestRecipientFromContact(t *testing.T) {
	id := uuid.New()
	r, ok := RecipientFromContact(Contact{
		ID: id, Email: " ana@example.com ", FirstName: "Ana", LastName: "",
		Attributes: map[string]json.RawMessage{"first_name": json.RawMessage(`"Pisado"`), "puntos": json.RawMessage(`120`)},
	})
	if !ok || r.Email != "ana@example.com" || r.Name != "Ana" || r.ContactID == nil || *r.ContactID != id {
		t.Fatalf("destinatario: %+v", r)
	}
	if string(r.Variables["first_name"]) != `"Ana"` || string(r.Variables["puntos"]) != `120` || string(r.Variables["last_name"]) != `""` {
		t.Fatalf("variables: %v", r.Variables)
	}
	if _, ok := RecipientFromContact(Contact{ID: id}); ok {
		t.Fatal("un contacto sin direccion no es destinatario")
	}
	if _, ok := RecipientFromContact(Contact{Email: "a@example.com"}); ok {
		t.Fatal("un contacto sin id no es destinatario")
	}
	page := RecipientsFromContacts([]Contact{{ID: id, Email: "a@x.com"}, {ID: uuid.New(), Email: "A@x.com"}, {ID: uuid.New()}})
	if len(page) != 1 {
		t.Fatalf("repetidos y sin direccion fuera: %d", len(page))
	}
}

func TestBatchKeysAndLease(t *testing.T) {
	id := uuid.MustParse("7d0e6c1a-0b7e-4a4e-9b38-7a7a5b1c0001")
	if got := BatchIdempotencyKey(id, 3); got != "campaign:7d0e6c1a-0b7e-4a4e-9b38-7a7a5b1c0001:batch:3" {
		t.Fatalf("clave: %s", got)
	}
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	c := &Campaign{ID: id, TenantID: uuid.New()}
	first := NextBatch(c, nil, now)
	if first.Seq != 1 || first.CursorIn != nil || !first.Leased(now) || first.Leased(now.Add(BatchLease)) {
		t.Fatalf("primer lote: %+v", first)
	}
	cursor := "p2"
	prev := &Batch{Seq: 1, Status: BatchDelivered, CursorOut: &cursor}
	next := NextBatch(c, prev, now)
	if next.Seq != 2 || next.CursorIn == nil || *next.CursorIn != "p2" || next.IdempotencyKey() != BatchIdempotencyKey(id, 2) {
		t.Fatalf("siguiente lote: %+v", next)
	}
	if prev.Exhausted() || !(&Batch{Status: BatchDelivered}).Exhausted() {
		t.Fatal("Exhausted")
	}
	if BatchLease <= CallTimeout {
		t.Fatal("la reserva debe superar la duracion de la llamada")
	}
}

func TestParseDeliveryKind(t *testing.T) {
	if k, ok := ParseDeliveryKind("transactional.email.opened"); !ok || k != KindOpened || !k.UniquePerMessage() {
		t.Fatal("opened")
	}
	if k, ok := ParseDeliveryKind("transactional.email.delivered"); !ok || k.UniquePerMessage() {
		t.Fatal("delivered")
	}
	for _, s := range []string{"transactional.message.queued", "transactional.email.queued", "campaigns.campaign.started", ""} {
		if _, ok := ParseDeliveryKind(s); ok {
			t.Errorf("%q no alimenta contadores", s)
		}
	}
}

func TestTestContactIDMarksPreviewRecipients(t *testing.T) {
	campaign := uuid.New()
	id := TestContactID(campaign, " QA@example.com ")
	if id != TestContactID(campaign, "qa@example.com") {
		t.Fatal("el id sintetico no depende de mayusculas ni espacios")
	}
	if id == TestContactID(uuid.New(), "qa@example.com") || id == TestContactID(campaign, "dev@example.com") {
		t.Fatal("el id sintetico es propio de la campana y de la direccion")
	}
	if !IsTestContact(campaign, id, "qa@example.com") || IsTestContact(campaign, uuid.New(), "qa@example.com") || IsTestContact(campaign, id, "") {
		t.Fatal("IsTestContact")
	}
}

func TestNormalizeTestRecipients(t *testing.T) {
	got, err := NormalizeTestRecipients([]string{" qa@example.com", "QA@example.com", "dev@example.com"})
	if err != nil || len(got) != 2 {
		t.Fatalf("got=%v err=%v", got, err)
	}
	for _, in := range [][]string{nil, {"a@x.com", "b@x.com", "c@x.com", "d@x.com", "e@x.com", "f@x.com"}, {"no-valida"}} {
		if _, err := NormalizeTestRecipients(in); !errors.Is(err, ErrInvalidCampaign) {
			t.Errorf("%v: %v", in, err)
		}
	}
}

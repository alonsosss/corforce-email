package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/ports"
	"github.com/google/uuid"
)

// sent es lo que transactional acepto en total: direccion -> mensaje y contacto, con la
// peticion que lo llevo.
type sentMessage struct {
	messageID uuid.UUID
	contactID uuid.UUID
	call      ports.BatchRequest
}

func (h *harness) accepted() map[string][]sentMessage {
	out := map[string][]sentMessage{}
	seen := map[string]bool{}
	for _, call := range h.sender.calls {
		if seen[call.IdempotencyKey] {
			continue
		}
		seen[call.IdempotencyKey] = true
		res := h.sender.results[call.IdempotencyKey]
		if res == nil {
			continue
		}
		i := 0
		for _, r := range call.Recipients {
			if h.sender.suppress[r.Email] {
				continue
			}
			out[r.Email] = append(out[r.Email], sentMessage{messageID: res.MessageIDs[i], contactID: *r.ContactID, call: call})
			i++
		}
	}
	return out
}

// event simula un evento de transactional del mensaje.
func (h *harness) event(t *testing.T, campaignID uuid.UUID, m sentMessage, kind domain.DeliveryKind) {
	t.Helper()
	msg, contactID := m.messageID, m.contactID
	if _, err := h.uc.RecordDeliveryEvent(h.ctx, domain.DeliveryEvent{
		EventID: uuid.NewString(), TenantID: h.tenantID, CampaignID: campaignID,
		MessageID: &msg, ContactID: &contactID, Kind: kind, OccurredAt: h.clock.now(),
	}); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) audienceOf(pages ...[]domain.Contact) {
	h.audience.pages = map[string]*ports.AudiencePage{}
	for i, p := range pages {
		key := ""
		if i > 0 {
			key = fmt.Sprintf("p%d", i+1)
		}
		page := &ports.AudiencePage{Contacts: p}
		if i < len(pages)-1 {
			page.NextCursor = strPtr(fmt.Sprintf("p%d", i+2))
		}
		h.audience.pages[key] = page
	}
}

func (h *harness) tick(t *testing.T) {
	t.Helper()
	if err := h.uc.Tick(h.ctx, h.tenantID); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) phases(campaignID uuid.UUID) []domain.Phase {
	out, _ := fakePhases{s: h.store}.List(context.Background(), h.tenantID, campaignID)
	return out
}

func contactsNamed(prefix string, n int, zone string) []domain.Contact {
	out := make([]domain.Contact, n)
	for i := range out {
		out[i] = contact(fmt.Sprintf("%s%02d@example.com", prefix, i))
		out[i].Timezone = zone
	}
	return out
}

func (h *harness) abCampaign(t *testing.T, ab domain.ABTest, resend *domain.Resend) *domain.Campaign {
	t.Helper()
	c, err := h.uc.Create(h.ctx, h.tenantID, domain.NewCampaignInput{
		Name: "AB " + uuid.NewString(), TemplateID: uuid.New(), FromEmail: "news@shop.example.com",
		Audience: domain.Audience{ListIDs: []uuid.UUID{uuid.New()}}, CreatedBy: uuid.New(),
		ABTest: &ab, Resend: resend,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Prueba A/B completa: la muestra sale repartida por variante con su asunto, la campana
// espera la ventana, la ganadora sale con los contadores reales al resto y nadie recibe
// dos veces.
func TestABTestSampleWindowWinner(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	other := uuid.New()
	h.audienceOf(contactsNamed("a", 30, ""), contactsNamed("b", 30, ""))
	c := h.abCampaign(t, domain.ABTest{
		Criterion: domain.CriterionOpens, SamplePercent: 50, DecisionWindowMinutes: 120,
		Variants: []domain.ABVariant{{Subject: "Asunto A"}, {Subject: "Asunto B", TemplateID: &other}},
	}, nil)
	h.templates.version = 4
	started, err := h.uc.Start(ctx, h.tenantID, c.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v := started.ABTest.Variants; *v[0].PinnedVersion != 4 || *v[1].PinnedVersion != 4 || h.templates.calls != 2 {
		t.Fatalf("iniciar fija la version de cada variante (la B con su plantilla): %+v, %d consultas", v, h.templates.calls)
	}

	h.tick(t)
	h.tick(t)
	if len(h.sender.calls) != 2 {
		t.Fatalf("un lote por variante de la muestra: %d", len(h.sender.calls))
	}
	for i, call := range h.sender.calls {
		want := []string{"Asunto A", "Asunto B"}[i]
		if call.Subject != want || call.TemplateVersion != 4 {
			t.Fatalf("lote %d: asunto %q version %d", i, call.Subject, call.TemplateVersion)
		}
		if call.UTMContent != []string{"ab-a", "ab-b"}[i] || call.CampaignName != started.Name {
			t.Fatalf("lote %d: utm_content %q", i, call.UTMContent)
		}
		if (i == 1) != (call.TemplateID == other) {
			t.Fatalf("lote %d con la plantilla equivocada", i)
		}
		for _, r := range call.Recipients {
			if v, in := started.ABTest.Assign(c.ID, *r.ContactID); !in || v != i {
				t.Fatalf("%s no pertenece a la variante %d", r.Email, i)
			}
		}
	}
	sample := len(h.sender.calls[0].Recipients) + len(h.sender.calls[1].Recipients)
	if sample == 0 || sample == 60 {
		t.Fatalf("la muestra es una parte de la audiencia: %d", sample)
	}
	camp := h.campaign(c.ID)
	windowEnd := h.clock.now().Add(2 * time.Hour)
	if camp.Status != domain.StatusSending || camp.ResumeAfter == nil || !camp.ResumeAfter.Equal(windowEnd) || camp.ABWinner != nil {
		t.Fatalf("tras la muestra la campana espera la ventana: %+v", camp)
	}

	// Aperturas: la B abre mas aunque la A tenga mas entregas.
	for _, msgs := range h.accepted() {
		m := msgs[0]
		h.event(t, c.ID, m, domain.KindDelivered)
		if m.call.Subject == "Asunto B" {
			h.event(t, c.ID, m, domain.KindOpened)
		}
	}

	h.clock.advance(time.Hour)
	h.tick(t)
	if len(h.sender.calls) != 2 || h.published("campaigns.campaign.ab_decided") != nil {
		t.Fatal("antes de que venza la ventana no se decide ni se envia")
	}
	h.clock.advance(time.Hour)
	h.tick(t)
	camp = h.campaign(c.ID)
	if camp.ABWinner == nil || *camp.ABWinner != 1 || camp.ABDecision == nil || camp.ABDecision.Reason != domain.ReasonCriterion {
		t.Fatalf("gana la B por aperturas: %+v", camp.ABDecision)
	}
	ev := h.published("campaigns.campaign.ab_decided")
	if ev == nil || ev.payload["winner"] != "B" || ev.payload["criterion"] != "opens" || ev.payload["campaign_id"] != c.ID.String() {
		t.Fatalf("la decision queda en la outbox: %+v", ev)
	}
	if results, _ := ev.payload["results"].([]map[string]any); len(results) != 2 || results[1]["opened"].(int64) == 0 {
		t.Fatalf("con los contadores de la decision: %+v", ev.payload["results"])
	}
	if camp.Status != domain.StatusCompleted || h.published("campaigns.campaign.completed") == nil {
		t.Fatalf("la ganadora sale al resto y la campana termina: %s", camp.Status)
	}
	last := h.sender.calls[len(h.sender.calls)-1]
	if last.Subject != "Asunto B" || last.TemplateID != other || len(last.Recipients) != 60-sample {
		t.Fatalf("la ganadora va al resto con su contenido: %q %d destinatarios", last.Subject, len(last.Recipients))
	}
	got := h.accepted()
	if len(got) != 60 {
		t.Fatalf("cada contacto de la audiencia recibe la campana: %d", len(got))
	}
	for email, msgs := range got {
		if len(msgs) != 1 {
			t.Fatalf("%s recibio %d veces", email, len(msgs))
		}
	}
	for _, p := range h.phases(c.ID) {
		if p.Status != domain.PhaseDone {
			t.Fatalf("fase %s sin cerrar", p.Key)
		}
	}
}

// Un contacto que entra en la audiencia despues de la muestra, aunque su posicion sea de
// la muestra, recibe la ganadora; y una caida al cerrar el lote de la ganadora no duplica.
func TestABWinnerReachesLateContactsWithoutDuplicates(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	h.audienceOf(contactsNamed("a", 20, ""))
	c := h.abCampaign(t, domain.ABTest{
		Criterion: domain.CriterionClicks, SamplePercent: 50, DecisionWindowMinutes: 60,
		Variants: []domain.ABVariant{{Subject: "A"}, {Subject: "B"}, {Subject: "C"}},
	}, nil)
	if _, err := h.uc.Start(ctx, h.tenantID, c.ID, nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		h.tick(t)
	}
	camp := h.campaign(c.ID)
	var late domain.Contact
	for {
		late = contact(uuid.NewString()[:8] + "@example.com")
		if _, in := camp.ABTest.Assign(c.ID, late.ID); in {
			break
		}
	}
	h.audience.pages[""].Contacts = append(h.audience.pages[""].Contacts, late)

	h.clock.advance(time.Hour)
	h.store.failMarkDelivered = 1
	h.tick(t)
	if h.campaign(c.ID).Status != domain.StatusSending {
		t.Fatal("la caida deja la campana en envio")
	}
	created := h.sender.created
	h.clock.advance(domain.BatchLease)
	h.tick(t)
	if h.sender.created != created {
		t.Fatalf("el reintento con la misma clave no crea mensajes: %d -> %d", created, h.sender.created)
	}
	camp = h.campaign(c.ID)
	if camp.Status != domain.StatusCompleted || *camp.ABWinner != 0 || camp.ABDecision.Reason != domain.ReasonFirst {
		t.Fatalf("sin clics gana la A por empate: %s %+v", camp.Status, camp.ABDecision)
	}
	got := h.accepted()
	if len(got) != 21 || len(got[late.Email]) != 1 {
		t.Fatalf("el contacto nuevo recibe la ganadora una vez: %d %v", len(got), got[late.Email])
	}
	if n := len(h.store.events); n == 0 {
		t.Fatal("sin eventos")
	}
	decided := 0
	for _, e := range h.store.events {
		if e.subject == "campaigns.campaign.ab_decided" {
			decided++
		}
	}
	if decided != 1 {
		t.Fatalf("la decision se publica una vez: %d", decided)
	}
}

// Reenvio: espera su retraso tras la ronda inicial y va solo a quien se entrego y no
// abrio ni hizo clic, sigue en la audiencia (no se dio de baja) y no esta suprimido.
func TestResendToNonOpeners(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	people := contactsNamed("p", 7, "")
	h.audienceOf(people)
	c, err := h.uc.Create(ctx, h.tenantID, domain.NewCampaignInput{
		Name: "Reenvio", TemplateID: uuid.New(), FromEmail: "news@shop.example.com",
		Audience: domain.Audience{ListIDs: []uuid.UUID{uuid.New()}}, CreatedBy: uuid.New(),
		Resend: &domain.Resend{Subject: "Recordatorio", DelayMinutes: 48 * 60},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.uc.Start(ctx, h.tenantID, c.ID, nil); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	camp := h.campaign(c.ID)
	if camp.Status != domain.StatusSending || camp.ResumeAfter == nil || !camp.ResumeAfter.Equal(h.clock.now().Add(48*time.Hour)) {
		t.Fatalf("tras la ronda inicial la campana espera el reenvio: %+v", camp)
	}
	sent := h.accepted()
	msg := func(i int) sentMessage { return sent[people[i].Email][0] }
	h.event(t, c.ID, msg(0), domain.KindDelivered)
	h.event(t, c.ID, msg(0), domain.KindOpened)
	h.event(t, c.ID, msg(1), domain.KindDelivered)
	h.event(t, c.ID, msg(2), domain.KindDelivered)
	h.event(t, c.ID, msg(2), domain.KindClicked)
	h.event(t, c.ID, msg(3), domain.KindBounced)
	h.event(t, c.ID, msg(4), domain.KindDelivered)
	h.event(t, c.ID, msg(5), domain.KindDelivered)
	h.event(t, c.ID, msg(6), domain.KindDelivered)
	// La 4 se da de baja (contacts ya no la devuelve) y la 5 queda suprimida.
	h.audience.pages[""].Contacts = append(append([]domain.Contact{}, people[:4]...), people[5:]...)
	h.sender.suppress[people[5].Email] = true

	h.clock.advance(47 * time.Hour)
	h.tick(t)
	if len(h.sender.calls) != 1 {
		t.Fatal("el reenvio espera su retraso")
	}
	h.clock.advance(time.Hour)
	h.tick(t)
	if len(h.sender.calls) != 2 {
		t.Fatalf("un lote de reenvio: %d llamadas", len(h.sender.calls))
	}
	resend := h.sender.calls[1]
	var emails []string
	for _, r := range resend.Recipients {
		emails = append(emails, r.Email)
	}
	want := []string{people[1].Email, people[5].Email, people[6].Email}
	if resend.UTMContent != domain.UTMContentResend {
		t.Fatalf("utm_content del reenvio: %q", resend.UTMContent)
	}
	if resend.Subject != "Recordatorio" || strings.Join(emails, ",") != strings.Join(want, ",") {
		t.Fatalf("reenvio a %v con %q, quiero %v", emails, resend.Subject, want)
	}
	camp = h.campaign(c.ID)
	if camp.Status != domain.StatusCompleted {
		t.Fatalf("el reenvio cierra la campana: %s", camp.Status)
	}
	if camp.Counters.Suppressed != 1 {
		t.Fatalf("la supresion del reenvio se cuenta: %+v", camp.Counters)
	}
	h.clock.advance(7 * 24 * time.Hour)
	h.tick(t)
	if len(h.sender.calls) != 2 {
		t.Fatal("el reenvio sale una sola vez")
	}
	kinds := map[domain.PhaseKind]int{}
	for _, p := range h.phases(c.ID) {
		kinds[p.Kind]++
	}
	if kinds[domain.PhaseMain] != 1 || kinds[domain.PhaseResend] != 1 {
		t.Fatalf("fases: %v", kinds)
	}
}

// Pausar mientras se espera el reenvio y reanudar despues no lo adelanta.
func TestResendWaitSurvivesPause(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	h.audienceOf(contactsNamed("p", 2, ""))
	c, err := h.uc.Create(ctx, h.tenantID, domain.NewCampaignInput{
		Name: "Reenvio pausa", TemplateID: uuid.New(), FromEmail: "news@shop.example.com",
		Audience: domain.Audience{ListIDs: []uuid.UUID{uuid.New()}}, CreatedBy: uuid.New(),
		Resend: &domain.Resend{Subject: "Otra vez", DelayMinutes: 24 * 60},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.uc.Start(ctx, h.tenantID, c.ID, nil); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	if _, err := h.uc.Pause(ctx, h.tenantID, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.uc.Resume(ctx, h.tenantID, c.ID); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	camp := h.campaign(c.ID)
	if camp.Status != domain.StatusSending || camp.ResumeAfter == nil || len(h.sender.calls) != 1 {
		t.Fatalf("al reanudar vuelve a esperar el reenvio: %+v", camp)
	}
}

// Envio por zona horaria: el primer tramo no envia a nadie y da de alta un tramo por
// instante; cada contacto recibe a su hora local, con la zona de respaldo para quien no
// tiene una valida.
func TestTimezoneDeliverySlots(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	madrid := contactsNamed("madrid", 2, "Europe/Madrid")
	lima := contactsNamed("lima", 1, "America/Lima")
	sinZona := contactsNamed("sinzona", 1, "")
	invalida := contactsNamed("invalida", 1, "Mars/Olympus")
	h.audienceOf(append(append(append(append([]domain.Contact{}, lima...), madrid...), sinZona...), invalida...))
	var sendTimes []time.Time
	h.sender.hook = func(ports.BatchRequest) { sendTimes = append(sendTimes, h.clock.now()) }

	c := h.draft("Zona")
	if _, err := h.uc.ScheduleLocal(ctx, h.tenantID, c.ID, "2026-09-02T09:00", "America/New_York", nil); err != nil {
		t.Fatal(err)
	}
	if got := h.campaign(c.ID).ScheduledAt; !got.Equal(time.Date(2026, 9, 1, 19, 0, 0, 0, time.UTC)) {
		t.Fatalf("arranca cuando la primera zona del mundo marca las 09:00: %s", got)
	}
	h.tick(t)
	if h.campaign(c.ID).Status != domain.StatusScheduled {
		t.Fatal("antes de su hora sigue programada")
	}
	h.clock.t = time.Date(2026, 9, 1, 19, 0, 0, 0, time.UTC)
	h.tick(t)
	if len(h.sender.calls) != 0 {
		t.Fatal("a las 19:00 UTC nadie alcanzo las 09:00")
	}
	slots := h.phases(c.ID)
	if len(slots) != 4 {
		t.Fatalf("el primer tramo y uno por instante distinto: %d", len(slots))
	}
	madridAt := time.Date(2026, 9, 2, 7, 0, 0, 0, time.UTC)
	if ra := h.campaign(c.ID).ResumeAfter; ra == nil || !ra.Equal(madridAt) {
		t.Fatalf("la campana espera al primer tramo: %v", ra)
	}

	for _, at := range []time.Time{madridAt, time.Date(2026, 9, 2, 13, 0, 0, 0, time.UTC), time.Date(2026, 9, 2, 14, 0, 0, 0, time.UTC)} {
		h.clock.t = at.Add(-time.Minute)
		h.tick(t)
		before := len(h.sender.calls)
		h.clock.t = at
		h.tick(t)
		if len(h.sender.calls) != before+1 {
			t.Fatalf("un lote a las %s", at)
		}
	}
	want := [][]string{
		{madrid[0].Email, madrid[1].Email},
		{sinZona[0].Email, invalida[0].Email},
		{lima[0].Email},
	}
	for i, call := range h.sender.calls {
		var got []string
		for _, r := range call.Recipients {
			got = append(got, r.Email)
		}
		if strings.Join(got, ",") != strings.Join(want[i], ",") {
			t.Fatalf("tramo %d envia a %v, quiero %v", i, got, want[i])
		}
		if call.Subject != "" {
			t.Fatal("el envio por zona usa el asunto de la plantilla")
		}
	}
	if !sendTimes[0].Equal(madridAt) {
		t.Fatalf("Madrid sale a las 07:00 UTC: %s", sendTimes[0])
	}
	if h.campaign(c.ID).Status != domain.StatusCompleted {
		t.Fatalf("el ultimo tramo cierra la campana: %s", h.campaign(c.ID).Status)
	}
}

// Una pagina sin nadie de la fase no consume una pasada entera: la misma pasada sigue con
// la pagina siguiente.
func TestEmptyPagesDoNotStallTheTick(t *testing.T) {
	h := newHarness()
	h.uc = New(Deps{
		Campaigns: fakeCampaigns{s: h.store, now: h.clock.now}, Batches: fakeBatches{s: h.store, now: h.clock.now},
		Phases: fakePhases{s: h.store, now: h.clock.now}, Ledger: fakeLedger{s: h.store}, Stats: fakeStats{s: h.store},
		Tx: h.store, Events: fakeEvents{s: h.store}, Audience: h.audience, Sender: h.sender, Templates: h.templates,
		Config: Config{BatchSize: 1}, Now: h.clock.now,
	})
	var pages [][]domain.Contact
	for i := 0; i < 6; i++ {
		pages = append(pages, []domain.Contact{{ID: uuid.New()}})
	}
	pages = append(pages, contactsNamed("ultimo", 1, ""))
	h.audienceOf(pages...)
	c := h.sending("Paginas vacias")
	h.tick(t)
	if len(h.sender.calls) != 1 || h.campaign(c.ID).Status != domain.StatusCompleted {
		t.Fatalf("una sola pasada recorre las paginas vacias y entrega la ultima: %d llamadas, %s",
			len(h.sender.calls), h.campaign(c.ID).Status)
	}
}

// Las fases que filtran piden a contacts solo lo que cabe en el lote y llenan el lote con
// varias paginas.
func TestFilteredPhaseFillsTheBatchAcrossPages(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	var pages [][]domain.Contact
	for i := 0; i < 5; i++ {
		pages = append(pages, contactsNamed(fmt.Sprintf("pg%d-", i), 10, ""))
	}
	h.audienceOf(pages...)
	c := h.abCampaign(t, domain.ABTest{
		Criterion: domain.CriterionOpens, SamplePercent: 20, DecisionWindowMinutes: 60,
		Variants: []domain.ABVariant{{Subject: "A"}, {Subject: "B"}},
	}, nil)
	if _, err := h.uc.Start(ctx, h.tenantID, c.ID, nil); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	if len(h.audience.calls) != 5 || len(h.sender.calls) > 1 {
		t.Fatalf("la muestra A lee las cinco paginas para un solo lote: %d paginas, %d lotes", len(h.audience.calls), len(h.sender.calls))
	}
	for i, q := range h.audience.calls {
		if q.Limit < 1 || q.Limit > domain.MaxBatchSize {
			t.Fatalf("consulta %d con limite %d", i, q.Limit)
		}
	}
}

func TestSendTestVariant(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	other := uuid.New()
	seven := 7
	c := h.abCampaign(t, domain.ABTest{
		Criterion: domain.CriterionOpens, SamplePercent: 20, DecisionWindowMinutes: 60,
		Variants: []domain.ABVariant{{Subject: "A"}, {Subject: "B", TemplateID: &other, TemplateVersion: &seven}},
	}, nil)
	h.templates.version = 3
	if _, err := h.uc.SendTest(ctx, h.tenantID, c.ID, TestInput{Emails: []string{"qa@example.com"}, Variant: intPtr(1)}); err != nil {
		t.Fatal(err)
	}
	call := h.sender.calls[0]
	if call.TemplateID != other || call.TemplateVersion != 7 || call.Subject != "B" || call.Tags["test"] != "true" {
		t.Fatalf("la prueba de la variante B usa su plantilla, version y asunto: %+v", call)
	}
	if _, err := h.uc.SendTest(ctx, h.tenantID, c.ID, TestInput{Emails: []string{"qa@example.com"}, Variant: intPtr(0)}); err != nil {
		t.Fatal(err)
	}
	if call := h.sender.calls[1]; call.TemplateID != c.TemplateID || call.TemplateVersion != 3 || call.Subject != "A" {
		t.Fatalf("la A usa la plantilla de la campana y la version publicada: %+v", call)
	}
	if _, err := h.uc.SendTest(ctx, h.tenantID, c.ID, TestInput{Emails: []string{"qa@example.com"}, Variant: intPtr(2)}); err == nil {
		t.Fatal("una variante inexistente no se prueba")
	}
	plain := h.draft("Sin AB")
	if _, err := h.uc.SendTest(ctx, h.tenantID, plain.ID, TestInput{Emails: []string{"qa@example.com"}, Variant: intPtr(0)}); err == nil {
		t.Fatal("sin prueba A/B no hay variantes")
	}
}

func TestScheduleLocalRejectsABAndBadInput(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	c := h.abCampaign(t, domain.ABTest{
		Criterion: domain.CriterionOpens, SamplePercent: 20, DecisionWindowMinutes: 60,
		Variants: []domain.ABVariant{{Subject: "A"}, {Subject: "B"}},
	}, nil)
	if _, err := h.uc.ScheduleLocal(ctx, h.tenantID, c.ID, "2026-09-10T09:00", "UTC", nil); err != domain.ErrABWithTimezone {
		t.Fatalf("A/B por zona: %v", err)
	}
	plain := h.draft("Zona mala")
	if _, err := h.uc.ScheduleLocal(ctx, h.tenantID, plain.ID, "manana", "UTC", nil); err == nil {
		t.Fatal("hora local ilegible")
	}
	if h.templates.calls != 0 {
		t.Fatal("un rechazo de entrada no consulta templates")
	}
}

func TestPlanView(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	h.audienceOf(contactsNamed("a", 10, ""))
	c := h.abCampaign(t, domain.ABTest{
		Criterion: domain.CriterionOpens, SamplePercent: 50, DecisionWindowMinutes: 60,
		Variants: []domain.ABVariant{{Subject: "A"}, {Subject: "B"}},
	}, nil)
	if _, err := h.uc.Start(ctx, h.tenantID, c.ID, nil); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	h.tick(t)
	_, plan, err := h.uc.Plan(ctx, h.tenantID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Phases) != 3 || plan.Phases[0].Status != domain.PhaseDone || plan.Phases[2].NotBefore == nil {
		t.Fatalf("plan: %+v", plan.Phases)
	}
	var accepted int64
	for _, e := range plan.Engagement {
		if e.Kind != domain.PhaseSample {
			t.Fatalf("solo hay mensajes de la muestra: %+v", e)
		}
		accepted += e.Accepted
	}
	if int(accepted) != plan.Phases[0].Accepted+plan.Phases[1].Accepted {
		t.Fatalf("los mensajes por variante cuadran con los totales de las fases: %d", accepted)
	}
	if _, _, err := h.uc.Plan(ctx, h.tenantID, uuid.New()); err != domain.ErrCampaignNotFound {
		t.Fatalf("campana inexistente: %v", err)
	}
}

func intPtr(v int) *int { return &v }

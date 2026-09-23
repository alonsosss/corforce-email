//go:build integration

package app

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func countRows(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func TestIntegrationPhaseObjects(t *testing.T) {
	ctx, pool, h, _ := setupDB(t)
	for _, rel := range []string{"campaigns.phases", "campaigns.recipients"} {
		var name *string
		if err := pool.QueryRow(context.Background(), `SELECT to_regclass($1)::text`, rel).Scan(&name); err != nil || name == nil {
			t.Errorf("%s no existe: %v", rel, err)
		}
	}
	c, err := h.uc.Create(ctx, h.tenantID, domain.NewCampaignInput{
		Name: "Restricciones " + uuid.NewString(), TemplateID: uuid.New(), FromEmail: "news@shop.example.com",
		Audience: domain.Audience{ListIDs: []uuid.UUID{uuid.New()}}, CreatedBy: uuid.New(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, sql := range map[string]string{
		"A/B con zona horaria": `UPDATE campaigns.campaigns SET ab_test = '{}', local_send_at = now(), fallback_timezone = 'UTC' WHERE id = $1`,
		"hora sin zona":        `UPDATE campaigns.campaigns SET local_send_at = now() WHERE id = $1`,
		"ganadora sin prueba":  `UPDATE campaigns.campaigns SET ab_winner = 0, ab_decided_at = now(), ab_decision = '{}' WHERE id = $1`,
	} {
		if _, err := pool.Exec(context.Background(), sql, c.ID); err == nil {
			t.Errorf("%s: la base debe rechazarlo", name)
		}
	}
}

// Una base con lotes anteriores a las fases: la migracion les da su fase main, con sus
// totales, y el orquestador continua la audiencia donde iba.
func TestIntegrationLegacyBatchesJoinMainPhase(t *testing.T) {
	ctx, pool, h, _ := setupDB(t)
	c := createSending(t, ctx, h, "Heredada "+uuid.NewString())
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO campaigns.batches (tenant_id, campaign_id, seq, cursor_out, recipients, status, accepted)
		 VALUES ($1, $2, 1, 'p2', 2, 'delivered', 2)`, h.tenantID, c.ID); err != nil {
		t.Fatal(err)
	}
	_, file, _, _ := runtime.Caller(0)
	sql, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "migrations/tenant/canonical/campaigns/03_phases.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("reaplicar la migracion: %v", err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM campaigns.phases WHERE campaign_id = $1 AND key = 'main' AND targeted = 2 AND accepted = 2`, c.ID); n != 1 {
		t.Fatalf("fase main sembrada con los totales: %d", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM campaigns.batches WHERE campaign_id = $1 AND phase_id IS NULL`, c.ID); n != 0 {
		t.Fatalf("lotes sin fase: %d", n)
	}
	h.audienceOf(contactsNamed("a", 2, ""), contactsNamed("b", 1, ""))
	if out, err := h.uc.ProcessBatch(ctx, h.tenantID, c.ID); err != nil || out != OutcomeCompleted {
		t.Fatalf("continua con el cursor del lote heredado: %s %v", out, err)
	}
	if q := h.audience.calls[0]; q.Cursor == nil || *q.Cursor != "p2" {
		t.Fatalf("pide la pagina siguiente: %+v", q)
	}
}

func TestIntegrationABTestFlow(t *testing.T) {
	ctx, pool, h, _ := setupDB(t)
	h.audienceOf(contactsNamed("a", 25, ""), contactsNamed("b", 25, ""))
	c := h.abCampaign(t, domain.ABTest{
		Criterion: domain.CriterionClicks, SamplePercent: 40, DecisionWindowMinutes: 60,
		Variants: []domain.ABVariant{{Subject: "Variante A"}, {Subject: "Variante B"}},
	}, nil)
	if _, err := h.uc.Start(ctx, h.tenantID, c.ID, nil); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	h.tick(t)
	got, err := h.uc.Get(ctx, h.tenantID, c.ID)
	if err != nil || got.ResumeAfter == nil || got.ABTest.Variants[1].PinnedVersion == nil {
		t.Fatalf("tras la muestra espera la ventana con las versiones fijadas: %+v %v", got, err)
	}
	for _, msgs := range h.accepted() {
		h.event(t, c.ID, msgs[0], domain.KindDelivered)
		if msgs[0].call.Subject == "Variante A" {
			h.event(t, c.ID, msgs[0], domain.KindClicked)
		}
	}
	_, plan, err := h.uc.Plan(ctx, h.tenantID, c.ID)
	if err != nil || len(plan.Engagement) != 2 {
		t.Fatalf("interaccion por variante: %+v %v", plan, err)
	}
	for _, e := range plan.Engagement {
		if e.Delivered != e.Accepted || (*e.Variant == 0) != (e.Clicked > 0) {
			t.Fatalf("contadores por variante: %+v", e)
		}
	}

	h.clock.advance(time.Hour)
	h.tick(t)
	got, err = h.uc.Get(ctx, h.tenantID, c.ID)
	if err != nil || got.Status != domain.StatusCompleted || got.ABWinner == nil || *got.ABWinner != 0 ||
		got.ABDecision == nil || got.ABDecision.Criterion != domain.CriterionClicks || len(got.ABDecision.Results) != 2 {
		t.Fatalf("gana la A por clics y la decision queda guardada: %+v %v", got, err)
	}
	if n := outboxCount(t, pool, "campaigns.campaign.ab_decided", c.ID); n != 1 {
		t.Fatalf("decision en la outbox: %d", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM campaigns.recipients WHERE campaign_id = $1 AND round = 'initial'`, c.ID); n != 50 {
		t.Fatalf("cada contacto consta una vez en la ronda inicial: %d", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM campaigns.message_engagement WHERE campaign_id = $1 AND phase_kind = 'winner'`, c.ID); n == 0 {
		t.Fatal("los mensajes de la ganadora quedan con su fase")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM campaigns.phases WHERE campaign_id = $1 AND status <> 'done'`, c.ID); n != 0 {
		t.Fatalf("fases abiertas: %d", n)
	}
	for email, msgs := range h.accepted() {
		if len(msgs) != 1 {
			t.Fatalf("%s recibio %d veces", email, len(msgs))
		}
	}
}

func TestIntegrationResendEligibility(t *testing.T) {
	ctx, pool, h, _ := setupDB(t)
	people := contactsNamed("r", 5, "")
	h.audienceOf(people)
	c, err := h.uc.Create(ctx, h.tenantID, domain.NewCampaignInput{
		Name: "Reenvio " + uuid.NewString(), TemplateID: uuid.New(), FromEmail: "news@shop.example.com",
		Audience: domain.Audience{ListIDs: []uuid.UUID{uuid.New()}}, CreatedBy: uuid.New(),
		Resend: &domain.Resend{Subject: "Recordatorio", DelayMinutes: 24 * 60},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.uc.Start(ctx, h.tenantID, c.ID, nil); err != nil {
		t.Fatal(err)
	}
	h.tick(t)
	sent := h.accepted()
	m := func(i int) sentMessage { return sent[people[i].Email][0] }
	h.event(t, c.ID, m(0), domain.KindDelivered)
	h.event(t, c.ID, m(0), domain.KindOpened)
	h.event(t, c.ID, m(1), domain.KindDelivered)
	h.event(t, c.ID, m(2), domain.KindClicked)
	h.event(t, c.ID, m(2), domain.KindDelivered)
	h.event(t, c.ID, m(4), domain.KindDelivered)
	h.event(t, c.ID, m(4), domain.KindDelivered)

	h.clock.advance(24 * time.Hour)
	h.tick(t)
	var emails []string
	for _, r := range h.sender.calls[len(h.sender.calls)-1].Recipients {
		emails = append(emails, r.Email)
	}
	if want := people[1].Email + "," + people[4].Email; strings.Join(emails, ",") != want {
		t.Fatalf("reenvio a %v, quiero %s", emails, want)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM campaigns.recipients WHERE campaign_id = $1 AND round = 'resend'`, c.ID); n != 2 {
		t.Fatalf("el reenvio queda anotado: %d", n)
	}
	got, _ := h.uc.Get(ctx, h.tenantID, c.ID)
	if got.Status != domain.StatusCompleted {
		t.Fatalf("estado final: %s", got.Status)
	}
	eligible, err := h.uc.ledger.ResendEligible(ctx, h.tenantID, c.ID, []uuid.UUID{people[1].ID, people[4].ID})
	if err != nil || len(eligible) != 0 {
		t.Fatalf("tras el reenvio nadie vuelve a cumplir: %v %v", eligible, err)
	}
}

func TestIntegrationTimezoneSlots(t *testing.T) {
	ctx, pool, h, _ := setupDB(t)
	madrid := contactsNamed("tz-madrid", 1, "Europe/Madrid")
	lima := contactsNamed("tz-lima", 1, "America/Lima")
	h.audienceOf(append(madrid, lima...))
	c := createDraft(t, ctx, h, "Zona "+uuid.NewString())
	day := h.clock.now().Add(72 * time.Hour).UTC()
	local := time.Date(day.Year(), day.Month(), day.Day(), 9, 0, 0, 0, time.UTC).Format(domain.LocalDateTimeLayout)
	if _, err := h.uc.ScheduleLocal(ctx, h.tenantID, c.ID, local, "America/Lima", nil); err != nil {
		t.Fatal(err)
	}
	got, _ := h.uc.Get(ctx, h.tenantID, c.ID)
	if got.TimezoneDelivery == nil || got.TimezoneDelivery.LocalSendAt.String() != local || got.TimezoneDelivery.FallbackTimezone != "America/Lima" {
		t.Fatalf("la hora local se guarda sin zona: %+v", got.TimezoneDelivery)
	}
	h.clock.t = *got.ScheduledAt
	h.tick(t)
	if n := countRows(t, pool, `SELECT count(*) FROM campaigns.phases WHERE campaign_id = $1 AND kind = 'zone'`, c.ID); n != 4 {
		t.Fatalf("primer tramo, uno por zona y el de cierre: %d", n)
	}
	l, _ := domain.ParseLocalDateTime(local)
	madridAt := l.In(mustLoad(t, "Europe/Madrid"))
	if err := h.uc.registerSlots(ctx, got, []time.Time{madridAt}); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM campaigns.phases WHERE campaign_id = $1 AND kind = 'zone'`, c.ID); n != 4 {
		t.Fatalf("dar de alta un tramo existente no lo duplica: %d", n)
	}
	h.clock.t = madridAt
	h.tick(t)
	h.clock.t = l.In(mustLoad(t, "America/Lima"))
	h.tick(t)
	h.clock.t = l.Latest()
	h.tick(t)
	got, _ = h.uc.Get(ctx, h.tenantID, c.ID)
	if got.Status != domain.StatusCompleted || len(h.sender.calls) != 2 {
		t.Fatalf("un lote por tramo y la campana termina en el tramo de cierre: %s %d", got.Status, len(h.sender.calls))
	}
	if h.sender.calls[0].Recipients[0].Email != madrid[0].Email || h.sender.calls[1].Recipients[0].Email != lima[0].Email {
		t.Fatal("cada contacto en su tramo")
	}
}

func createDraft(t *testing.T, ctx context.Context, h *harness, name string) *domain.Campaign {
	t.Helper()
	c, err := h.uc.Create(ctx, h.tenantID, domain.NewCampaignInput{
		Name: name, TemplateID: uuid.New(), FromEmail: "news@shop.example.com",
		Audience: domain.Audience{ListIDs: []uuid.UUID{uuid.New()}}, CreatedBy: uuid.New(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := domain.LoadTimezone(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

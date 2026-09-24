//go:build integration

package app

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/alonsosss/corforce-email/services/automations/internal/ports"
	"github.com/google/uuid"
)

// Un flujo guardado antes de las ramas (pasos sin ids) se lee como la cadena s1 -> s2.
func TestIntegracionFlujoAnteriorSeLeeComoCadena(t *testing.T) {
	d := setupDB(t, Config{})
	id := uuid.New()
	list := uuid.New()
	if _, err := d.pool.Exec(d.ctx,
		`INSERT INTO automations.workflows (id, tenant_id, name, status, trigger_type, steps, created_by)
		 VALUES ($1, $2, $3, 'draft', 'contact.created', $4::jsonb, $5)`,
		id, d.tenant, "Anterior "+id.String()[:8],
		`[{"type":"wait","duration":"1d"},{"type":"add_to_list","list_id":"`+list.String()+`"}]`, d.user); err != nil {
		t.Fatal(err)
	}
	w, err := d.uc.GetWorkflow(d.ctx, d.tenant, id)
	if err != nil {
		t.Fatal(err)
	}
	if w.Steps[0].ID != "s1" || w.Steps[0].Next != "s2" || w.Steps[1].ID != "s2" || w.Steps[1].Next != "" {
		t.Fatalf("pasos: %+v", w.Steps)
	}
}

func TestIntegracionRamaPorAperturaYDisparadorPorFecha(t *testing.T) {
	d := setupDB(t, Config{DateScanInterval: time.Minute})
	yes, no := uuid.New(), uuid.New()
	w := d.activate(t, false,
		sendAs("e", "rama"), branchOn(domain.Condition{Kind: domain.ConditionEmailOpened, Step: "e"}, "si", "no"),
		listAs("si", yes), listAs("no", no))
	c := d.sendable("ana@example.com")
	if _, err := d.uc.HandleTrigger(d.ctx, domain.TriggerEvent{EventID: uuid.NewString(), TenantID: d.tenant, Type: domain.TriggerContactCreated, ContactID: c.ID}); err != nil {
		t.Fatal(err)
	}
	if err := d.uc.Tick(d.ctx, d.tenant); err != nil {
		t.Fatal(err)
	}
	var messageID uuid.UUID
	if err := d.pool.QueryRow(d.ctx, `SELECT message_id FROM automations.run_messages WHERE workflow_id = $1`, w.ID).Scan(&messageID); err != nil {
		t.Fatalf("el correo del paso queda registrado: %v", err)
	}
	for i := 0; i < 2; i++ {
		if ok, err := d.uc.RecordMessageEngagement(d.ctx, d.tenant, messageID, true, d.clock.Now()); err != nil || !ok {
			t.Fatalf("clic: %v %v", ok, err)
		}
	}
	if ok, _ := d.uc.RecordMessageEngagement(d.ctx, d.tenant, uuid.New(), false, d.clock.Now()); ok {
		t.Fatal("un mensaje ajeno no se anota")
	}
	for i := 0; i < 2; i++ {
		if err := d.uc.Tick(d.ctx, d.tenant); err != nil {
			t.Fatal(err)
		}
	}
	if !d.contacts.IsMember(yes, c.ID) || d.contacts.IsMember(no, c.ID) {
		t.Fatal("un clic cuenta como apertura y sigue por then")
	}

	// Disparador por fecha: columnas propias y entrada unica por ano entre replicas.
	hour := 9
	dw, err := d.uc.CreateWorkflow(d.ctx, d.tenant, domain.NewWorkflowInput{
		Name: "Cumple " + uuid.NewString()[:8], CreatedBy: d.user, ReEntry: true,
		Trigger: domain.Trigger{Type: domain.TriggerContactDate, Attribute: "cumple", Hour: &hour, Timezone: "America/Lima"},
		Steps:   []domain.Step{listAs("regalo", uuid.New())},
	})
	if err != nil {
		t.Fatal(err)
	}
	if dw, err = d.uc.ActivateWorkflow(d.ctx, d.tenant, dw.ID); err != nil {
		t.Fatal(err)
	}
	got, err := d.uc.GetWorkflow(d.ctx, d.tenant, dw.ID)
	if err != nil || got.Trigger.Attribute != "cumple" || got.Trigger.Hour == nil || *got.Trigger.Hour != 9 || got.Trigger.Timezone != "America/Lima" {
		t.Fatalf("disparador guardado: %+v %v", got, err)
	}
	a := uuid.New()
	d.rules.Pages = []ports.AnniversaryPage{{Matches: []ports.AnniversaryMatch{{ContactID: a, Occurrence: "2026-09-23"}}}}

	var wg sync.WaitGroup
	var entered atomic.Int32
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := d.uc.ScanDateTriggers(d.ctx, d.tenant)
			if err != nil {
				t.Error(err)
			}
			entered.Add(int32(n))
		}()
	}
	wg.Wait()
	if entered.Load() != 1 {
		t.Fatalf("cuatro replicas a la vez, una entrada: %d", entered.Load())
	}
	d.clock.Advance(2 * time.Minute)
	if n, err := d.uc.ScanDateTriggers(d.ctx, d.tenant); err != nil || n != 0 {
		t.Fatalf("un recorrido posterior el mismo dia no duplica: %d %v", n, err)
	}
	var runs int
	if err := d.pool.QueryRow(d.ctx, `SELECT count(*) FROM automations.runs WHERE workflow_id = $1 AND entry_key = 'date:2026'`, dw.ID).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("ejecuciones del ano: %d %v", runs, err)
	}

	// El CHECK de la base rechaza un disparador por fecha a medias.
	if _, err := d.pool.Exec(d.ctx,
		`INSERT INTO automations.workflows (id, tenant_id, name, status, trigger_type, steps, created_by)
		 VALUES ($1, $2, 'x-'||$1::text, 'draft', 'contact.date', '[{"type":"wait","duration":"1d"}]'::jsonb, $3)`,
		uuid.New(), d.tenant, d.user); err == nil {
		t.Fatal("contact.date sin atributo, hora ni zona")
	}
}

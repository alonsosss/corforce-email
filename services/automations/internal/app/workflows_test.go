package app

import (
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/alonsosss/corforce-email/services/automations/internal/ports"
	"github.com/google/uuid"
)

func (f *fixture) draft(t *testing.T, name string, steps ...domain.Step) *domain.Workflow {
	t.Helper()
	w, err := f.uc.CreateWorkflow(ctx, f.tenant, domain.NewWorkflowInput{
		Name: name, Trigger: contactCreated, Steps: steps, CreatedBy: f.user,
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestCrearFlujoEnBorradorConNombreUnico(t *testing.T) {
	f := newFixture(t, Config{})
	w := f.draft(t, "Bienvenida", domain.Step{Type: domain.StepWait, Duration: "1d"}, sendEmail())
	if w.Status != domain.StatusDraft {
		t.Fatalf("estado: %s", w.Status)
	}
	if _, err := f.uc.CreateWorkflow(ctx, f.tenant, domain.NewWorkflowInput{
		Name: "BIENVENIDA", Trigger: contactCreated, Steps: []domain.Step{sendEmail()}, CreatedBy: f.user,
	}); !errors.Is(err, domain.ErrNameTaken) {
		t.Fatalf("nombre repetido sin distinguir mayusculas: %v", err)
	}
	if _, err := f.uc.CreateWorkflow(ctx, uuid.New(), domain.NewWorkflowInput{
		Name: "Bienvenida", Trigger: contactCreated, Steps: []domain.Step{sendEmail()}, CreatedBy: f.user,
	}); err != nil {
		t.Fatalf("otra empresa puede usar el nombre: %v", err)
	}
}

func TestActivarFijaLaVersionYExigeMarketing(t *testing.T) {
	f := newFixture(t, Config{})
	w := f.draft(t, "A", domain.Step{Type: domain.StepWait, Duration: "1h"}, sendEmail())

	f.templates.Kind = KindTransactional
	if _, err := f.uc.ActivateWorkflow(ctx, f.tenant, w.ID); !errors.Is(err, domain.ErrTemplateNotMarketing) {
		t.Fatalf("una plantilla transaccional no sirve: %v", err)
	}
	f.templates.Kind = KindMarketing
	f.templates.Err = domain.ErrTemplateVariables
	if _, err := f.uc.ActivateWorkflow(ctx, f.tenant, w.ID); !errors.Is(err, domain.ErrTemplateVersionRequired) {
		t.Fatalf("sin poder renderizar hay que indicar la version: %v", err)
	}
	f.templates.Err = ports.ErrUnavailable
	if _, err := f.uc.ActivateWorkflow(ctx, f.tenant, w.ID); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatalf("templates caido: %v", err)
	}
	if got := f.store.Workflow(w.ID); got.Status != domain.StatusDraft {
		t.Fatal("un intento fallido no cambia el flujo")
	}
	f.templates.Err = nil
	active, err := f.uc.ActivateWorkflow(ctx, f.tenant, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active.Status != domain.StatusActive || active.Steps[1].TemplateVersion == nil || *active.Steps[1].TemplateVersion != 3 {
		t.Fatalf("activo con la version publicada fijada: %+v", active.Steps[1])
	}
	if n := len(f.store.Published("automations.workflow.activated")); n != 1 {
		t.Fatalf("evento de activacion: %d", n)
	}
	if _, err := f.uc.ActivateWorkflow(ctx, f.tenant, w.ID); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("activar un activo: %v", err)
	}
}

func TestActivarConVersionIndicadaAunqueLaPlantillaExijaVariables(t *testing.T) {
	f := newFixture(t, Config{})
	step := sendEmail()
	step.TemplateVersion = ptr(7)
	w := f.draft(t, "B", step)
	f.templates.Err = domain.ErrTemplateVariables
	active, err := f.uc.ActivateWorkflow(ctx, f.tenant, w.ID)
	if err != nil || *active.Steps[0].TemplateVersion != 7 {
		t.Fatalf("la version indicada se respeta: %v", err)
	}
}

func TestSoloSeEditaEnBorradorOPausaYSoloSeBorraEnBorradorOArchivado(t *testing.T) {
	f := newFixture(t, Config{})
	w := f.draft(t, "C", sendEmail())
	name := "C2"
	if _, err := f.uc.UpdateWorkflow(ctx, f.tenant, w.ID, domain.Patch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.ActivateWorkflow(ctx, f.tenant, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.UpdateWorkflow(ctx, f.tenant, w.ID, domain.Patch{Name: &name}); !errors.Is(err, domain.ErrNotEditable) {
		t.Fatalf("activo: %v", err)
	}
	if err := f.uc.DeleteWorkflow(ctx, f.tenant, w.ID); !errors.Is(err, domain.ErrNotDeletable) {
		t.Fatalf("borrar activo: %v", err)
	}
	if _, err := f.uc.PauseWorkflow(ctx, f.tenant, w.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.store.Workflow(w.ID); got.PauseReason != "manual" {
		t.Fatalf("motivo por defecto: %q", got.PauseReason)
	}
	steps := []domain.Step{{Type: domain.StepWait, Duration: "2d"}, sendEmail()}
	edited, err := f.uc.UpdateWorkflow(ctx, f.tenant, w.ID, domain.Patch{Steps: steps})
	if err != nil || len(edited.Steps) != 2 {
		t.Fatalf("pausado se edita: %v", err)
	}
	if _, err := f.uc.ArchiveWorkflow(ctx, f.tenant, w.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.uc.DeleteWorkflow(ctx, f.tenant, w.ID); err != nil {
		t.Fatalf("borrar archivado: %v", err)
	}
	if _, err := f.uc.GetWorkflow(ctx, f.tenant, w.ID); !errors.Is(err, domain.ErrWorkflowNotFound) {
		t.Fatalf("borrado: %v", err)
	}
}

func TestArchivarCancelaLasEjecucionesPendientes(t *testing.T) {
	f := newFixture(t, Config{})
	c := f.sendable("ana@example.com")
	w := f.activeWorkflow(t, contactCreated, false, domain.Step{Type: domain.StepWait, Duration: "1d"}, sendEmail())
	if f.enter(t, c.ID) != 1 {
		t.Fatal("entra")
	}
	if _, err := f.uc.ArchiveWorkflow(ctx, f.tenant, w.ID); err != nil {
		t.Fatal(err)
	}
	run := f.onlyRun(t, w)
	if run.Status != domain.RunCancelled || run.ErrorCode != domain.CodeWorkflowArchived || run.FinishedAt == nil {
		t.Fatalf("cancelada: %+v", run)
	}
	if n := len(f.store.Published("automations.workflow.archived")); n != 1 {
		t.Fatalf("evento de archivo: %d", n)
	}
	f.tick(t)
	if len(f.sender.MarketingCalls) != 0 {
		t.Fatal("una ejecucion cancelada no avanza")
	}
}

func TestPausarCongelaLasEjecucionesYReactivarLasReanuda(t *testing.T) {
	f := newFixture(t, Config{})
	c := f.sendable("ana@example.com")
	w := f.activeWorkflow(t, contactCreated, false, sendEmail())
	f.enter(t, c.ID)
	if _, err := f.uc.PauseWorkflow(ctx, f.tenant, w.ID, "revision"); err != nil {
		t.Fatal(err)
	}
	f.tick(t)
	if len(f.sender.MarketingCalls) != 0 || f.onlyRun(t, w).Status != domain.RunWaiting {
		t.Fatal("pausado: la ejecucion espera sin avanzar")
	}
	if _, err := f.uc.ActivateWorkflow(ctx, f.tenant, w.ID); err != nil {
		t.Fatal(err)
	}
	f.tick(t)
	if len(f.sender.MarketingCalls) != 1 || f.onlyRun(t, w).Status != domain.RunCompleted {
		t.Fatalf("reactivado: %d envios, %+v", len(f.sender.MarketingCalls), f.onlyRun(t, w))
	}
}

func TestEjecucionesDeUnFlujoYDeOtraEmpresa(t *testing.T) {
	f := newFixture(t, Config{})
	c := f.sendable("ana@example.com")
	w := f.activeWorkflow(t, contactCreated, false, sendEmail())
	f.enter(t, c.ID)
	runs, total, err := f.uc.ListRuns(ctx, f.tenant, ports.RunFilter{WorkflowID: w.ID, Page: 1, PerPage: 25})
	if err != nil || total != 1 || len(runs) != 1 {
		t.Fatalf("listado: %v %d", err, total)
	}
	if _, err := f.uc.GetRun(ctx, uuid.New(), runs[0].ID); !errors.Is(err, domain.ErrRunNotFound) {
		t.Fatalf("otra empresa no la ve: %v", err)
	}
	if _, _, err := f.uc.ListRuns(ctx, uuid.New(), ports.RunFilter{WorkflowID: w.ID, Page: 1, PerPage: 25}); !errors.Is(err, domain.ErrWorkflowNotFound) {
		t.Fatalf("flujo de otra empresa: %v", err)
	}
}

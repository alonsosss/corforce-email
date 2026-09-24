package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/alonsosss/corforce-email/services/automations/internal/ports"
	"github.com/google/uuid"
)

func (uc *UseCase) CreateWorkflow(ctx context.Context, tenantID uuid.UUID, in domain.NewWorkflowInput) (*domain.Workflow, error) {
	w, err := domain.NewWorkflow(tenantID, in)
	if err != nil {
		return nil, err
	}
	if err := uc.workflows.Insert(ctx, w); err != nil {
		return nil, err
	}
	return w, nil
}

func (uc *UseCase) GetWorkflow(ctx context.Context, tenantID, id uuid.UUID) (*domain.Workflow, error) {
	return uc.workflows.Get(ctx, tenantID, id)
}

func (uc *UseCase) ListWorkflows(ctx context.Context, tenantID uuid.UUID, f ports.WorkflowFilter) ([]domain.Workflow, int64, error) {
	return uc.workflows.List(ctx, tenantID, f)
}

func (uc *UseCase) UpdateWorkflow(ctx context.Context, tenantID, id uuid.UUID, p domain.Patch) (*domain.Workflow, error) {
	var out *domain.Workflow
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		w, err := uc.workflows.GetForUpdate(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if p.Steps != nil && !reflect.DeepEqual(p.Steps, w.Steps) {
			open, err := uc.openRuns(ctx, tenantID, id)
			if err != nil {
				return err
			}
			if open > 0 {
				return domain.ErrStepsLockedByRuns
			}
		}
		if err := w.ApplyPatch(p); err != nil {
			return err
		}
		if err := uc.workflows.Update(ctx, w); err != nil {
			return err
		}
		out = w
		return nil
	})
	return out, err
}

// openRuns cuenta las ejecuciones del flujo que aun no terminaron.
func (uc *UseCase) openRuns(ctx context.Context, tenantID, id uuid.UUID) (int64, error) {
	var total int64
	for _, st := range []domain.RunStatus{domain.RunWaiting, domain.RunRunning} {
		_, n, err := uc.runs.List(ctx, tenantID, ports.RunFilter{WorkflowID: id, Status: st, Page: 1, PerPage: 1})
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

func (uc *UseCase) DeleteWorkflow(ctx context.Context, tenantID, id uuid.UUID) error {
	return uc.tx.Transact(ctx, func(ctx context.Context) error {
		w, err := uc.workflows.GetForUpdate(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if !w.Deletable() {
			return domain.ErrNotDeletable
		}
		return uc.workflows.Delete(ctx, tenantID, id)
	})
}

// ActivateWorkflow comprueba cada plantilla en templates, y en contacts los segmentos y
// atributos de las ramas y del disparador por fecha, FUERA de la transaccion; fija la
// version de cada paso de envio y activa. Si el flujo cambio entre la comprobacion y la
// activacion, se rechaza: se habria activado algo distinto de lo comprobado.
func (uc *UseCase) ActivateWorkflow(ctx context.Context, tenantID, id uuid.UUID) (*domain.Workflow, error) {
	w, err := uc.workflows.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if !w.CanActivate() {
		return nil, domain.TransitionError(w.Status, domain.StatusActive)
	}
	versions := make(map[int]int)
	for i, s := range w.Steps {
		if s.Type != domain.StepSendEmail {
			continue
		}
		v, err := uc.checkMarketingTemplate(ctx, tenantID, s)
		if err != nil {
			return nil, fmt.Errorf("steps[%d]: %w", i, err)
		}
		versions[i] = v
	}
	if err := uc.checkRules(ctx, tenantID, w); err != nil {
		return nil, err
	}

	var out *domain.Workflow
	err = uc.tx.Transact(ctx, func(ctx context.Context) error {
		cur, err := uc.workflows.GetForUpdate(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if !cur.UpdatedAt.Equal(w.UpdatedAt) {
			return domain.ErrConcurrentChange
		}
		for i, v := range versions {
			version := v
			cur.Steps[i].TemplateVersion = &version
		}
		if err := cur.Activate(uc.now()); err != nil {
			return err
		}
		if err := uc.workflows.Update(ctx, cur); err != nil {
			return err
		}
		out = cur
		return uc.publishActivated(ctx, cur)
	})
	return out, err
}

// checkMarketingTemplate renderiza la plantilla del paso sin variables: si sale, se sabe
// su version y su tipo, que debe ser marketing. Si la plantilla exige variables (del
// contacto, que aqui no hay), solo se puede seguir con una version indicada en el paso; su
// tipo lo comprueba entonces transactional en el primer envio (TEMPLATE_NOT_MARKETING).
func (uc *UseCase) checkMarketingTemplate(ctx context.Context, tenantID uuid.UUID, s domain.Step) (int, error) {
	callCtx, cancel := context.WithTimeout(ctx, domain.CallTimeout)
	defer cancel()
	rendered, err := uc.templates.Render(callCtx, tenantID, ports.RenderRequest{TemplateID: *s.TemplateID, Version: s.TemplateVersion})
	switch {
	case errors.Is(err, domain.ErrTemplateVariables):
		if s.TemplateVersion != nil {
			return *s.TemplateVersion, nil
		}
		return 0, domain.ErrTemplateVersionRequired
	case err != nil:
		return 0, err
	}
	switch rendered.Kind {
	case KindMarketing:
	case "":
		return 0, domain.ErrTemplateKindUnknown
	default:
		return 0, domain.ErrTemplateNotMarketing
	}
	if rendered.Version < 1 {
		return 0, fmt.Errorf("%w: templates no devolvio el numero de version", ports.ErrUnavailable)
	}
	return rendered.Version, nil
}

// PauseWorkflow congela el flujo: sus ejecuciones dejan de avanzar hasta reactivarlo.
func (uc *UseCase) PauseWorkflow(ctx context.Context, tenantID, id uuid.UUID, reason string) (*domain.Workflow, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = domain.PauseReasonManual
	}
	var out *domain.Workflow
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		w, err := uc.workflows.GetForUpdate(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if err := w.Pause(reason); err != nil {
			return err
		}
		if err := uc.workflows.Update(ctx, w); err != nil {
			return err
		}
		out = w
		return uc.publishPaused(ctx, w)
	})
	return out, err
}

// ArchiveWorkflow retira el flujo y cancela sus ejecuciones pendientes en la misma
// transaccion. Un paso que estaba en vuelo no puede ya cerrar su ejecucion: sus escrituras
// exigen que siga running.
func (uc *UseCase) ArchiveWorkflow(ctx context.Context, tenantID, id uuid.UUID) (*domain.Workflow, error) {
	var out *domain.Workflow
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		w, err := uc.workflows.GetForUpdate(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if err := w.Archive(); err != nil {
			return err
		}
		if err := uc.workflows.Update(ctx, w); err != nil {
			return err
		}
		if _, err := uc.runs.CancelByWorkflow(ctx, tenantID, id, uc.now()); err != nil {
			return err
		}
		out = w
		return uc.publishArchived(ctx, w)
	})
	return out, err
}

func (uc *UseCase) ListRuns(ctx context.Context, tenantID uuid.UUID, f ports.RunFilter) ([]domain.Run, int64, error) {
	if _, err := uc.workflows.Get(ctx, tenantID, f.WorkflowID); err != nil {
		return nil, 0, err
	}
	return uc.runs.List(ctx, tenantID, f)
}

func (uc *UseCase) GetRun(ctx context.Context, tenantID, id uuid.UUID) (*domain.Run, error) {
	return uc.runs.Get(ctx, tenantID, id)
}

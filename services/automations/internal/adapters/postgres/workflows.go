package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/alonsosss/corforce-email/services/automations/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const workflowColumns = `id, tenant_id, name, description, status, trigger_type, trigger_campaign_id, list_id,
	re_entry, steps, pause_reason, created_by, activated_at, created_at, updated_at`

// activeStatus es el predicado del indice parcial idx_automations_workflows_active_trigger.
const activeStatus = `status = '` + string(domain.StatusActive) + `'`

type WorkflowRepository struct {
	pool *db.ContextPool
}

func NewWorkflowRepository(pool *db.ContextPool) *WorkflowRepository {
	return &WorkflowRepository{pool: pool}
}

func scanWorkflow(row pgx.Row) (*domain.Workflow, error) {
	var (
		w              domain.Workflow
		status, typ    string
		steps          []byte
		triggerCampaig *uuid.UUID
	)
	err := row.Scan(&w.ID, &w.TenantID, &w.Name, &w.Description, &status, &typ, &triggerCampaig, &w.ListID,
		&w.ReEntry, &steps, &w.PauseReason, &w.CreatedBy, &w.ActivatedAt, &w.CreatedAt, &w.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrWorkflowNotFound
	}
	if err != nil {
		return nil, err
	}
	w.Status = domain.Status(status)
	w.Trigger = domain.Trigger{Type: domain.TriggerType(typ), CampaignID: triggerCampaig}
	if err := json.Unmarshal(steps, &w.Steps); err != nil {
		return nil, fmt.Errorf("pasos ilegibles en el flujo %s: %w", w.ID, err)
	}
	return &w, nil
}

func (r *WorkflowRepository) Insert(ctx context.Context, w *domain.Workflow) error {
	steps, err := json.Marshal(w.Steps)
	if err != nil {
		return err
	}
	err = r.pool.QueryRow(ctx,
		`INSERT INTO automations.workflows (id, tenant_id, name, description, status, trigger_type,
		        trigger_campaign_id, list_id, re_entry, steps, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 RETURNING created_at, updated_at`,
		w.ID, w.TenantID, w.Name, w.Description, string(w.Status), string(w.Trigger.Type),
		w.Trigger.CampaignID, w.ListID, w.ReEntry, steps, w.CreatedBy,
	).Scan(&w.CreatedAt, &w.UpdatedAt)
	if isUniqueViolation(err) {
		return domain.ErrNameTaken
	}
	return err
}

func (r *WorkflowRepository) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Workflow, error) {
	return scanWorkflow(r.pool.QueryRow(ctx,
		`SELECT `+workflowColumns+` FROM automations.workflows WHERE tenant_id = $1 AND id = $2`, tenantID, id))
}

func (r *WorkflowRepository) GetForUpdate(ctx context.Context, tenantID, id uuid.UUID) (*domain.Workflow, error) {
	return scanWorkflow(r.pool.QueryRow(ctx,
		`SELECT `+workflowColumns+` FROM automations.workflows WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenantID, id))
}

func (r *WorkflowRepository) Update(ctx context.Context, w *domain.Workflow) error {
	steps, err := json.Marshal(w.Steps)
	if err != nil {
		return err
	}
	err = r.pool.QueryRow(ctx,
		`UPDATE automations.workflows
		    SET name = $3, description = $4, status = $5, trigger_type = $6, trigger_campaign_id = $7,
		        list_id = $8, re_entry = $9, steps = $10, pause_reason = $11, activated_at = $12
		  WHERE tenant_id = $1 AND id = $2
		  RETURNING updated_at`,
		w.TenantID, w.ID, w.Name, w.Description, string(w.Status), string(w.Trigger.Type), w.Trigger.CampaignID,
		w.ListID, w.ReEntry, steps, w.PauseReason, w.ActivatedAt,
	).Scan(&w.UpdatedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return domain.ErrWorkflowNotFound
	case isUniqueViolation(err):
		return domain.ErrNameTaken
	}
	return err
}

func (r *WorkflowRepository) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM automations.workflows WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrWorkflowNotFound
	}
	return nil
}

func (r *WorkflowRepository) List(ctx context.Context, tenantID uuid.UUID, f ports.WorkflowFilter) ([]domain.Workflow, int64, error) {
	search := ""
	if f.Search != "" {
		search = "%" + escapeLike(f.Search) + "%"
	}
	const where = `tenant_id = $1 AND ($2 = '' OR status = $2) AND ($3 = '' OR name ILIKE $3)`
	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM automations.workflows WHERE `+where,
		tenantID, string(f.Status), search).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+workflowColumns+` FROM automations.workflows WHERE `+where+`
		  ORDER BY created_at DESC, id LIMIT $4 OFFSET $5`,
		tenantID, string(f.Status), search, f.PerPage, offset(f.Page, f.PerPage))
	if err != nil {
		return nil, 0, err
	}
	out, err := collectWorkflows(rows)
	return out, total, err
}

func (r *WorkflowRepository) ListActiveByTrigger(ctx context.Context, tenantID uuid.UUID, trigger domain.TriggerType) ([]domain.Workflow, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+workflowColumns+` FROM automations.workflows
		  WHERE tenant_id = $1 AND `+activeStatus+` AND trigger_type = $2
		  ORDER BY created_at, id`,
		tenantID, string(trigger))
	if err != nil {
		return nil, err
	}
	return collectWorkflows(rows)
}

func collectWorkflows(rows pgx.Rows) ([]domain.Workflow, error) {
	defer rows.Close()
	out := []domain.Workflow{}
	for rows.Next() {
		w, err := scanWorkflow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

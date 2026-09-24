package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/alonsosss/corforce-email/services/automations/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// RunMessageRepository guarda el correo de cada paso de envio (automations.run_messages).
type RunMessageRepository struct {
	pool *db.ContextPool
}

func NewRunMessageRepository(pool *db.ContextPool) *RunMessageRepository {
	return &RunMessageRepository{pool: pool}
}

func (r *RunMessageRepository) Record(ctx context.Context, m domain.RunMessage) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO automations.run_messages (tenant_id, run_id, workflow_id, contact_id, step_id, message_id)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT DO NOTHING`,
		m.TenantID, m.RunID, m.WorkflowID, m.ContactID, m.StepID, m.MessageID)
	return err
}

func (r *RunMessageRepository) Get(ctx context.Context, tenantID, runID uuid.UUID, stepID string) (*domain.RunMessage, error) {
	m := domain.RunMessage{TenantID: tenantID, RunID: runID, StepID: stepID}
	err := r.pool.QueryRow(ctx,
		`SELECT workflow_id, contact_id, message_id, opened_at, clicked_at FROM automations.run_messages
		  WHERE tenant_id = $1 AND run_id = $2 AND step_id = $3`,
		tenantID, runID, stepID).Scan(&m.WorkflowID, &m.ContactID, &m.MessageID, &m.OpenedAt, &m.ClickedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// MarkEngagement conserva la primera hora de cada hito: la rama pregunta si ocurrio, no
// cuando por ultima vez. Sin cambios no escribe.
func (r *RunMessageRepository) MarkEngagement(ctx context.Context, tenantID, messageID uuid.UUID, openedAt, clickedAt *time.Time) (bool, error) {
	var found bool
	err := r.pool.QueryRow(ctx,
		`WITH upd AS (
		   UPDATE automations.run_messages
		      SET opened_at = COALESCE(opened_at, $3), clicked_at = COALESCE(clicked_at, $4)
		    WHERE tenant_id = $1 AND message_id = $2
		      AND ((opened_at IS NULL AND $3::timestamptz IS NOT NULL) OR (clicked_at IS NULL AND $4::timestamptz IS NOT NULL))
		   RETURNING 1)
		 SELECT EXISTS (SELECT 1 FROM upd)
		     OR EXISTS (SELECT 1 FROM automations.run_messages WHERE tenant_id = $1 AND message_id = $2)`,
		tenantID, messageID, openedAt, clickedAt).Scan(&found)
	return found, err
}

// DateScanRepository reparte el recorrido de aniversarios (automations.date_scans).
type DateScanRepository struct {
	pool *db.ContextPool
}

func NewDateScanRepository(pool *db.ContextPool) *DateScanRepository {
	return &DateScanRepository{pool: pool}
}

// Claim inserta o adelanta scanned_at solo si el anterior es de antes de since: con dos
// replicas a la vez, la fila bloqueada por la primera hace que la segunda no actualice nada.
func (r *DateScanRepository) Claim(ctx context.Context, tenantID, workflowID uuid.UUID, now, since time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`INSERT INTO automations.date_scans (workflow_id, tenant_id, scanned_at)
		 SELECT $2, $1, $3 WHERE EXISTS (SELECT 1 FROM automations.workflows WHERE tenant_id = $1 AND id = $2)
		 ON CONFLICT (workflow_id) DO UPDATE SET scanned_at = EXCLUDED.scanned_at
		  WHERE automations.date_scans.scanned_at < $4`,
		tenantID, workflowID, now, since)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

var (
	_ ports.RunMessageRepository = (*RunMessageRepository)(nil)
	_ ports.DateScanRepository   = (*DateScanRepository)(nil)
)

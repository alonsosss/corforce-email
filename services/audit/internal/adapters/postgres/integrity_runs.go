package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// IntegrityRunRepo guarda las verificaciones de cadena en audit.integrity_runs. El latido se
// compara con el reloj de la base y no con el del proceso: varias replicas con relojes distintos
// deben coincidir en cual verificacion esta viva.
type IntegrityRunRepo struct{ pool *db.ContextPool }

func NewIntegrityRunRepo(pool *db.ContextPool) *IntegrityRunRepo {
	return &IntegrityRunRepo{pool: pool}
}

const runColumns = `id, tenant_id, mode, origin, requested_by, status, phase, owner, cancel_requested, target_seq, current_seq,
	checked_rows, chains, result, COALESCE(error_code, ''), started_at, heartbeat_at, finished_at`

func scanRun(row pgx.Row) (*domain.IntegrityRun, error) {
	var (
		r              domain.IntegrityRun
		chains, result []byte
	)
	if err := row.Scan(&r.ID, &r.TenantID, &r.Mode, &r.Trigger, &r.RequestedBy, &r.Status, &r.Phase, &r.Owner, &r.CancelRequested,
		&r.TargetSeq, &r.CurrentSeq, &r.Checked, &chains, &result, &r.ErrorCode, &r.StartedAt, &r.HeartbeatAt, &r.FinishedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(chains, &r.Chains); err != nil {
		return nil, fmt.Errorf("estado de las cadenas de la verificacion %s: %w", r.ID, err)
	}
	if result != nil {
		r.Result = &domain.ChainIntegrity{}
		if err := json.Unmarshal(result, r.Result); err != nil {
			return nil, fmt.Errorf("resultado de la verificacion %s: %w", r.ID, err)
		}
	}
	return &r, nil
}

func (r *IntegrityRunRepo) Open(ctx context.Context, run *domain.IntegrityRun) (*domain.IntegrityRun, error) {
	chains, err := json.Marshal(run.Chains)
	if err != nil {
		return nil, err
	}
	// Otro proceso puede cerrar la verificacion en curso entre el conflicto y su lectura: se reintenta.
	for attempt := 0; attempt < 3; attempt++ {
		created, err := scanRun(r.pool.QueryRow(ctx,
			`INSERT INTO audit.integrity_runs (id, tenant_id, mode, origin, requested_by, phase, owner, target_seq, current_seq, checked_rows, chains)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			 ON CONFLICT (tenant_id) WHERE status = 'running' DO NOTHING
			 RETURNING `+runColumns,
			run.ID, run.TenantID, run.Mode, run.Trigger, run.RequestedBy, run.Phase, run.Owner, run.TargetSeq, run.CurrentSeq, run.Checked, chains))
		if err == nil {
			return created, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		active, err := r.Active(ctx, run.TenantID)
		if err != nil {
			return nil, err
		}
		if active != nil {
			return active, domain.ErrRunActive
		}
	}
	return nil, errors.New("no se pudo abrir la verificación: la empresa cambia de estado sin parar")
}

func (r *IntegrityRunRepo) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.IntegrityRun, error) {
	run, err := scanRun(r.pool.QueryRow(ctx, `SELECT `+runColumns+` FROM audit.integrity_runs WHERE id = $1 AND tenant_id = $2`, id, tenantID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrRunNotFound
	}
	return run, err
}

func (r *IntegrityRunRepo) Active(ctx context.Context, tenantID uuid.UUID) (*domain.IntegrityRun, error) {
	run, err := scanRun(r.pool.QueryRow(ctx, `SELECT `+runColumns+` FROM audit.integrity_runs WHERE tenant_id = $1 AND status = 'running'`, tenantID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return run, err
}

func (r *IntegrityRunRepo) LastCompleted(ctx context.Context, tenantID uuid.UUID, onlyOK bool, mode domain.RunMode) (*domain.IntegrityRun, error) {
	filter := ""
	if onlyOK {
		filter = ` AND (result->>'ok')::boolean`
	}
	run, err := scanRun(r.pool.QueryRow(ctx,
		`SELECT `+runColumns+` FROM audit.integrity_runs
		  WHERE tenant_id = $1 AND status = 'completed'`+filter+` AND ($2 = '' OR mode = $2)
		  ORDER BY finished_at DESC LIMIT 1`, tenantID, string(mode)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return run, err
}

func (r *IntegrityRunRepo) List(ctx context.Context, tenantID uuid.UUID, limit int) ([]*domain.IntegrityRun, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+runColumns+` FROM audit.integrity_runs WHERE tenant_id = $1 ORDER BY started_at DESC LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []*domain.IntegrityRun
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (r *IntegrityRunRepo) Claim(ctx context.Context, tenantID, id uuid.UUID, owner string, staleAfter time.Duration) (*domain.IntegrityRun, bool, error) {
	run, err := scanRun(r.pool.QueryRow(ctx,
		`UPDATE audit.integrity_runs SET owner = $3, heartbeat_at = now()
		  WHERE id = $1 AND tenant_id = $2 AND status = 'running'
		    AND (owner = $3 OR heartbeat_at < now() - make_interval(secs => $4))
		 RETURNING `+runColumns, id, tenantID, owner, staleAfter.Seconds()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return run, true, nil
}

func (r *IntegrityRunRepo) Progress(ctx context.Context, tenantID, id uuid.UUID, owner string, p domain.RunProgress) error {
	chains, err := json.Marshal(p.Chains)
	if err != nil {
		return err
	}
	var cancel bool
	err = r.pool.QueryRow(ctx,
		`UPDATE audit.integrity_runs SET phase = $4, checked_rows = $5, current_seq = $6, chains = $7, heartbeat_at = now()
		  WHERE id = $1 AND tenant_id = $2 AND owner = $3 AND status = 'running'
		 RETURNING cancel_requested`,
		id, tenantID, owner, p.Phase, p.Checked, p.CurrentSeq, chains).Scan(&cancel)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrRunLost
	}
	if err != nil {
		return err
	}
	if cancel {
		return domain.ErrRunCancelled
	}
	return nil
}

func (r *IntegrityRunRepo) Finish(ctx context.Context, tenantID, id uuid.UUID, owner string, f domain.RunFinish) error {
	chains, err := json.Marshal(f.Chains)
	if err != nil {
		return err
	}
	var result []byte
	if f.Result != nil {
		if result, err = json.Marshal(f.Result); err != nil {
			return err
		}
	}
	phase := ""
	if f.Status == domain.RunCompleted {
		phase = domain.RunPhaseDone
	}
	tag, err := r.pool.Exec(ctx,
		`UPDATE audit.integrity_runs
		    SET status = $4, phase = COALESCE(NULLIF($5, ''), phase), result = $6, error_code = NULLIF($7, ''), chains = $8,
		        heartbeat_at = now(), finished_at = now()
		  WHERE id = $1 AND tenant_id = $2 AND owner = $3 AND status = 'running'`,
		id, tenantID, owner, f.Status, phase, result, f.ErrorCode, chains)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrRunLost
	}
	return nil
}

func (r *IntegrityRunRepo) RequestCancel(ctx context.Context, tenantID, id uuid.UUID) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE audit.integrity_runs SET cancel_requested = true WHERE id = $1 AND tenant_id = $2 AND status = 'running'`, id, tenantID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

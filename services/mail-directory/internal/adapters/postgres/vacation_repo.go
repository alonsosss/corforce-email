package postgres

import (
	"context"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

// VacationRepo guarda mail.vacation_replies. Corre dentro de la transaccion con RLS del
// Transactor y ademas filtra por tenant_id.
type VacationRepo struct{ pool *db.ContextPool }

func NewVacationRepo(pool *db.ContextPool) *VacationRepo { return &VacationRepo{pool: pool} }

func (r *VacationRepo) ByUsername(ctx context.Context, tenantID uuid.UUID, username string) (*domain.VacationReply, error) {
	var v domain.VacationReply
	err := r.pool.QueryRow(ctx,
		`SELECT id, tenant_id, username, enabled, subject, message, interval_days, starts_on, ends_on,
		        script_data, created_at, updated_at
		   FROM mail.vacation_replies WHERE tenant_id = $1 AND username = $2`,
		tenantID, username,
	).Scan(&v.ID, &v.TenantID, &v.Username, &v.Enabled, &v.Subject, &v.Message, &v.IntervalDays,
		&v.StartsOn, &v.EndsOn, &v.ScriptData, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &v, nil
}

// Upsert crea la respuesta o reemplaza la del buzon entera. La restriccion UNIQUE es por buzon:
// solo el mismo tenant_id puede reemplazar su fila (RLS), y el WHERE lo repite.
func (r *VacationRepo) Upsert(ctx context.Context, v *domain.VacationReply) error {
	return mapErr(r.pool.QueryRow(ctx,
		`INSERT INTO mail.vacation_replies
		        (id, tenant_id, username, enabled, subject, message, interval_days, starts_on, ends_on, script_data)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 ON CONFLICT (username) DO UPDATE
		    SET enabled = EXCLUDED.enabled, subject = EXCLUDED.subject, message = EXCLUDED.message,
		        interval_days = EXCLUDED.interval_days, starts_on = EXCLUDED.starts_on, ends_on = EXCLUDED.ends_on,
		        script_data = EXCLUDED.script_data
		  WHERE mail.vacation_replies.tenant_id = EXCLUDED.tenant_id
		 RETURNING id, created_at, updated_at`,
		v.ID, v.TenantID, v.Username, v.Enabled, v.Subject, v.Message, v.IntervalDays, v.StartsOn, v.EndsOn, v.ScriptData,
	).Scan(&v.ID, &v.CreatedAt, &v.UpdatedAt))
}

func (r *VacationRepo) DeleteByUsername(ctx context.Context, tenantID uuid.UUID, username string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM mail.vacation_replies WHERE tenant_id = $1 AND username = $2`, tenantID, username)
	return err
}

// MailboxLocator localiza un buzon por su nombre en toda la celda. Corre fuera de TransactRLS
// con el rol de servicio, igual que SenderIdentityRepo y mail-auth: quien llama no tiene empresa.
type MailboxLocator struct{ pool *db.ContextPool }

func NewMailboxLocator(pool *db.ContextPool) *MailboxLocator { return &MailboxLocator{pool: pool} }

func (l *MailboxLocator) Locate(ctx context.Context, username string) (uuid.UUID, uuid.UUID, error) {
	var tenantID, id uuid.UUID
	err := l.pool.QueryRow(ctx,
		`SELECT tenant_id, id FROM mail.mailboxes WHERE username = $1 AND active = 1`, username,
	).Scan(&tenantID, &id)
	if err != nil {
		return uuid.Nil, uuid.Nil, mapErr(err)
	}
	return tenantID, id, nil
}

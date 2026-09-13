package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DeletedAccountRepo retira las asignaciones de roles de las cuentas que identity borra.
type DeletedAccountRepo struct {
	pool *pgxpool.Pool
}

func NewDeletedAccountRepo(pool *pgxpool.Pool) *DeletedAccountRepo {
	return &DeletedAccountRepo{pool: pool}
}

// RemoveRolesOfDeletedUser borra en una sentencia las asignaciones del usuario a roles de la
// empresa, y solo si la cuenta ya no existe en ella segun la vista que publica identity: una
// entrega tardia no retira los roles de una cuenta que se volvio a crear o se restauro.
func (r *DeletedAccountRepo) RemoveRolesOfDeletedUser(ctx context.Context, tenantID, userID uuid.UUID) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM access_control.user_roles ur
		  USING access_control.roles r
		  WHERE ur.role_id = r.id AND ur.user_id = $1 AND r.tenant_id = $2
		    AND NOT EXISTS (SELECT 1 FROM identity.v_user_status u WHERE u.user_id = $1 AND u.tenant_id = $2)`,
		userID, tenantID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

package postgres

import (
	"context"
	"errors"
	"net/netip"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// FirewallRepository implementa ports.FirewallRepository. Las tablas del cortafuegos son
// de plataforma, sin empresa ni politica para mail_app: corre como duena del pool y el
// caso de uso ya exigio al operador.
type FirewallRepository struct {
	pool *db.ContextPool
}

func NewFirewallRepository(pool *db.ContextPool) *FirewallRepository {
	return &FirewallRepository{pool: pool}
}

const firewallNetworkColumns = `id, list, network, note, created_at`

func scanFirewallNetwork(row pgx.Row) (*domain.FirewallNetwork, error) {
	var n domain.FirewallNetwork
	var list string
	var network netip.Prefix
	if err := row.Scan(&n.ID, &list, &network, &n.Note, &n.CreatedAt); err != nil {
		return nil, err
	}
	n.List = domain.FirewallList(list)
	n.Network = network.String()
	return &n, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (r *FirewallRepository) CreateFirewallNetwork(ctx context.Context, n *domain.FirewallNetwork) error {
	saved, err := scanFirewallNetwork(r.pool.QueryRow(ctx,
		`INSERT INTO mail_security.firewall_networks (list, network, note) VALUES ($1, $2, $3) RETURNING `+firewallNetworkColumns,
		string(n.List), n.Network, n.Note))
	if isUniqueViolation(err) {
		return domain.ErrAlreadyExists
	}
	if err != nil {
		return err
	}
	*n = *saved
	return nil
}

func (r *FirewallRepository) DeleteFirewallNetwork(ctx context.Context, id uuid.UUID) (*domain.FirewallNetwork, error) {
	n, err := scanFirewallNetwork(r.pool.QueryRow(ctx,
		`DELETE FROM mail_security.firewall_networks WHERE id = $1 RETURNING `+firewallNetworkColumns, id))
	return n, notFound(err)
}

func (r *FirewallRepository) UpsertFirewallOptions(ctx context.Context, o *domain.FirewallOptions) error {
	return r.pool.QueryRow(ctx, `
		INSERT INTO mail_security.firewall_options
		    (ban_time, max_ban_time, ban_time_increment, max_attempts, retry_window, netban_ipv4, netban_ipv6)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (scope) DO UPDATE SET
		    ban_time = EXCLUDED.ban_time, max_ban_time = EXCLUDED.max_ban_time,
		    ban_time_increment = EXCLUDED.ban_time_increment, max_attempts = EXCLUDED.max_attempts,
		    retry_window = EXCLUDED.retry_window, netban_ipv4 = EXCLUDED.netban_ipv4, netban_ipv6 = EXCLUDED.netban_ipv6
		RETURNING updated_at`,
		o.BanTime, o.MaxBanTime, o.BanTimeIncrement, o.MaxAttempts, o.RetryWindow, o.NetbanIPv4, o.NetbanIPv6,
	).Scan(&o.UpdatedAt)
}

func (r *PolicyReader) AllFirewallNetworks(ctx context.Context) ([]domain.FirewallNetwork, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+firewallNetworkColumns+` FROM mail_security.firewall_networks ORDER BY list, network`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.FirewallNetwork{}
	for rows.Next() {
		n, err := scanFirewallNetwork(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

func (r *PolicyReader) FirewallOptions(ctx context.Context) (*domain.FirewallOptions, error) {
	var o domain.FirewallOptions
	err := r.pool.QueryRow(ctx, `
		SELECT ban_time, max_ban_time, ban_time_increment, max_attempts, retry_window, netban_ipv4, netban_ipv6, updated_at
		  FROM mail_security.firewall_options WHERE scope = 'cell'`).
		Scan(&o.BanTime, &o.MaxBanTime, &o.BanTimeIncrement, &o.MaxAttempts, &o.RetryWindow, &o.NetbanIPv4, &o.NetbanIPv6, &o.UpdatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	return &o, nil
}

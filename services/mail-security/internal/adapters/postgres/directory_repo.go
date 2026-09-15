package postgres

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DirectoryRepository implementa ports.DirectoryReader sobre las vistas publicadas por
// mail-directory (mail.v_routing_*). Nunca toca mail.* directamente: la vista es el
// contrato que su dueno sostiene; la tabla puede cambiar sin avisar.
//
// Las vistas corren con los privilegios de su dueno y por tanto NO estan sujetas a la
// politica RLS del rol mail_app: toda consulta acotada por empresa lleva tenant_id.
type DirectoryRepository struct {
	pool *db.ContextPool
}

func NewDirectoryRepository(pool *db.ContextPool) *DirectoryRepository {
	return &DirectoryRepository{pool: pool}
}

// AliasGoto: active 1 (activo) y 2 (solo recibe) siguen entregando, como en Postfix.
func (r *DirectoryRepository) AliasGoto(ctx context.Context, address string) (string, bool, error) {
	var goto_ string
	err := r.pool.QueryRow(ctx, `SELECT goto FROM mail.v_routing_aliases WHERE address = $1 AND active IN (1, 2)`, address).Scan(&goto_)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return goto_, err == nil, err
}

func (r *DirectoryRepository) AliasDomainTarget(ctx context.Context, domainName string) (string, bool, error) {
	var target string
	err := r.pool.QueryRow(ctx, `SELECT target_domain FROM mail.v_routing_alias_domains WHERE alias_domain = $1 AND active`, domainName).Scan(&target)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return target, err == nil, err
}

func (r *DirectoryRepository) MailboxByUsername(ctx context.Context, username string) (*domain.Mailbox, error) {
	var m domain.Mailbox
	err := r.pool.QueryRow(ctx, `SELECT tenant_id, username, domain, active, kind FROM mail.v_routing_mailboxes WHERE username = $1`, username).
		Scan(&m.TenantID, &m.Username, &m.Domain, &m.Active, &m.Kind)
	if err != nil {
		return nil, notFound(err)
	}
	return &m, nil
}

// ActiveDomains: dominios activos y dominios alias activos cuyo destino esta activo.
func (r *DirectoryRepository) ActiveDomains(ctx context.Context) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT domain FROM mail.v_routing_domains WHERE active
		UNION
		SELECT ad.alias_domain FROM mail.v_routing_alias_domains ad
		  JOIN mail.v_routing_domains d ON d.domain = ad.target_domain AND d.active
		 WHERE ad.active
		ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *DirectoryRepository) DomainActive(ctx context.Context, domainName string) (bool, error) {
	var active bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM mail.v_routing_domains WHERE domain = $1 AND active
			UNION ALL
			SELECT 1 FROM mail.v_routing_alias_domains ad
			  JOIN mail.v_routing_domains d ON d.domain = ad.target_domain AND d.active
			 WHERE ad.alias_domain = $1 AND ad.active)`, domainName).Scan(&active)
	return active, err
}

func (r *DirectoryRepository) ObjectOwnedBy(ctx context.Context, tenantID uuid.UUID, object string) (bool, error) {
	var owned bool
	var err error
	if domain.ObjectKindOf(object) == domain.ObjectMailbox {
		err = r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mail.v_routing_mailboxes WHERE tenant_id = $1 AND username = $2)`, tenantID, object).Scan(&owned)
	} else {
		err = r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mail.v_routing_domains WHERE tenant_id = $1 AND domain = $2)`, tenantID, object).Scan(&owned)
	}
	return owned, err
}

func (r *DirectoryRepository) DomainStates(ctx context.Context, names []string) (map[string]domain.DirectoryDomain, error) {
	out := make(map[string]domain.DirectoryDomain, len(names))
	if len(names) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT domain, tenant_id, active FROM mail.v_routing_domains WHERE domain = ANY($1)`, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d domain.DirectoryDomain
		if err := rows.Scan(&d.Name, &d.TenantID, &d.Active); err != nil {
			return nil, err
		}
		out[d.Name] = d
	}
	return out, rows.Err()
}

func (r *DirectoryRepository) AliasDomainsOf(ctx context.Context, targetDomain string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT alias_domain FROM mail.v_routing_alias_domains WHERE target_domain = $1 AND active ORDER BY alias_domain`, targetDomain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// AliasesTargeting: goto es una lista separada por comas; se compara elemento a
// elemento para no confundir ana@x con juana@x.
func (r *DirectoryRepository) AliasesTargeting(ctx context.Context, username string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT address FROM mail.v_routing_aliases
		 WHERE active IN (1, 2) AND $1 = ANY (string_to_array(replace(goto, ' ', ''), ','))
		 ORDER BY address`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// BCCDestination lee la vista publicada mail.v_routing_bcc_maps (mail-directory, 04).
// Si hubiera varias filas activas para el mismo origen se toma la mas antigua por
// orden estable; Rspamd solo admite un destino por llamada.
func (r *DirectoryRepository) BCCDestination(ctx context.Context, kind, localDest string) (string, bool, error) {
	var dest string
	err := r.pool.QueryRow(ctx, `
		SELECT bcc_dest FROM mail.v_routing_bcc_maps
		 WHERE type = $1 AND local_dest = $2 AND active
		 ORDER BY bcc_dest LIMIT 1`, kind, localDest).Scan(&dest)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return dest, err == nil, err
}

// InternalAliases solo cuenta los aliases plenamente activos: uno en solo-recepcion (2)
// no envia ni recibe de fuera, y marcarlo interno no cambia nada.
func (r *DirectoryRepository) InternalAliases(ctx context.Context) ([]domain.InternalAlias, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT address, domain FROM mail.v_routing_aliases
		 WHERE internal AND active = 1
		 ORDER BY address`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.InternalAlias
	for rows.Next() {
		var a domain.InternalAlias
		if err := rows.Scan(&a.Address, &a.Domain); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

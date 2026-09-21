package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/jackc/pgx/v5"
)

// chainTables fija la tabla de cada cadena. El nombre nunca viene de fuera: solo se interpola uno
// de estos dos.
var chainTables = map[domain.ChainName]string{
	domain.ChainAuditLogs:      "audit.audit_logs",
	domain.ChainSecurityEvents: "audit.security_events",
}

// ChainAnchorRepo guarda las anclas en audit.chain_anchors, que el rol del servicio solo puede
// leer y anadir.
type ChainAnchorRepo struct{ pool *db.ContextPool }

func NewChainAnchorRepo(pool *db.ContextPool) *ChainAnchorRepo { return &ChainAnchorRepo{pool: pool} }

func chainTable(chain domain.ChainName) (string, error) {
	t, ok := chainTables[chain]
	if !ok {
		return "", fmt.Errorf("cadena desconocida %q", chain)
	}
	return t, nil
}

func (r *ChainAnchorRepo) Head(ctx context.Context, chain domain.ChainName) (*domain.ChainHead, error) {
	table, err := chainTable(chain)
	if err != nil {
		return nil, err
	}
	var h domain.ChainHead
	err = r.pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT seq, entry_hash, COALESCE(hash_version,0) FROM %s WHERE seq IS NOT NULL AND entry_hash IS NOT NULL ORDER BY seq DESC LIMIT 1`, table),
	).Scan(&h.Seq, &h.Hash, &h.HashVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &h, nil
}

const anchorColumns = `id, tenant_id, chain, head_seq, head_hash, hash_version, anchored_at`

func scanAnchor(row pgx.Row) (*domain.ChainAnchor, error) {
	var a domain.ChainAnchor
	if err := row.Scan(&a.ID, &a.TenantID, &a.Chain, &a.HeadSeq, &a.HeadHash, &a.HashVersion, &a.AnchoredAt); err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *ChainAnchorRepo) Findings(ctx context.Context, chain domain.ChainName) (domain.AnchorFindings, error) {
	var f domain.AnchorFindings
	table, err := chainTable(chain)
	if err != nil {
		return f, err
	}
	last, err := scanAnchor(r.pool.QueryRow(ctx,
		`SELECT `+anchorColumns+` FROM audit.chain_anchors WHERE chain = $1 ORDER BY head_seq DESC LIMIT 1`, string(chain)))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return f, err
	}
	f.Last = last
	if f.Last == nil {
		return f, nil
	}
	// LEFT JOIN: un ancla cuya posicion ya no tiene fila tampoco coincide.
	mismatched, err := scanAnchor(r.pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT a.id, a.tenant_id, a.chain, a.head_seq, a.head_hash, a.hash_version, a.anchored_at
		               FROM audit.chain_anchors a LEFT JOIN %s t ON t.seq = a.head_seq
		              WHERE a.chain = $1 AND t.entry_hash IS DISTINCT FROM a.head_hash
		              ORDER BY a.head_seq LIMIT 1`, table), string(chain)))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return f, err
	}
	f.Mismatched = mismatched
	return f, nil
}

func (r *ChainAnchorRepo) Save(ctx context.Context, a *domain.ChainAnchor) (bool, error) {
	err := r.pool.QueryRow(ctx,
		`INSERT INTO audit.chain_anchors (tenant_id, chain, head_seq, head_hash, hash_version)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (chain, head_seq) DO NOTHING
		 RETURNING id, anchored_at`,
		a.TenantID, string(a.Chain), a.HeadSeq, a.HeadHash, a.HashVersion).Scan(&a.ID, &a.AnchoredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

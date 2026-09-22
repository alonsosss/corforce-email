package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

// Facts es lo que la cadena dice hoy de un ancla recibida por otro canal (el correo de anclas):
// la posicion de su cabeza, el hash que ocupa la posicion del ancla y si la tabla de anclas la
// conserva. Lo usa el verificador externo (audit verificar-ancla), con la base que se le da.
func (r *ChainAnchorRepo) Facts(ctx context.Context, a domain.ChainAnchor) (domain.ChainFacts, error) {
	var f domain.ChainFacts
	table, err := chainTable(a.Chain)
	if err != nil {
		return f, err
	}
	err = r.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT COALESCE((SELECT max(seq) FROM %[1]s WHERE entry_hash IS NOT NULL), 0),
		       COALESCE((SELECT entry_hash FROM %[1]s WHERE seq = $1), ''),
		       EXISTS (SELECT 1 FROM audit.chain_anchors WHERE chain = $2 AND head_seq = $1 AND head_hash = $3)`, table),
		a.HeadSeq, string(a.Chain), a.HeadHash).Scan(&f.HeadSeq, &f.HashAtSeq, &f.AnchorRecorded)
	return f, err
}

// TenantChain es la cadena de una base de empresa abierta por su DSN, para el verificador externo
// (audit verificar-ancla): lee los hechos de cada ancla con el mismo repositorio que el servicio.
type TenantChain struct {
	repo *ChainAnchorRepo
	pool *pgxpool.Pool
}

func OpenTenantChain(ctx context.Context, dsn string) (*TenantChain, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &TenantChain{repo: NewChainAnchorRepo(&db.ContextPool{}), pool: pool}, nil
}

func (c *TenantChain) Facts(ctx context.Context, a domain.ChainAnchor) (domain.ChainFacts, error) {
	return c.repo.Facts(db.WithPool(ctx, c.pool), a)
}

func (c *TenantChain) Close() { c.pool.Close() }

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

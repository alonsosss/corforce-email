package postgres

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/alonsosss/corforce-email/services/contacts/internal/ports"
	"github.com/alonsosss/corforce-email/services/contacts/internal/segment"
	"github.com/google/uuid"
)

// SegmentQuery evalua segmentos. El WHERE lo produce internal/segment con placeholders;
// aqui solo se anaden las condiciones fijas (empresa, enviable, cursor) y el orden.
type SegmentQuery struct {
	pool *db.ContextPool
}

func NewSegmentQuery(pool *db.ContextPool) *SegmentQuery {
	return &SegmentQuery{pool: pool}
}

func (q *SegmentQuery) Count(ctx context.Context, tenantID uuid.UUID, def segment.Definition, schema segment.Schema) (int64, error) {
	args := segment.NewArgs(tenantID)
	where, err := segment.Compile(def, schema, args)
	if err != nil {
		return 0, err
	}
	var n int64
	err = q.pool.QueryRow(ctx, `SELECT count(*) FROM contacts.contacts c WHERE c.tenant_id = $1 AND `+where, args.Values()...).Scan(&n)
	return n, err
}

func (q *SegmentQuery) Page(ctx context.Context, tenantID uuid.UUID, def segment.Definition, schema segment.Schema, limit, offset int) ([]domain.Contact, error) {
	args := segment.NewArgs(tenantID)
	where, err := segment.Compile(def, schema, args)
	if err != nil {
		return nil, err
	}
	sql := `SELECT ` + contactColumns + ` FROM contacts.contacts c WHERE c.tenant_id = $1 AND ` + where +
		` ORDER BY c.created_at DESC, c.id LIMIT ` + args.Add(limit) + ` OFFSET ` + args.Add(offset)
	rows, err := q.pool.Query(ctx, sql, args.Values()...)
	if err != nil {
		return nil, err
	}
	return collectContacts(rows)
}

func (q *SegmentQuery) Audience(ctx context.Context, tenantID uuid.UUID, spec ports.AudienceSpec) ([]domain.Contact, error) {
	sql, args, err := audienceSQL(tenantID, spec)
	if err != nil {
		return nil, err
	}
	rows, err := q.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return collectContacts(rows)
}

// sendableConditions es la regla de contacto enviable en SQL, con la empresa en $1. Es la
// misma que domain.Contact.Sendable y la unica escritura de esa regla en SQL: la audiencia
// y la consulta de enviables por id la comparten.
//
// Las condiciones van como literales y no como argumentos a proposito: son las del indice
// parcial idx_contacts_contacts_sendable, y el planificador solo puede usarlo si ve el
// mismo literal.
func sendableConditions() []string {
	return []string{
		"c.tenant_id = $1",
		"c.status = '" + string(domain.StatusActive) + "'",
		"c.marketing_consent = '" + string(domain.ConsentGranted) + "'",
	}
}

func (q *SegmentQuery) Sendable(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]domain.Contact, error) {
	sql, args := sendableSQL(tenantID, ids)
	rows, err := q.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return collectContacts(rows)
}

// sendableSQL filtra por id sobre el mismo indice parcial (tenant_id, id).
func sendableSQL(tenantID uuid.UUID, ids []uuid.UUID) (string, []any) {
	conds := append(sendableConditions(), "c.id = ANY($2::uuid[])")
	return `SELECT ` + contactColumns + ` FROM contacts.contacts c WHERE ` + strings.Join(conds, " AND ") +
		` ORDER BY c.id`, []any{tenantID, ids}
}

// audienceSQL arma la consulta de una pagina de audiencia.
//
// El recorrido es por el indice parcial de enviables en orden de id, asi que la
// paginacion por keyset (c.id > cursor ... ORDER BY c.id LIMIT n) no ordena nada: lee la
// siguiente tanda y para.
func audienceSQL(tenantID uuid.UUID, spec ports.AudienceSpec) (string, []any, error) {
	args := segment.NewArgs(tenantID)
	conds := sendableConditions()
	if spec.After != uuid.Nil {
		conds = append(conds, "c.id > "+args.Add(spec.After)+"::uuid")
	}
	var include []string
	if len(spec.ListIDs) > 0 {
		include = append(include, "EXISTS (SELECT 1 FROM contacts.list_members lm WHERE lm.list_id = ANY("+
			args.Add(spec.ListIDs)+"::uuid[]) AND lm.contact_id = c.id)")
	}
	for _, def := range spec.Include {
		where, err := segment.Compile(def, spec.Schema, args)
		if err != nil {
			return "", nil, err
		}
		include = append(include, where)
	}
	if len(include) > 0 {
		conds = append(conds, "("+strings.Join(include, " OR ")+")")
	}
	for _, def := range spec.Exclude {
		where, err := segment.Compile(def, spec.Schema, args)
		if err != nil {
			return "", nil, err
		}
		conds = append(conds, "NOT "+where)
	}
	sql := `SELECT ` + contactColumns + ` FROM contacts.contacts c WHERE ` + strings.Join(conds, " AND ") +
		` ORDER BY c.id LIMIT ` + args.Add(spec.Limit)
	return sql, args.Values(), nil
}

func marshalImportErrors(errs []domain.ImportError) ([]byte, error) {
	if errs == nil {
		errs = []domain.ImportError{}
	}
	return json.Marshal(errs)
}

func unmarshalImportErrors(b []byte, dst *[]domain.ImportError) error {
	*dst = []domain.ImportError{}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, dst)
}

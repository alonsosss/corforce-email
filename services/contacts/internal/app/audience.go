package app

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/alonsosss/corforce-email/services/contacts/internal/ports"
	"github.com/alonsosss/corforce-email/services/contacts/internal/segment"
	"github.com/google/uuid"
)

const (
	// MaxAudienceLimit es el tope y el valor por defecto de una pagina de audiencia.
	MaxAudienceLimit = 1000
	// MaxAudienceRefs es el tope de listas y de segmentos (de cada clase) por consulta.
	MaxAudienceRefs = 100
)

// AudienceInput es la peticion de campaigns: union de listas y segmentos, menos las
// exclusiones, por paginas.
type AudienceInput struct {
	ListIDs           []uuid.UUID
	SegmentIDs        []uuid.UUID
	ExcludeSegmentIDs []uuid.UUID
	Cursor            string
	Limit             int
}

// AudiencePage es una pagina. NextCursor nil = no hay mas.
type AudiencePage struct {
	Contacts   []domain.Contact
	NextCursor *string
}

// EncodeCursor codifica el ultimo id entregado; es opaco para quien lo recibe.
func EncodeCursor(id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString(id[:])
}

// DecodeCursor valida y decodifica un cursor. Vacio = primera pagina.
func DecodeCursor(s string) (uuid.UUID, error) {
	if s == "" {
		return uuid.Nil, nil
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil || len(raw) != len(uuid.UUID{}) {
		return uuid.Nil, domain.ErrInvalidCursor
	}
	id, err := uuid.FromBytes(raw)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, domain.ErrInvalidCursor
	}
	return id, nil
}

// Audience devuelve una pagina de contactos enviables (status active y consentimiento
// vigente concedido) que estan en alguna de las listas o cumplen alguno de los segmentos,
// y no cumplen ninguno de los excluidos. Sin duplicados y en orden estable de id: una
// campana que reanuda con el cursor no repite ni salta a nadie que ya existiera.
func (uc *UseCase) Audience(ctx context.Context, tenantID uuid.UUID, in AudienceInput) (*AudiencePage, error) {
	lists, include, exclude := dedupeIDs(in.ListIDs), dedupeIDs(in.SegmentIDs), dedupeIDs(in.ExcludeSegmentIDs)
	if len(lists)+len(include) == 0 || len(lists) > MaxAudienceRefs || len(include) > MaxAudienceRefs || len(exclude) > MaxAudienceRefs {
		return nil, domain.ErrInvalidAudience
	}
	limit := in.Limit
	if limit == 0 {
		limit = MaxAudienceLimit
	}
	if limit < 1 || limit > MaxAudienceLimit {
		return nil, domain.ErrInvalidLimit
	}
	after, err := DecodeCursor(in.Cursor)
	if err != nil {
		return nil, err
	}

	if len(lists) > 0 {
		found, err := uc.lists.ExistingIDs(ctx, tenantID, lists)
		if err != nil {
			return nil, err
		}
		if missing := missingIDs(lists, found); len(missing) > 0 {
			return nil, fmt.Errorf("%w: %s", domain.ErrListNotFound, missing[0])
		}
	}
	defs, err := uc.definitions(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	schema := schemaFor(defs)
	includeDefs, err := uc.loadDefinitions(ctx, tenantID, include, schema)
	if err != nil {
		return nil, err
	}
	excludeDefs, err := uc.loadDefinitions(ctx, tenantID, exclude, schema)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, segmentQueryTimeout)
	defer cancel()
	// Se pide uno de mas para saber, sin otra consulta, si queda otra pagina.
	rows, err := uc.query.Audience(ctx, tenantID, ports.AudienceSpec{
		ListIDs: lists, Include: includeDefs, Exclude: excludeDefs, Schema: schema,
		After: after, Limit: limit + 1,
	})
	if err != nil {
		return nil, err
	}
	page := &AudiencePage{Contacts: rows}
	if len(rows) > limit {
		page.Contacts = rows[:limit]
		next := EncodeCursor(rows[limit-1].ID)
		page.NextCursor = &next
	}
	if page.Contacts == nil {
		page.Contacts = []domain.Contact{}
	}
	return page, nil
}

// loadDefinitions trae y valida las definiciones de los segmentos pedidos.
func (uc *UseCase) loadDefinitions(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID, schema segment.Schema) ([]segment.Definition, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	segs, err := uc.segments.GetMany(ctx, tenantID, ids)
	if err != nil {
		return nil, err
	}
	found := make([]uuid.UUID, 0, len(segs))
	for _, s := range segs {
		found = append(found, s.ID)
	}
	if missing := missingIDs(ids, found); len(missing) > 0 {
		return nil, fmt.Errorf("%w: %s", domain.ErrSegmentNotFound, missing[0])
	}
	out := make([]segment.Definition, 0, len(segs))
	for _, s := range segs {
		def, err := segment.Parse(s.Definition)
		if err == nil {
			err = segment.Validate(def, schema)
		}
		if err != nil {
			return nil, asSegmentError(fmt.Errorf("segmento %s: %w", s.ID, err))
		}
		out = append(out, def)
	}
	return out, nil
}

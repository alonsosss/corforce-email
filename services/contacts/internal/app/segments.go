package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/alonsosss/corforce-email/services/contacts/internal/segment"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// previewSample es el numero de contactos de muestra de una previsualizacion.
const previewSample = 10

// checkDefinition lee, valida contra los atributos declarados y comprueba que existan las
// listas referenciadas. Devuelve la definicion, el esquema con que se valido y la forma
// canonica que se guarda.
func (uc *UseCase) checkDefinition(ctx context.Context, tenantID uuid.UUID, raw json.RawMessage) (segment.Definition, segment.Schema, json.RawMessage, error) {
	def, err := segment.Parse(raw)
	if err != nil {
		return segment.Definition{}, segment.Schema{}, nil, asSegmentError(err)
	}
	defs, err := uc.definitions(ctx, tenantID)
	if err != nil {
		return segment.Definition{}, segment.Schema{}, nil, err
	}
	schema := schemaFor(defs)
	if err := segment.Validate(def, schema); err != nil {
		return segment.Definition{}, segment.Schema{}, nil, asSegmentError(err)
	}
	if refs := segment.Refs(def); len(refs.Lists) > 0 {
		ids := make([]uuid.UUID, 0, len(refs.Lists))
		for _, l := range refs.Lists {
			id, err := uuid.Parse(l)
			if err != nil {
				return segment.Definition{}, segment.Schema{}, nil, segmentError{fmt.Errorf("%w: lista %s no válida", segment.ErrInvalid, l)}
			}
			ids = append(ids, id)
		}
		found, err := uc.lists.ExistingIDs(ctx, tenantID, ids)
		if err != nil {
			return segment.Definition{}, segment.Schema{}, nil, err
		}
		if missing := missingIDs(ids, found); len(missing) > 0 {
			return segment.Definition{}, segment.Schema{}, nil, segmentError{fmt.Errorf("%w: la lista %s no existe", segment.ErrInvalid, missing[0])}
		}
	}
	canonical, err := json.Marshal(def)
	if err != nil {
		return segment.Definition{}, segment.Schema{}, nil, err
	}
	return def, schema, canonical, nil
}

// storedDefinition relee la definicion guardada de un segmento con el esquema actual.
func (uc *UseCase) storedDefinition(ctx context.Context, tenantID uuid.UUID, s *domain.Segment) (segment.Definition, segment.Schema, error) {
	def, err := segment.Parse(s.Definition)
	if err != nil {
		return segment.Definition{}, segment.Schema{}, asSegmentError(err)
	}
	defs, err := uc.definitions(ctx, tenantID)
	if err != nil {
		return segment.Definition{}, segment.Schema{}, err
	}
	return def, schemaFor(defs), nil
}

// segmentUsing dice si algun segmento de la empresa cumple match sobre sus referencias.
// Una definicion guardada que no se puede leer se registra y se trata como uso: ante la
// duda no se borra lo que podria estar usando.
func (uc *UseCase) segmentUsing(ctx context.Context, tenantID uuid.UUID, match func(segment.References) bool) (bool, error) {
	all, err := uc.segments.ListAll(ctx, tenantID)
	if err != nil {
		return false, err
	}
	for _, s := range all {
		def, err := segment.Parse(s.Definition)
		if err != nil {
			uc.logger.Error("contacts: definicion de segmento ilegible", zap.String("segment_id", s.ID.String()), zap.Error(err))
			return true, nil
		}
		if match(segment.Refs(def)) {
			return true, nil
		}
	}
	return false, nil
}

type SegmentInput struct {
	Name        string
	Description string
	Definition  json.RawMessage
}

func (uc *UseCase) CreateSegment(ctx context.Context, tenantID uuid.UUID, in SegmentInput) (*domain.Segment, error) {
	name, err := normalizeName(in.Name, domain.ErrInvalidSegmentName)
	if err != nil {
		return nil, err
	}
	_, _, canonical, err := uc.checkDefinition(ctx, tenantID, in.Definition)
	if err != nil {
		return nil, err
	}
	s := &domain.Segment{TenantID: tenantID, Name: name, Description: strings.TrimSpace(in.Description), Definition: canonical}
	if err := uc.segments.Create(ctx, s); err != nil {
		return nil, err
	}
	return s, nil
}

func (uc *UseCase) GetSegment(ctx context.Context, tenantID, id uuid.UUID) (*domain.Segment, error) {
	return uc.segments.Get(ctx, tenantID, id)
}

func (uc *UseCase) ListSegments(ctx context.Context, tenantID uuid.UUID, page, perPage int) ([]domain.Segment, int64, error) {
	return uc.segments.List(ctx, tenantID, page, perPage)
}

type UpdateSegmentInput struct {
	Name        *string
	Description *string
	Definition  json.RawMessage
}

func (uc *UseCase) UpdateSegment(ctx context.Context, tenantID, id uuid.UUID, in UpdateSegmentInput) (*domain.Segment, error) {
	s, err := uc.segments.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if in.Name != nil {
		if s.Name, err = normalizeName(*in.Name, domain.ErrInvalidSegmentName); err != nil {
			return nil, err
		}
	}
	if in.Description != nil {
		s.Description = strings.TrimSpace(*in.Description)
	}
	if len(in.Definition) > 0 {
		_, _, canonical, err := uc.checkDefinition(ctx, tenantID, in.Definition)
		if err != nil {
			return nil, err
		}
		s.Definition = canonical
	}
	if err := uc.segments.Update(ctx, s); err != nil {
		return nil, err
	}
	return s, nil
}

func (uc *UseCase) DeleteSegment(ctx context.Context, tenantID, id uuid.UUID) error {
	return uc.segments.Delete(ctx, tenantID, id)
}

// SegmentMeta es lo que una definicion de esta empresa puede usar: campos, operadores,
// valores de los enumerados y atributos declarados, tomados del dominio y del compilador.
func (uc *UseCase) SegmentMeta(ctx context.Context, tenantID uuid.UUID) (segment.Catalog, error) {
	defs, err := uc.definitions(ctx, tenantID)
	if err != nil {
		return segment.Catalog{}, err
	}
	return segment.Describe(schemaFor(defs)), nil
}

// Preview es el tamano de un segmento y una muestra de sus contactos.
type Preview struct {
	Count  int64            `json:"count"`
	Sample []domain.Contact `json:"sample"`
}

// PreviewSegment evalua una definicion sin guardarla. Cuenta todos los contactos que
// cumplen, enviables o no: la audiencia de un envio se filtra aparte.
func (uc *UseCase) PreviewSegment(ctx context.Context, tenantID uuid.UUID, raw json.RawMessage) (*Preview, error) {
	def, schema, _, err := uc.checkDefinition(ctx, tenantID, raw)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, segmentQueryTimeout)
	defer cancel()
	count, err := uc.query.Count(ctx, tenantID, def, schema)
	if err != nil {
		return nil, err
	}
	sample, err := uc.query.Page(ctx, tenantID, def, schema, previewSample, 0)
	if err != nil {
		return nil, err
	}
	if sample == nil {
		sample = []domain.Contact{}
	}
	return &Preview{Count: count, Sample: sample}, nil
}

// SegmentContacts pagina los contactos que hoy cumplen el segmento.
func (uc *UseCase) SegmentContacts(ctx context.Context, tenantID, id uuid.UUID, page, perPage int) ([]domain.Contact, int64, error) {
	s, err := uc.segments.Get(ctx, tenantID, id)
	if err != nil {
		return nil, 0, err
	}
	def, schema, err := uc.storedDefinition(ctx, tenantID, s)
	if err != nil {
		return nil, 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, segmentQueryTimeout)
	defer cancel()
	total, err := uc.query.Count(ctx, tenantID, def, schema)
	if err != nil {
		return nil, 0, err
	}
	rows, err := uc.query.Page(ctx, tenantID, def, schema, perPage, (page-1)*perPage)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func missingIDs(want, found []uuid.UUID) []uuid.UUID {
	have := make(map[uuid.UUID]struct{}, len(found))
	for _, id := range found {
		have[id] = struct{}{}
	}
	var out []uuid.UUID
	for _, id := range want {
		if _, ok := have[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}

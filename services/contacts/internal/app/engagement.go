package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/alonsosss/corforce-email/services/contacts/internal/ports"
	"github.com/alonsosss/corforce-email/services/contacts/internal/segment"
	"github.com/google/uuid"
)

const (
	// pruneBatch y maxPruneRounds acotan la poda de una empresa por pasada.
	pruneBatch     = 5000
	maxPruneRounds = 200
	// MaxMatchIDs es el tope de ids por consulta de coincidencia (ramas de automations).
	MaxMatchIDs = 500
	// MaxAnniversaryLimit es el tope y el valor por defecto de una tanda de aniversarios.
	MaxAnniversaryLimit = 1000
)

// RecordEngagement proyecta un hito de transactional.email.* en la interaccion del contacto
// con el envio. Devuelve false cuando no guarda nada: hito anterior a la retencion o
// contacto que ya no existe. domain.ErrInvalidEngagement si el hito nunca se podra aplicar.
func (uc *UseCase) RecordEngagement(ctx context.Context, ev domain.EngagementEvent) (bool, error) {
	touch, ok, err := ev.Touch(uc.now(), uc.cfg.EngagementRetention)
	if err != nil || !ok {
		return false, err
	}
	return uc.engagement.Touch(ctx, touch)
}

// PruneEngagement olvida la interaccion sin actividad dentro de la retencion.
func (uc *UseCase) PruneEngagement(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	before := uc.now().Add(-uc.cfg.EngagementRetention)
	var total int64
	for round := 0; round < maxPruneRounds; round++ {
		n, err := uc.engagement.Prune(ctx, tenantID, before, pruneBatch)
		total += n
		if err != nil || n < pruneBatch {
			return total, err
		}
	}
	return total, nil
}

// MatchInput pregunta cuales de unos contactos cumplen un segmento guardado o una
// definicion suelta (exactamente una de las dos). Sin ids solo se valida: automations lo
// usa al activar un flujo para comprobar sus ramas.
type MatchInput struct {
	SegmentID  *uuid.UUID
	Definition json.RawMessage
	ContactIDs []uuid.UUID
}

// ErrMatchSource: la consulta no dice que evaluar o dice las dos cosas.
var ErrMatchSource = errors.New("indique segment_id o definition, no ambos")

// MatchContacts evalua con el mismo compilador que los segmentos y la audiencia. Un
// contacto que no existe no aparece.
func (uc *UseCase) MatchContacts(ctx context.Context, tenantID uuid.UUID, in MatchInput) ([]uuid.UUID, error) {
	if (in.SegmentID == nil) == (len(in.Definition) == 0) {
		return nil, ErrMatchSource
	}
	ids := dedupeIDs(in.ContactIDs)
	if len(ids) > MaxMatchIDs {
		return nil, domain.ErrInvalidContactIDs
	}
	var (
		def    segment.Definition
		schema segment.Schema
		err    error
	)
	if in.SegmentID != nil {
		s, gerr := uc.segments.Get(ctx, tenantID, *in.SegmentID)
		if gerr != nil {
			return nil, gerr
		}
		def, schema, err = uc.storedDefinition(ctx, tenantID, s)
		if err == nil {
			err = asSegmentError(segment.Validate(def, schema))
		}
	} else {
		def, schema, _, err = uc.checkDefinition(ctx, tenantID, in.Definition)
	}
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []uuid.UUID{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, segmentQueryTimeout)
	defer cancel()
	out, err := uc.matcher.MatchAmong(ctx, tenantID, def, schema, ids)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []uuid.UUID{}
	}
	return out, nil
}

// AnniversaryInput es una tanda de contactos cuyo aniversario del atributo de fecha cae hoy
// a la hora Hour o despues, en su zona o en FallbackTimezone si no la tienen.
type AnniversaryInput struct {
	Attribute        string
	Hour             int
	FallbackTimezone string
	ListID           *uuid.UUID
	Cursor           string
	Limit            int
}

// AnniversaryMatch es un contacto y la fecha local de su aniversario de este ano, que
// automations usa como clave de entrada (una vez al ano).
type AnniversaryMatch struct {
	ContactID  uuid.UUID `json:"contact_id"`
	Occurrence string    `json:"occurrence"`
}

// AnniversaryPage: NextCursor nil = recorrido terminado. Una tanda puede no traer ninguna
// coincidencia y aun asi tener cursor: el prefiltro de la base no conoce las zonas.
type AnniversaryPage struct {
	Matches    []AnniversaryMatch
	NextCursor *string
}

func (uc *UseCase) Anniversaries(ctx context.Context, tenantID uuid.UUID, in AnniversaryInput) (*AnniversaryPage, error) {
	if in.Hour < 0 || in.Hour > domain.MaxAnniversaryHour {
		return nil, domain.ErrInvalidAnniversary
	}
	fallback, err := time.LoadLocation(in.FallbackTimezone)
	if err != nil || in.FallbackTimezone == "" || in.FallbackTimezone == "Local" {
		return nil, domain.ErrInvalidAnniversary
	}
	defs, err := uc.definitions(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if d, ok := defs[in.Attribute]; !ok || d.Type != domain.AttrDate {
		return nil, fmt.Errorf("%w: %q no es un atributo de fecha declarado", domain.ErrInvalidAnniversary, in.Attribute)
	}
	limit := in.Limit
	if limit == 0 {
		limit = MaxAnniversaryLimit
	}
	if limit < 1 || limit > MaxAnniversaryLimit {
		return nil, domain.ErrInvalidLimit
	}
	after, err := DecodeCursor(in.Cursor)
	if err != nil {
		return nil, err
	}
	if in.ListID != nil {
		if _, err := uc.lists.Get(ctx, tenantID, *in.ListID); err != nil {
			return nil, err
		}
	}
	now := uc.now()
	ctx, cancel := context.WithTimeout(ctx, segmentQueryTimeout)
	defer cancel()
	candidates, err := uc.matcher.AnniversaryCandidates(ctx, tenantID, ports.AnniversaryQuery{
		Attribute: in.Attribute, MonthDays: domain.AnniversaryCandidates(now), ListID: in.ListID,
		After: after, Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	page := &AnniversaryPage{Matches: []AnniversaryMatch{}}
	zones := map[string]*time.Location{}
	for _, c := range candidates {
		loc := fallback
		if c.Timezone != nil && *c.Timezone != "" {
			z, seen := zones[*c.Timezone]
			if !seen {
				z, _ = time.LoadLocation(*c.Timezone)
				zones[*c.Timezone] = z
			}
			if z != nil {
				loc = z
			}
		}
		if occ, ok := domain.AnniversaryOccurrence(c.Value, loc, now, in.Hour); ok {
			page.Matches = append(page.Matches, AnniversaryMatch{ContactID: c.ID, Occurrence: occ})
		}
	}
	if len(candidates) == limit {
		next := EncodeCursor(candidates[len(candidates)-1].ID)
		page.NextCursor = &next
	}
	return page, nil
}

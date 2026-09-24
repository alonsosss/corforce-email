package apptest

import (
	"context"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/alonsosss/corforce-email/services/automations/internal/ports"
	"github.com/google/uuid"
)

// RunMessages guarda el correo de cada paso con la unicidad de la base: uno por (run,
// paso) y uno por mensaje.
type RunMessages struct {
	mu   sync.Mutex
	Rows []domain.RunMessage
}

func (r *RunMessages) Record(_ context.Context, m domain.RunMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.Rows {
		if (x.RunID == m.RunID && x.StepID == m.StepID) || x.MessageID == m.MessageID {
			return nil
		}
	}
	r.Rows = append(r.Rows, m)
	return nil
}

func (r *RunMessages) Get(_ context.Context, tenantID, runID uuid.UUID, stepID string) (*domain.RunMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.Rows {
		if x.TenantID == tenantID && x.RunID == runID && x.StepID == stepID {
			out := x
			return &out, nil
		}
	}
	return nil, nil
}

func (r *RunMessages) MarkEngagement(_ context.Context, tenantID, messageID uuid.UUID, openedAt, clickedAt *time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, x := range r.Rows {
		if x.TenantID != tenantID || x.MessageID != messageID {
			continue
		}
		if x.OpenedAt == nil && openedAt != nil {
			t := *openedAt
			r.Rows[i].OpenedAt = &t
		}
		if x.ClickedAt == nil && clickedAt != nil {
			t := *clickedAt
			r.Rows[i].ClickedAt = &t
		}
		return true, nil
	}
	return false, nil
}

// ByMessage devuelve la fila del mensaje.
func (r *RunMessages) ByMessage(id uuid.UUID) (domain.RunMessage, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.Rows {
		if x.MessageID == id {
			return x, true
		}
	}
	return domain.RunMessage{}, false
}

// DateScans reserva como la base: un recorrido por flujo y ventana.
type DateScans struct {
	mu   sync.Mutex
	Last map[uuid.UUID]time.Time
}

func NewDateScans() *DateScans { return &DateScans{Last: map[uuid.UUID]time.Time{}} }

func (d *DateScans) Claim(_ context.Context, _ uuid.UUID, workflowID uuid.UUID, now, since time.Time) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if last, ok := d.Last[workflowID]; ok && !last.Before(since) {
		return false, nil
	}
	d.Last[workflowID] = now
	return true, nil
}

// Rules es contacts evaluando reglas: Matching dice que contactos cumplen cada segmento o
// cada definicion (por su texto), Anniversaries devuelve sus paginas en orden.
type Rules struct {
	mu             sync.Mutex
	Segments       map[uuid.UUID]map[uuid.UUID]bool
	Definitions    map[string]map[uuid.UUID]bool
	MatchErr       error
	Pages          []ports.AnniversaryPage
	AnniversaryErr error
	MatchCalls     []ports.MatchQuery
	AnnivCalls     []ports.AnniversaryQuery
}

func NewRules() *Rules {
	return &Rules{Segments: map[uuid.UUID]map[uuid.UUID]bool{}, Definitions: map[string]map[uuid.UUID]bool{}}
}

func (r *Rules) Match(_ context.Context, _ uuid.UUID, q ports.MatchQuery) ([]uuid.UUID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.MatchCalls = append(r.MatchCalls, q)
	if r.MatchErr != nil {
		return nil, r.MatchErr
	}
	var set map[uuid.UUID]bool
	if q.SegmentID != nil {
		s, ok := r.Segments[*q.SegmentID]
		if !ok {
			return nil, &ports.RejectedError{Status: 404, Code: "NOT_FOUND", Message: "segmento no encontrado"}
		}
		set = s
	} else {
		set = r.Definitions[string(q.Definition)]
	}
	out := []uuid.UUID{}
	for _, id := range q.ContactIDs {
		if set[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func (r *Rules) Anniversaries(_ context.Context, _ uuid.UUID, q ports.AnniversaryQuery) (*ports.AnniversaryPage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.AnnivCalls = append(r.AnnivCalls, q)
	if r.AnniversaryErr != nil {
		return nil, r.AnniversaryErr
	}
	idx := 0
	if q.Cursor != "" {
		for i := range r.Pages {
			if r.Pages[i].NextCursor == q.Cursor {
				idx = i + 1
			}
		}
	}
	if idx >= len(r.Pages) {
		return &ports.AnniversaryPage{}, nil
	}
	p := r.Pages[idx]
	return &p, nil
}

var (
	_ ports.RunMessageRepository = (*RunMessages)(nil)
	_ ports.DateScanRepository   = (*DateScans)(nil)
	_ ports.ContactRules         = (*Rules)(nil)
)

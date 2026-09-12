package app

import (
	"context"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/suppression/internal/domain"
	"github.com/alonsosss/corforce-email/services/suppression/internal/ports"
	"github.com/google/uuid"
)

// fakeEntryRepo guarda las exclusiones en memoria con la misma unicidad que la tabla.
type fakeEntryRepo struct {
	entries []*domain.Entry
}

func (f *fakeEntryRepo) find(tenantID uuid.UUID, email string) *domain.Entry {
	for _, e := range f.entries {
		if e.TenantID == tenantID && e.Email == email {
			return e
		}
	}
	return nil
}

func (f *fakeEntryRepo) GetByID(_ context.Context, tenantID, id uuid.UUID) (*domain.Entry, error) {
	for _, e := range f.entries {
		if e.TenantID == tenantID && e.ID == id {
			return e, nil
		}
	}
	return nil, domain.ErrEntryNotFound
}

func (f *fakeEntryRepo) GetByEmailForUpdate(_ context.Context, tenantID uuid.UUID, email string) (*domain.Entry, error) {
	if e := f.find(tenantID, email); e != nil {
		return e, nil
	}
	return nil, domain.ErrEntryNotFound
}

func (f *fakeEntryRepo) FindByEmails(_ context.Context, tenantID uuid.UUID, emails []string) ([]domain.Entry, error) {
	var out []domain.Entry
	for _, email := range emails {
		if e := f.find(tenantID, email); e != nil {
			out = append(out, *e)
		}
	}
	return out, nil
}

func (f *fakeEntryRepo) List(_ context.Context, tenantID uuid.UUID, fl ports.ListFilter) ([]domain.Entry, int64, error) {
	var out []domain.Entry
	for _, e := range f.entries {
		if e.TenantID != tenantID {
			continue
		}
		if fl.Reason != "" && e.Reason != fl.Reason {
			continue
		}
		if fl.Search != "" && !strings.Contains(e.Email, fl.Search) {
			continue
		}
		out = append(out, *e)
	}
	return out, int64(len(out)), nil
}

func (f *fakeEntryRepo) Insert(_ context.Context, e *domain.Entry) error {
	if f.find(e.TenantID, e.Email) != nil {
		return domain.ErrEntryAlreadyExists
	}
	e.ID = uuid.New()
	e.CreatedAt = time.Now()
	e.UpdatedAt = e.CreatedAt
	f.entries = append(f.entries, e)
	return nil
}

func (f *fakeEntryRepo) InsertMissing(ctx context.Context, tenantID uuid.UUID, emails []string, reason domain.Reason, source, detail string) ([]domain.Entry, error) {
	var added []domain.Entry
	for _, email := range emails {
		e := &domain.Entry{TenantID: tenantID, Email: email, Reason: reason, Source: source, Detail: detail}
		if err := f.Insert(ctx, e); err != nil {
			continue
		}
		added = append(added, *e)
	}
	return added, nil
}

func (f *fakeEntryRepo) Update(_ context.Context, e *domain.Entry) error {
	for _, cur := range f.entries {
		if cur.ID == e.ID {
			*cur = *e
			return nil
		}
	}
	return domain.ErrEntryNotFound
}

func (f *fakeEntryRepo) Delete(_ context.Context, tenantID, id uuid.UUID) error {
	for i, e := range f.entries {
		if e.TenantID == tenantID && e.ID == id {
			f.entries = append(f.entries[:i], f.entries[i+1:]...)
			return nil
		}
	}
	return domain.ErrEntryNotFound
}

func (f *fakeEntryRepo) CountByReason(_ context.Context, tenantID uuid.UUID, now time.Time) ([]domain.ReasonCount, error) {
	counts := map[domain.Reason]int64{}
	for _, e := range f.entries {
		if e.TenantID == tenantID && e.Active(now) {
			counts[e.Reason]++
		}
	}
	var out []domain.ReasonCount
	for r, c := range counts {
		out = append(out, domain.ReasonCount{Reason: r, Count: c})
	}
	return out, nil
}

type fakeImportRepo struct {
	imports []*domain.Import
}

func (f *fakeImportRepo) Create(_ context.Context, imp *domain.Import) error {
	imp.ID = uuid.New()
	imp.CreatedAt = time.Now()
	f.imports = append(f.imports, imp)
	return nil
}

func (f *fakeImportRepo) List(_ context.Context, tenantID uuid.UUID, _, _ int) ([]domain.Import, int64, error) {
	var out []domain.Import
	for _, i := range f.imports {
		if i.TenantID == tenantID {
			out = append(out, *i)
		}
	}
	return out, int64(len(out)), nil
}

// fakeTx ejecuta el cuerpo sin transaccion real.
type fakeTx struct{}

func (fakeTx) Transact(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

// fakePublisher registra los eventos encolados como "subject|email|reason".
type fakePublisher struct {
	events []string
}

func (f *fakePublisher) EntryAdded(_ context.Context, e *domain.Entry) error {
	f.events = append(f.events, "added|"+e.Email+"|"+string(e.Reason))
	return nil
}

func (f *fakePublisher) EntryRemoved(_ context.Context, e *domain.Entry) error {
	f.events = append(f.events, "removed|"+e.Email+"|"+string(e.Reason))
	return nil
}

type fixture struct {
	uc      *UseCase
	entries *fakeEntryRepo
	imports *fakeImportRepo
	events  *fakePublisher
	now     time.Time
	tenant  uuid.UUID
}

func newFixture() *fixture {
	f := &fixture{
		entries: &fakeEntryRepo{},
		imports: &fakeImportRepo{},
		events:  &fakePublisher{},
		now:     time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC),
		tenant:  uuid.New(),
	}
	f.uc = New(Deps{
		Entries: f.entries, Imports: f.imports, Tx: fakeTx{}, Events: f.events,
		Now: func() time.Time { return f.now },
	})
	return f
}

package app

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/suppression/internal/domain"
	"github.com/alonsosss/corforce-email/services/suppression/internal/ports"
	"github.com/google/uuid"
)

// fakeEntryRepo guarda las causas en memoria con la misma unicidad que la tabla: una fila
// por (empresa, direccion, causa).
type fakeEntryRepo struct {
	entries []*domain.Entry
	locks   []string
}

func (f *fakeEntryRepo) find(tenantID uuid.UUID, email string, reason domain.Reason) *domain.Entry {
	for _, e := range f.entries {
		if e.TenantID == tenantID && e.Email == email && e.Reason == reason {
			return e
		}
	}
	return nil
}

// reasonsOf devuelve las causas guardadas de una direccion, vigentes o no, ordenadas.
func (f *fakeEntryRepo) reasonsOf(tenantID uuid.UUID, email string) []string {
	var out []string
	for _, e := range f.entries {
		if e.TenantID == tenantID && e.Email == email {
			out = append(out, string(e.Reason))
		}
	}
	sort.Strings(out)
	return out
}

func (f *fakeEntryRepo) GetByID(_ context.Context, tenantID, id uuid.UUID) (*domain.Entry, error) {
	for _, e := range f.entries {
		if e.TenantID == tenantID && e.ID == id {
			return e, nil
		}
	}
	return nil, domain.ErrEntryNotFound
}

func (f *fakeEntryRepo) LockAddress(_ context.Context, tenantID uuid.UUID, email string) error {
	f.locks = append(f.locks, tenantID.String()+":"+email)
	return nil
}

func (f *fakeEntryRepo) GetCauseForUpdate(_ context.Context, tenantID uuid.UUID, email string, reason domain.Reason) (*domain.Entry, error) {
	if e := f.find(tenantID, email, reason); e != nil {
		return e, nil
	}
	return nil, domain.ErrEntryNotFound
}

func (f *fakeEntryRepo) FindByEmails(_ context.Context, tenantID uuid.UUID, emails []string) ([]domain.Entry, error) {
	var out []domain.Entry
	for _, email := range emails {
		for _, e := range f.entries {
			if e.TenantID == tenantID && e.Email == email {
				out = append(out, *e)
			}
		}
	}
	return out, nil
}

func (f *fakeEntryRepo) ListAddresses(ctx context.Context, tenantID uuid.UUID, fl ports.ListFilter, now time.Time) ([]string, int64, error) {
	var all []domain.Entry
	for _, e := range f.entries {
		if e.TenantID == tenantID && (fl.Search == "" || strings.Contains(e.Email, fl.Search)) {
			all = append(all, *e)
		}
	}
	var out []string
	for _, a := range domain.Aggregate(all, now) {
		if fl.Reason == "" || a.Reason == fl.Reason {
			out = append(out, a.Email)
		}
	}
	return out, int64(len(out)), nil
}

func (f *fakeEntryRepo) Insert(_ context.Context, e *domain.Entry) error {
	if f.find(e.TenantID, e.Email, e.Reason) != nil {
		return domain.ErrEntryAlreadyExists
	}
	e.ID = uuid.New()
	e.CreatedAt = time.Now()
	e.UpdatedAt = e.CreatedAt
	f.entries = append(f.entries, e)
	return nil
}

func (f *fakeEntryRepo) InsertMissing(ctx context.Context, tenantID uuid.UUID, emails []string, reason domain.Reason, source, detail string, now time.Time) ([]domain.Entry, error) {
	var added []domain.Entry
	for _, email := range emails {
		if cur := f.find(tenantID, email, reason); cur != nil {
			if cur.Active(now) {
				continue
			}
			cur.Source, cur.Detail, cur.MessageID, cur.CampaignID, cur.ExpiresAt = source, detail, nil, nil, nil
			added = append(added, *cur)
			continue
		}
		e := &domain.Entry{TenantID: tenantID, Email: email, Reason: reason, Source: source, Detail: detail}
		if err := f.Insert(ctx, e); err != nil {
			return nil, err
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
	var all []domain.Entry
	for _, e := range f.entries {
		if e.TenantID == tenantID {
			all = append(all, *e)
		}
	}
	counts := map[domain.Reason]int64{}
	for _, a := range domain.Aggregate(all, now) {
		if a.Active() {
			counts[a.Reason]++
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

// fakePublisher registra los eventos encolados como "subject|email|reason|restantes",
// con las causas restantes separadas por comas.
type fakePublisher struct {
	events []string
}

func joinReasons(reasons []domain.Reason) string {
	parts := make([]string, len(reasons))
	for i, r := range reasons {
		parts[i] = string(r)
	}
	return strings.Join(parts, ",")
}

func (f *fakePublisher) EntryAdded(_ context.Context, e *domain.Entry, reasons []domain.Reason) error {
	f.events = append(f.events, "added|"+e.Email+"|"+string(e.Reason)+"|"+joinReasons(reasons))
	return nil
}

func (f *fakePublisher) EntryRemoved(_ context.Context, e *domain.Entry, reasons []domain.Reason) error {
	f.events = append(f.events, "removed|"+e.Email+"|"+string(e.Reason)+"|"+joinReasons(reasons))
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

// cause crea una fila ya guardada, como la dejaria el repositorio.
func (f *fixture) cause(email string, reason domain.Reason, expiresAt *time.Time) *domain.Entry {
	e := &domain.Entry{ID: uuid.New(), TenantID: f.tenant, Email: email, Reason: reason, Source: "seed", ExpiresAt: expiresAt}
	f.entries.entries = append(f.entries.entries, e)
	return e
}

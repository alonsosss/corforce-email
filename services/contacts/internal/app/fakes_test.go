package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/alonsosss/corforce-email/services/contacts/internal/ports"
	"github.com/alonsosss/corforce-email/services/contacts/internal/segment"
	"github.com/google/uuid"
)

// fakeStore guarda en memoria lo que las tablas guardan, con las mismas reglas que la
// base: unicidad, proyeccion del consentimiento vigente (el trigger) y seudonimizacion
// en el borrado. Los repositorios devuelven copias, como una fila leida de nuevo.
type fakeStore struct {
	now      func() time.Time
	contacts map[uuid.UUID]*domain.Contact
	consents []*domain.Consent
	tokens   []*domain.ConfirmationToken
	lists    map[uuid.UUID]*domain.List
	members  map[uuid.UUID]map[uuid.UUID]bool
	attrs    []*domain.AttributeDefinition
	segments map[uuid.UUID]*domain.Segment
	imports  []*domain.Import
}

func newStore(now func() time.Time) *fakeStore {
	return &fakeStore{
		now: now, contacts: map[uuid.UUID]*domain.Contact{}, lists: map[uuid.UUID]*domain.List{},
		members: map[uuid.UUID]map[uuid.UUID]bool{}, segments: map[uuid.UUID]*domain.Segment{},
	}
}

func copyContact(c *domain.Contact) *domain.Contact {
	out := *c
	out.Attributes = make(map[string]any, len(c.Attributes))
	for k, v := range c.Attributes {
		out.Attributes[k] = v
	}
	out.Tags = append([]string{}, c.Tags...)
	return &out
}

// ── Contactos ────────────────────────────────────────────────────────────────

type fakeContacts struct{ s *fakeStore }

func (f fakeContacts) find(tenantID uuid.UUID, pred func(*domain.Contact) bool) *domain.Contact {
	for _, c := range f.s.contacts {
		if c.TenantID == tenantID && pred(c) {
			return c
		}
	}
	return nil
}

func (f fakeContacts) Insert(_ context.Context, c *domain.Contact) error {
	if f.find(c.TenantID, func(x *domain.Contact) bool { return x.Email == c.Email }) != nil {
		return domain.ErrContactExists
	}
	c.ID = uuid.New()
	c.CreatedAt, c.UpdatedAt = f.s.now(), f.s.now()
	if c.ConsentStatus == "" {
		c.ConsentStatus = domain.ConsentNone
	}
	if c.Attributes == nil {
		c.Attributes = map[string]any{}
	}
	if c.Tags == nil {
		c.Tags = []string{}
	}
	f.s.contacts[c.ID] = copyContact(c)
	return nil
}

func (f fakeContacts) GetByID(_ context.Context, tenantID, id uuid.UUID) (*domain.Contact, error) {
	if c, ok := f.s.contacts[id]; ok && c.TenantID == tenantID {
		return copyContact(c), nil
	}
	return nil, domain.ErrContactNotFound
}

func (f fakeContacts) GetForUpdate(ctx context.Context, tenantID, id uuid.UUID) (*domain.Contact, error) {
	return f.GetByID(ctx, tenantID, id)
}

func (f fakeContacts) GetByEmailForUpdate(_ context.Context, tenantID uuid.UUID, email string) (*domain.Contact, error) {
	if c := f.find(tenantID, func(x *domain.Contact) bool { return x.Email == email }); c != nil {
		return copyContact(c), nil
	}
	return nil, domain.ErrContactNotFound
}

func (f fakeContacts) Update(_ context.Context, c *domain.Contact) error {
	cur, ok := f.s.contacts[c.ID]
	if !ok || cur.TenantID != c.TenantID {
		return domain.ErrContactNotFound
	}
	next := copyContact(c)
	next.ConsentStatus = cur.ConsentStatus // la proyeccion la escribe la base, no Update
	next.UpdatedAt = f.s.now()
	f.s.contacts[c.ID] = next
	return nil
}

func (f fakeContacts) List(_ context.Context, tenantID uuid.UUID, fl ports.ContactFilter) ([]domain.Contact, int64, error) {
	var out []domain.Contact
	for _, c := range f.s.contacts {
		if c.TenantID != tenantID || (fl.Status != "" && c.Status != fl.Status) {
			continue
		}
		if fl.Search != "" && !strings.Contains(c.Email, fl.Search) {
			continue
		}
		out = append(out, *copyContact(c))
	}
	return out, int64(len(out)), nil
}

// ListAfter recorre en orden de id, como el keyset de la base.
func (f fakeContacts) ListAfter(_ context.Context, tenantID uuid.UUID, after uuid.UUID, limit int) ([]domain.Contact, error) {
	var out []domain.Contact
	for _, c := range f.s.contacts {
		if c.TenantID != tenantID || bytes.Compare(c.ID[:], after[:]) <= 0 {
			continue
		}
		out = append(out, *copyContact(c))
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i].ID[:], out[j].ID[:]) < 0 })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f fakeContacts) FindByEmailsForUpdate(_ context.Context, tenantID uuid.UUID, emails []string) ([]domain.Contact, error) {
	var out []domain.Contact
	for _, e := range emails {
		if c := f.find(tenantID, func(x *domain.Contact) bool { return x.Email == e }); c != nil {
			out = append(out, *copyContact(c))
		}
	}
	return out, nil
}

func (f fakeContacts) InsertMany(ctx context.Context, contacts []domain.Contact) ([]domain.Contact, error) {
	var out []domain.Contact
	for i := range contacts {
		c := contacts[i]
		if err := f.Insert(ctx, &c); err != nil {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func (f fakeContacts) UpdateMany(_ context.Context, contacts []domain.Contact) error {
	for _, c := range contacts {
		cur, ok := f.s.contacts[c.ID]
		if !ok {
			continue
		}
		next := copyContact(&c)
		next.Status, next.ConsentStatus = cur.Status, cur.ConsentStatus
		f.s.contacts[c.ID] = next
	}
	return nil
}

func (f fakeContacts) Erase(_ context.Context, tenantID, id, pseudonym uuid.UUID, sha string) error {
	c, ok := f.s.contacts[id]
	if !ok || c.TenantID != tenantID {
		return domain.ErrContactNotFound
	}
	delete(f.s.contacts, id)
	for _, m := range f.s.members {
		delete(m, id)
	}
	var keep []*domain.ConfirmationToken
	for _, t := range f.s.tokens {
		if t.ContactID != id {
			keep = append(keep, t)
		}
	}
	f.s.tokens = keep
	for _, cs := range f.s.consents {
		if cs.ContactID == id {
			cs.ContactID, cs.IP, cs.UserAgent = pseudonym, nil, nil
			cs.Evidence["email_sha256"] = sha
		}
	}
	return nil
}

func (f fakeContacts) StripAttribute(_ context.Context, tenantID uuid.UUID, key string) error {
	for _, c := range f.s.contacts {
		if c.TenantID == tenantID {
			delete(c.Attributes, key)
		}
	}
	return nil
}

// ── Consentimiento y tokens ──────────────────────────────────────────────────

type fakeConsents struct{ s *fakeStore }

func (f fakeConsents) Append(_ context.Context, c *domain.Consent) error {
	c.ID = uuid.New()
	c.OccurredAt = f.s.now()
	if c.Evidence == nil {
		c.Evidence = map[string]any{}
	}
	stored := *c
	f.s.consents = append(f.s.consents, &stored)
	if cur, ok := f.s.contacts[c.ContactID]; ok && c.Purpose == domain.PurposeMarketing {
		cur.ConsentStatus = c.Status
	}
	return nil
}

func (f fakeConsents) AppendMany(ctx context.Context, cs []domain.Consent) error {
	for i := range cs {
		if err := f.Append(ctx, &cs[i]); err != nil {
			return err
		}
	}
	return nil
}

func (f fakeConsents) ListByContact(_ context.Context, tenantID, contactID uuid.UUID) ([]domain.Consent, error) {
	var out []domain.Consent
	for _, c := range f.s.consents {
		if c.TenantID == tenantID && c.ContactID == contactID {
			out = append(out, *c)
		}
	}
	return out, nil
}

type fakeTokens struct{ s *fakeStore }

func (f fakeTokens) Create(_ context.Context, t *domain.ConfirmationToken) error {
	t.ID = uuid.New()
	t.CreatedAt = f.s.now()
	stored := *t
	f.s.tokens = append(f.s.tokens, &stored)
	return nil
}

func (f fakeTokens) DeleteUnused(_ context.Context, tenantID, contactID uuid.UUID) error {
	var keep []*domain.ConfirmationToken
	for _, t := range f.s.tokens {
		if !(t.TenantID == tenantID && t.ContactID == contactID && t.UsedAt == nil) {
			keep = append(keep, t)
		}
	}
	f.s.tokens = keep
	return nil
}

func (f fakeTokens) GetByHash(_ context.Context, tenantID uuid.UUID, hash string) (*domain.ConfirmationToken, error) {
	for _, t := range f.s.tokens {
		if t.TenantID == tenantID && t.TokenHash == hash {
			out := *t
			return &out, nil
		}
	}
	return nil, domain.ErrInvalidConfirmation
}

func (f fakeTokens) GetByHashForUpdate(ctx context.Context, tenantID uuid.UUID, hash string) (*domain.ConfirmationToken, error) {
	return f.GetByHash(ctx, tenantID, hash)
}

func (f fakeTokens) MarkUsed(_ context.Context, tenantID, id uuid.UUID, at time.Time) error {
	for _, t := range f.s.tokens {
		if t.TenantID == tenantID && t.ID == id && t.UsedAt == nil {
			t.UsedAt = &at
			return nil
		}
	}
	return domain.ErrInvalidConfirmation
}

// ── Listas, atributos, segmentos, importaciones ──────────────────────────────

type fakeLists struct{ s *fakeStore }

func (f fakeLists) Create(_ context.Context, l *domain.List) error {
	for _, x := range f.s.lists {
		if x.TenantID == l.TenantID && x.Name == l.Name {
			return domain.ErrListExists
		}
	}
	l.ID = uuid.New()
	stored := *l
	f.s.lists[l.ID] = &stored
	f.s.members[l.ID] = map[uuid.UUID]bool{}
	return nil
}

func (f fakeLists) Get(_ context.Context, tenantID, id uuid.UUID) (*domain.List, error) {
	l, ok := f.s.lists[id]
	if !ok || l.TenantID != tenantID {
		return nil, domain.ErrListNotFound
	}
	out := *l
	out.MemberCount = int64(len(f.s.members[id]))
	return &out, nil
}

func (f fakeLists) Update(_ context.Context, l *domain.List) error {
	if _, ok := f.s.lists[l.ID]; !ok {
		return domain.ErrListNotFound
	}
	stored := *l
	f.s.lists[l.ID] = &stored
	return nil
}

func (f fakeLists) Delete(_ context.Context, tenantID, id uuid.UUID) error {
	if l, ok := f.s.lists[id]; !ok || l.TenantID != tenantID {
		return domain.ErrListNotFound
	}
	delete(f.s.lists, id)
	delete(f.s.members, id)
	return nil
}

func (f fakeLists) List(ctx context.Context, tenantID uuid.UUID, _, _ int) ([]domain.List, int64, error) {
	var out []domain.List
	for id, l := range f.s.lists {
		if l.TenantID == tenantID {
			got, _ := f.Get(ctx, tenantID, id)
			out = append(out, *got)
		}
	}
	return out, int64(len(out)), nil
}

func (f fakeLists) ExistingIDs(_ context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error) {
	var out []uuid.UUID
	for _, id := range ids {
		if l, ok := f.s.lists[id]; ok && l.TenantID == tenantID {
			out = append(out, id)
		}
	}
	return out, nil
}

func (f fakeLists) AddMembers(_ context.Context, tenantID, listID uuid.UUID, ids []uuid.UUID) (int, error) {
	n := 0
	for _, id := range ids {
		c, ok := f.s.contacts[id]
		if !ok || c.TenantID != tenantID || f.s.members[listID][id] {
			continue
		}
		f.s.members[listID][id] = true
		n++
	}
	return n, nil
}

func (f fakeLists) RemoveMembers(_ context.Context, _ uuid.UUID, listID uuid.UUID, contactIDs []uuid.UUID) (int, error) {
	n := 0
	for _, id := range contactIDs {
		if f.s.members[listID][id] {
			delete(f.s.members[listID], id)
			n++
		}
	}
	return n, nil
}

func (f fakeLists) MembersAmong(_ context.Context, tenantID, listID uuid.UUID, contactIDs []uuid.UUID) ([]uuid.UUID, error) {
	var out []uuid.UUID
	for _, id := range contactIDs {
		if c, ok := f.s.contacts[id]; ok && c.TenantID == tenantID && f.s.members[listID][id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func (f fakeLists) ListsOf(ctx context.Context, tenantID, contactID uuid.UUID) ([]domain.List, error) {
	var out []domain.List
	for id, m := range f.s.members {
		if m[contactID] {
			l, err := f.Get(ctx, tenantID, id)
			if err == nil {
				out = append(out, *l)
			}
		}
	}
	return out, nil
}

type fakeAttributes struct{ s *fakeStore }

func (f fakeAttributes) List(_ context.Context, tenantID uuid.UUID) ([]domain.AttributeDefinition, error) {
	var out []domain.AttributeDefinition
	for _, d := range f.s.attrs {
		if d.TenantID == tenantID {
			out = append(out, *d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (f fakeAttributes) Get(_ context.Context, tenantID uuid.UUID, key string) (*domain.AttributeDefinition, error) {
	for _, d := range f.s.attrs {
		if d.TenantID == tenantID && d.Key == key {
			out := *d
			return &out, nil
		}
	}
	return nil, domain.ErrAttributeNotFound
}

func (f fakeAttributes) Create(ctx context.Context, d *domain.AttributeDefinition) error {
	if _, err := f.Get(ctx, d.TenantID, d.Key); err == nil {
		return domain.ErrAttributeExists
	}
	d.ID = uuid.New()
	stored := *d
	f.s.attrs = append(f.s.attrs, &stored)
	return nil
}

func (f fakeAttributes) Update(_ context.Context, d *domain.AttributeDefinition) error {
	for i, x := range f.s.attrs {
		if x.TenantID == d.TenantID && x.Key == d.Key {
			stored := *d
			f.s.attrs[i] = &stored
			return nil
		}
	}
	return domain.ErrAttributeNotFound
}

func (f fakeAttributes) Delete(_ context.Context, tenantID uuid.UUID, key string) error {
	for i, x := range f.s.attrs {
		if x.TenantID == tenantID && x.Key == key {
			f.s.attrs = append(f.s.attrs[:i], f.s.attrs[i+1:]...)
			return nil
		}
	}
	return domain.ErrAttributeNotFound
}

type fakeSegments struct{ s *fakeStore }

func (f fakeSegments) Create(_ context.Context, s *domain.Segment) error {
	for _, x := range f.s.segments {
		if x.TenantID == s.TenantID && x.Name == s.Name {
			return domain.ErrSegmentExists
		}
	}
	s.ID = uuid.New()
	stored := *s
	f.s.segments[s.ID] = &stored
	return nil
}

func (f fakeSegments) Get(_ context.Context, tenantID, id uuid.UUID) (*domain.Segment, error) {
	s, ok := f.s.segments[id]
	if !ok || s.TenantID != tenantID {
		return nil, domain.ErrSegmentNotFound
	}
	out := *s
	return &out, nil
}

func (f fakeSegments) Update(_ context.Context, s *domain.Segment) error {
	stored := *s
	f.s.segments[s.ID] = &stored
	return nil
}

func (f fakeSegments) Delete(_ context.Context, _ uuid.UUID, id uuid.UUID) error {
	if _, ok := f.s.segments[id]; !ok {
		return domain.ErrSegmentNotFound
	}
	delete(f.s.segments, id)
	return nil
}

func (f fakeSegments) List(ctx context.Context, tenantID uuid.UUID, _, _ int) ([]domain.Segment, int64, error) {
	all, _ := f.ListAll(ctx, tenantID)
	return all, int64(len(all)), nil
}

func (f fakeSegments) ListAll(_ context.Context, tenantID uuid.UUID) ([]domain.Segment, error) {
	var out []domain.Segment
	for _, s := range f.s.segments {
		if s.TenantID == tenantID {
			out = append(out, *s)
		}
	}
	return out, nil
}

func (f fakeSegments) GetMany(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]domain.Segment, error) {
	var out []domain.Segment
	for _, id := range ids {
		if s, err := f.Get(ctx, tenantID, id); err == nil {
			out = append(out, *s)
		}
	}
	return out, nil
}

// fakeQuery no evalua los segmentos (eso lo prueba el paquete segment y la integracion):
// devuelve los contactos enviables, filtrados por lista si la hay, en orden de id.
type fakeQuery struct {
	s        *fakeStore
	lastSpec *ports.AudienceSpec
}

func (f *fakeQuery) Count(_ context.Context, tenantID uuid.UUID, _ segment.Definition, _ segment.Schema) (int64, error) {
	n := int64(0)
	for _, c := range f.s.contacts {
		if c.TenantID == tenantID {
			n++
		}
	}
	return n, nil
}

func (f *fakeQuery) Page(_ context.Context, tenantID uuid.UUID, _ segment.Definition, _ segment.Schema, limit, _ int) ([]domain.Contact, error) {
	var out []domain.Contact
	for _, c := range f.s.contacts {
		if c.TenantID == tenantID && len(out) < limit {
			out = append(out, *copyContact(c))
		}
	}
	return out, nil
}

func (f *fakeQuery) Audience(_ context.Context, tenantID uuid.UUID, spec ports.AudienceSpec) ([]domain.Contact, error) {
	f.lastSpec = &spec
	var out []domain.Contact
	for _, c := range f.s.contacts {
		if c.TenantID != tenantID || !c.Sendable() || bytes.Compare(c.ID[:], spec.After[:]) <= 0 {
			continue
		}
		if len(spec.ListIDs) > 0 && len(spec.Include) == 0 {
			in := false
			for _, l := range spec.ListIDs {
				in = in || f.s.members[l][c.ID]
			}
			if !in {
				continue
			}
		}
		out = append(out, *copyContact(c))
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i].ID[:], out[j].ID[:]) < 0 })
	if len(out) > spec.Limit {
		out = out[:spec.Limit]
	}
	return out, nil
}

// Sendable aplica la misma regla que el SQL: domain.Contact.Sendable.
func (f *fakeQuery) Sendable(_ context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]domain.Contact, error) {
	var out []domain.Contact
	for _, id := range ids {
		if c, ok := f.s.contacts[id]; ok && c.TenantID == tenantID && c.Sendable() {
			out = append(out, *copyContact(c))
		}
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i].ID[:], out[j].ID[:]) < 0 })
	return out, nil
}

type fakeImports struct{ s *fakeStore }

func (f fakeImports) Create(_ context.Context, imp *domain.Import) error {
	stored := *imp
	f.s.imports = append(f.s.imports, &stored)
	return nil
}

func (f fakeImports) Get(_ context.Context, tenantID, id uuid.UUID) (*domain.Import, error) {
	for _, i := range f.s.imports {
		if i.TenantID == tenantID && i.ID == id {
			out := *i
			return &out, nil
		}
	}
	return nil, domain.ErrImportNotFound
}

func (f fakeImports) List(_ context.Context, tenantID uuid.UUID, _, _ int) ([]domain.Import, int64, error) {
	var out []domain.Import
	for _, i := range f.s.imports {
		if i.TenantID == tenantID {
			out = append(out, *i)
		}
	}
	return out, int64(len(out)), nil
}

// fakeSuppression es el estado vigente de suppression: las causas de cada direccion en
// el momento de la consulta, con su hora de alta, que el test fija antes de aplicar cada
// evento. batch, si no es nil, es lo que devuelve la consulta en bloque del barrido: la
// foto que el barrido leyo sin bloquear, que puede haber cambiado al bloquear la fila.
//
// failFromBatch, si es mayor que cero, hace fallar la consulta en bloque con ese numero de
// orden y las siguientes: suppression que cae a mitad de una importacion.
type fakeSuppression struct {
	causes        map[string][]domain.ActiveCause
	batch         map[string][]domain.ActiveCause
	err           error
	failFromBatch int
	calls         int
	batchSizes    []int
}

func (f *fakeSuppression) ActiveCauses(_ context.Context, _ uuid.UUID, email string) ([]domain.ActiveCause, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return append([]domain.ActiveCause{}, f.causes[email]...), nil
}

func (f *fakeSuppression) ActiveCausesOf(_ context.Context, _ uuid.UUID, emails []string) (map[string][]domain.ActiveCause, error) {
	f.batchSizes = append(f.batchSizes, len(emails))
	if f.err != nil {
		return nil, f.err
	}
	if f.failFromBatch > 0 && len(f.batchSizes) >= f.failFromBatch {
		return nil, errors.New("suppression: status 503")
	}
	source := f.causes
	if f.batch != nil {
		source = f.batch
	}
	out := map[string][]domain.ActiveCause{}
	for _, e := range emails {
		if causes := source[e]; len(causes) > 0 {
			out[e] = append([]domain.ActiveCause{}, causes...)
		}
	}
	return out, nil
}

type fakeTx struct{}

func (fakeTx) Transact(ctx context.Context, fn func(ctx context.Context) error) error { return fn(ctx) }

// fakePublisher registra los eventos como "subject|detalle", y aparte la hora de
// consentimiento de cada resuscripcion.
type fakePublisher struct {
	events      []string
	confirmURL  string
	consentedAt []time.Time
}

func (p *fakePublisher) add(e string) error { p.events = append(p.events, e); return nil }

func (p *fakePublisher) ContactCreated(_ context.Context, c *domain.Contact) error {
	return p.add("contact.created|" + c.Email)
}
func (p *fakePublisher) ContactUpdated(_ context.Context, c *domain.Contact, changed []string) error {
	return p.add("contact.updated|" + c.Email + "|" + strings.Join(changed, ","))
}
func (p *fakePublisher) ContactDeleted(_ context.Context, _, id uuid.UUID) error {
	return p.add("contact.deleted|" + id.String())
}
func (p *fakePublisher) ContactResubscribed(_ context.Context, c *domain.Contact, consentedAt time.Time) error {
	p.consentedAt = append(p.consentedAt, consentedAt)
	return p.add("contact.resubscribed|" + c.Email)
}
func (p *fakePublisher) ConsentGranted(_ context.Context, c *domain.Consent) error {
	return p.add("consent.granted|" + string(c.Method))
}
func (p *fakePublisher) ConsentRevoked(_ context.Context, c *domain.Consent) error {
	return p.add("consent.revoked|" + string(c.Method))
}
func (p *fakePublisher) ConsentRequested(_ context.Context, c *domain.Contact, url string) error {
	p.confirmURL = url
	return p.add("consent.requested|" + c.Email)
}
func (p *fakePublisher) ImportCompleted(_ context.Context, imp *domain.Import) error {
	return p.add("import.completed|" + imp.ID.String())
}

func (p *fakePublisher) count(prefix string) int {
	n := 0
	for _, e := range p.events {
		if strings.HasPrefix(e, prefix) {
			n++
		}
	}
	return n
}

// counterReader es una fuente de bytes determinista y distinta en cada lectura.
type counterReader struct{ n byte }

func (r *counterReader) Read(p []byte) (int, error) {
	for i := range p {
		r.n++
		p[i] = r.n
	}
	return len(p), nil
}

type fixture struct {
	uc     *UseCase
	s      *fakeStore
	ev     *fakePublisher
	query  *fakeQuery
	sup    *fakeSuppression
	tenant uuid.UUID
	now    time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{ev: &fakePublisher{}, tenant: uuid.New(), now: time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)}
	f.s = newStore(func() time.Time { return f.now })
	f.query = &fakeQuery{s: f.s}
	f.sup = &fakeSuppression{causes: map[string][]domain.ActiveCause{}}
	f.uc = New(Deps{
		Contacts: fakeContacts{f.s}, Consents: fakeConsents{f.s}, Tokens: fakeTokens{f.s},
		Lists: fakeLists{f.s}, Attributes: fakeAttributes{f.s}, Segments: fakeSegments{f.s},
		Query: f.query, Imports: fakeImports{f.s}, Tx: fakeTx{}, Events: f.ev, Suppression: f.sup,
		Config: Config{PublicBaseURL: "https://app.example.com", DOITTL: 72 * time.Hour, ImportMaxRows: 1000},
		Now:    func() time.Time { return f.now }, Random: &counterReader{},
	})
	return f
}

// addContact siembra un contacto con el estado y el consentimiento vigente dados.
func (f *fixture) addContact(t *testing.T, email string, status domain.Status, consent domain.ConsentStatus) *domain.Contact {
	t.Helper()
	c := &domain.Contact{TenantID: f.tenant, Email: email, Status: status, ConsentStatus: consent, Source: domain.SourceAPI}
	if err := (fakeContacts{f.s}).Insert(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *fixture) declare(t *testing.T, key string, typ domain.AttrType, required bool) {
	t.Helper()
	if _, err := f.uc.CreateAttribute(context.Background(), f.tenant, CreateAttributeInput{Key: key, Type: string(typ), Required: required}); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) contact(t *testing.T, id uuid.UUID) *domain.Contact {
	t.Helper()
	c, err := (fakeContacts{f.s}).GetByID(context.Background(), f.tenant, id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

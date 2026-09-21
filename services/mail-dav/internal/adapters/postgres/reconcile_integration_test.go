//go:build integration

package postgres

// Conciliacion de buzones borrados contra Postgres real, con el rol de servicio sujeto a las politicas de fila.
// La enumeracion (mail_dav.stale_mailbox_ids) es lo unico que salta la politica por buzon, asi que se prueba
// que solo devuelve ids de la empresa pedida y con datos mas viejos que la gracia, y el barrido completo, con el
// caso de uso real, que retira los datos de los buzones que el directorio no conoce, y solo esos.

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/mailreconcile"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/adapters/reconcile"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
)

// age envejece los datos de un buzon: la migracion pone created_at en now().
func (e *env) age(t *testing.T, p domain.Principal, by time.Duration) {
	t.Helper()
	for _, table := range []string{"addressbooks", "calendars"} {
		if _, err := e.owner.Exec(context.Background(),
			`UPDATE mail_dav.`+table+` SET created_at = now() - make_interval(secs => $2) WHERE tenant_id = $1 AND mailbox_id = $3`,
			p.TenantID, by.Seconds(), p.MailboxID); err != nil {
			t.Fatal(err)
		}
	}
}

func sortedIDs(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	sort.Strings(out)
	return out
}

func sameIDs(got []uuid.UUID, want ...uuid.UUID) bool {
	g, w := sortedIDs(got), sortedIDs(want)
	if len(g) != len(w) {
		return false
	}
	for i := range g {
		if g[i] != w[i] {
			return false
		}
	}
	return true
}

func TestLaEnumeracionSoloDevuelveBuzonesViejosDeLaEmpresaPedida(t *testing.T) {
	e := setup(t)
	old := newPrincipal("vieja")
	oldCalendarOnly := sameTenant(old, "solo-calendario")
	recent := sameTenant(old, "reciente")
	otherTenant := newPrincipal("ajena")
	e.cleanup(t, old, oldCalendarOnly, recent, otherTenant)

	e.book(t, old, "contacts")
	e.calendar(t, oldCalendarOnly, "calendar")
	e.book(t, recent, "contacts")
	e.book(t, otherTenant, "contacts")
	e.age(t, old, 48*time.Hour)
	e.age(t, oldCalendarOnly, 48*time.Hour)
	e.age(t, otherTenant, 48*time.Hour)

	cutoff := time.Now().Add(-24 * time.Hour)
	got, err := e.repo.StaleMailboxIDs(e.ctx, old.TenantID, cutoff, uuid.Nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !sameIDs(got, old.MailboxID, oldCalendarOnly.MailboxID) {
		t.Fatalf("estancados de la empresa: %v (no debe incluir al reciente ni al de otra empresa)", got)
	}
	got, err = e.repo.StaleMailboxIDs(e.ctx, otherTenant.TenantID, cutoff, uuid.Nil, 100)
	if err != nil || !sameIDs(got, otherTenant.MailboxID) {
		t.Fatalf("estancados de la otra empresa: %v %v", got, err)
	}
	got, err = e.repo.StaleMailboxIDs(e.ctx, uuid.New(), cutoff, uuid.Nil, 100)
	if err != nil || len(got) != 0 {
		t.Fatalf("empresa sin datos: %v %v", got, err)
	}
}

func TestElBuzonSeConsideraPorSuDatoMasAntiguo(t *testing.T) {
	e := setup(t)
	p := newPrincipal("mixta")
	e.cleanup(t, p)
	e.book(t, p, "contacts")
	e.book(t, p, "familia")
	e.age(t, p, 72*time.Hour)
	// Un dato que llego despues, en vuelo: el buzon sigue siendo candidato por el mas antiguo.
	e.book(t, p, "reciente")

	got, err := e.repo.StaleMailboxIDs(e.ctx, p.TenantID, time.Now().Add(-24*time.Hour), uuid.Nil, 10)
	if err != nil || !sameIDs(got, p.MailboxID) {
		t.Fatalf("estancados: %v %v", got, err)
	}
}

func TestLaEnumeracionPaginaEnOrden(t *testing.T) {
	e := setup(t)
	first := newPrincipal("p0")
	all := []domain.Principal{first}
	for _, n := range []string{"p1", "p2", "p3", "p4"} {
		all = append(all, sameTenant(first, n))
	}
	e.cleanup(t, all...)
	want := make([]uuid.UUID, 0, len(all))
	for _, p := range all {
		e.book(t, p, "contacts")
		e.age(t, p, 48*time.Hour)
		want = append(want, p.MailboxID)
	}

	var seen []uuid.UUID
	after := uuid.Nil
	for {
		page, err := e.repo.StaleMailboxIDs(e.ctx, first.TenantID, time.Now().Add(-24*time.Hour), after, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) > 2 {
			t.Fatalf("pagina de %d con limite 2", len(page))
		}
		if len(page) == 0 {
			break
		}
		seen = append(seen, page...)
		after = page[len(page)-1]
	}
	if !sameIDs(seen, want...) {
		t.Fatalf("recorrido: %v, quiero %v", seen, want)
	}
	if !sort.SliceIsSorted(seen, func(i, j int) bool { return seen[i].String() < seen[j].String() }) {
		t.Fatalf("no vienen en orden: %v", seen)
	}
}

// El rol de servicio no puede listar buzones por si mismo: por eso existe la funcion.
func TestSinLaFuncionElRolDeServicioNoVeLosBuzonesDeOtros(t *testing.T) {
	e := setup(t)
	p := newPrincipal("ana")
	e.cleanup(t, p)
	e.book(t, p, "contacts")
	var n int
	if err := e.service.QueryRow(context.Background(), `SELECT count(*) FROM mail_dav.addressbooks WHERE tenant_id = $1`, p.TenantID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("el rol de servicio sin sesion ve %d filas: la politica de fila no aisla", n)
	}
}

type serviceBinder struct{ e *env }

func (b serviceBinder) Bind(_ context.Context, p domain.Principal) (context.Context, error) {
	return middleware.WithIdentity(b.e.ctx, p.MailboxID.String(), p.TenantID.String()), nil
}

// knownMailboxes es el directorio de la prueba: los ids que dice conocer por empresa.
type knownMailboxes map[uuid.UUID]map[uuid.UUID]struct{}

func (k knownMailboxes) Existing(_ context.Context, tenantID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]struct{}, error) {
	out := map[uuid.UUID]struct{}{}
	for _, id := range ids {
		if _, ok := k[tenantID][id]; ok {
			out[id] = struct{}{}
		}
	}
	return out, nil
}

type singleTenant struct {
	e      *env
	tenant uuid.UUID
}

func (s singleTenant) ForEach(_ context.Context, fn func(context.Context, uuid.UUID)) error {
	fn(s.e.ctx, s.tenant)
	return nil
}

func newSweeper(t *testing.T, e *env, tenant uuid.UUID, dir mailreconcile.Directory, cfg mailreconcile.Config) *mailreconcile.Sweeper {
	t.Helper()
	uc, err := app.New(app.Deps{
		Tenant: serviceBinder{e}, Store: e.repo, Calendars: e.repo, Index: e.repo,
		Config: app.Config{
			Limits:                 domain.Limits{MaxVCardBytes: 4096, MaxVCardProperties: 20, MaxContactsPerMailbox: 10, MaxAddressbooksPerMailbox: 3, MaxChangesRetained: 5, MaxMailboxBytes: 1 << 20, MaxReadBytes: 1 << 20},
			Calendar:               domain.CalendarLimits{MaxEventBytes: 16 << 10, MaxEventProperties: 100, MaxEventsPerMailbox: 10, MaxCalendarsPerMailbox: 3, MaxRecurrenceWork: 5000, MaxQueryWork: 50000},
			DefaultAddressbookName: "Contactos", DefaultCalendarName: "Calendario",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Interval = time.Hour
	return mailreconcile.New(mailreconcile.Deps{
		Name: "mail-dav", Config: cfg,
		Lock:      func(context.Context) (func(), bool) { return func() {}, true },
		Tenants:   singleTenant{e: e, tenant: tenant},
		Directory: dir, Store: reconcile.NewStore(uc),
	})
}

func TestElBarridoRetiraLosDatosDeLosBuzonesQueElDirectorioNoConoceYSoloEsos(t *testing.T) {
	e := setup(t)
	alive := newPrincipal("viva")
	gone := sameTenant(alive, "borrada")
	goneCalendar := sameTenant(alive, "borrada-calendario")
	recreated := domain.Principal{TenantID: alive.TenantID, MailboxID: uuid.New(), Username: gone.Username}
	fresh := sameTenant(alive, "recien-creada")
	otherTenantGone := newPrincipal("otra-empresa")
	all := []domain.Principal{alive, gone, goneCalendar, recreated, fresh, otherTenantGone}
	e.cleanup(t, all...)
	for _, p := range []domain.Principal{alive, gone, recreated, fresh, otherTenantGone} {
		e.seedMailbox(t, p, p.Username[:3])
	}
	e.calendar(t, goneCalendar, "calendar")
	for _, p := range []domain.Principal{alive, gone, goneCalendar, recreated, otherTenantGone} {
		e.age(t, p, 48*time.Hour)
	}
	before := map[uuid.UUID]int{}
	for _, p := range []domain.Principal{alive, recreated, fresh, otherTenantGone} {
		before[p.MailboxID] = e.storedRows(t, p)
	}

	// El directorio conoce a la viva, a la recreada (mismo nombre que la borrada, otro id) y a la recien creada.
	dir := knownMailboxes{alive.TenantID: {alive.MailboxID: {}, recreated.MailboxID: {}, fresh.MailboxID: {}}}
	rep := newSweeper(t, e, alive.TenantID, dir, mailreconcile.Config{Grace: 24 * time.Hour, BatchSize: 2, MaxPurgesPerTenant: 100}).Pass(context.Background())

	if rep.Purged != 2 || rep.Failures != 0 {
		t.Fatalf("informe: %+v", rep)
	}
	if n := e.storedRows(t, gone) + e.storedCalendarRows(t, gone); n != 0 {
		t.Fatalf("quedan %d filas del buzon borrado", n)
	}
	if n := e.storedCalendarRows(t, goneCalendar); n != 0 {
		t.Fatalf("quedan %d filas del calendario del buzon borrado", n)
	}
	for _, p := range []domain.Principal{alive, recreated, fresh, otherTenantGone} {
		if got := e.storedRows(t, p); got != before[p.MailboxID] {
			t.Errorf("el buzon %s perdio datos que no debia: %d -> %d", p.Username, before[p.MailboxID], got)
		}
	}

	rep = newSweeper(t, e, alive.TenantID, dir, mailreconcile.Config{Grace: 24 * time.Hour, BatchSize: 2, MaxPurgesPerTenant: 100}).Pass(context.Background())
	if rep.Purged != 0 {
		t.Fatalf("la segunda pasada no debe encontrar nada: %+v", rep)
	}
}

func TestElBarridoNoTocaLoQueEstaDentroDeLaGraciaAunqueElDirectorioNoLoConozca(t *testing.T) {
	e := setup(t)
	p := newPrincipal("recien")
	e.cleanup(t, p)
	e.seedMailbox(t, p, "rec")
	before := e.storedRows(t, p)

	rep := newSweeper(t, e, p.TenantID, knownMailboxes{}, mailreconcile.Config{Grace: 24 * time.Hour, BatchSize: 10, MaxPurgesPerTenant: 100}).Pass(context.Background())
	if rep.Purged != 0 || e.storedRows(t, p) != before {
		t.Fatalf("retiro datos de un buzon dentro de la gracia: %+v", rep)
	}

	e.age(t, p, 25*time.Hour)
	rep = newSweeper(t, e, p.TenantID, knownMailboxes{}, mailreconcile.Config{Grace: 24 * time.Hour, BatchSize: 10, MaxPurgesPerTenant: 100}).Pass(context.Background())
	if rep.Purged != 1 || e.storedRows(t, p) != 0 {
		t.Fatalf("pasada la gracia debe retirarse: %+v", rep)
	}
}

func TestUnBuzonDeOtraEmpresaConElMismoIdNoImpideRetirarElDatoDeLaSuya(t *testing.T) {
	e := setup(t)
	p := newPrincipal("ana")
	e.cleanup(t, p)
	e.seedMailbox(t, p, "ana")
	e.age(t, p, 48*time.Hour)
	// El directorio conoce ese id, pero como buzon de otra empresa: para la de p no existe.
	dir := knownMailboxes{uuid.New(): {p.MailboxID: {}}}
	rep := newSweeper(t, e, p.TenantID, dir, mailreconcile.Config{Grace: time.Hour, BatchSize: 10, MaxPurgesPerTenant: 100}).Pass(context.Background())
	if rep.Purged != 1 || e.storedRows(t, p) != 0 {
		t.Fatalf("informe: %+v", rep)
	}
}

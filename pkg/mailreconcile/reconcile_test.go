package mailreconcile

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
)

var clock = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

type fakeStore struct {
	// data son los ids con datos por empresa, en orden.
	data      map[uuid.UUID][]uuid.UUID
	purged    []uuid.UUID
	purgedIn  []uuid.UUID
	beforeGot []time.Time
	limits    []int
	listErr   error
	purgeErr  map[uuid.UUID]error
}

func (f *fakeStore) StaleMailboxes(_ context.Context, tenantID uuid.UUID, before time.Time, after uuid.UUID, limit int) ([]uuid.UUID, error) {
	f.beforeGot = append(f.beforeGot, before)
	f.limits = append(f.limits, limit)
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []uuid.UUID
	for _, id := range f.data[tenantID] {
		if id.String() > after.String() {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeStore) PurgeMailbox(_ context.Context, tenantID, mailboxID uuid.UUID) (int, error) {
	if err := f.purgeErr[mailboxID]; err != nil {
		return 0, err
	}
	f.purged = append(f.purged, mailboxID)
	f.purgedIn = append(f.purgedIn, tenantID)
	ids := f.data[tenantID]
	for i, id := range ids {
		if id == mailboxID {
			f.data[tenantID] = append(ids[:i:i], ids[i+1:]...)
			break
		}
	}
	return 1, nil
}

type fakeDirectory struct {
	existing map[uuid.UUID]map[uuid.UUID]bool
	err      error
	asked    [][]uuid.UUID
	tenants  []uuid.UUID
	// omit hace que la respuesta no incluya nada, como una respuesta vacia.
	omitAll bool
}

func (f *fakeDirectory) Existing(_ context.Context, tenantID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]struct{}, error) {
	f.asked = append(f.asked, ids)
	f.tenants = append(f.tenants, tenantID)
	if f.err != nil {
		return nil, f.err
	}
	out := map[uuid.UUID]struct{}{}
	if f.omitAll {
		return out, nil
	}
	for _, id := range ids {
		if f.existing[tenantID][id] {
			out[id] = struct{}{}
		}
	}
	return out, nil
}

type fakeTenants struct {
	ids []uuid.UUID
	err error
}

func (f fakeTenants) ForEach(ctx context.Context, fn func(context.Context, uuid.UUID)) error {
	for _, id := range f.ids {
		fn(ctx, id)
	}
	return f.err
}

func leader(ok bool) LeaderLock {
	return func(context.Context) (func(), bool) { return func() {}, ok }
}

type fixture struct {
	sweeper *Sweeper
	store   *fakeStore
	dir     *fakeDirectory
	tenant  uuid.UUID
}

func newFixture(cfg Config, tenants ...uuid.UUID) *fixture {
	f := &fixture{
		store:  &fakeStore{data: map[uuid.UUID][]uuid.UUID{}, purgeErr: map[uuid.UUID]error{}},
		dir:    &fakeDirectory{existing: map[uuid.UUID]map[uuid.UUID]bool{}},
		tenant: uuid.New(),
	}
	if len(tenants) == 0 {
		tenants = []uuid.UUID{f.tenant}
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = 200
	}
	if cfg.MaxPurgesPerTenant == 0 {
		cfg.MaxPurgesPerTenant = 100
	}
	if cfg.Grace == 0 {
		cfg.Grace = 24 * time.Hour
	}
	cfg.Interval = time.Hour
	f.sweeper = New(Deps{
		Name: "prueba", Config: cfg, Lock: leader(true), Tenants: fakeTenants{ids: tenants}, Directory: f.dir, Store: f.store,
		Now: func() time.Time { return clock },
	})
	return f
}

func (f *fixture) mailboxes(tenant uuid.UUID, live, gone int) (liveIDs, goneIDs []uuid.UUID) {
	if f.dir.existing[tenant] == nil {
		f.dir.existing[tenant] = map[uuid.UUID]bool{}
	}
	for i := 0; i < live; i++ {
		id := uuid.New()
		liveIDs = append(liveIDs, id)
		f.store.data[tenant] = append(f.store.data[tenant], id)
		f.dir.existing[tenant][id] = true
	}
	for i := 0; i < gone; i++ {
		id := uuid.New()
		goneIDs = append(goneIDs, id)
		f.store.data[tenant] = append(f.store.data[tenant], id)
	}
	return liveIDs, goneIDs
}

func contains(ids []uuid.UUID, id uuid.UUID) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func TestRetiraSoloLosBuzonesQueElDirectorioNoConoce(t *testing.T) {
	f := newFixture(Config{})
	live, gone := f.mailboxes(f.tenant, 3, 2)
	rep := f.sweeper.Pass(context.Background())

	if rep.Purged != 2 || rep.Failures != 0 || rep.Skipped {
		t.Fatalf("informe: %+v", rep)
	}
	for _, id := range gone {
		if !contains(f.store.purged, id) {
			t.Errorf("no retiro el buzon borrado %s", id)
		}
	}
	for _, id := range live {
		if contains(f.store.purged, id) {
			t.Errorf("retiro el buzon vivo %s", id)
		}
	}
}

func TestLaGraciaSePasaAlAlmacenParaNoTocarLoRecienCreado(t *testing.T) {
	f := newFixture(Config{Grace: 6 * time.Hour})
	f.mailboxes(f.tenant, 0, 1)
	f.sweeper.Pass(context.Background())

	if len(f.store.beforeGot) == 0 || !f.store.beforeGot[0].Equal(clock.Add(-6*time.Hour)) {
		t.Fatalf("el limite de antiguedad debe ser ahora menos la gracia: %v", f.store.beforeGot)
	}
}

func TestSiElDirectorioNoRespondeNoSeBorraNada(t *testing.T) {
	f := newFixture(Config{})
	f.mailboxes(f.tenant, 1, 3)
	f.dir.err = errors.New("mail-directory caido")

	rep := f.sweeper.Pass(context.Background())
	if len(f.store.purged) != 0 {
		t.Fatalf("borro %d buzones sin respuesta del directorio", len(f.store.purged))
	}
	if rep.Failures != 1 || rep.Purged != 0 {
		t.Fatalf("informe: %+v", rep)
	}
}

func TestSiElAlmacenFallaNoSeConsultaNiSeBorra(t *testing.T) {
	f := newFixture(Config{})
	f.mailboxes(f.tenant, 0, 2)
	f.store.listErr = errors.New("base caida")
	rep := f.sweeper.Pass(context.Background())
	if len(f.dir.asked) != 0 || len(f.store.purged) != 0 || rep.Failures != 1 {
		t.Fatalf("consultas %d, borrados %d, informe %+v", len(f.dir.asked), len(f.store.purged), rep)
	}
}

func TestUnFalloAlBorrarDetieneAEsaEmpresaYSigueConLasDemas(t *testing.T) {
	first, other := uuid.New(), uuid.New()
	f := newFixture(Config{}, first, other)
	_, goneFirst := f.mailboxes(first, 0, 1)
	_, goneOther := f.mailboxes(other, 0, 1)
	f.store.purgeErr[goneFirst[0]] = errors.New("fallo transitorio")

	rep := f.sweeper.Pass(context.Background())
	if rep.Failures != 1 || rep.Purged != 1 || !contains(f.store.purged, goneOther[0]) {
		t.Fatalf("informe %+v, borrados %v", rep, f.store.purged)
	}
	if contains(f.store.purged, goneFirst[0]) {
		t.Fatal("dio por borrado lo que fallo")
	}
}

func TestElTopeAcotaLoQueSeRetiraPorEmpresaYPasada(t *testing.T) {
	f := newFixture(Config{MaxPurgesPerTenant: 3, BatchSize: 4})
	_, gone := f.mailboxes(f.tenant, 0, 9)
	rep := f.sweeper.Pass(context.Background())
	if rep.Purged != 3 || rep.Capped != 1 {
		t.Fatalf("informe: %+v", rep)
	}
	if len(f.store.data[f.tenant]) != len(gone)-3 {
		t.Fatalf("quedan %d, quiero %d", len(f.store.data[f.tenant]), len(gone)-3)
	}
	// La siguiente pasada sigue donde quedo.
	rep = f.sweeper.Pass(context.Background())
	if rep.Purged != 3 {
		t.Fatalf("segunda pasada: %+v", rep)
	}
}

func TestUnDirectorioQueNoDiceNadaNoVaciaUnaEmpresaEntera(t *testing.T) {
	f := newFixture(Config{MaxPurgesPerTenant: 5})
	live, _ := f.mailboxes(f.tenant, 40, 0)
	f.dir.omitAll = true
	rep := f.sweeper.Pass(context.Background())
	if rep.Purged != 5 {
		t.Fatalf("el tope debe acotar el dano de un directorio erroneo: %+v", rep)
	}
	if len(f.store.data[f.tenant]) != len(live)-5 {
		t.Fatalf("quedan %d", len(f.store.data[f.tenant]))
	}
}

func TestRecorreTodosLosLotesSinSaltarseNinguno(t *testing.T) {
	f := newFixture(Config{BatchSize: 4})
	live, gone := f.mailboxes(f.tenant, 6, 7)
	rep := f.sweeper.Pass(context.Background())
	if rep.Purged != 7 {
		t.Fatalf("retiro %d de %d: %+v", rep.Purged, len(gone), rep)
	}
	for _, id := range live {
		if contains(f.store.purged, id) {
			t.Fatalf("retiro un buzon vivo")
		}
	}
	for _, batch := range f.dir.asked {
		if len(batch) > 4 {
			t.Fatalf("lote de %d por encima del tamano configurado", len(batch))
		}
	}
	for _, l := range f.store.limits {
		if l != 4 {
			t.Fatalf("limite pedido al almacen: %d", l)
		}
	}
}

func TestPreguntaSiempreConLaEmpresaDelDatoYBorraEnEsaEmpresa(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	f := newFixture(Config{}, a, b)
	_, goneA := f.mailboxes(a, 0, 1)
	_, goneB := f.mailboxes(b, 0, 1)
	f.sweeper.Pass(context.Background())

	for i, id := range f.store.purged {
		want := a
		if id == goneB[0] {
			want = b
		} else if id != goneA[0] {
			t.Fatalf("borro un id ajeno: %s", id)
		}
		if f.store.purgedIn[i] != want {
			t.Fatalf("el buzon %s se borro en la empresa %s, no en %s", id, f.store.purgedIn[i], want)
		}
	}
	for _, tenant := range f.dir.tenants {
		if tenant != a && tenant != b {
			t.Fatalf("pregunto por una empresa que no es de los datos: %s", tenant)
		}
	}
}

func TestUnBuzonDeOtraEmpresaNoSalvaAlDeLaSuya(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	f := newFixture(Config{}, a)
	_, gone := f.mailboxes(a, 0, 1)
	// El mismo id existe como buzon de otra empresa: para A no existe.
	f.dir.existing[b] = map[uuid.UUID]bool{gone[0]: true}
	rep := f.sweeper.Pass(context.Background())
	if rep.Purged != 1 {
		t.Fatalf("el buzon de la empresa A ya no existe para A aunque otra empresa tenga ese id: %+v", rep)
	}
}

func TestSinElCerrojoDeLiderNoHaceNada(t *testing.T) {
	f := newFixture(Config{})
	f.sweeper.lock = leader(false)
	f.mailboxes(f.tenant, 0, 2)
	rep := f.sweeper.Pass(context.Background())
	if !rep.Skipped || len(f.store.purged) != 0 || len(f.dir.asked) != 0 {
		t.Fatalf("informe %+v", rep)
	}
}

func TestElCerrojoSeSueltaAlTerminar(t *testing.T) {
	f := newFixture(Config{})
	released := 0
	f.sweeper.lock = func(context.Context) (func(), bool) { return func() { released++ }, true }
	f.sweeper.Pass(context.Background())
	if released != 1 {
		t.Fatalf("cerrojo soltado %d veces", released)
	}
}

func TestUnFalloAlListarLasEmpresasSeCuentaYNoBorra(t *testing.T) {
	f := newFixture(Config{})
	f.mailboxes(f.tenant, 0, 1)
	f.sweeper.tenants = fakeTenants{err: errors.New("registro caido")}
	rep := f.sweeper.Pass(context.Background())
	if rep.Failures != 1 || rep.Tenants != 0 || len(f.store.purged) != 0 {
		t.Fatalf("informe %+v", rep)
	}
}

func TestSinIntervaloElBarridoNoCorre(t *testing.T) {
	f := newFixture(Config{})
	f.sweeper.cfg.Interval = 0
	f.mailboxes(f.tenant, 0, 1)
	done := make(chan struct{})
	go func() { f.sweeper.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run con el barrido desactivado debe volver al instante")
	}
	if len(f.store.purged) != 0 {
		t.Fatal("un barrido desactivado borro")
	}
}

func TestRunSeDetieneConElContexto(t *testing.T) {
	f := newFixture(Config{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.sweeper.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run no se detuvo con el contexto")
	}
}

func TestSinDatosNoPreguntaAlDirectorio(t *testing.T) {
	f := newFixture(Config{})
	f.sweeper.Pass(context.Background())
	if len(f.dir.asked) != 0 {
		t.Fatalf("pregunto %d veces sin tener datos", len(f.dir.asked))
	}
}

//go:build integration

package postgres

// Conciliacion de buzones borrados contra Postgres real: la enumeracion solo devuelve ids de la empresa pedida
// y con trabajos mas viejos que la gracia, y el barrido completo, con el caso de uso real y la outbox real,
// retira los trabajos de los buzones que el directorio no conoce, y solo esos, con las mismas garantias que el
// consumidor de mail.mailbox.deleted.

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/mailreconcile"
	outboxadapter "github.com/alonsosss/corforce-email/services/mail-migration/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/google/uuid"
)

func ageJob(t *testing.T, ctx context.Context, r *Repository, j *domain.Job, by time.Duration) {
	t.Helper()
	if _, err := r.pool.Exec(ctx, `UPDATE mail_migration.jobs SET created_at = now() - make_interval(secs => $2) WHERE id = $1`, j.ID, by.Seconds()); err != nil {
		t.Fatal(err)
	}
}

func jobIn(t *testing.T, ctx context.Context, r *Repository, tenant, mailbox uuid.UUID, age time.Duration) *domain.Job {
	t.Helper()
	j := newJob(tenant)
	j.MailboxID = mailbox
	if err := insert(t, ctx, r, j, 100); err != nil {
		t.Fatal(err)
	}
	if age > 0 {
		ageJob(t, ctx, r, j, age)
	}
	return j
}

func sameSet(got []uuid.UUID, want ...uuid.UUID) bool {
	g, w := make([]string, len(got)), make([]string, len(want))
	for i, id := range got {
		g[i] = id.String()
	}
	for i, id := range want {
		w[i] = id.String()
	}
	sort.Strings(g)
	sort.Strings(w)
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
	ctx, repo, _ := setup(t)
	tenant, other := uuid.New(), uuid.New()
	old, recent, foreign := uuid.New(), uuid.New(), uuid.New()
	jobIn(t, ctx, repo, tenant, old, 48*time.Hour)
	jobIn(t, ctx, repo, tenant, recent, time.Minute)
	jobIn(t, ctx, repo, other, foreign, 48*time.Hour)

	cutoff := time.Now().Add(-24 * time.Hour)
	got, err := repo.StaleMailboxIDs(ctx, tenant, cutoff, uuid.Nil, 100)
	if err != nil || !sameSet(got, old) {
		t.Fatalf("estancados de la empresa: %v %v (no debe incluir al reciente ni al de otra empresa)", got, err)
	}
	got, err = repo.StaleMailboxIDs(ctx, other, cutoff, uuid.Nil, 100)
	if err != nil || !sameSet(got, foreign) {
		t.Fatalf("estancados de la otra empresa: %v %v", got, err)
	}
	got, err = repo.StaleMailboxIDs(ctx, uuid.New(), cutoff, uuid.Nil, 100)
	if err != nil || len(got) != 0 {
		t.Fatalf("empresa sin trabajos: %v %v", got, err)
	}
}

func TestElBuzonSeConsideraPorSuTrabajoMasAntiguo(t *testing.T) {
	ctx, repo, _ := setup(t)
	tenant, mailbox := uuid.New(), uuid.New()
	old := finished(t, ctx, repo, tenant, mailbox, domain.StatusSucceeded)
	ageJob(t, ctx, repo, old, 72*time.Hour)
	// Un trabajo que llego despues, en vuelo: el buzon sigue siendo candidato por el mas antiguo.
	jobIn(t, ctx, repo, tenant, mailbox, 0)

	got, err := repo.StaleMailboxIDs(ctx, tenant, time.Now().Add(-24*time.Hour), uuid.Nil, 10)
	if err != nil || !sameSet(got, mailbox) {
		t.Fatalf("estancados: %v %v", got, err)
	}
}

func TestLaEnumeracionPaginaEnOrden(t *testing.T) {
	ctx, repo, _ := setup(t)
	tenant := uuid.New()
	var want []uuid.UUID
	for i := 0; i < 5; i++ {
		id := uuid.New()
		want = append(want, id)
		jobIn(t, ctx, repo, tenant, id, 48*time.Hour)
	}
	var seen []uuid.UUID
	after := uuid.Nil
	for {
		page, err := repo.StaleMailboxIDs(ctx, tenant, time.Now().Add(-24*time.Hour), after, 2)
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
	if !sameSet(seen, want...) {
		t.Fatalf("recorrido: %v, quiero %v", seen, want)
	}
	if !sort.SliceIsSorted(seen, func(i, j int) bool { return seen[i].String() < seen[j].String() }) {
		t.Fatalf("no vienen en orden: %v", seen)
	}
}

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
	ctx    context.Context
	tenant uuid.UUID
}

func (s singleTenant) ForEach(_ context.Context, fn func(context.Context, uuid.UUID)) error {
	fn(s.ctx, s.tenant)
	return nil
}

func sweeperFor(ctx context.Context, repo *Repository, tenant uuid.UUID, dir mailreconcile.Directory, cfg mailreconcile.Config) *mailreconcile.Sweeper {
	cfg.Interval = time.Hour
	return mailreconcile.New(mailreconcile.Deps{
		Name: "mail-migration", Config: cfg,
		Lock:    func(context.Context) (func(), bool) { return func() {}, true },
		Tenants: singleTenant{ctx: ctx, tenant: tenant}, Directory: dir,
		Store: purgeUseCase(repo, outboxadapter.NewPublisher(repo.pool)),
	})
}

func TestElBarridoRetiraLosTrabajosDeLosBuzonesQueElDirectorioNoConoceYSoloEsos(t *testing.T) {
	ctx, repo, _ := setup(t)
	tenant, other := uuid.New(), uuid.New()
	alive, gone, goneActive, recreated, fresh, foreign := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()

	finishedJob := finished(t, ctx, repo, tenant, gone, domain.StatusFailed)
	ageJob(t, ctx, repo, finishedJob, 48*time.Hour)
	activeJob := jobIn(t, ctx, repo, tenant, goneActive, 48*time.Hour)
	for _, mailbox := range []uuid.UUID{alive, recreated} {
		jobIn(t, ctx, repo, tenant, mailbox, 48*time.Hour)
	}
	jobIn(t, ctx, repo, tenant, fresh, time.Minute)
	jobIn(t, ctx, repo, other, foreign, 48*time.Hour)
	outboxBefore := outboxCount(t, ctx, repo, tenant)

	// El directorio conoce a la viva, a la recreada y a la recien creada.
	dir := knownMailboxes{tenant: {alive: {}, recreated: {}, fresh: {}}}
	rep := sweeperFor(ctx, repo, tenant, dir, mailreconcile.Config{Grace: 24 * time.Hour, BatchSize: 2, MaxPurgesPerTenant: 100}).Pass(ctx)

	if rep.Purged != 2 || rep.Failures != 0 {
		t.Fatalf("informe: %+v", rep)
	}
	for name, mailbox := range map[string]uuid.UUID{"borrado": gone, "borrado con trabajo activo": goneActive} {
		if n := jobsOf(t, ctx, repo, tenant, mailbox); n != 0 {
			t.Errorf("quedan %d trabajos del buzon %s", n, name)
		}
	}
	for name, mailbox := range map[string]uuid.UUID{"vivo": alive, "recreado": recreated, "recien creado": fresh} {
		if n := jobsOf(t, ctx, repo, tenant, mailbox); n != 1 {
			t.Errorf("el buzon %s perdio su trabajo: %d", name, n)
		}
	}
	if n := jobsOf(t, ctx, repo, other, foreign); n != 1 {
		t.Errorf("otra empresa perdio su trabajo: %d", n)
	}

	// Como el consumidor: el trabajo activo anuncia su cancelacion (sin datos personales) en la misma transaccion.
	cancelled := outboxOfJob(t, ctx, repo, tenant, activeJob.ID)
	if len(cancelled) != 1 || cancelled[0].subject != outboxadapter.SubjectCancelled {
		t.Fatalf("eventos del trabajo activo: %+v", cancelled)
	}
	if total := outboxCount(t, ctx, repo, tenant); total != outboxBefore+1 {
		t.Errorf("solo el trabajo activo anuncia su cancelacion: %d eventos, antes %d", total, outboxBefore)
	}

	rep = sweeperFor(ctx, repo, tenant, dir, mailreconcile.Config{Grace: 24 * time.Hour, BatchSize: 2, MaxPurgesPerTenant: 100}).Pass(ctx)
	if rep.Purged != 0 {
		t.Fatalf("la segunda pasada no debe encontrar nada: %+v", rep)
	}
}

func TestElBarridoNoTocaLoQueEstaDentroDeLaGraciaAunqueElDirectorioNoLoConozca(t *testing.T) {
	ctx, repo, _ := setup(t)
	tenant, mailbox := uuid.New(), uuid.New()
	job := jobIn(t, ctx, repo, tenant, mailbox, 0)
	cfg := mailreconcile.Config{Grace: 24 * time.Hour, BatchSize: 10, MaxPurgesPerTenant: 100}

	if rep := sweeperFor(ctx, repo, tenant, knownMailboxes{}, cfg).Pass(ctx); rep.Purged != 0 || jobsOf(t, ctx, repo, tenant, mailbox) != 1 {
		t.Fatalf("retiro un trabajo dentro de la gracia: %+v", rep)
	}
	ageJob(t, ctx, repo, job, 25*time.Hour)
	if rep := sweeperFor(ctx, repo, tenant, knownMailboxes{}, cfg).Pass(ctx); rep.Purged != 1 || jobsOf(t, ctx, repo, tenant, mailbox) != 0 {
		t.Fatalf("pasada la gracia debe retirarse: %+v", rep)
	}
}

func TestUnBuzonDeOtraEmpresaConElMismoIdNoImpideRetirarElTrabajoDeLaSuya(t *testing.T) {
	ctx, repo, _ := setup(t)
	tenant, mailbox := uuid.New(), uuid.New()
	jobIn(t, ctx, repo, tenant, mailbox, 48*time.Hour)
	// El directorio conoce ese id, pero como buzon de otra empresa: para la de este trabajo no existe.
	dir := knownMailboxes{uuid.New(): {mailbox: {}}}
	rep := sweeperFor(ctx, repo, tenant, dir, mailreconcile.Config{Grace: time.Hour, BatchSize: 10, MaxPurgesPerTenant: 100}).Pass(ctx)
	if rep.Purged != 1 || jobsOf(t, ctx, repo, tenant, mailbox) != 0 {
		t.Fatalf("informe: %+v", rep)
	}
}

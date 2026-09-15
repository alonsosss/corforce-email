package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const testDKIMPEM = "-----BEGIN RSA PRIVATE KEY-----\neA==\n-----END RSA PRIVATE KEY-----"

type dkimFixture struct {
	uc      *DKIMUseCase
	dir     *apptest.Directory
	store   *apptest.Store
	lock    *apptest.DKIMLock
	tenants *apptest.Tenants
	metrics *apptest.DKIMMetrics
}

func newDKIMFixture() *dkimFixture {
	f := &dkimFixture{dir: apptest.NewDirectory(), store: apptest.NewStore(), lock: &apptest.DKIMLock{},
		tenants: &apptest.Tenants{Gone: map[uuid.UUID]bool{}}, metrics: &apptest.DKIMMetrics{}}
	sync := NewRedisSync(f.store, f.dir, apptest.NewPolicyReader(), zap.NewNop())
	f.uc = NewDKIMUseCase(DKIMDeps{Directory: f.dir, Lock: f.lock, Sync: sync, Tenants: f.tenants, Metrics: f.metrics, Logger: zap.NewNop()})
	return f
}

func conElCerrojo(context.Context) (func(), bool) { return func() {}, true }

// seed deja claves en los motores sin pasar por el caso de uso, como quedarian tras un fallo
// entre pasos o antes de esta regla.
func (f *dkimFixture) seed(t *testing.T, domainName string, selectors ...string) {
	t.Helper()
	for _, s := range selectors {
		if err := f.store.HSet(context.Background(), domain.RedisDKIMPrivKeys, domain.DKIMKeyField(s, domainName), testDKIMPEM); err != nil {
			t.Fatal(err)
		}
		if err := f.store.HSet(context.Background(), domain.RedisDKIMSelectors, domainName, s); err != nil {
			t.Fatal(err)
		}
	}
}

// signs dice que selectores del dominio hay en DKIM_PRIV_KEYS y cual firma.
func (f *dkimFixture) signs(domainName string) (map[string]bool, string) {
	have := map[string]bool{}
	for field := range f.store.Hashes[domain.RedisDKIMPrivKeys] {
		if s, d, ok := domain.SplitDKIMKeyField(field); ok && d == domainName {
			have[s] = true
		}
	}
	return have, f.store.Hashes[domain.RedisDKIMSelectors][domainName]
}

func dkimKeys(selectors ...string) []domain.DKIMKey {
	out := make([]domain.DKIMKey, 0, len(selectors))
	for _, s := range selectors {
		out = append(out, domain.DKIMKey{Selector: s, PrivateKeyPEM: testDKIMPEM})
	}
	return out
}

func TestSoloEntranClavesDeUnDominioActivoDeLaEmpresa(t *testing.T) {
	f := newDKIMFixture()
	ctx := context.Background()
	mine, other := uuid.New(), uuid.New()
	f.dir.Domains["acme.com"] = mine
	f.dir.Domains["ajeno.com"] = other
	f.dir.InactiveDomains["baja.com"] = mine
	f.dir.InactiveDomains["ajena-baja.com"] = other

	if err := f.uc.PutKeys(ctx, mine, "ACME.com", dkimKeys("s1")); err != nil {
		t.Fatalf("dominio activo de la empresa: %v", err)
	}
	if have, active := f.signs("acme.com"); !have["s1"] || active != "s1" {
		t.Fatalf("acme.com: %v firma %q", have, active)
	}
	for _, name := range []string{"baja.com", "nuevo.com"} {
		if err := f.uc.PutKeys(ctx, mine, name, dkimKeys("s1")); !errors.Is(err, domain.ErrDKIMDomainNotActive) {
			t.Errorf("%s (inactivo o fuera del directorio): %v", name, err)
		}
		if err := f.uc.PutKey(ctx, mine, domain.DKIMKey{Domain: name, Selector: "s1", PrivateKeyPEM: testDKIMPEM}); !errors.Is(err, domain.ErrDKIMDomainNotActive) {
			t.Errorf("%s por la forma anterior: %v", name, err)
		}
	}
	for _, name := range []string{"ajeno.com", "ajena-baja.com"} {
		if err := f.uc.PutKeys(ctx, mine, name, dkimKeys("s1")); !errors.Is(err, domain.ErrObjectNotOwned) {
			t.Errorf("%s (de otra empresa): %v", name, err)
		}
		if err := f.uc.DeleteDomain(ctx, mine, name); !errors.Is(err, domain.ErrObjectNotOwned) {
			t.Errorf("retirar las de %s: %v", name, err)
		}
	}
	if names, _ := f.uc.sync.DKIMDomains(ctx); len(names) != 1 || names[0] != "acme.com" {
		t.Fatalf("en los motores solo acme.com: %v", names)
	}
	if len(f.lock.Taken) == 0 {
		t.Fatal("las decisiones se toman con el cerrojo del dominio")
	}
}

// El juego completo deja exactamente esas claves del dominio: una que la fila de domain-service
// ya olvido (una rotacion que no llego a retirarla) no sobrevive a la siguiente publicacion.
func TestElJuegoCompletoRetiraLosSelectoresQueNoVienen(t *testing.T) {
	f := newDKIMFixture()
	ctx := context.Background()
	tenant := uuid.New()
	f.dir.Domains["acme.com"] = tenant
	f.dir.Domains["sub.acme.com"] = tenant
	f.seed(t, "acme.com", "viejo")
	f.seed(t, "sub.acme.com", "s1")

	if err := f.uc.PutKeys(ctx, tenant, "acme.com", dkimKeys("nuevo", "anterior")); err != nil {
		t.Fatal(err)
	}
	have, active := f.signs("acme.com")
	if len(have) != 2 || !have["nuevo"] || !have["anterior"] || active != "anterior" {
		t.Fatalf("acme.com: %v firma %q", have, active)
	}
	if have, active := f.signs("sub.acme.com"); !have["s1"] || active != "s1" {
		t.Fatalf("sub.acme.com no se toca: %v firma %q", have, active)
	}

	// La forma anterior conserva las demas y deja firmando la que llega.
	if err := f.uc.PutKey(ctx, tenant, domain.DKIMKey{Domain: "acme.com", Selector: "otra", PrivateKeyPEM: testDKIMPEM}); err != nil {
		t.Fatal(err)
	}
	if have, active := f.signs("acme.com"); len(have) != 3 || active != "otra" {
		t.Fatalf("forma anterior: %v firma %q", have, active)
	}
}

func TestElJuegoDeClavesSeValida(t *testing.T) {
	f := newDKIMFixture()
	tenant := uuid.New()
	f.dir.Domains["acme.com"] = tenant
	casos := map[string]struct {
		domain string
		keys   []domain.DKIMKey
	}{
		"sin claves":          {"acme.com", nil},
		"mas de las que hay":  {"acme.com", dkimKeys("a", "b", "c")},
		"selector repetido":   {"acme.com", dkimKeys("a", "A")},
		"selector con puntos": {"acme.com", dkimKeys("a.b")},
		"pem ilegible":        {"acme.com", []domain.DKIMKey{{Selector: "a", PrivateKeyPEM: "no es pem"}}},
		"dominio invalido":    {"no es un dominio", dkimKeys("a")},
	}
	for nombre, c := range casos {
		var verr *domain.ValidationError
		if err := f.uc.PutKeys(context.Background(), tenant, c.domain, c.keys); !errors.As(err, &verr) {
			t.Errorf("%s: %v", nombre, err)
		}
	}
	if names, _ := f.uc.sync.DKIMDomains(context.Background()); len(names) != 0 {
		t.Fatalf("nada invalido llega a los motores: %v", names)
	}
}

func TestUnDominioQueLaCeldaYaNoSirvePierdeSusClaves(t *testing.T) {
	f := newDKIMFixture()
	ctx := context.Background()
	tenant := uuid.New()
	f.dir.Domains["acme.com"] = tenant
	f.dir.InactiveDomains["baja.com"] = tenant
	f.seed(t, "acme.com", "s1")
	f.seed(t, "baja.com", "s1", "s2")
	f.seed(t, "borrado.com", "s1")

	for i := 0; i < 2; i++ {
		for _, name := range []string{"acme.com", "baja.com", "borrado.com", "no es un dominio"} {
			if err := f.uc.ForgetIfNotServed(ctx, name); err != nil {
				t.Fatalf("%s (pasada %d): %v", name, i+1, err)
			}
		}
	}
	if names, _ := f.uc.sync.DKIMDomains(ctx); len(names) != 1 || names[0] != "acme.com" {
		t.Fatalf("solo quedan las del dominio activo: %v", names)
	}
}

func TestElRepasoRetiraLoQueLaCeldaNoDebeFirmar(t *testing.T) {
	f := newDKIMFixture()
	ctx := context.Background()
	activa, retirada := uuid.New(), uuid.New()
	f.dir.Domains["acme.com"] = activa
	f.dir.Domains["otro.acme.com"] = activa
	f.dir.InactiveDomains["baja.com"] = activa
	f.dir.Domains["vieja.com"] = retirada
	f.tenants.Gone[retirada] = true
	f.seed(t, "acme.com", "s1", "s2")
	f.seed(t, "otro.acme.com", "s1")
	f.seed(t, "baja.com", "s1")
	f.seed(t, "vieja.com", "s1")
	f.seed(t, "fantasma.com", "s1")
	if err := f.store.HSet(ctx, domain.RedisDKIMSelectors, "solo-selector.com", "s1"); err != nil {
		t.Fatal(err)
	}

	rep, err := f.uc.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep != (DKIMReconcileReport{Domains: 6, NotServed: 3, TenantGone: 1}) {
		t.Fatalf("informe: %+v", rep)
	}
	if names, _ := f.uc.sync.DKIMDomains(ctx); len(names) != 2 || names[0] != "acme.com" || names[1] != "otro.acme.com" {
		t.Fatalf("quedan: %v", names)
	}
	if have, active := f.signs("acme.com"); len(have) != 2 || active != "s2" {
		t.Fatalf("las de acme.com no se tocan: %v firma %q", have, active)
	}
	if f.tenants.Calls != 2 {
		t.Fatalf("una consulta por empresa, no por dominio: %d", f.tenants.Calls)
	}

	// Una segunda pasada no encuentra nada que retirar.
	if rep, err := f.uc.Reconcile(ctx); err != nil || rep.NotServed+rep.TenantGone != 0 {
		t.Fatalf("segunda pasada: %+v %v", rep, err)
	}
}

// Sin respuesta de organization no se decide nada sobre la empresa: sus claves se quedan. Lo que
// no depende de ella (un dominio fuera del directorio) se retira igual.
func TestElRepasoSinOrganizationConservaLasClavesDeSusEmpresas(t *testing.T) {
	f := newDKIMFixture()
	ctx := context.Background()
	tenant := uuid.New()
	f.dir.Domains["acme.com"] = tenant
	f.dir.Domains["beta.com"] = tenant
	f.seed(t, "acme.com", "s1")
	f.seed(t, "beta.com", "s1")
	f.seed(t, "fantasma.com", "s1")
	f.tenants.Down = true

	rep, err := f.uc.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep != (DKIMReconcileReport{Domains: 3, NotServed: 1, Unresolved: 2}) || f.tenants.Calls != 1 {
		t.Fatalf("informe: %+v, consultas %d", rep, f.tenants.Calls)
	}
	if names, _ := f.uc.sync.DKIMDomains(ctx); len(names) != 2 {
		t.Fatalf("quedan las de la empresa sin respuesta: %v", names)
	}
}

// El repaso decide con lo que vio al empezar, pero retira con el cerrojo tomado y leyendo otra
// vez: un dominio activado entretanto conserva las claves que acaba de recibir.
func TestElRepasoNoRetiraUnDominioActivadoEntretanto(t *testing.T) {
	f := newDKIMFixture()
	ctx := context.Background()
	tenant := uuid.New()
	f.seed(t, "nuevo.com", "s1")
	f.lock.Before = func(name string) {
		if name == "nuevo.com" {
			f.dir.Domains["nuevo.com"] = tenant
		}
	}
	rep, err := f.uc.Reconcile(ctx)
	if err != nil || rep.NotServed != 0 {
		t.Fatalf("informe: %+v %v", rep, err)
	}
	if have, _ := f.signs("nuevo.com"); !have["s1"] {
		t.Fatal("la clave del dominio recien activado se retiro")
	}
}

func TestElRepasoSoloCorreEnLaReplicaConElCerrojo(t *testing.T) {
	f := newDKIMFixture()
	ctx := context.Background()
	f.seed(t, "fantasma.com", "s1")

	f.uc.reconcileTick(ctx, time.Minute, func(context.Context) (func(), bool) { return nil, false })
	if names, _ := f.uc.sync.DKIMDomains(ctx); len(names) != 1 {
		t.Fatalf("sin el cerrojo no se toca nada: %v", names)
	}
	released := false
	f.uc.reconcileTick(ctx, time.Minute, func(context.Context) (func(), bool) { return func() { released = true }, true })
	if names, _ := f.uc.sync.DKIMDomains(ctx); len(names) != 0 || !released {
		t.Fatalf("con el cerrojo se retira y se suelta: %v, soltado %v", names, released)
	}
}

// El repaso cuenta lo que retira, por motivo, y lo que conserva sin respuesta de organization, y
// cada pasada entera sella la ultima completa, tambien la que no pudo preguntar por una empresa.
func TestElRepasoCuentaLoQueRetiraYSellaLaPasadaCompleta(t *testing.T) {
	f := newDKIMFixture()
	ctx := context.Background()
	activa, retirada := uuid.New(), uuid.New()
	f.dir.Domains["acme.com"] = activa
	f.dir.Domains["vieja.com"] = retirada
	f.tenants.Gone[retirada] = true
	f.seed(t, "acme.com", "s1")
	f.seed(t, "vieja.com", "s1")
	f.seed(t, "fantasma.com", "s1")

	antes := time.Now()
	f.uc.reconcileTick(ctx, time.Minute, conElCerrojo)
	m := f.metrics
	if m.Removed[domain.DKIMNotServed] != 1 || m.Removed[domain.DKIMTenantGone] != 1 || m.Unresolved != 0 ||
		len(m.Reconciled) != 1 || m.Reconciled[0].Before(antes) {
		t.Fatalf("primera pasada: %+v", m)
	}

	f.tenants.Down = true
	f.uc.reconcileTick(ctx, time.Minute, conElCerrojo)
	if m.Removed[domain.DKIMNotServed] != 1 || m.Removed[domain.DKIMTenantGone] != 1 || m.Unresolved != 1 || len(m.Reconciled) != 2 {
		t.Fatalf("sin organization: %+v", m)
	}

	// La replica sin el cerrojo no repasa ni cuenta nada.
	f.uc.reconcileTick(ctx, time.Minute, func(context.Context) (func(), bool) { return nil, false })
	if m.Unresolved != 1 || len(m.Reconciled) != 2 {
		t.Fatalf("sin el cerrojo: %+v", m)
	}
}

// Una pasada que no termina en su plazo no sella la ultima completa, pero lo que ya retiro cuenta.
// Una pasada cortada por el apagado tampoco la sella.
func TestElRepasoIncompletoNoSellaLaPasada(t *testing.T) {
	f := newDKIMFixture()
	f.seed(t, "a.com", "s1")
	f.seed(t, "b.com", "s1")
	f.lock.Before = func(name string) {
		if name == "a.com" {
			time.Sleep(50 * time.Millisecond)
		}
	}
	f.uc.reconcileTick(context.Background(), 10*time.Millisecond, conElCerrojo)
	if f.metrics.Removed[domain.DKIMNotServed] != 1 || len(f.metrics.Reconciled) != 0 {
		t.Fatalf("pasada fuera de plazo: %+v", f.metrics)
	}
	if names, _ := f.uc.sync.DKIMDomains(context.Background()); len(names) != 1 || names[0] != "b.com" {
		t.Fatalf("la pasada se corta en el plazo: quedan %v", names)
	}

	f.lock.Before = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.uc.reconcileTick(ctx, time.Minute, conElCerrojo)
	if f.metrics.Removed[domain.DKIMNotServed] != 1 || len(f.metrics.Reconciled) != 0 {
		t.Fatalf("pasada cortada por el apagado: %+v", f.metrics)
	}
}

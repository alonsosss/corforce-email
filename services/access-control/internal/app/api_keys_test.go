package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/access-control/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// keyUsers es fakeUserRoles con politica: la de quien crea la clave.
type keyUsers struct {
	fakeUserRoles
	policy *domain.AccessPolicy
}

func (k *keyUsers) GetAccessPolicy(context.Context, uuid.UUID, uuid.UUID) (*domain.AccessPolicy, error) {
	if k.policy == nil {
		return &domain.AccessPolicy{}, nil
	}
	return k.policy, nil
}

type fakeKeys struct {
	keys      map[uuid.UUID]*domain.APIKey
	events    []domain.APIKeyEvent
	grantable []*domain.Permission
	touched   int
	rehashed  int
	err       error
}

func (f *fakeKeys) Create(_ context.Context, k *domain.APIKey, e domain.APIKeyEvent) error {
	if f.err != nil {
		return f.err
	}
	cp := *k
	f.keys[k.ID] = &cp
	f.events = append(f.events, e)
	return nil
}
func (f *fakeKeys) List(_ context.Context, tenantID uuid.UUID) ([]*domain.APIKey, error) {
	var out []*domain.APIKey
	for _, k := range f.keys {
		if k.TenantID == tenantID {
			out = append(out, k)
		}
	}
	return out, nil
}
func (f *fakeKeys) Get(_ context.Context, tenantID, id uuid.UUID) (*domain.APIKey, error) {
	if k, ok := f.keys[id]; ok && k.TenantID == tenantID {
		return k, nil
	}
	return nil, domain.ErrAPIKeyNotFound
}
func (f *fakeKeys) GetByPrefix(_ context.Context, prefix string) (*domain.APIKey, error) {
	if f.err != nil {
		return nil, f.err
	}
	for _, k := range f.keys {
		if k.Prefix == prefix {
			cp := *k
			return &cp, nil
		}
	}
	return nil, domain.ErrAPIKeyNotFound
}
func (f *fakeKeys) CountActive(_ context.Context, tenantID uuid.UUID, now time.Time) (int, error) {
	n := 0
	for _, k := range f.keys {
		if k.TenantID == tenantID && k.Status(now) == domain.APIKeyActive {
			n++
		}
	}
	return n, nil
}
func (f *fakeKeys) Revoke(_ context.Context, tenantID, id, actor uuid.UUID, at time.Time, event func(*domain.APIKey) domain.APIKeyEvent) (*domain.APIKey, error) {
	k, ok := f.keys[id]
	if !ok || k.TenantID != tenantID {
		return nil, domain.ErrAPIKeyNotFound
	}
	if k.RevokedAt != nil {
		return nil, domain.ErrAPIKeyRevoked
	}
	k.RevokedAt, k.RevokedBy = &at, &actor
	f.events = append(f.events, event(k))
	return k, nil
}
func (f *fakeKeys) TouchUsage(context.Context, uuid.UUID, time.Time, string, time.Duration) error {
	f.touched++
	return nil
}
func (f *fakeKeys) Rehash(_ context.Context, id uuid.UUID, hash []byte, keyID string) error {
	f.rehashed++
	f.keys[id].SecretHash, f.keys[id].HashKeyID = hash, keyID
	return nil
}
func (f *fakeKeys) GrantablePermissions(context.Context) ([]*domain.Permission, error) {
	return f.grantable, nil
}

// fakeHasher firma con una llave activa y conoce una retirada.
type fakeHasher struct{ active string }

func (h *fakeHasher) sum(id string, in []byte) []byte {
	m := hmac.New(sha256.New, []byte("llave-"+id))
	m.Write(in)
	return m.Sum(nil)
}
func (h *fakeHasher) Hash(in []byte) ([]byte, string, error) {
	return h.sum(h.active, in), h.active, nil
}
func (h *fakeHasher) Verify(in, hash []byte, id string) (bool, bool, error) {
	if id != "a1" && id != "a0" {
		return false, false, errors.New("llave desconocida")
	}
	return hmac.Equal(h.sum(id, in), hash), id == h.active, nil
}

type fakeTenants struct{ active bool }

func (f *fakeTenants) IsActive(context.Context, uuid.UUID) (bool, error) { return f.active, nil }

type fakeRevocations struct{ ids []uuid.UUID }

func (f *fakeRevocations) Revoked(_ context.Context, id uuid.UUID) { f.ids = append(f.ids, id) }

type fakeKeyMetrics struct{ results []string }

func (f *fakeKeyMetrics) Resolved(r string) { f.results = append(f.results, r) }

var (
	permSend = &domain.Permission{ID: uuid.New(), Module: "transactional", Resource: "messages", Action: "create", Scope: domain.PermissionScopeTenant}
	permRead = &domain.Permission{ID: uuid.New(), Module: "transactional", Resource: "messages", Action: "read", Scope: domain.PermissionScopeTenant}
)

type keysFixture struct {
	uc      *APIKeysUseCase
	keys    *fakeKeys
	users   *keyUsers
	gate    *fakeGate
	tenants *fakeTenants
	revs    *fakeRevocations
	metrics *fakeKeyMetrics
	hasher  *fakeHasher
	tenant  uuid.UUID
	owner   Actor
	now     time.Time
}

func newKeysFixture() *keysFixture {
	f := &keysFixture{
		keys:    &fakeKeys{keys: map[uuid.UUID]*domain.APIKey{}, grantable: []*domain.Permission{permSend, permRead}},
		users:   &keyUsers{},
		gate:    &fakeGate{},
		tenants: &fakeTenants{active: true},
		revs:    &fakeRevocations{},
		metrics: &fakeKeyMetrics{},
		hasher:  &fakeHasher{active: "a1"},
		tenant:  uuid.New(),
		owner:   Actor{UserID: uuid.New(), Privileged: true},
		now:     time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC),
	}
	f.users.roles = []*domain.Role{{Name: testTenantAdmin}}
	f.uc = NewAPIKeysUseCase(APIKeysDeps{
		Keys: f.keys, Hasher: f.hasher, Users: f.users, Modules: f.gate, Tenants: f.tenants, Revocations: f.revs,
		Metrics: f.metrics, SystemRoles: SystemRoles{Superadmin: testSuperadmin, TenantAdmin: testTenantAdmin},
		Random: bytes.NewReader(bytes.Repeat([]byte{7, 13, 21, 99}, 1000)), Now: func() time.Time { return f.now }, Logger: zap.NewNop(),
	})
	return f
}

func (f *keysFixture) create(t *testing.T, scopes ...ScopeRef) *CreatedAPIKey {
	t.Helper()
	if len(scopes) == 0 {
		scopes = []ScopeRef{{"transactional", "messages", "create"}}
	}
	c, err := f.uc.Create(context.Background(), CreateAPIKeyCommand{TenantID: f.tenant, Actor: f.owner, Name: "Tienda en linea", Scopes: scopes})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Las dos familias de credencial (docs/adr/0017) se distinguen desde el token y no se confunden al
// resolver: una de aprovisionamiento no pasa por clave de envio ni al reves. Cada familia va en su
// propio fixture porque el aleatorio de prueba es ciclico y repetiria el prefijo.
func TestFamiliasDeCredencial(t *testing.T) {
	crear := func(f *keysFixture, kind string) *CreatedAPIKey {
		t.Helper()
		c, err := f.uc.Create(context.Background(), CreateAPIKeyCommand{
			TenantID: f.tenant, Actor: f.owner, Name: "Integracion", Kind: kind,
			Scopes: []ScopeRef{{"transactional", "messages", "create"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	for _, c := range []struct{ kind, prefijo string }{
		{domain.APIKeyKindProvisioning, domain.APIKeyProvisioningTokenPrefix},
		{domain.APIKeyKindSending, domain.APIKeyTokenPrefix},
		// Sin familia explicita, la de envio: las claves que ya existian no cambian de significado.
		{"", domain.APIKeyTokenPrefix},
	} {
		f := newKeysFixture()
		creada := crear(f, c.kind)
		esperada := c.kind
		if esperada == "" {
			esperada = domain.APIKeyKindSending
		}
		if !strings.HasPrefix(creada.Token, c.prefijo) || creada.Key.Kind != esperada {
			t.Fatalf("%q: token %q familia %q", c.kind, creada.Token, creada.Key.Kind)
		}
		if f.keys.keys[creada.Key.ID].Kind != esperada {
			t.Fatalf("%q: la familia se guarda con la clave", c.kind)
		}
		res, err := f.uc.Resolve(context.Background(), creada.Token, "")
		if err != nil || res.Kind != esperada {
			t.Fatalf("%q: resuelta %+v %v", c.kind, res, err)
		}
		// El mismo secreto con el prefijo de la otra familia no autentica.
		otra := domain.APIKeyKindProvisioning
		if esperada == domain.APIKeyKindProvisioning {
			otra = domain.APIKeyKindSending
		}
		_, _, secret, _ := domain.ParseAPIKeyToken(creada.Token)
		suplantado := domain.FormatAPIKeyToken(otra, creada.Key.Prefix, secret)
		if _, err := f.uc.Resolve(context.Background(), suplantado, ""); !errors.Is(err, domain.ErrAPIKeyInvalid) {
			t.Errorf("%q con el prefijo de la otra familia: %v", c.kind, err)
		}
	}
	// Una familia inventada no crea nada.
	f := newKeysFixture()
	if _, err := f.uc.Create(context.Background(), CreateAPIKeyCommand{
		TenantID: f.tenant, Actor: f.owner, Name: "X", Kind: "inventada",
		Scopes: []ScopeRef{{"transactional", "messages", "create"}},
	}); err == nil {
		t.Fatal("una familia desconocida debe rechazarse")
	}
}

func TestCrearClaveGuardaSoloElHash(t *testing.T) {
	f := newKeysFixture()
	c := f.create(t, ScopeRef{"transactional", "messages", "create"}, ScopeRef{" transactional", "messages", "create"})
	kind, prefix, secret, err := domain.ParseAPIKeyToken(c.Token)
	if err != nil || prefix != c.Key.Prefix || kind != domain.APIKeyKindSending {
		t.Fatalf("token: %q %v", c.Token, err)
	}
	stored := f.keys.keys[c.Key.ID]
	if bytes.Contains(stored.SecretHash, []byte(secret)) || stored.HashKeyID != "a1" || len(stored.Scopes) != 1 {
		t.Fatalf("guardado: %+v", stored)
	}
	if len(f.keys.events) != 1 || f.keys.events[0].Type != domain.APIKeyEventCreated || f.keys.events[0].ActorID != f.owner.UserID {
		t.Fatalf("evento de alta: %+v", f.keys.events)
	}
}

func TestCrearClaveValida(t *testing.T) {
	f := newKeysFixture()
	ctx := context.Background()
	past := f.now.Add(10 * time.Minute)
	far := f.now.Add(6 * 365 * 24 * time.Hour)
	var validation *APIKeyValidationError
	for name, cmd := range map[string]CreateAPIKeyCommand{
		"sin nombre":       {Name: " ", Scopes: []ScopeRef{{"transactional", "messages", "create"}}},
		"sin permisos":     {Name: "x"},
		"caduca ya":        {Name: "x", Scopes: []ScopeRef{{"transactional", "messages", "create"}}, ExpiresAt: &past},
		"caduca muy lejos": {Name: "x", Scopes: []ScopeRef{{"transactional", "messages", "create"}}, ExpiresAt: &far},
	} {
		cmd.TenantID, cmd.Actor = f.tenant, f.owner
		if _, err := f.uc.Create(ctx, cmd); !errors.As(err, &validation) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := f.uc.Create(ctx, CreateAPIKeyCommand{TenantID: f.tenant, Actor: f.owner, Name: "x",
		Scopes: []ScopeRef{{"access", "roles", "create"}}}); !errors.Is(err, domain.ErrAPIKeyScopeNotGrantable) {
		t.Fatalf("un permiso fuera del catalogo de claves: %v", err)
	}
}

// Quien no es administrador solo da a la clave permisos que tiene.
func TestCrearClaveAcotadaAQuienLaCrea(t *testing.T) {
	f := newKeysFixture()
	f.owner.Privileged = false
	f.users.policy = &domain.AccessPolicy{Permissions: []domain.Permission{*permRead}}
	_, err := f.uc.Create(context.Background(), CreateAPIKeyCommand{TenantID: f.tenant, Actor: f.owner, Name: "x",
		Scopes: []ScopeRef{{"transactional", "messages", "create"}}})
	if !errors.Is(err, domain.ErrPermissionNotHeld) {
		t.Fatalf("permiso no tenido: %v", err)
	}
	got, err := f.uc.GrantableFor(context.Background(), f.owner, f.tenant)
	if err != nil || len(got) != 1 || got[0].Action != "read" {
		t.Fatalf("solo se ofrece lo que se tiene: %+v %v", got, err)
	}
}

func TestLimiteDeClavesVigentes(t *testing.T) {
	f := newKeysFixture()
	for i := 0; i < domain.MaxAPIKeysPerTenant; i++ {
		id := uuid.New()
		f.keys.keys[id] = &domain.APIKey{ID: id, TenantID: f.tenant, Prefix: uuid.NewString()}
	}
	if _, err := f.uc.Create(context.Background(), CreateAPIKeyCommand{TenantID: f.tenant, Actor: f.owner, Name: "x",
		Scopes: []ScopeRef{{"transactional", "messages", "create"}}}); !errors.Is(err, domain.ErrAPIKeyLimit) {
		t.Fatalf("tope: %v", err)
	}
}

func TestResolverClave(t *testing.T) {
	f := newKeysFixture()
	c := f.create(t)
	ctx := context.Background()
	res, err := f.uc.Resolve(ctx, c.Token, "198.51.100.7")
	if err != nil || res.TenantID != f.tenant || len(res.Scopes) != 1 || f.keys.touched != 1 {
		t.Fatalf("resuelta: %+v %v", res, err)
	}
	_, _, secret, _ := domain.ParseAPIKeyToken(c.Token)
	for name, token := range map[string]string{
		"malformada":   "cfm_corto_x",
		"otro secreto": domain.FormatAPIKeyToken(domain.APIKeyKindSending, c.Key.Prefix, secret[:len(secret)-1]+"a"),
		"inexistente":  domain.FormatAPIKeyToken(domain.APIKeyKindSending, "zzzzzzzzzzzz", secret),
		// Una clave de envio presentada con el prefijo de la otra familia no autentica.
		"otra familia": domain.FormatAPIKeyToken(domain.APIKeyKindProvisioning, c.Key.Prefix, secret),
		"sin prefijo":  secret,
	} {
		if _, err := f.uc.Resolve(ctx, token, ""); !errors.Is(err, domain.ErrAPIKeyInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if f.metrics.results[0] != "ok" {
		t.Fatalf("metricas: %v", f.metrics.results)
	}
}

func TestResolverRechazaClaveSinValor(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(f *keysFixture, k *domain.APIKey){
		"revocada":         func(f *keysFixture, k *domain.APIKey) { at := f.now; k.RevokedAt = &at },
		"caducada":         func(f *keysFixture, k *domain.APIKey) { at := f.now.Add(-time.Second); k.ExpiresAt = &at },
		"empresa inactiva": func(f *keysFixture, _ *domain.APIKey) { f.tenants.active = false },
		"creador borrado":  func(f *keysFixture, _ *domain.APIKey) { f.users.accountErr = domain.ErrUserNotFound },
		"creador inactivo": func(f *keysFixture, _ *domain.APIKey) { f.users.account = &domain.UserAccount{Status: "inactive"} },
		"creador sin rol ni permiso": func(f *keysFixture, _ *domain.APIKey) {
			f.users.roles = nil
			f.users.policy = &domain.AccessPolicy{Permissions: []domain.Permission{*permRead}}
		},
		"modulo sin contratar": func(f *keysFixture, _ *domain.APIKey) {
			f.gate.availability = domain.ModuleAvailability{Restricted: true, Gated: map[string]bool{"transactional": false}}
		},
	}
	for name, mutate := range cases {
		f := newKeysFixture()
		c := f.create(t)
		mutate(f, f.keys.keys[c.Key.ID])
		if _, err := f.uc.Resolve(ctx, c.Token, ""); !errors.Is(err, domain.ErrAPIKeyInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// El alcance efectivo es el de la clave acotado a lo que su creador tiene hoy.
func TestAlcanceEfectivo(t *testing.T) {
	f := newKeysFixture()
	c := f.create(t, ScopeRef{"transactional", "messages", "create"}, ScopeRef{"transactional", "messages", "read"})
	f.users.roles = nil
	f.users.policy = &domain.AccessPolicy{Permissions: []domain.Permission{{Module: "transactional", Resource: "messages", Action: "*"}}}
	res, err := f.uc.Resolve(context.Background(), c.Token, "")
	if err != nil || len(res.Scopes) != 2 {
		t.Fatalf("comodin del creador: %+v %v", res, err)
	}
	f.users.policy = &domain.AccessPolicy{Permissions: []domain.Permission{*permRead}}
	res, err = f.uc.Resolve(context.Background(), c.Token, "")
	if err != nil || len(res.Scopes) != 1 || res.Scopes[0].Action != "read" {
		t.Fatalf("pierde el envio: %+v %v", res, err)
	}
}

func TestRehashConLaLlaveActiva(t *testing.T) {
	f := newKeysFixture()
	f.hasher.active = "a0"
	c := f.create(t)
	f.hasher.active = "a1"
	if _, err := f.uc.Resolve(context.Background(), c.Token, ""); err != nil {
		t.Fatal(err)
	}
	if f.keys.rehashed != 1 || f.keys.keys[c.Key.ID].HashKeyID != "a1" {
		t.Fatal("una clave firmada con la llave retirada se vuelve a firmar con la activa")
	}
	if _, err := f.uc.Resolve(context.Background(), c.Token, ""); err != nil || f.keys.rehashed != 1 {
		t.Fatalf("despues ya no: %v", err)
	}
}

func TestResolverSinBase(t *testing.T) {
	f := newKeysFixture()
	c := f.create(t)
	f.keys.err = errors.New("base caida")
	if _, err := f.uc.Resolve(context.Background(), c.Token, ""); err == nil || errors.Is(err, domain.ErrAPIKeyInvalid) {
		t.Fatalf("un fallo de la base no es una clave invalida: %v", err)
	}
}

func TestRevocarClave(t *testing.T) {
	f := newKeysFixture()
	c := f.create(t)
	ctx := context.Background()
	k, err := f.uc.Revoke(ctx, f.tenant, f.owner, c.Key.ID)
	if err != nil || k.RevokedAt == nil || len(f.revs.ids) != 1 || f.revs.ids[0] != c.Key.ID {
		t.Fatalf("revocada: %+v %v %v", k, err, f.revs.ids)
	}
	if f.keys.events[len(f.keys.events)-1].Type != domain.APIKeyEventRevoked {
		t.Fatal("evento de revocacion")
	}
	if _, err := f.uc.Resolve(ctx, c.Token, ""); !errors.Is(err, domain.ErrAPIKeyInvalid) {
		t.Fatalf("revocada no resuelve: %v", err)
	}
	if _, err := f.uc.Revoke(ctx, f.tenant, f.owner, c.Key.ID); !errors.Is(err, domain.ErrAPIKeyRevoked) {
		t.Fatalf("dos veces: %v", err)
	}
	if _, err := f.uc.Revoke(ctx, uuid.New(), f.owner, c.Key.ID); !errors.Is(err, domain.ErrAPIKeyNotFound) {
		t.Fatalf("de otra empresa: %v", err)
	}
}

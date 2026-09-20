package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/ports"
	"github.com/google/uuid"
)

const cfToken = "cf_token_de_prueba_0123456789abcdefXYZ"

// ── Dobles ───────────────────────────────────────────────────────────────────

// fakeDNSRepo guarda las conexiones y el modo de cada dominio sobre el fakeRepo, con la misma
// semantica que el repositorio real: una transaccion que falla no deja nada.
type fakeDNSRepo struct {
	mu        sync.Mutex
	repo      *fakeRepo
	providers map[string]*domain.DNSProviderConnection
	saveErr   error
}

func (f *fakeDNSRepo) key(tenantID uuid.UUID, p domain.DNSProvider) string {
	return tenantID.String() + "/" + string(p)
}

func (f *fakeDNSRepo) GetDNSProvider(_ context.Context, tenantID uuid.UUID, p domain.DNSProvider) (*domain.DNSProviderConnection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.providers[f.key(tenantID, p)]
	if !ok {
		return nil, domain.ErrDNSProviderNotConnected
	}
	cp := *c
	return &cp, nil
}

func (f *fakeDNSRepo) SaveDNSProvider(_ context.Context, c *domain.DNSProviderConnection) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return f.saveErr
	}
	if old, ok := f.providers[f.key(c.TenantID, c.Provider)]; ok {
		c.ID = old.ID
	}
	cp := *c
	f.providers[f.key(c.TenantID, c.Provider)] = &cp
	return nil
}

func (f *fakeDNSRepo) UpdateDNSProviderZones(_ context.Context, c *domain.DNSProviderConnection) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if stored, ok := f.providers[f.key(c.TenantID, c.Provider)]; ok && string(stored.TokenEnc) == string(c.TokenEnc) {
		stored.Zones, stored.ZonesVisible, stored.LastValidatedAt = c.Zones, c.ZonesVisible, c.LastValidatedAt
	}
	return nil
}

func (f *fakeDNSRepo) DeleteDNSProvider(_ context.Context, tenantID uuid.UUID, p domain.DNSProvider) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.providers[f.key(tenantID, p)]
	delete(f.providers, f.key(tenantID, p))
	return ok, nil
}

func (f *fakeDNSRepo) ResetDNSMode(_ context.Context, tenantID uuid.UUID, mode domain.DNSMode) (int64, error) {
	f.repo.mu.Lock()
	defer f.repo.mu.Unlock()
	var n int64
	for _, d := range f.repo.domains {
		if d.TenantID == tenantID && d.DNSMode == mode {
			d.DNSMode = domain.DNSModeManual
			n++
		}
	}
	return n, nil
}

func (f *fakeDNSRepo) SetDNSMode(_ context.Context, tenantID, id uuid.UUID, mode domain.DNSMode) error {
	f.repo.mu.Lock()
	defer f.repo.mu.Unlock()
	d, ok := f.repo.domains[id]
	if !ok || d.TenantID != tenantID {
		return domain.ErrDomainNotFound
	}
	d.DNSMode = mode
	return nil
}

func (f *fakeDNSRepo) MarkDNSPublished(_ context.Context, tenantID, id uuid.UUID, at time.Time) error {
	f.repo.mu.Lock()
	defer f.repo.mu.Unlock()
	if d, ok := f.repo.domains[id]; ok && d.TenantID == tenantID {
		d.DNSPublishedAt = &at
	}
	return nil
}

func (f *fakeDNSRepo) Transact(ctx context.Context, fn func(context.Context) error) error {
	f.mu.Lock()
	providers := make(map[string]*domain.DNSProviderConnection, len(f.providers))
	for k, v := range f.providers {
		cp := *v
		providers[k] = &cp
	}
	f.mu.Unlock()
	f.repo.mu.Lock()
	modes := make(map[uuid.UUID]domain.DNSMode, len(f.repo.domains))
	for id, d := range f.repo.domains {
		modes[id] = d.DNSMode
	}
	f.repo.mu.Unlock()
	if err := fn(ctx); err != nil {
		f.mu.Lock()
		f.providers = providers
		f.mu.Unlock()
		f.repo.mu.Lock()
		for id, m := range modes {
			if d, ok := f.repo.domains[id]; ok {
				d.DNSMode = m
			}
		}
		f.repo.mu.Unlock()
		return err
	}
	return nil
}

// fakeCloudflare es la API del proveedor en memoria: zonas por token, registros por zona y fallos
// inyectables. Refleja lo publicado en el DNS falso para que la verificacion lo vea.
type fakeCloudflare struct {
	mu       sync.Mutex
	tokens   map[string][]domain.DNSZone
	inactive map[string]bool
	records  map[string][]domain.ProviderRecord
	nextID   int
	dns      *fakeDNS
	// verifyErr, listErr y writeErr simulan fallos del proveedor.
	verifyErr error
	listErr   error
	writeErr  error
	// writeErrFor falla solo las escrituras de ese nombre.
	writeErrFor map[string]error
	writes      []string
	// tokensSeen son los tokens con los que se llamo.
	tokensSeen []string
}

func newFakeCloudflare(dns *fakeDNS) *fakeCloudflare {
	return &fakeCloudflare{
		tokens: map[string][]domain.DNSZone{}, inactive: map[string]bool{},
		records: map[string][]domain.ProviderRecord{}, dns: dns, writeErrFor: map[string]error{},
	}
}

func (f *fakeCloudflare) auth(token domain.APIToken) ([]domain.DNSZone, error) {
	f.tokensSeen = append(f.tokensSeen, token.Reveal())
	zones, ok := f.tokens[token.Reveal()]
	if !ok {
		return nil, domain.ErrDNSProviderTokenInvalid
	}
	return zones, nil
}

func (f *fakeCloudflare) VerifyToken(_ context.Context, token domain.APIToken) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.verifyErr != nil {
		return f.verifyErr
	}
	if _, err := f.auth(token); err != nil {
		return err
	}
	if f.inactive[token.Reveal()] {
		return domain.ErrDNSProviderTokenInvalid
	}
	return nil
}

func (f *fakeCloudflare) ListZones(_ context.Context, token domain.APIToken) ([]domain.DNSZone, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.auth(token)
}

func (f *fakeCloudflare) zoneAllowed(token domain.APIToken, zone domain.DNSZone) error {
	zones, err := f.auth(token)
	if err != nil {
		return err
	}
	for _, z := range zones {
		if z.ID == zone.ID {
			return nil
		}
	}
	return domain.ErrDNSZoneNotFound
}

func (f *fakeCloudflare) ListRecords(_ context.Context, token domain.APIToken, zone domain.DNSZone, recordType, name string) ([]domain.ProviderRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.zoneAllowed(token, zone); err != nil {
		return nil, err
	}
	var out []domain.ProviderRecord
	for _, r := range f.records[zone.ID] {
		if r.Type == recordType && domain.SameHost(r.Name, name) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeCloudflare) write(token domain.APIToken, zone domain.DNSZone, op, name string) error {
	if err := f.zoneAllowed(token, zone); err != nil {
		return err
	}
	if !domain.HostInZone(name, zone.Name) {
		return fmt.Errorf("escritura fuera de la zona %s: %s", zone.Name, name)
	}
	if f.writeErr != nil {
		return f.writeErr
	}
	if err := f.writeErrFor[name]; err != nil {
		return err
	}
	f.writes = append(f.writes, op+" "+name)
	return nil
}

func (f *fakeCloudflare) CreateRecord(_ context.Context, token domain.APIToken, zone domain.DNSZone, rec domain.ProviderRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.write(token, zone, "crear", rec.Name); err != nil {
		return err
	}
	f.nextID++
	rec.ID = fmt.Sprintf("r%d", f.nextID)
	f.records[zone.ID] = append(f.records[zone.ID], comoCloudflare(rec))
	f.mirror()
	return nil
}

// comoCloudflare guarda el registro como lo hace el proveedor real: el contenido de un TXT queda
// entre comillas (el adaptador se las pone) y al listarlo vuelve asi, no como se escribio.
func comoCloudflare(rec domain.ProviderRecord) domain.ProviderRecord {
	if rec.Type == "TXT" {
		rec.Content = domain.QuoteTXT(rec.Content)
	}
	return rec
}

// sinComillas es lo que responde el DNS para un TXT guardado con comillas: las cadenas unidas.
func sinComillas(v string) string {
	var b strings.Builder
	dentro, escapado := false, false
	for _, c := range strings.TrimSpace(v) {
		switch {
		case escapado:
			b.WriteRune(c)
			escapado = false
		case dentro && c == '\\':
			escapado = true
		case c == '"':
			dentro = !dentro
		case dentro:
			b.WriteRune(c)
		}
	}
	if !strings.HasPrefix(strings.TrimSpace(v), `"`) {
		return strings.TrimSpace(v)
	}
	return b.String()
}

func (f *fakeCloudflare) UpdateRecord(_ context.Context, token domain.APIToken, zone domain.DNSZone, rec domain.ProviderRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.write(token, zone, "cambiar", rec.Name); err != nil {
		return err
	}
	for i, r := range f.records[zone.ID] {
		if r.ID == rec.ID {
			f.records[zone.ID][i] = comoCloudflare(rec)
			f.mirror()
			return nil
		}
	}
	return domain.ErrDNSZoneNotFound
}

func (f *fakeCloudflare) DeleteRecord(_ context.Context, token domain.APIToken, zone domain.DNSZone, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var name string
	for _, r := range f.records[zone.ID] {
		if r.ID == id {
			name = r.Name
		}
	}
	if err := f.write(token, zone, "borrar", name); err != nil {
		return err
	}
	kept := f.records[zone.ID][:0]
	for _, r := range f.records[zone.ID] {
		if r.ID != id {
			kept = append(kept, r)
		}
	}
	f.records[zone.ID] = kept
	f.mirror()
	return nil
}

// seed pone en la zona un registro del cliente.
func (f *fakeCloudflare) seed(zoneID string, rec domain.ProviderRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	rec.ID = fmt.Sprintf("r%d", f.nextID)
	f.records[zoneID] = append(f.records[zoneID], rec)
	f.mirror()
}

func (f *fakeCloudflare) find(zoneID, recordType, name string) []domain.ProviderRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.ProviderRecord
	for _, r := range f.records[zoneID] {
		if r.Type == recordType && domain.SameHost(r.Name, name) {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeCloudflare) mirror() {
	f.dns.txt, f.dns.mx = map[string][]string{}, map[string][]domain.MXRecord{}
	for _, records := range f.records {
		for _, r := range records {
			switch r.Type {
			case "MX":
				f.dns.mx[r.Name] = append(f.dns.mx[r.Name], domain.MXRecord{Host: r.Content, Priority: uint16(r.Priority)})
			default:
				f.dns.txt[r.Name] = append(f.dns.txt[r.Name], sinComillas(r.Content))
			}
		}
	}
}

type dnsEvent struct {
	subject string
	actor   uuid.UUID
	pub     *domain.DNSPublication
	reset   int64
}

type fakeDNSEvents struct {
	mu     sync.Mutex
	events []dnsEvent
	err    error
}

func (f *fakeDNSEvents) add(e dnsEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, e)
	return nil
}

func (f *fakeDNSEvents) DNSProviderConnected(_ context.Context, c *domain.DNSProviderConnection) error {
	return f.add(dnsEvent{subject: "domains.dns_provider.connected", actor: c.ConnectedBy})
}

func (f *fakeDNSEvents) DNSProviderDisconnected(_ context.Context, _ uuid.UUID, _ domain.DNSProvider, actor uuid.UUID, reset int64, _ time.Time) error {
	return f.add(dnsEvent{subject: "domains.dns_provider.disconnected", actor: actor, reset: reset})
}

func (f *fakeDNSEvents) DNSPublished(_ context.Context, _ *domain.Domain, p *domain.DNSPublication, actor uuid.UUID) error {
	return f.add(dnsEvent{subject: "domains.domain.dns_published", actor: actor, pub: p})
}

func (f *fakeDNSEvents) count(subject string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, e := range f.events {
		if e.subject == subject {
			n++
		}
	}
	return n
}

func (f *fakeDNSEvents) last() dnsEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.events[len(f.events)-1]
}

type dnsHarness struct {
	*harness
	dnsRepo *fakeDNSRepo
	cf      *fakeCloudflare
	dnsEvts *fakeDNSEvents
}

func newDNSHarness(t *testing.T) *dnsHarness {
	t.Helper()
	h := newHarness(t)
	dh := &dnsHarness{
		harness: h,
		dnsRepo: &fakeDNSRepo{repo: h.repo, providers: map[string]*domain.DNSProviderConnection{}},
		cf:      newFakeCloudflare(h.dns),
		dnsEvts: &fakeDNSEvents{},
	}
	dh.cf.tokens[cfToken] = []domain.DNSZone{{ID: "zacme", Name: "acme.com"}, {ID: "zotra", Name: "otra.net"}}
	h.uc = New(Deps{
		Repo: h.repo, DNS: h.dns, Cipher: h.keyRing,
		MailDirectory: h.directory, MailSecurity: h.security, DomainIndex: h.index, Events: h.events, KeyEvents: h.events,
		DNSProviders: dh.dnsRepo, DNSAPIs: map[domain.DNSProvider]ports.DNSProviderAPI{domain.DNSProviderCloudflare: dh.cf},
		DNSEvents: dh.dnsEvts,
		Platform:  testPlatform, PlatformHostname: platformHost,
		DKIMRotationGrace: 72 * time.Hour,
		Now:               func() time.Time { return h.now },
	})
	return dh
}

func (h *dnsHarness) connect(t *testing.T) *domain.DNSProviderConnection {
	t.Helper()
	c, err := h.uc.ConnectDNSProvider(context.Background(), h.tenantID, h.actor, "cloudflare", cfToken)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return c
}

func (h *dnsHarness) automatic(t *testing.T, name string, purpose domain.Purpose) *domain.Domain {
	t.Helper()
	d := h.create(t, name, purpose)
	if _, err := h.uc.SetDNSMode(context.Background(), h.tenantID, d.ID, "cloudflare"); err != nil {
		t.Fatalf("SetDNSMode: %v", err)
	}
	return d
}

func (h *dnsHarness) publish(t *testing.T, id uuid.UUID, replace ...string) *PublishDNSResult {
	t.Helper()
	res, err := h.uc.PublishDNS(context.Background(), h.tenantID, id, h.actor, replace)
	if err != nil {
		t.Fatalf("PublishDNS: %v", err)
	}
	return res
}

func actions(p *domain.DNSPublication) map[domain.RecordKind]domain.RecordAction {
	out := map[domain.RecordKind]domain.RecordAction{}
	for _, r := range p.Records {
		out[r.Kind] = r.Action
	}
	return out
}

// ── Conexion ─────────────────────────────────────────────────────────────────

func TestConectarGuardaElTokenCifradoYAnunciaSinEl(t *testing.T) {
	h := newDNSHarness(t)
	ctx := context.Background()
	if c, err := h.uc.DNSProviderStatus(ctx, h.tenantID, "cloudflare"); err != nil || c != nil {
		t.Fatalf("sin conectar: %v %v", c, err)
	}
	c := h.connect(t)
	if c.TokenHint != "fXYZ" || c.ZonesVisible != 2 || strings.Join(c.Zones, ",") != "acme.com,otra.net" || c.ConnectedBy != h.actor {
		t.Errorf("conexion %+v", c)
	}
	stored, err := h.dnsRepo.GetDNSProvider(ctx, h.tenantID, domain.DNSProviderCloudflare)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored.TokenEnc), cfToken) || strings.Contains(string(stored.TokenEnc), "0123456789") {
		t.Fatal("el token se guardo en claro")
	}
	plain, err := h.keyRing.Decrypt(stored.TokenEnc)
	if err != nil || string(plain) != cfToken {
		t.Fatalf("el token cifrado no se recupera: %v", err)
	}
	if h.dnsEvts.count("domains.dns_provider.connected") != 1 || h.dnsEvts.last().actor != h.actor {
		t.Errorf("eventos %+v", h.dnsEvts.events)
	}
	if got, err := h.uc.DNSProviderStatus(ctx, h.tenantID, "cloudflare"); err != nil || got == nil || got.TokenHint != "fXYZ" {
		t.Errorf("estado %+v %v", got, err)
	}
	if got, _ := h.uc.DNSProviderStatus(ctx, uuid.New(), "cloudflare"); got != nil {
		t.Error("otra empresa ve la conexion de esta")
	}
}

func TestConectarRechazaTokensQueNoSirven(t *testing.T) {
	h := newDNSHarness(t)
	h.cf.tokens["token_sin_zonas_0123456789abcdef"] = nil
	h.cf.tokens["token_inactivo_0123456789abcdefgh"] = []domain.DNSZone{{ID: "z", Name: "acme.com"}}
	h.cf.inactive["token_inactivo_0123456789abcdefgh"] = true
	ctx := context.Background()
	for nombre, c := range map[string]struct {
		provider, token string
		want            error
	}{
		"formato no valido":     {"cloudflare", "corto", domain.ErrInvalidDNSProviderToken},
		"con salto de linea":    {"cloudflare", "abcdefghijklmnopqrst\nuvwxyz", domain.ErrInvalidDNSProviderToken},
		"desconocido":           {"cloudflare", "token_que_no_existe_0123456789", domain.ErrDNSProviderTokenInvalid},
		"inactivo":              {"cloudflare", "token_inactivo_0123456789abcdefgh", domain.ErrDNSProviderTokenInvalid},
		"sin zonas":             {"cloudflare", "token_sin_zonas_0123456789abcdef", domain.ErrDNSProviderNoZones},
		"proveedor no admitido": {"route53", cfToken, domain.ErrUnsupportedDNSProvider},
	} {
		_, err := h.uc.ConnectDNSProvider(ctx, h.tenantID, h.actor, c.provider, c.token)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: %v", nombre, err)
		}
		if err != nil && strings.Contains(err.Error(), c.token) && len(c.token) > 10 {
			t.Errorf("%s: el error lleva el token: %v", nombre, err)
		}
	}
	h.cf.verifyErr = domain.ErrDNSProviderPermissionDenied
	if _, err := h.uc.ConnectDNSProvider(ctx, h.tenantID, h.actor, "cloudflare", cfToken); !errors.Is(err, domain.ErrDNSProviderPermissionDenied) {
		t.Errorf("sin permiso: %v", err)
	}
	h.cf.verifyErr = domain.ErrDNSProviderRateLimited
	if _, err := h.uc.ConnectDNSProvider(ctx, h.tenantID, h.actor, "cloudflare", cfToken); !errors.Is(err, domain.ErrDNSProviderRateLimited) {
		t.Errorf("limite: %v", err)
	}
	if len(h.dnsRepo.providers) != 0 || len(h.dnsEvts.events) != 0 {
		t.Error("un token rechazado no se guarda ni se anuncia")
	}
}

func TestConectarSinPoderEncolarElEventoNoGuardaNada(t *testing.T) {
	h := newDNSHarness(t)
	h.dnsEvts.err = errDown
	if _, err := h.uc.ConnectDNSProvider(context.Background(), h.tenantID, h.actor, "cloudflare", cfToken); !errors.Is(err, errDown) {
		t.Fatalf("err %v", err)
	}
	if len(h.dnsRepo.providers) != 0 {
		t.Error("la conexion quedo sin su evento")
	}
}

func TestReconectarReemplazaElToken(t *testing.T) {
	h := newDNSHarness(t)
	first := h.connect(t)
	otro := "otro_token_de_prueba_0123456789ABCD"
	h.cf.tokens[otro] = []domain.DNSZone{{ID: "zacme", Name: "acme.com"}}
	second, err := h.uc.ConnectDNSProvider(context.Background(), h.tenantID, h.actor, "cloudflare", otro)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.TokenHint != "ABCD" || second.ZonesVisible != 1 || len(h.dnsRepo.providers) != 1 {
		t.Errorf("reconexion %+v", second)
	}
}

func TestDesconectarBorraElTokenYVuelveAManual(t *testing.T) {
	h := newDNSHarness(t)
	ctx := context.Background()
	h.connect(t)
	d := h.automatic(t, "acme.com", domain.PurposeCorporate)
	manual := h.create(t, "ventas.acme.com", domain.PurposeSending)
	h.publish(t, d.ID)
	before := len(h.cf.records["zacme"])

	res, err := h.uc.DisconnectDNSProvider(ctx, h.tenantID, h.actor, "cloudflare")
	if err != nil || !res.Disconnected || res.DomainsReset != 1 {
		t.Fatalf("desconectar %+v %v", res, err)
	}
	if len(h.dnsRepo.providers) != 0 {
		t.Error("el token sigue guardado")
	}
	for _, id := range []uuid.UUID{d.ID, manual.ID} {
		if got, _ := h.repo.GetByID(ctx, h.tenantID, id); got.DNSMode != domain.DNSModeManual {
			t.Errorf("%s en modo %s", got.Domain, got.DNSMode)
		}
	}
	if len(h.cf.records["zacme"]) != before {
		t.Error("desconectar no retira lo publicado")
	}
	if e := h.dnsEvts.last(); e.subject != "domains.dns_provider.disconnected" || e.reset != 1 || e.actor != h.actor {
		t.Errorf("evento %+v", e)
	}
	again, err := h.uc.DisconnectDNSProvider(ctx, h.tenantID, h.actor, "cloudflare")
	if err != nil || again.Disconnected || h.dnsEvts.count("domains.dns_provider.disconnected") != 1 {
		t.Errorf("repetir: %+v %v", again, err)
	}
	if _, err := h.uc.PublishDNS(ctx, h.tenantID, d.ID, h.actor, nil); !errors.Is(err, domain.ErrDNSModeManual) {
		t.Errorf("publicar tras desconectar: %v", err)
	}
}

// ── Modo ─────────────────────────────────────────────────────────────────────

func TestCambiarDeModo(t *testing.T) {
	h := newDNSHarness(t)
	ctx := context.Background()
	d := h.create(t, "acme.com", domain.PurposeCorporate)
	if d.DNSMode != domain.DNSModeManual {
		t.Fatalf("modo por defecto %s", d.DNSMode)
	}
	if _, err := h.uc.SetDNSMode(ctx, h.tenantID, d.ID, "cloudflare"); !errors.Is(err, domain.ErrDNSProviderNotConnected) {
		t.Errorf("sin conexion: %v", err)
	}
	if _, err := h.uc.SetDNSMode(ctx, h.tenantID, d.ID, "automatico"); !errors.Is(err, domain.ErrInvalidDNSMode) {
		t.Errorf("modo no valido: %v", err)
	}
	h.connect(t)
	ajeno := h.create(t, "acme.org", domain.PurposeCorporate)
	if _, err := h.uc.SetDNSMode(ctx, h.tenantID, ajeno.ID, "cloudflare"); !errors.Is(err, domain.ErrDNSZoneNotFound) {
		t.Errorf("zona ajena: %v", err)
	}
	sub := h.create(t, "correo.acme.com", domain.PurposeSending)
	if got, err := h.uc.SetDNSMode(ctx, h.tenantID, sub.ID, "cloudflare"); err != nil || got.DNSMode != "cloudflare" {
		t.Errorf("subdominio de una zona visible: %v %v", got, err)
	}
	if _, err := h.uc.SetDNSMode(ctx, uuid.New(), d.ID, "manual"); !errors.Is(err, domain.ErrDomainNotFound) {
		t.Errorf("dominio de otra empresa: %v", err)
	}
	if got, err := h.uc.SetDNSMode(ctx, h.tenantID, sub.ID, "manual"); err != nil || got.DNSMode != domain.DNSModeManual {
		t.Errorf("volver a manual: %v %v", got, err)
	}
	if len(h.cf.writes) != 0 {
		t.Errorf("cambiar de modo escribio en la zona: %v", h.cf.writes)
	}
}

// ── Publicacion ──────────────────────────────────────────────────────────────

func TestPublicarCreaLosRegistrosYVerifica(t *testing.T) {
	h := newDNSHarness(t)
	h.connect(t)
	d := h.automatic(t, "acme.com", domain.PurposeCorporate)
	res := h.publish(t, d.ID)

	want := map[domain.RecordKind]domain.RecordAction{
		domain.RecordOwnershipTXT: domain.RecordCreated, domain.RecordMX: domain.RecordCreated, domain.RecordSPF: domain.RecordCreated,
		domain.RecordDKIM: domain.RecordCreated, domain.RecordDMARC: domain.RecordCreated,
	}
	if got := actions(res.Publication); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("acciones %v", got)
	}
	for _, r := range h.cf.records["zacme"] {
		if !r.Managed() {
			t.Errorf("registro sin la marca de la plataforma: %+v", r)
		}
	}
	if res.Publication.Zone != "acme.com" || !res.Publication.Complete() {
		t.Errorf("publicacion %+v", res.Publication)
	}
	if res.Verification == nil || res.Verification.Outcome != domain.OutcomeVerified || res.Domain.Status != domain.StatusVerified {
		t.Fatalf("verificacion %+v", res.Verification)
	}
	if res.Domain.DNSPublishedAt == nil {
		t.Error("sin fecha de publicacion")
	}
	if e := h.dnsEvts.last(); e.subject != "domains.domain.dns_published" || e.actor != h.actor || e.pub.Count(domain.RecordCreated) != 5 {
		t.Errorf("evento %+v", e)
	}
	for _, tok := range h.cf.tokensSeen {
		if tok != cfToken {
			t.Errorf("token enviado %q", tok)
		}
	}

	writes := len(h.cf.writes)
	again := h.publish(t, d.ID)
	for kind, action := range actions(again.Publication) {
		if action != domain.RecordUnchanged {
			t.Errorf("repetir: %s %s", kind, action)
		}
	}
	if len(h.cf.writes) != writes {
		t.Errorf("repetir escribio: %v", h.cf.writes[writes:])
	}
}

func TestPublicarNoPisaRegistrosDelClienteSinConfirmacion(t *testing.T) {
	h := newDNSHarness(t)
	h.connect(t)
	d := h.automatic(t, "acme.com", domain.PurposeCorporate)
	h.cf.seed("zacme", domain.ProviderRecord{Type: "TXT", Name: "acme.com", Content: "v=spf1 include:_spf.google.com ~all"})
	h.cf.seed("zacme", domain.ProviderRecord{Type: "MX", Name: "acme.com", Content: "aspmx.l.google.com", Priority: 1})
	h.cf.seed("zacme", domain.ProviderRecord{Type: "TXT", Name: "acme.com", Content: "google-site-verification=abc"})

	res := h.publish(t, d.ID)
	got := actions(res.Publication)
	if got[domain.RecordSPF] != domain.RecordConflict || got[domain.RecordMX] != domain.RecordConflict || got[domain.RecordDKIM] != domain.RecordCreated {
		t.Fatalf("acciones %v", got)
	}
	for _, r := range res.Publication.Records {
		if r.Kind == domain.RecordMX && strings.Join(r.Existing, ",") != "aspmx.l.google.com priority 1" {
			t.Errorf("valores del cliente %v", r.Existing)
		}
	}
	if spf := h.cf.find("zacme", "TXT", "acme.com"); len(spf) != 2 {
		t.Errorf("el SPF del cliente se toco: %+v", spf)
	}
	if res.Publication.Complete() || res.Domain.DNSPublishedAt != nil {
		t.Error("con conflictos no esta publicada")
	}
	if res.Verification.Outcome != domain.OutcomeFailed {
		t.Errorf("la verificacion ve el SPF del cliente: %s", res.Verification.Outcome)
	}

	if _, err := h.uc.PublishDNS(context.Background(), h.tenantID, d.ID, h.actor, []string{"spf", "a"}); !errors.Is(err, domain.ErrInvalidRecordKind) {
		t.Errorf("tipo no valido: %v", err)
	}
	solo := h.publish(t, d.ID, "spf")
	if got := actions(solo.Publication); got[domain.RecordSPF] != domain.RecordReplaced || got[domain.RecordMX] != domain.RecordConflict {
		t.Errorf("confirmar solo el SPF: %v", got)
	}
	all := h.publish(t, d.ID, "mx")
	if got := actions(all.Publication); got[domain.RecordSPF] != domain.RecordUnchanged || got[domain.RecordMX] != domain.RecordReplaced {
		t.Errorf("confirmar el MX: %v", got)
	}
	if mx := h.cf.find("zacme", "MX", "acme.com"); len(mx) != 1 || mx[0].Content != testPlatform.MXHostname || !mx[0].Managed() {
		t.Errorf("MX %+v", mx)
	}
	if txt := h.cf.find("zacme", "TXT", "acme.com"); len(txt) != 2 {
		t.Errorf("la verificacion de google no es un SPF y se queda: %+v", txt)
	}
	if all.Verification.Outcome != domain.OutcomeVerified || all.Domain.DNSPublishedAt == nil {
		t.Errorf("tras confirmar: %s", all.Verification.Outcome)
	}
}

func TestPublicarExigeModoAutomaticoYZona(t *testing.T) {
	h := newDNSHarness(t)
	ctx := context.Background()
	d := h.create(t, "acme.com", domain.PurposeCorporate)
	if _, err := h.uc.PublishDNS(ctx, h.tenantID, d.ID, h.actor, nil); !errors.Is(err, domain.ErrDNSModeManual) {
		t.Errorf("manual: %v", err)
	}
	h.connect(t)
	if _, err := h.uc.SetDNSMode(ctx, h.tenantID, d.ID, "cloudflare"); err != nil {
		t.Fatal(err)
	}
	// El token pierde la zona despues de elegir el modo.
	h.cf.tokens[cfToken] = []domain.DNSZone{{ID: "zotra", Name: "otra.net"}}
	if _, err := h.uc.PublishDNS(ctx, h.tenantID, d.ID, h.actor, nil); !errors.Is(err, domain.ErrDNSZoneNotFound) {
		t.Errorf("sin zona: %v", err)
	}
	if len(h.cf.writes) != 0 {
		t.Errorf("escribio sin zona: %v", h.cf.writes)
	}
	stored, _ := h.dnsRepo.GetDNSProvider(ctx, h.tenantID, domain.DNSProviderCloudflare)
	if stored.ZonesVisible != 1 {
		t.Errorf("la validacion no anoto las zonas nuevas: %d", stored.ZonesVisible)
	}
}

func TestPublicarCortaAnteFallosDelProveedor(t *testing.T) {
	for nombre, fallo := range map[string]error{
		"token revocado":     domain.ErrDNSProviderTokenInvalid,
		"sin permiso de DNS": domain.ErrDNSProviderPermissionDenied,
		"limite":             domain.ErrDNSProviderRateLimited,
		"proveedor caido":    domain.ErrDNSProviderUnavailable,
	} {
		h := newDNSHarness(t)
		h.connect(t)
		d := h.automatic(t, "acme.com", domain.PurposeCorporate)
		h.cf.writeErr = fallo
		if _, err := h.uc.PublishDNS(context.Background(), h.tenantID, d.ID, h.actor, nil); !errors.Is(err, fallo) {
			t.Errorf("%s: %v", nombre, err)
		}
		if h.dnsEvts.count("domains.domain.dns_published") != 0 {
			t.Errorf("%s: se anuncio una publicacion cortada", nombre)
		}
	}
	h := newDNSHarness(t)
	h.connect(t)
	d := h.automatic(t, "acme.com", domain.PurposeCorporate)
	h.cf.listErr = domain.ErrDNSProviderTokenInvalid
	if _, err := h.uc.PublishDNS(context.Background(), h.tenantID, d.ID, h.actor, nil); !errors.Is(err, domain.ErrDNSProviderTokenInvalid) {
		t.Errorf("token caducado al listar zonas: %v", err)
	}
}

func TestUnRechazoDelProveedorSoloFallaEseRegistro(t *testing.T) {
	h := newDNSHarness(t)
	h.connect(t)
	d := h.automatic(t, "acme.com", domain.PurposeCorporate)
	h.cf.writeErrFor["_dmarc.acme.com"] = domain.ErrDNSProviderRejected
	res := h.publish(t, d.ID)
	got := actions(res.Publication)
	if got[domain.RecordDMARC] != domain.RecordFailed || got[domain.RecordSPF] != domain.RecordCreated {
		t.Errorf("acciones %v", got)
	}
	if res.Publication.Complete() || res.Domain.DNSPublishedAt != nil {
		t.Error("con un registro fallido no esta completa")
	}
	// DMARC es recomendado: el dominio verifica igual.
	if res.Verification.Outcome != domain.OutcomeVerified {
		t.Errorf("verificacion %s", res.Verification.Outcome)
	}
}

// ── Claves DKIM en modo automatico ───────────────────────────────────────────

func TestRotarEnAutomaticoPublicaLaClaveNueva(t *testing.T) {
	h := newDNSHarness(t)
	ctx := context.Background()
	h.connect(t)
	d := h.automatic(t, "acme.com", domain.PurposeCorporate)
	h.publish(t, d.ID)
	h.now = h.now.Add(40 * 24 * time.Hour)

	rot, err := h.uc.RotateDKIM(ctx, h.tenantID, d.ID, h.actor)
	if err != nil {
		t.Fatal(err)
	}
	if rot.DNS == nil || rot.DNS.Err != nil || rot.DNS.Publication == nil {
		t.Fatalf("automatizacion %+v", rot.DNS)
	}
	if got := actions(rot.DNS.Publication); got[domain.RecordDKIM] != domain.RecordCreated || got[domain.RecordDKIMPrevious] != domain.RecordUnchanged || len(got) != 2 {
		t.Errorf("acciones %v", got)
	}
	if n := len(h.cf.find("zacme", "TXT", domain.DKIMHost(rot.Domain.DKIMSelector, "acme.com"))); n != 1 {
		t.Errorf("TXT de la clave nueva: %d", n)
	}
	if n := len(h.cf.find("zacme", "TXT", domain.DKIMHost(d.DKIMSelector, "acme.com"))); n != 1 {
		t.Error("la clave anterior sigue publicada durante la gracia")
	}

	manual := h.create(t, "acme.org", domain.PurposeCorporate)
	if rot, err := h.uc.RotateDKIM(ctx, h.tenantID, manual.ID, h.actor); err != nil || rot.DNS != nil {
		t.Errorf("en manual no se publica nada: %+v %v", rot, err)
	}
}

func TestRevocarEnAutomaticoRetiraSoloLosTXTDeLaPlataforma(t *testing.T) {
	h := newDNSHarness(t)
	ctx := context.Background()
	h.connect(t)
	d := h.automatic(t, "acme.com", domain.PurposeCorporate)
	h.publish(t, d.ID)
	h.now = h.now.Add(40 * 24 * time.Hour)
	rot, err := h.uc.RotateDKIM(ctx, h.tenantID, d.ID, h.actor)
	if err != nil {
		t.Fatal(err)
	}
	// El cliente publico a mano otro TXT en el nombre de la clave anterior.
	oldHost := domain.DKIMHost(d.DKIMSelector, "acme.com")
	h.cf.seed("zacme", domain.ProviderRecord{Type: "TXT", Name: oldHost, Content: "v=DKIM1; k=rsa; p=COPIA"})

	rev, err := h.uc.RevokeDKIM(ctx, h.tenantID, d.ID, RevokeDKIMRequest{CurrentSelector: rot.Domain.DKIMSelector, Reason: "expuesta", ActorID: h.actor})
	if err != nil {
		t.Fatal(err)
	}
	if rev.DNS == nil || rev.DNS.Err != nil {
		t.Fatalf("automatizacion %+v", rev.DNS)
	}
	newHost := domain.DKIMHost(rev.Domain.DKIMSelector, "acme.com")
	if n := len(h.cf.find("zacme", "TXT", newHost)); n != 1 {
		t.Errorf("TXT nuevo %d", n)
	}
	if n := len(h.cf.find("zacme", "TXT", domain.DKIMHost(rot.Domain.DKIMSelector, "acme.com"))); n != 0 {
		t.Error("el TXT revocado de la plataforma sigue publicado")
	}
	left := h.cf.find("zacme", "TXT", oldHost)
	if len(left) != 1 || left[0].Managed() {
		t.Errorf("en el nombre revocado solo queda el del cliente: %+v", left)
	}
	pub := rev.DNS.Publication
	if len(pub.Removed) != 2 || strings.Join(pub.Kept, ",") != oldHost {
		t.Errorf("retirados %v, del cliente %v", pub.Removed, pub.Kept)
	}
}

func TestElFalloDelProveedorNoDeshaceLaRotacion(t *testing.T) {
	h := newDNSHarness(t)
	ctx := context.Background()
	h.connect(t)
	d := h.automatic(t, "acme.com", domain.PurposeCorporate)
	h.cf.writeErr = domain.ErrDNSProviderRateLimited
	rot, err := h.uc.RotateDKIM(ctx, h.tenantID, d.ID, h.actor)
	if err != nil {
		t.Fatalf("la rotacion depende del proveedor: %v", err)
	}
	if rot.DNS == nil || !errors.Is(rot.DNS.Err, domain.ErrDNSProviderRateLimited) {
		t.Errorf("automatizacion %+v", rot.DNS)
	}
	if got, _ := h.repo.GetByID(ctx, h.tenantID, d.ID); got.DKIMSelector == d.DKIMSelector {
		t.Error("la rotacion no se guardo")
	}
}

func TestElBarridoRetiraDeLaZonaLaClaveFueraDeGracia(t *testing.T) {
	h := newDNSHarness(t)
	ctx := context.Background()
	h.connect(t)
	d := h.automatic(t, "acme.com", domain.PurposeCorporate)
	h.publish(t, d.ID)
	h.now = h.now.Add(40 * 24 * time.Hour)
	if _, err := h.uc.RotateDKIM(ctx, h.tenantID, d.ID, h.actor); err != nil {
		t.Fatal(err)
	}
	h.verify(t, d.ID)
	h.now = h.now.Add(80 * time.Hour)
	if rep := h.uc.SweepTenant(ctx, h.tenantID); rep.Retired != 1 {
		t.Fatalf("barrido %+v", rep)
	}
	if n := len(h.cf.find("zacme", "TXT", domain.DKIMHost(d.DKIMSelector, "acme.com"))); n != 0 {
		t.Error("el TXT de la clave retirada sigue en la zona")
	}
}

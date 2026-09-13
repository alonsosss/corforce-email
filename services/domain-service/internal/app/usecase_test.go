package app

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/crypto"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/google/uuid"
)

const (
	testActiveKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testOldKey    = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	platformHost  = "mail.plataforma.example"
)

var testPlatform = domain.PlatformDNS{
	MXHostname: "mx.plataforma.example",
	SPFInclude: "include:spf.plataforma.example",
	DMARCRUA:   "dmarc@plataforma.example",
}

type harness struct {
	uc        *UseCase
	repo      *fakeRepo
	dns       *fakeDNS
	directory *fakeDirectory
	security  *fakeSecurity
	events    *fakePublisher
	keyRing   *crypto.KeyRing
	tenantID  uuid.UUID
	now       time.Time
}

func testKeyRing(t *testing.T, active, old string) *crypto.KeyRing {
	t.Helper()
	t.Setenv("TEST_DOMAIN_KEY", active)
	t.Setenv("TEST_DOMAIN_KEY_OLD", old)
	kr, err := crypto.LoadKeyRing("TEST_DOMAIN_KEY", "TEST_DOMAIN_KEY_OLD")
	if err != nil {
		t.Fatalf("LoadKeyRing: %v", err)
	}
	return kr
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		dns: newFakeDNS(), directory: &fakeDirectory{},
		security: &fakeSecurity{}, events: &fakePublisher{},
		keyRing:  testKeyRing(t, testActiveKey, ""),
		tenantID: uuid.New(),
		now:      time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC),
	}
	h.repo = newFakeRepo(func() time.Time { return h.now })
	h.uc = New(Deps{
		Repo: h.repo, DNS: h.dns, Cipher: h.keyRing,
		MailDirectory: h.directory, MailSecurity: h.security, Events: h.events,
		Platform: testPlatform, PlatformHostname: platformHost,
		DKIMRotationGrace: 72 * time.Hour,
		Now:               func() time.Time { return h.now },
	})
	return h
}

func (h *harness) create(t *testing.T, name string, purpose domain.Purpose) *domain.Domain {
	t.Helper()
	d, err := h.uc.Create(context.Background(), h.tenantID, CreateRequest{Domain: name, Purpose: string(purpose)})
	if err != nil {
		t.Fatalf("Create(%s): %v", name, err)
	}
	return d
}

func (h *harness) verify(t *testing.T, id uuid.UUID) *VerifyResult {
	t.Helper()
	res, err := h.uc.Verify(context.Background(), h.tenantID, id)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	return res
}

// ── Alta ─────────────────────────────────────────────────────────────────────

func TestCreateGeneratesTokenAndEncryptedDKIM(t *testing.T) {
	h := newHarness(t)
	d := h.create(t, "  Acme.COM. ", domain.PurposeCorporate)

	if d.Domain != "acme.com" || d.Status != domain.StatusPending || d.DMARCPolicy != domain.DMARCQuarantine {
		t.Errorf("dominio = %s/%s/%s", d.Domain, d.Status, d.DMARCPolicy)
	}
	if len(d.VerificationToken) != 32 {
		t.Errorf("token = %q; want 32 hex", d.VerificationToken)
	}
	if d.DKIMSelector != "cfm202609" || d.DKIMKeyBits != 2048 {
		t.Errorf("selector/bits = %s/%d", d.DKIMSelector, d.DKIMKeyBits)
	}
	if strings.Contains(string(d.DKIMPrivateKeyEnc), "PRIVATE KEY") {
		t.Fatal("la clave privada se guardo en claro")
	}
	pemBytes, err := h.keyRing.Decrypt(d.DKIMPrivateKeyEnc)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "RSA PRIVATE KEY" {
		t.Fatal("el PEM descifrado no es una clave RSA")
	}
	priv, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("ParsePKCS1PrivateKey: %v", err)
	}
	pubDER, _ := base64.StdEncoding.DecodeString(d.DKIMPublicKey)
	pub, err := x509.ParsePKIXPublicKey(pubDER)
	if err != nil {
		t.Fatalf("ParsePKIXPublicKey: %v", err)
	}
	if !priv.PublicKey.Equal(pub) {
		t.Error("la clave publica del TXT no corresponde a la privada custodiada")
	}
	if h.events.count("domains.domain.created") != 1 {
		t.Errorf("eventos = %v", h.events.subjects)
	}
	records := h.uc.ExpectedRecords(d)
	if len(records) != 5 || records[0].Host != "_cfm-verify.acme.com" || records[0].Value != "cfm-verify="+d.VerificationToken {
		t.Errorf("records = %+v", records)
	}
}

func TestCreateRejects(t *testing.T) {
	h := newHarness(t)
	h.create(t, "acme.com", domain.PurposeBoth)
	cases := []struct {
		name string
		req  CreateRequest
		want error
	}{
		{"duplicado", CreateRequest{Domain: "ACME.com", Purpose: "corporate"}, domain.ErrDomainAlreadyExists},
		{"plataforma", CreateRequest{Domain: "smtp." + platformHost, Purpose: "corporate"}, domain.ErrPlatformDomain},
		{"ip", CreateRequest{Domain: "10.0.0.1", Purpose: "corporate"}, domain.ErrInvalidDomainName},
		{"purpose", CreateRequest{Domain: "otro.com", Purpose: "marketing"}, domain.ErrInvalidPurpose},
		{"dmarc", CreateRequest{Domain: "otro.com", Purpose: "sending", DMARCPolicy: "block"}, domain.ErrInvalidDMARCPolicy},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := h.uc.Create(context.Background(), h.tenantID, c.req); !errors.Is(err, c.want) {
				t.Errorf("err = %v; want %v", err, c.want)
			}
		})
	}
}

func TestKeyRingRotationStillDecrypts(t *testing.T) {
	h := newHarness(t)
	d := h.create(t, "acme.com", domain.PurposeSending)
	// El servicio arranca con una llave nueva y la anterior en MAIL_ENCRYPTION_KEYS_OLD.
	rotated := testKeyRing(t, testOldKey, testActiveKey)
	pemBytes, err := rotated.Decrypt(d.DKIMPrivateKeyEnc)
	if err != nil || !strings.Contains(string(pemBytes), "RSA PRIVATE KEY") {
		t.Fatalf("la llave retirada debe seguir abriendo lo cifrado: %v", err)
	}
	if _, err := testKeyRing(t, testOldKey, "").Decrypt(d.DKIMPrivateKeyEnc); !errors.Is(err, crypto.ErrUndecryptable) {
		t.Errorf("sin la llave original no debe abrirse: %v", err)
	}
}

// ── Verificacion ─────────────────────────────────────────────────────────────

func TestVerifyAllRecordsActivatesAndPublishes(t *testing.T) {
	h := newHarness(t)
	d := h.create(t, "acme.com", domain.PurposeCorporate)
	h.dns.publishZone(h.uc, d)

	res := h.verify(t, d.ID)
	if res.Outcome != domain.OutcomeVerified || res.Domain.Status != domain.StatusVerified || res.Domain.VerifiedAt == nil {
		t.Fatalf("res = %s / %s", res.Outcome, res.Domain.Status)
	}
	if len(res.Checks) != 5 || len(h.repo.checks) != 5 {
		t.Errorf("checks = %d guardadas %d", len(res.Checks), len(h.repo.checks))
	}
	if len(h.directory.calls) != 1 || !h.directory.calls[0].active || h.directory.calls[0].domain != "acme.com" {
		t.Errorf("activacion = %+v", h.directory.calls)
	}
	pub := h.security.lastPublished()
	if pub.domain != "acme.com" || len(pub.keys) != 1 || pub.keys[0].Selector != "cfm202609" ||
		!strings.Contains(pub.keys[0].PrivateKeyPEM, "RSA PRIVATE KEY") {
		t.Errorf("publicacion DKIM = %+v", pub)
	}
	if h.events.count("domains.domain.verified") != 1 || len(res.IntegrationErrors) != 0 {
		t.Errorf("eventos = %v, errores = %v", h.events.subjects, res.IntegrationErrors)
	}

	// Una segunda verificacion no vuelve a emitir el evento pero si resincroniza.
	h.verify(t, d.ID)
	if h.events.count("domains.domain.verified") != 1 || len(h.security.published) != 2 {
		t.Errorf("segunda verificacion: eventos %v, publicaciones %d", h.events.subjects, len(h.security.published))
	}
}

func TestVerifyMissingSPFFailsWithDetail(t *testing.T) {
	h := newHarness(t)
	d := h.create(t, "acme.com", domain.PurposeCorporate)
	h.dns.publishZone(h.uc, d)
	h.dns.txt["acme.com"] = []string{"v=spf1 include:_spf.google.com ~all"}

	res := h.verify(t, d.ID)
	if res.Outcome != domain.OutcomeFailed || res.Domain.Status != domain.StatusFailed {
		t.Fatalf("res = %s / %s", res.Outcome, res.Domain.Status)
	}
	var spf *domain.DNSCheck
	for i := range res.Checks {
		if res.Checks[i].Record == domain.RecordSPF {
			spf = &res.Checks[i]
		}
	}
	if spf == nil || spf.OK || !strings.Contains(spf.Detail, "include:spf.plataforma.example") {
		t.Errorf("spf = %+v", spf)
	}
	if len(h.directory.calls) != 0 || len(h.security.published) != 0 {
		t.Error("un dominio fallido no se activa ni publica claves")
	}
	if h.events.count("domains.domain.failed") != 1 {
		t.Errorf("eventos = %v", h.events.subjects)
	}
}

func TestVerifyWithoutDMARCStillVerifies(t *testing.T) {
	h := newHarness(t)
	d := h.create(t, "acme.com", domain.PurposeSending)
	h.dns.publishZone(h.uc, d)
	delete(h.dns.txt, "_dmarc.acme.com")

	res := h.verify(t, d.ID)
	if res.Outcome != domain.OutcomeVerified {
		t.Fatalf("outcome = %s", res.Outcome)
	}
	if len(h.directory.calls) != 0 {
		t.Error("un dominio solo de envio no se activa en el directorio de la celda")
	}
	if len(h.security.published) != 1 {
		t.Error("las claves DKIM se publican aunque el dominio sea solo de envio")
	}
}

func TestVerifyDNSErrorIsInconclusiveAndKeepsStatus(t *testing.T) {
	h := newHarness(t)
	d := h.create(t, "acme.com", domain.PurposeCorporate)
	h.dns.publishZone(h.uc, d)
	h.verify(t, d.ID)

	h.dns.errs["acme.com"] = errDown
	res := h.verify(t, d.ID)
	if res.Outcome != domain.OutcomeInconclusive || res.Domain.Status != domain.StatusVerified {
		t.Errorf("res = %s / %s; un timeout no debe tumbar un dominio verificado", res.Outcome, res.Domain.Status)
	}
}

func TestVerifyIntegrationFailureKeepsVerified(t *testing.T) {
	h := newHarness(t)
	d := h.create(t, "acme.com", domain.PurposeCorporate)
	h.dns.publishZone(h.uc, d)
	h.directory.err = errDown
	h.security.err = errDown

	res := h.verify(t, d.ID)
	if res.Domain.Status != domain.StatusVerified || len(res.IntegrationErrors) != 2 {
		t.Errorf("status = %s, errores = %v", res.Domain.Status, res.IntegrationErrors)
	}

	// El barrido cura lo que fallo.
	h.directory.err, h.security.err = nil, nil
	rep := h.uc.SweepTenant(context.Background(), h.tenantID)
	if rep.Rechecked != 1 || len(h.directory.calls) != 1 || len(h.security.published) != 1 {
		t.Errorf("sweep = %+v, activaciones %d, publicaciones %d", rep, len(h.directory.calls), len(h.security.published))
	}
}

// ── Barrido ──────────────────────────────────────────────────────────────────

func TestSweepVerifiedDomainLosingSPFFails(t *testing.T) {
	h := newHarness(t)
	d := h.create(t, "acme.com", domain.PurposeCorporate)
	h.dns.publishZone(h.uc, d)
	h.verify(t, d.ID)

	h.dns.txt["acme.com"] = nil
	rep := h.uc.SweepTenant(context.Background(), h.tenantID)
	stored, _ := h.repo.GetByID(context.Background(), h.tenantID, d.ID)
	if rep.Failed != 1 || stored.Status != domain.StatusFailed || stored.VerifiedAt != nil {
		t.Errorf("sweep = %+v, status = %s", rep, stored.Status)
	}
	last := h.directory.calls[len(h.directory.calls)-1]
	if last.active {
		t.Error("al caer, el dominio debe desactivarse en mail-directory")
	}
	if h.events.count("domains.domain.failed") != 1 {
		t.Errorf("eventos = %v", h.events.subjects)
	}

	// Un dominio fallido no se vuelve a barrer: lo relanza el cliente.
	if rep := h.uc.SweepTenant(context.Background(), h.tenantID); rep.Rechecked != 0 {
		t.Errorf("segundo sweep = %+v", rep)
	}
}

func TestSweepVerifiedDomainLosingOnlyDKIMStaysVerified(t *testing.T) {
	h := newHarness(t)
	d := h.create(t, "acme.com", domain.PurposeCorporate)
	h.dns.publishZone(h.uc, d)
	h.verify(t, d.ID)

	delete(h.dns.txt, domain.DKIMHost(d.DKIMSelector, d.Domain))
	h.uc.SweepTenant(context.Background(), h.tenantID)
	stored, _ := h.repo.GetByID(context.Background(), h.tenantID, d.ID)
	if stored.Status != domain.StatusVerified {
		t.Errorf("status = %s; el DKIM ausente se registra pero no tumba el dominio en el barrido", stored.Status)
	}
	// A mano si: el cliente pidio una verificacion completa.
	if res := h.verify(t, d.ID); res.Domain.Status != domain.StatusFailed {
		t.Errorf("verificacion manual: status = %s", res.Domain.Status)
	}
}

func TestSweepPendingDomainVerifiesButNeverFails(t *testing.T) {
	h := newHarness(t)
	d := h.create(t, "acme.com", domain.PurposeCorporate)

	h.uc.SweepTenant(context.Background(), h.tenantID)
	stored, _ := h.repo.GetByID(context.Background(), h.tenantID, d.ID)
	if stored.Status != domain.StatusPending || stored.LastCheckedAt == nil {
		t.Errorf("pendiente sin registros: status = %s", stored.Status)
	}
	if h.events.count("domains.domain.failed") != 0 {
		t.Error("el barrido no marca como fallido a un pendiente")
	}

	h.dns.publishZone(h.uc, d)
	h.uc.SweepTenant(context.Background(), h.tenantID)
	stored, _ = h.repo.GetByID(context.Background(), h.tenantID, d.ID)
	if stored.Status != domain.StatusVerified || h.events.count("domains.domain.verified") != 1 || len(h.directory.calls) != 1 {
		t.Errorf("pendiente con registros: status = %s, eventos %v", stored.Status, h.events.subjects)
	}
}

func TestSweepSkipsOldPendingDomains(t *testing.T) {
	h := newHarness(t)
	d := h.create(t, "acme.com", domain.PurposeCorporate)
	h.dns.publishZone(h.uc, d)
	h.now = h.now.Add(8 * 24 * time.Hour)
	if rep := h.uc.SweepTenant(context.Background(), h.tenantID); rep.Rechecked != 0 {
		t.Errorf("un pendiente de mas de 7 dias no se barre: %+v", rep)
	}
}

// ── Rotacion DKIM ────────────────────────────────────────────────────────────

func TestRotateDKIMKeepsPreviousAndSignsWithItUntilPublished(t *testing.T) {
	h := newHarness(t)
	d := h.create(t, "acme.com", domain.PurposeCorporate)
	h.dns.publishZone(h.uc, d)
	h.verify(t, d.ID)
	oldSelector, oldPublic := d.DKIMSelector, d.DKIMPublicKey

	res, err := h.uc.RotateDKIM(context.Background(), h.tenantID, d.ID)
	if err != nil {
		t.Fatalf("RotateDKIM: %v", err)
	}
	rotated := res.Domain
	if rotated.DKIMSelector == oldSelector || rotated.DKIMSelector != "cfm20260912" {
		t.Errorf("selector nuevo = %s (mismo mes: se afina a dia)", rotated.DKIMSelector)
	}
	if rotated.DKIMPreviousSelector != oldSelector || rotated.DKIMPreviousPublicKey != oldPublic || rotated.DKIMRotatedAt == nil {
		t.Errorf("clave anterior no conservada: %+v", rotated.DKIMPreviousSelector)
	}
	if rotated.DKIMPublicKey == oldPublic {
		t.Error("la clave publica debe cambiar")
	}
	if res.Record.Host != "cfm20260912._domainkey.acme.com" || !strings.HasPrefix(res.Record.Value, "v=DKIM1; k=rsa; p=") {
		t.Errorf("record = %+v", res.Record)
	}
	if res.GraceUntil != h.now.Add(72*time.Hour) {
		t.Errorf("grace_until = %s", res.GraceUntil)
	}
	// Tras rotar se depositan ambas y sigue firmando la anterior (ultima en el orden).
	pub := h.security.lastPublished()
	if len(pub.keys) != 2 || pub.keys[0].Selector != rotated.DKIMSelector || pub.keys[1].Selector != oldSelector {
		t.Errorf("publicacion tras rotar = %+v", selectorsOf(pub))
	}
	if h.events.count("domains.domain.dkim_rotated") != 1 {
		t.Errorf("eventos = %v", h.events.subjects)
	}

	// El cliente aun no publico el TXT nuevo: la verificacion sigue verified y firma
	// con la anterior. Cuando lo publica, el orden se invierte y firma la nueva.
	vr := h.verify(t, d.ID)
	if vr.Domain.Status != domain.StatusVerified {
		t.Fatalf("status = %s con el TXT anterior publicado", vr.Domain.Status)
	}
	if pub := h.security.lastPublished(); pub.keys[len(pub.keys)-1].Selector != oldSelector {
		t.Errorf("sin TXT nuevo debe seguir firmando %s: %v", oldSelector, selectorsOf(pub))
	}
	h.dns.publishZone(h.uc, rotated)
	h.verify(t, d.ID)
	if pub := h.security.lastPublished(); pub.keys[len(pub.keys)-1].Selector != rotated.DKIMSelector {
		t.Errorf("con TXT nuevo debe firmar %s: %v", rotated.DKIMSelector, selectorsOf(pub))
	}

	// Vencida la gracia, el barrido retira la anterior.
	h.now = h.now.Add(73 * time.Hour)
	rep := h.uc.SweepTenant(context.Background(), h.tenantID)
	stored, _ := h.repo.GetByID(context.Background(), h.tenantID, d.ID)
	if rep.Retired != 1 || stored.HasPreviousDKIM() || len(h.security.retired) != 1 || h.security.retired[0] != "acme.com/"+oldSelector {
		t.Errorf("retiro = %+v, previous=%v, retirados=%v", rep, stored.HasPreviousDKIM(), h.security.retired)
	}
}

func TestSweepKeepsPreviousDKIMWhileNewTXTUnpublished(t *testing.T) {
	h := newHarness(t)
	d := h.create(t, "acme.com", domain.PurposeSending)
	h.dns.publishZone(h.uc, d)
	h.verify(t, d.ID)
	if _, err := h.uc.RotateDKIM(context.Background(), h.tenantID, d.ID); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(100 * time.Hour)
	rep := h.uc.SweepTenant(context.Background(), h.tenantID)
	stored, _ := h.repo.GetByID(context.Background(), h.tenantID, d.ID)
	if rep.Retired != 0 || !stored.HasPreviousDKIM() {
		t.Error("sin el TXT nuevo publicado, la clave anterior se conserva aunque venza la gracia")
	}
}

func TestRotateDKIMOnPendingDomainDoesNotPublish(t *testing.T) {
	h := newHarness(t)
	d := h.create(t, "acme.com", domain.PurposeSending)
	if _, err := h.uc.RotateDKIM(context.Background(), h.tenantID, d.ID); err != nil {
		t.Fatal(err)
	}
	if len(h.security.published) != 0 {
		t.Error("un dominio sin verificar no tiene claves en los motores")
	}
}

func selectorsOf(p published) []string {
	out := make([]string, 0, len(p.keys))
	for _, k := range p.keys {
		out = append(out, k.Selector)
	}
	return out
}

func TestDKIMSelectorAvoidsCollisions(t *testing.T) {
	now := time.Date(2026, 9, 12, 15, 4, 5, 0, time.UTC)
	if s := dkimSelector(now); s != "cfm202609" {
		t.Errorf("base = %s", s)
	}
	if s := dkimSelector(now, "cfm202609"); s != "cfm20260912" {
		t.Errorf("con el mes ocupado = %s", s)
	}
	if s := dkimSelector(now, "cfm202609", "cfm20260912"); s != "cfm20260912150405" {
		t.Errorf("con mes y dia ocupados = %s", s)
	}
}

// ── Actualizacion y baja ─────────────────────────────────────────────────────

func TestUpdatePurposeTransitions(t *testing.T) {
	h := newHarness(t)
	d := h.create(t, "acme.com", domain.PurposeSending)
	h.dns.publishZone(h.uc, d)
	h.verify(t, d.ID)

	both := string(domain.PurposeBoth)
	upd, err := h.uc.Update(context.Background(), h.tenantID, d.ID, UpdateRequest{Purpose: &both})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Status != domain.StatusPending || upd.VerifiedAt != nil {
		t.Errorf("anadir corporate exige verificar el MX: status = %s", upd.Status)
	}

	h.dns.publishZone(h.uc, upd)
	h.verify(t, d.ID)
	sending := string(domain.PurposeSending)
	if _, err := h.uc.Update(context.Background(), h.tenantID, d.ID, UpdateRequest{Purpose: &sending}); err != nil {
		t.Fatal(err)
	}
	last := h.directory.calls[len(h.directory.calls)-1]
	if last.active {
		t.Error("quitar corporate desactiva el dominio en el directorio")
	}

	h.directory.err = domain.ErrDomainHasMailboxes
	both2 := string(domain.PurposeBoth)
	h.uc.Update(context.Background(), h.tenantID, d.ID, UpdateRequest{Purpose: &both2})
	h.dns.publishZone(h.uc, upd)
	h.directory.err = nil
	h.verify(t, d.ID)
	h.directory.err = domain.ErrDomainHasMailboxes
	if _, err := h.uc.Update(context.Background(), h.tenantID, d.ID, UpdateRequest{Purpose: &sending}); !errors.Is(err, domain.ErrDomainHasMailboxes) {
		t.Errorf("con buzones debe rechazar: %v", err)
	}
	if _, err := h.uc.Update(context.Background(), h.tenantID, d.ID, UpdateRequest{}); !errors.Is(err, domain.ErrNothingToUpdate) {
		t.Errorf("sin cambios: %v", err)
	}
}

func TestDeleteOrderAndConflicts(t *testing.T) {
	h := newHarness(t)
	d := h.create(t, "acme.com", domain.PurposeCorporate)

	h.directory.err = domain.ErrDomainHasMailboxes
	if err := h.uc.Delete(context.Background(), h.tenantID, d.ID); !errors.Is(err, domain.ErrDomainHasMailboxes) {
		t.Errorf("con buzones: %v", err)
	}
	h.directory.err = nil
	h.security.err = errDown
	if err := h.uc.Delete(context.Background(), h.tenantID, d.ID); !errors.Is(err, domain.ErrIntegrationUnavailable) {
		t.Errorf("sin mail-security: %v", err)
	}
	if _, err := h.repo.GetByID(context.Background(), h.tenantID, d.ID); err != nil {
		t.Error("la fila no se borra si la clave sigue en Redis")
	}

	h.security.err = nil
	if err := h.uc.Delete(context.Background(), h.tenantID, d.ID); err != nil {
		t.Fatal(err)
	}
	if len(h.security.deleted) != 1 || h.security.deleted[0] != "acme.com" {
		t.Errorf("claves borradas = %v", h.security.deleted)
	}
	if _, err := h.repo.GetByID(context.Background(), h.tenantID, d.ID); !errors.Is(err, domain.ErrDomainNotFound) {
		t.Error("la fila debe desaparecer")
	}
	if h.events.count("domains.domain.deleted") != 1 {
		t.Errorf("eventos = %v", h.events.subjects)
	}
}

func TestTenantIsolation(t *testing.T) {
	h := newHarness(t)
	d := h.create(t, "acme.com", domain.PurposeCorporate)
	other := uuid.New()
	if _, err := h.uc.Get(context.Background(), other, d.ID); !errors.Is(err, domain.ErrDomainNotFound) {
		t.Errorf("otra empresa no ve el dominio: %v", err)
	}
	if _, err := h.uc.Verify(context.Background(), other, d.ID); !errors.Is(err, domain.ErrDomainNotFound) {
		t.Errorf("otra empresa no verifica el dominio: %v", err)
	}
}

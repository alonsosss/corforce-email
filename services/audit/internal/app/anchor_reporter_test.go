package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/alonsosss/corforce-email/services/audit/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type sentMail struct{ to, subject, text string }

// fakeSender guarda cada envio y contesta segun la direccion: "suppressed@" queda suprimida,
// "rejected@" la rechaza transactional, "down@" no llega.
type fakeSender struct {
	mu   sync.Mutex
	sent []sentMail
}

func (s *fakeSender) SendAnchorReport(_ context.Context, to, subject, text string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, sentMail{to, subject, text})
	switch {
	case strings.HasPrefix(to, "suppressed@"):
		return true, nil
	case strings.HasPrefix(to, "rejected@"):
		return false, &ports.ReportRejectedError{Status: 422, Code: "VALIDATION_ERROR"}
	case strings.HasPrefix(to, "down@"):
		return false, ports.ErrReportUnavailable
	}
	return false, nil
}

func (s *fakeSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

func (s *fakeSender) last() sentMail {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sent[len(s.sent)-1]
}

type fakeSigner struct{ key []byte }

func (f fakeSigner) KeyID() string { return "0123456789abcdef" }
func (f fakeSigner) Sign(data []byte) []byte {
	m := hmac.New(sha256.New, f.key)
	m.Write(data)
	return m.Sum(nil)
}

type reportMetrics struct {
	mu       sync.Mutex
	results  []string
	success  []time.Time
	interval time.Duration
}

func (m *reportMetrics) ReportResult(r string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.results = append(m.results, r)
}
func (m *reportMetrics) ReportSucceeded(at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.success = append(m.success, at)
}
func (m *reportMetrics) ReportSchedule(d time.Duration) { m.interval = d }

func (m *reportMetrics) snapshot() ([]string, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.results...), len(m.success)
}

type fakeDirectory struct {
	tenants []domain.TenantRef
	err     error
}

func (d *fakeDirectory) ActiveTenants(context.Context) ([]domain.TenantRef, error) {
	return d.tenants, d.err
}

var (
	_ ports.AnchorReportSender  = (*fakeSender)(nil)
	_ ports.ReportSigner        = fakeSigner{}
	_ ports.AnchorReportMetrics = (*reportMetrics)(nil)
	_ ports.TenantDirectory     = (*fakeDirectory)(nil)
	_ ports.ChainBreakNotifier  = (*AnchorReporter)(nil)
)

type reporterRig struct {
	r       *AnchorReporter
	anchors *fakeAnchors
	sender  *fakeSender
	metrics *reportMetrics
	dir     *fakeDirectory
	clock   time.Time
}

func newReporterRig(recipients ...string) *reporterRig {
	rig := &reporterRig{
		anchors: &fakeAnchors{heads: map[domain.ChainName]*domain.ChainHead{}, found: map[domain.ChainName]domain.AnchorFindings{}},
		sender:  &fakeSender{},
		metrics: &reportMetrics{},
		dir:     &fakeDirectory{},
		clock:   time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC),
	}
	rig.r = NewAnchorReporter(AnchorReporterDeps{
		Anchors: rig.anchors, Directory: rig.dir, Sender: rig.sender, Signer: fakeSigner{key: []byte("k")},
		Metrics: rig.metrics, Recipients: recipients, Logger: zap.NewNop(),
	})
	rig.r.now = func() time.Time { return rig.clock }
	return rig
}

var (
	hex40 = strings.Repeat("40", 32)
	hex03 = strings.Repeat("03", 32)
)

func fullAnchor(tenant uuid.UUID, chain domain.ChainName, seq int64, hash string) *domain.ChainAnchor {
	return &domain.ChainAnchor{TenantID: tenant, Chain: chain, HeadSeq: seq, HeadHash: hash, HashVersion: 2,
		AnchoredAt: time.Date(2026, 9, 21, 23, 45, 0, 0, time.UTC)}
}

func TestCollectTomaLaUltimaAnclaDeCadaCadena(t *testing.T) {
	rig := newReporterRig("ops@example.org")
	tenant := uuid.New()
	rig.anchors.found[domain.ChainAuditLogs] = domain.AnchorFindings{Last: fullAnchor(tenant, domain.ChainAuditLogs, 40, hex40)}
	got, err := rig.r.Collect(context.Background(), domain.TenantRef{ID: tenant, Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	// La cadena de eventos no tiene ancla: no aparece, no se inventa una.
	if got.Tenant.Slug != "acme" || len(got.Anchors) != 1 || got.Anchors[0].HeadSeq != 40 || got.Anchors[0].Chain != domain.ChainAuditLogs {
		t.Fatalf("%+v", got)
	}
	rig.anchors.findErr = errors.New("base caida")
	if _, err := rig.r.Collect(context.Background(), domain.TenantRef{ID: tenant}); err == nil {
		t.Fatal("una base que no responde no da una empresa sin anclas")
	}
}

func TestElInformePeriodicoSaleFirmadoACadaDireccion(t *testing.T) {
	rig := newReporterRig("ops@example.org", "archivo@example.net")
	tenant := uuid.New()
	tenants := []domain.TenantAnchors{{Tenant: domain.TenantRef{ID: tenant, Slug: "acme"},
		Anchors: []domain.ChainAnchor{*fullAnchor(tenant, domain.ChainAuditLogs, 40, hex40)}}}
	if err := rig.r.SendScheduled(context.Background(), tenants); err != nil {
		t.Fatal(err)
	}
	if rig.sender.count() != 2 || rig.sender.sent[0].to != "ops@example.org" || rig.sender.sent[1].to != "archivo@example.net" {
		t.Fatalf("envios: %+v", rig.sender.sent)
	}
	mail := rig.sender.sent[0]
	if mail.subject != "[Core Force Mail] Anclas de auditoria 2026-09-22" {
		t.Fatalf("asunto: %q", mail.subject)
	}
	parsed, err := domain.ParseAnchorReport(mail.text)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Signature == nil || !hmac.Equal(parsed.Signature.MAC, fakeSigner{key: []byte("k")}.Sign(parsed.Block)) {
		t.Fatal("el bloque enviado no lleva la firma de la llave activa")
	}
	if len(parsed.Anchors) != 1 || parsed.Anchors[0].HeadSeq != 40 || parsed.Anchors[0].Tenant.Slug != "acme" {
		t.Fatalf("anclas: %+v", parsed.Anchors)
	}
	results, successes := rig.metrics.snapshot()
	if strings.Join(results, ",") != "sent,sent" || successes != 1 {
		t.Fatalf("metricas: %v %d", results, successes)
	}
}

func TestSinLlaveElInformeSaleSinFirmaYLoDice(t *testing.T) {
	rig := newReporterRig("ops@example.org")
	rig.r.signer = nil
	if err := rig.r.SendScheduled(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rig.sender.last().text, "signature: none") {
		t.Fatal("el informe sin llave debe declararlo")
	}
}

func TestUnaDireccionSuprimidaORechazadaCuentaPeroNoImpideElInforme(t *testing.T) {
	rig := newReporterRig("suppressed@example.org", "rejected@example.org", "ops@example.org")
	if err := rig.r.SendScheduled(context.Background(), nil); err != nil {
		t.Fatalf("una direccion valida basta para dar el informe por salido: %v", err)
	}
	results, successes := rig.metrics.snapshot()
	if strings.Join(results, ",") != "suppressed,rejected,sent" || successes != 1 {
		t.Fatalf("metricas: %v %d", results, successes)
	}
}

func TestSiNingunaDireccionLoRecibeNoHayExito(t *testing.T) {
	rig := newReporterRig("down@example.org", "suppressed@example.org")
	err := rig.r.SendScheduled(context.Background(), nil)
	if err == nil || !errors.Is(err, ports.ErrReportUnavailable) {
		t.Fatalf("error: %v", err)
	}
	results, successes := rig.metrics.snapshot()
	if strings.Join(results, ",") != "failed,suppressed" || successes != 0 {
		t.Fatalf("metricas: %v %d", results, successes)
	}
}

func TestUnaCadenaRotaSaleDeInmediatoConSusAnclasYSuSlug(t *testing.T) {
	rig := newReporterRig("ops@example.org")
	tenant := uuid.New()
	rig.dir.tenants = []domain.TenantRef{{ID: uuid.New(), Slug: "otra"}, {ID: tenant, Slug: "acme"}}
	rig.anchors.found[domain.ChainAuditLogs] = domain.AnchorFindings{Last: fullAnchor(tenant, domain.ChainAuditLogs, 40, hex40)}
	rig.anchors.found[domain.ChainSecurityEvents] = domain.AnchorFindings{Last: fullAnchor(tenant, domain.ChainSecurityEvents, 3, hex03)}

	ctx, cancel := context.WithCancel(context.Background())
	rig.r.ChainBroken(ctx, tenant, domain.ChainAuditLogs, domain.ReasonHeadBehindAnchor)
	// La peticion o el barrido que detecto la rotura termina antes de que el correo salga.
	cancel()
	rig.r.inflight.Wait()

	if rig.sender.count() != 1 {
		t.Fatalf("envios: %d", rig.sender.count())
	}
	mail := rig.sender.last()
	if mail.subject != "[Core Force Mail] Anclas de auditoria 2026-09-22: cadena rota" {
		t.Fatalf("asunto: %q", mail.subject)
	}
	parsed, err := domain.ParseAnchorReport(mail.text)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Cause != domain.ReportCauseChainBroken || parsed.Broken == nil || parsed.Broken.TenantID != tenant ||
		parsed.Broken.Chain != domain.ChainAuditLogs || parsed.Broken.Reason != domain.ReasonHeadBehindAnchor {
		t.Fatalf("rotura: %+v", parsed.Broken)
	}
	if len(parsed.Anchors) != 2 || parsed.Anchors[0].Tenant.Slug != "acme" || parsed.Anchors[1].Chain != domain.ChainSecurityEvents {
		t.Fatalf("anclas: %+v", parsed.Anchors)
	}
}

func TestElAvisoDeRoturaSaleComoMuchoUnaVezPorHoraYEmpresa(t *testing.T) {
	rig := newReporterRig("ops@example.org")
	a, b := uuid.New(), uuid.New()
	rig.r.ChainBroken(context.Background(), a, domain.ChainAuditLogs, domain.ReasonChainBroken)
	rig.r.ChainBroken(context.Background(), a, domain.ChainSecurityEvents, domain.ReasonAnchorMismatch)
	rig.r.ChainBroken(context.Background(), b, domain.ChainAuditLogs, domain.ReasonChainBroken)
	rig.r.inflight.Wait()
	if rig.sender.count() != 2 {
		t.Fatalf("una por empresa dentro de la hora; envios: %d", rig.sender.count())
	}
	rig.clock = rig.clock.Add(chainBreakNoticeEvery)
	rig.r.ChainBroken(context.Background(), a, domain.ChainAuditLogs, domain.ReasonChainBroken)
	rig.r.inflight.Wait()
	if rig.sender.count() != 3 {
		t.Fatalf("pasada la hora vuelve a salir; envios: %d", rig.sender.count())
	}
}

func TestSiNoSePuedenLeerLasAnclasElAvisoNoSaleVacio(t *testing.T) {
	rig := newReporterRig("ops@example.org")
	rig.anchors.findErr = errors.New("base caida")
	rig.r.ChainBroken(context.Background(), uuid.New(), domain.ChainAuditLogs, domain.ReasonChainBroken)
	rig.r.inflight.Wait()
	if rig.sender.count() != 0 {
		t.Fatal("no se envia un aviso sin anclas que cotejar")
	}
}

// ---- Enganches en el caso de uso ----

type recordedBreaks struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordedBreaks) ChainBroken(_ context.Context, tenantID uuid.UUID, chain domain.ChainName, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, tenantID.String()+"/"+string(chain)+"/"+reason)
}

func (r *recordedBreaks) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func TestUnaVerificacionEnLineaQueEncuentraRoturaAvisaFuera(t *testing.T) {
	r := newAnchorRig()
	breaks := &recordedBreaks{}
	r.uc.chainBreaks = breaks
	tenant := uuid.New()
	r.anchors.heads[domain.ChainAuditLogs] = &domain.ChainHead{Seq: 3, Hash: "h3"}
	r.anchors.found[domain.ChainAuditLogs] = domain.AnchorFindings{Last: anchorAt(domain.ChainAuditLogs, 5, "h5")}
	res, err := r.uc.VerifyChainIntegrity(context.Background(), tenant)
	if err != nil || res.OK {
		t.Fatalf("%+v %v", res, err)
	}
	if got := breaks.list(); len(got) != 1 || got[0] != tenant.String()+"/audit_logs/"+domain.ReasonHeadBehindAnchor {
		t.Fatalf("avisos: %v", got)
	}
	// Una cadena sana no avisa. El fake devuelve siempre el mismo veredicto, que la llamada
	// anterior dejo en rojo: se le da uno nuevo.
	r.logs.verified = &domain.ChainIntegrity{OK: true, Chain: domain.ChainAuditLogs}
	r.anchors.heads[domain.ChainAuditLogs] = &domain.ChainHead{Seq: 8, Hash: "h8"}
	if _, err := r.uc.VerifyChainIntegrity(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	if len(breaks.list()) != 1 {
		t.Fatal("una cadena intacta no debe avisar")
	}
}

func TestElAnclajeQueVeUnaCadenaMutiladaAvisaFuera(t *testing.T) {
	r := newAnchorRig()
	breaks := &recordedBreaks{}
	r.uc.chainBreaks = breaks
	tenant := uuid.New()
	r.anchors.heads[domain.ChainAuditLogs] = &domain.ChainHead{Seq: 3, Hash: "h3"}
	r.anchors.found[domain.ChainAuditLogs] = domain.AnchorFindings{Last: anchorAt(domain.ChainAuditLogs, 5, "h5")}
	results, err := r.uc.AnchorChains(context.Background(), tenant)
	if err != nil || results[0].Reason != domain.ReasonHeadBehindAnchor {
		t.Fatalf("%+v %v", results, err)
	}
	if got := breaks.list(); len(got) != 1 || !strings.HasSuffix(got[0], "/audit_logs/"+domain.ReasonHeadBehindAnchor) {
		t.Fatalf("avisos: %v", got)
	}
}

func TestUnaVerificacionEnSegundoPlanoRotaAvisaFuera(t *testing.T) {
	r := newRunRig(t, newMemRuns())
	breaks := &recordedBreaks{}
	r.uc.chainBreaks = breaks
	r.logs.broken = true
	tenant := uuid.New()
	run := startRun(t, r, tenant, domain.RunModeFull)
	waitUntil(t, "termina", func() bool { return r.runs.status(run.ID) == domain.RunCompleted })
	waitUntil(t, "avisa", func() bool { return len(breaks.list()) == 1 })
	if got := breaks.list()[0]; got != tenant.String()+"/audit_logs/"+domain.ReasonChainBroken {
		t.Fatalf("aviso: %s", got)
	}
}

func TestSinNotificadorNadaCambia(t *testing.T) {
	r := newAnchorRig()
	r.anchors.heads[domain.ChainAuditLogs] = &domain.ChainHead{Seq: 3, Hash: "h3"}
	r.anchors.found[domain.ChainAuditLogs] = domain.AnchorFindings{Last: anchorAt(domain.ChainAuditLogs, 5, "h5")}
	if res, err := r.uc.VerifyChainIntegrity(context.Background(), uuid.New()); err != nil || res.OK {
		t.Fatalf("%+v %v", res, err)
	}
}

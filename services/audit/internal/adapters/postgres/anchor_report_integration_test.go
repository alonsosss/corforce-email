//go:build integration

// Ancla externa (docs/adr/0006, seccion 8) de punta a punta contra un Postgres real: la cadena se
// ancla, el informe sale por un transactional falso (httptest) que captura el correo, y el
// verificador externo (audit verificar-ancla) lo lee tal cual, comprueba la firma y coteja cada
// ancla con la cadena por el mismo DSN. Despues se manipula la cadena, y el correo, y se comprueba
// que cada manipulacion se detecta.
package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/audit/internal/adapters/cli"
	"github.com/alonsosss/corforce-email/services/audit/internal/adapters/keyring"
	outboxadapter "github.com/alonsosss/corforce-email/services/audit/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/audit/internal/adapters/transactionalcli"
	"github.com/alonsosss/corforce-email/services/audit/internal/app"
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// fakeTransactional es el contrato interno de correo de plataforma: guarda cada envio.
type fakeTransactional struct {
	mu    sync.Mutex
	mails []map[string]string
	heads []http.Header
}

func (f *fakeTransactional) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/send-email" {
			t.Errorf("ruta: %s", r.URL.Path)
		}
		var body map[string]string
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.mails = append(f.mails, body)
		f.heads = append(f.heads, r.Header.Clone())
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"message_id":"`+uuid.NewString()+`","status":"queued"}}`)
	})
}

func (f *fakeTransactional) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.mails)
}

type countingMetrics struct {
	mu      sync.Mutex
	results []string
	success int
}

func (m *countingMetrics) ReportResult(r string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.results = append(m.results, r)
}
func (m *countingMetrics) ReportSucceeded(time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.success++
}
func (m *countingMetrics) ReportSchedule(time.Duration) {}

type staticDirectory struct{ tenants []domain.TenantRef }

func (d staticDirectory) ActiveTenants(context.Context) ([]domain.TenantRef, error) {
	return d.tenants, nil
}

func verify(t *testing.T, text string, args ...string) (int, string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "informe.txt")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := cli.VerifyAnchors(append([]string{path}, args...), &out, &errOut, openChain)
	return code, out.String(), errOut.String()
}

// openChain es lo que main da al verificador: la base por su DSN con el adaptador de este paquete.
func openChain(ctx context.Context, dsn string) (cli.ChainFactsReader, func(), error) {
	chain, err := OpenTenantChain(ctx, dsn)
	if err != nil {
		return nil, nil, err
	}
	return chain, chain.Close, nil
}

func TestElInformeDeAnclasSaleFirmadoYElVerificadorLoCotejaConLaCadena(t *testing.T) {
	e := setup(t)
	dsn := os.Getenv("AUDIT_TEST_DSN")
	t.Setenv("AUDIT_HASH_KEY", testKeyA)
	t.Setenv("AUDIT_HASH_KEYS_OLD", "")
	ring := testRing(t, testKeyA, "")
	e.useKeys(ring)
	e.seedChain(t, 8)
	e.seedEvents(t, 3)
	e.anchorAll(t)

	srv := &fakeTransactional{}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()
	platform := uuid.New()
	metrics := &countingMetrics{}
	reporter := app.NewAnchorReporter(app.AnchorReporterDeps{
		Anchors:    e.anchors,
		Directory:  staticDirectory{tenants: []domain.TenantRef{{ID: e.tenant, Slug: "acme"}}},
		Sender:     transactionalcli.New(ts.URL, "token-interno", platform),
		Signer:     keyring.NewSigner(ring),
		Metrics:    metrics,
		Recipients: []string{"anclas@example.org"},
		Logger:     zap.NewNop(),
	})

	collected, err := reporter.Collect(e.ctx, domain.TenantRef{ID: e.tenant, Slug: "acme"})
	if err != nil || len(collected.Anchors) != 2 {
		t.Fatalf("%+v %v", collected, err)
	}
	if err := reporter.SendScheduled(e.ctx, []domain.TenantAnchors{collected}); err != nil {
		t.Fatal(err)
	}
	if srv.count() != 1 || metrics.success != 1 {
		t.Fatalf("envios %d, exitos %d", srv.count(), metrics.success)
	}
	mail, head := srv.mails[0], srv.heads[0]
	if head.Get("X-Tenant-ID") != platform.String() || head.Get("X-Gateway-Token") != "token-interno" {
		t.Fatalf("el correo no sale como la empresa de plataforma: %v", head)
	}
	if mail["to"] != "anclas@example.org" || !strings.HasPrefix(mail["subject"], "[Core Force Mail] Anclas de auditoria ") || mail["html_body"] != "" {
		t.Fatalf("correo: %v", mail)
	}
	text := mail["text_body"]
	if strings.Contains(text, "Mozilla") || strings.Contains(text, "203.0.113") || strings.Contains(text, e.user.String()) {
		t.Fatal("el informe lleva datos de los apuntes")
	}

	// El receptor, fuera del servidor, comprueba la firma y coteja con la cadena.
	code, out, errOut := verify(t, text, "--dsn", dsn)
	if code != cli.ExitOK || strings.Count(out, "OK ") != 2 || !strings.Contains(out, "Firma correcta") {
		t.Fatalf("informe autentico: %d\n%s%s", code, out, errOut)
	}

	// Sigue valiendo cuando la cadena crece: el ancla es una posicion, no la cabeza.
	e.seedChain(t, 2)
	if code, out, _ := verify(t, text, "--dsn", dsn); code != cli.ExitOK || !strings.Contains(out, "cabeza actual 10") {
		t.Fatalf("cadena crecida: %d\n%s", code, out)
	}

	// Alterar el correo se nota por la firma, aunque la cadena este intacta.
	forged := strings.Replace(text, "seq=8 ", "seq=6 ", 1)
	if forged == text {
		t.Fatal("la mutacion no cambio nada")
	}
	if code, out, _ := verify(t, forged, "--dsn", dsn); code != cli.ExitEvidence || !strings.Contains(out, "FIRMA INVALIDA") {
		t.Fatalf("correo alterado: %d\n%s", code, out)
	}
	unsigned := strings.Split(text, "signature: ")[0] + "signature: none\n"
	if code, out, _ := verify(t, unsigned, "--dsn", dsn); code != cli.ExitEvidence || !strings.Contains(out, "FIRMA AUSENTE") {
		t.Fatalf("firma quitada: %d\n%s", code, out)
	}

	// Borrar las ultimas filas Y las anclas (lo que puede hacer el dueno de la base) deja la cadena
	// enlazada y sin rastro en el servidor; el correo lo delata.
	e.tamper(t, `DELETE FROM audit.audit_logs WHERE seq > 5`)
	e.tamper(t, `DELETE FROM audit.chain_anchors WHERE chain = 'audit_logs'`)
	e.assertIntact(t, 5)
	code, out, _ = verify(t, text, "--dsn", dsn)
	if code != cli.ExitEvidence || !strings.Contains(out, "MANIPULACION audit_logs (head_behind_anchor)") || !strings.Contains(out, "OK security_events") {
		t.Fatalf("borrado de la cola: %d\n%s", code, out)
	}
	if !strings.Contains(out, "AVISO audit_logs") {
		t.Fatalf("el borrado del ancla en la tabla debe avisarse:\n%s", out)
	}

	// Reescribir la fila anclada (y recalcular desde ahi con la llave) tambien se delata.
	e.forgeChain(t, 3, 2, ring.ActiveID(), v2Forger(t, ring, ring.ActiveID()))
	e.tamper(t, `UPDATE audit.security_events SET entry_hash = repeat('0', 64) WHERE seq = 3`)
	if code, out, _ := verify(t, text, "--dsn", dsn); code != cli.ExitEvidence || !strings.Contains(out, "MANIPULACION security_events (anchor_mismatch)") {
		t.Fatalf("fila reescrita: %d\n%s", code, out)
	}
}

func TestUnaVerificacionRotaEnviaElAvisoInmediatoPorTransactional(t *testing.T) {
	e := setup(t)
	ring := testRing(t, testKeyA, "")
	e.useKeys(ring)
	e.seedChain(t, 6)
	e.anchorAll(t)

	srv := &fakeTransactional{}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()
	reporter := app.NewAnchorReporter(app.AnchorReporterDeps{
		Anchors:    e.anchors,
		Directory:  staticDirectory{tenants: []domain.TenantRef{{ID: e.tenant, Slug: "acme"}}},
		Sender:     transactionalcli.New(ts.URL, "token-interno", uuid.New()),
		Signer:     keyring.NewSigner(ring),
		Metrics:    &countingMetrics{},
		Recipients: []string{"anclas@example.org"},
		Logger:     zap.NewNop(),
	})
	cp := &db.ContextPool{}
	uc := app.NewAuditUseCase(app.AuditDeps{
		Logs: e.logs, Security: e.security, Changes: e.changes, Summary: e.summary, Logger: zap.NewNop(),
		Anchors: e.anchors, AnchorEvents: outboxadapter.NewPublisher(cp), Tx: cp, ChainBreaks: reporter,
	})

	e.tamper(t, `DELETE FROM audit.audit_logs WHERE seq > 4`)
	res, err := uc.VerifyChainIntegrity(e.ctx, e.tenant)
	if err != nil || res.OK || res.Reason != domain.ReasonHeadBehindAnchor {
		t.Fatalf("%+v %v", res, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for srv.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if srv.count() != 1 {
		t.Fatal("el aviso de cadena rota no salio")
	}
	parsed, err := domain.ParseAnchorReport(srv.mails[0]["text_body"])
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Cause != domain.ReportCauseChainBroken || parsed.Broken == nil || parsed.Broken.Reason != domain.ReasonHeadBehindAnchor ||
		len(parsed.Anchors) != 1 || parsed.Anchors[0].HeadSeq != 6 || parsed.Anchors[0].Tenant.Slug != "acme" {
		t.Fatalf("aviso: %+v %+v", parsed.Broken, parsed.Anchors)
	}
	// Una segunda verificacion en la misma hora no repite el correo.
	if _, err := uc.VerifyChainIntegrity(e.ctx, e.tenant); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if srv.count() != 1 {
		t.Fatalf("envios: %d", srv.count())
	}
}

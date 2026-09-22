package cli

import (
	"bytes"
	"context"
	"errors"
	"mime/quotedprintable"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/crypto"
	"github.com/alonsosss/corforce-email/services/audit/internal/adapters/keyring"
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/google/uuid"
)

const (
	keyA = "0000000000000000000000000000000000000000000000000000000000000001"
	keyB = "0000000000000000000000000000000000000000000000000000000000000002"
)

var (
	tenantAcme = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	tenantBeta = uuid.MustParse("22222222-2222-4222-8222-222222222222")
	hashLogs   = strings.Repeat("ab", 32)
	hashEvents = strings.Repeat("cd", 32)
)

func ring(t *testing.T, active, old string) *crypto.MACKeyRing {
	t.Helper()
	t.Setenv("AUDIT_HASH_KEY", active)
	t.Setenv("AUDIT_HASH_KEYS_OLD", old)
	r, err := crypto.LoadMACKeyRing("AUDIT_HASH_KEY", "AUDIT_HASH_KEYS_OLD")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func report(tenants ...domain.TenantAnchors) domain.AnchorReport {
	return domain.AnchorReport{GeneratedAt: time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC), Cause: domain.ReportCauseScheduled, Tenants: tenants}
}

func acme() domain.TenantAnchors {
	at := time.Date(2026, 9, 21, 23, 45, 0, 0, time.UTC)
	return domain.TenantAnchors{Tenant: domain.TenantRef{ID: tenantAcme, Slug: "acme"}, Anchors: []domain.ChainAnchor{
		{TenantID: tenantAcme, Chain: domain.ChainAuditLogs, HeadSeq: 40, HeadHash: hashLogs, HashVersion: 2, AnchoredAt: at},
		{TenantID: tenantAcme, Chain: domain.ChainSecurityEvents, HeadSeq: 3, HeadHash: hashEvents, HashVersion: 2, AnchoredAt: at},
	}}
}

func signed(t *testing.T, r domain.AnchorReport, kr *crypto.MACKeyRing) string {
	t.Helper()
	s := keyring.NewSigner(kr)
	return r.Render(&domain.ReportSignature{KeyID: s.KeyID(), MAC: s.Sign(r.Block())})
}

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "informe.txt")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// flipMAC cambia el ultimo caracter del mac sin cambiar su longitud.
func flipMAC(text string) string {
	last := text[len(text)-2]
	replacement := byte('0')
	if last == '0' {
		replacement = 'f'
	}
	return text[:len(text)-2] + string(replacement) + "\n"
}

func run(t *testing.T, args []string, openDB OpenChain) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := VerifyAnchors(args, &out, &errOut, openDB)
	return code, out.String(), errOut.String()
}

func TestUnInformeAutenticoPasa(t *testing.T) {
	kr := ring(t, keyA, "")
	code, out, _ := run(t, []string{write(t, signed(t, report(acme()), kr))}, nil)
	if code != ExitOK || !strings.Contains(out, "Firma correcta") || !strings.Contains(out, "2 anclas") {
		t.Fatalf("%d %q", code, out)
	}
}

func TestAlterarElBloqueOLaFirmaEsEvidencia(t *testing.T) {
	kr := ring(t, keyA, "")
	good := signed(t, report(acme()), kr)
	casos := map[string]string{
		"hash cambiado":   strings.Replace(good, hashLogs, strings.Repeat("ab", 31)+"ac", 1),
		"seq cambiado":    strings.Replace(good, "seq=40", "seq=39", 1),
		"fecha cambiada":  strings.Replace(good, "generated_at: 2026-09-22", "generated_at: 2026-09-23", 1),
		"mac cambiado":    flipMAC(good),
		"firma suprimida": strings.Split(good, "signature: ")[0] + "signature: none\n",
		"ancla anadida":   strings.Replace(good, "anchor: tenant", "anchor: tenant=22222222-2222-4222-8222-222222222222 slug=beta chain=audit_logs seq=1 hash=00 hash_version=2 anchored_at=2026-09-21T00:00:00Z\nanchor: tenant", 1),
		"ancla eliminada": strings.Replace(good, "anchor: tenant=11111111-1111-4111-8111-111111111111 slug=acme chain=security_events seq=3 hash="+hashEvents+" hash_version=2 anchored_at=2026-09-21T23:45:00Z\n", "", 1),
		"rotura anadida":  strings.Replace(good, "cause: scheduled\n", "cause: scheduled\nbroken: tenant=11111111-1111-4111-8111-111111111111 chain=audit_logs reason=chain_broken\n", 1),
	}
	for name, text := range casos {
		t.Run(name, func(t *testing.T) {
			if text == good {
				t.Fatal("la mutacion no cambio nada")
			}
			code, out, _ := run(t, []string{write(t, text)}, nil)
			if code != ExitEvidence || !(strings.Contains(out, "FIRMA INVALIDA") || strings.Contains(out, "FIRMA AUSENTE")) {
				t.Fatalf("%d %q", code, out)
			}
		})
	}
}

func TestUnInformeFirmadoConUnaLlaveRetiradaSeComprueba(t *testing.T) {
	old := ring(t, keyA, "")
	text := signed(t, report(acme()), old)
	ring(t, keyB, keyA)
	if code, out, _ := run(t, []string{write(t, text)}, nil); code != ExitOK {
		t.Fatalf("%d %q", code, out)
	}
	// Sin la retirada no se puede comprobar: es un error, no evidencia.
	ring(t, keyB, "")
	if code, _, errOut := run(t, []string{write(t, text)}, nil); code != ExitError || !strings.Contains(errOut, "AUDIT_HASH_KEYS_OLD") {
		t.Fatalf("%d %q", code, errOut)
	}
}

func TestSinLlaveEnElEntorno(t *testing.T) {
	kr := ring(t, keyA, "")
	text := signed(t, report(acme()), kr)
	t.Setenv("AUDIT_HASH_KEY", "")
	if code, _, errOut := run(t, []string{write(t, text)}, nil); code != ExitError || !strings.Contains(errOut, "falta AUDIT_HASH_KEY") {
		t.Fatalf("%d %q", code, errOut)
	}
	code, out, _ := run(t, []string{write(t, report(acme()).Render(nil))}, nil)
	if code != ExitOK || !strings.Contains(out, "AVISO") {
		t.Fatalf("un informe sin firma y sin llave se lee con aviso: %d %q", code, out)
	}
}

func TestUnCorreoCompletoConQuotedPrintableSeDecodifica(t *testing.T) {
	kr := ring(t, keyA, "")
	body := signed(t, report(acme()), kr)
	var qp bytes.Buffer
	w := quotedprintable.NewWriter(&qp)
	_, _ = w.Write([]byte(body))
	_ = w.Close()
	if !strings.Contains(qp.String(), "=\r\n") {
		t.Fatal("la prueba necesita lineas plegadas por quoted-printable")
	}
	eml := "From: no-reply@example.org\r\nTo: ops@example.org\r\nSubject: [Core Force Mail] Anclas de auditoria 2026-09-22\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=\"xyz\"\r\n\r\n" +
		"--xyz\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n<p>no es aqui</p>\r\n" +
		"--xyz\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" +
		qp.String() + "\r\n--xyz--\r\n"
	code, out, errOut := run(t, []string{write(t, eml)}, nil)
	if code != ExitOK || !strings.Contains(out, "Firma correcta") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
}

type scriptedFacts struct {
	facts map[domain.ChainName]domain.ChainFacts
	err   error
	dsn   string
}

func (s *scriptedFacts) Facts(_ context.Context, a domain.ChainAnchor) (domain.ChainFacts, error) {
	return s.facts[a.Chain], s.err
}

func opener(s *scriptedFacts) OpenChain {
	return func(_ context.Context, dsn string) (ChainFactsReader, func(), error) {
		s.dsn = dsn
		return s, func() {}, nil
	}
}

func TestElCotejoConLaCadenaDaElVeredicto(t *testing.T) {
	kr := ring(t, keyA, "")
	path := write(t, signed(t, report(acme()), kr))
	intact := &scriptedFacts{facts: map[domain.ChainName]domain.ChainFacts{
		domain.ChainAuditLogs:      {HeadSeq: 55, HashAtSeq: hashLogs, AnchorRecorded: true},
		domain.ChainSecurityEvents: {HeadSeq: 3, HashAtSeq: hashEvents, AnchorRecorded: true},
	}}
	code, out, _ := run(t, []string{path, "--dsn", "postgres://x/mail_tenant_acme"}, opener(intact))
	if code != ExitOK || strings.Count(out, "OK ") != 2 || intact.dsn != "postgres://x/mail_tenant_acme" {
		t.Fatalf("%d %q", code, out)
	}

	truncated := &scriptedFacts{facts: map[domain.ChainName]domain.ChainFacts{
		domain.ChainAuditLogs:      {HeadSeq: 39, AnchorRecorded: false},
		domain.ChainSecurityEvents: {HeadSeq: 3, HashAtSeq: hashEvents, AnchorRecorded: true},
	}}
	code, out, _ = run(t, []string{path, "--dsn", "d"}, opener(truncated))
	if code != ExitEvidence || !strings.Contains(out, "MANIPULACION audit_logs (head_behind_anchor)") || !strings.Contains(out, "AVISO audit_logs") || !strings.Contains(out, "OK security_events") {
		t.Fatalf("%d %q", code, out)
	}

	rewritten := &scriptedFacts{facts: map[domain.ChainName]domain.ChainFacts{
		domain.ChainAuditLogs:      {HeadSeq: 55, HashAtSeq: strings.Repeat("ff", 32), AnchorRecorded: true},
		domain.ChainSecurityEvents: {HeadSeq: 3, HashAtSeq: hashEvents, AnchorRecorded: true},
	}}
	if code, out, _ = run(t, []string{path, "--dsn", "d"}, opener(rewritten)); code != ExitEvidence || !strings.Contains(out, "(anchor_mismatch)") {
		t.Fatalf("%d %q", code, out)
	}

	down := &scriptedFacts{err: errors.New("no responde")}
	if code, _, errOut := run(t, []string{path, "--dsn", "d"}, opener(down)); code != ExitError || !strings.Contains(errOut, "no responde") {
		t.Fatalf("%d %q", code, errOut)
	}
}

func TestUnaFirmaInvalidaMandaSobreUnCotejoCorrecto(t *testing.T) {
	kr := ring(t, keyA, "")
	text := signed(t, report(acme()), kr)
	path := write(t, flipMAC(text))
	intact := &scriptedFacts{facts: map[domain.ChainName]domain.ChainFacts{
		domain.ChainAuditLogs:      {HeadSeq: 55, HashAtSeq: hashLogs, AnchorRecorded: true},
		domain.ChainSecurityEvents: {HeadSeq: 3, HashAtSeq: hashEvents, AnchorRecorded: true},
	}}
	if code, out, _ := run(t, []string{path, "--dsn", "d"}, opener(intact)); code != ExitEvidence || !strings.Contains(out, "FIRMA INVALIDA") {
		t.Fatalf("%d %q", code, out)
	}
}

func TestConVariasEmpresasHayQueElegirCual(t *testing.T) {
	kr := ring(t, keyA, "")
	beta := domain.TenantAnchors{Tenant: domain.TenantRef{ID: tenantBeta, Slug: "beta"}, Anchors: []domain.ChainAnchor{
		{TenantID: tenantBeta, Chain: domain.ChainAuditLogs, HeadSeq: 9, HeadHash: hashLogs, HashVersion: 2, AnchoredAt: time.Now()},
	}}
	path := write(t, signed(t, report(acme(), beta), kr))
	facts := &scriptedFacts{facts: map[domain.ChainName]domain.ChainFacts{domain.ChainAuditLogs: {HeadSeq: 9, HashAtSeq: hashLogs, AnchorRecorded: true}}}
	if code, _, errOut := run(t, []string{path, "--dsn", "d"}, opener(facts)); code != ExitError || !strings.Contains(errOut, "--tenant") {
		t.Fatalf("%d %q", code, errOut)
	}
	if code, out, _ := run(t, []string{path, "--dsn", "d", "--tenant", "beta"}, opener(facts)); code != ExitOK || strings.Count(out, "OK ") != 1 {
		t.Fatalf("%d %q", code, out)
	}
	if code, out, _ := run(t, []string{path, "--dsn", "d", "--tenant", tenantBeta.String()}, opener(facts)); code != ExitOK || strings.Count(out, "OK ") != 1 {
		t.Fatalf("%d %q", code, out)
	}
	if code, _, errOut := run(t, []string{path, "--dsn", "d", "--tenant", "nadie"}, opener(facts)); code != ExitError || !strings.Contains(errOut, "nadie") {
		t.Fatalf("%d %q", code, errOut)
	}
}

func TestArgumentosYFicherosMalos(t *testing.T) {
	ring(t, keyA, "")
	if code, _, _ := run(t, nil, nil); code != ExitError {
		t.Fatal("sin fichero")
	}
	if code, _, _ := run(t, []string{filepath.Join(t.TempDir(), "no-existe")}, nil); code != ExitError {
		t.Fatal("fichero inexistente")
	}
	if code, _, errOut := run(t, []string{write(t, "un correo cualquiera\n")}, nil); code != ExitError || !strings.Contains(errOut, "informe legible") {
		t.Fatalf("%d %q", code, errOut)
	}
}

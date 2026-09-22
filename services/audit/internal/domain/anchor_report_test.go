package domain

import (
	"bytes"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var (
	tenantA = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	tenantB = uuid.MustParse("22222222-2222-4222-8222-222222222222")
	hashA   = strings.Repeat("ab", 32)
	hashB   = strings.Repeat("cd", 32)
	hashC   = strings.Repeat("ef", 32)
	at      = time.Date(2026, 9, 21, 9, 45, 0, 0, time.UTC)
	mac     = bytes.Repeat([]byte{0x5a}, 32)
)

func sampleReport() AnchorReport {
	return AnchorReport{
		GeneratedAt: time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC),
		Cause:       ReportCauseScheduled,
		Tenants: []TenantAnchors{
			{Tenant: TenantRef{ID: tenantB, Slug: "beta"}, Anchors: []ChainAnchor{
				{TenantID: tenantB, Chain: ChainSecurityEvents, HeadSeq: 7, HeadHash: hashC, HashVersion: 2, AnchoredAt: at},
				{TenantID: tenantB, Chain: ChainAuditLogs, HeadSeq: 120, HeadHash: hashB, HashVersion: 2, AnchoredAt: at},
			}},
			{Tenant: TenantRef{ID: tenantA}, Anchors: []ChainAnchor{
				{TenantID: tenantA, Chain: ChainAuditLogs, HeadSeq: 5, HeadHash: hashA, HashVersion: 1, AnchoredAt: at.Add(time.Minute)},
			}},
		},
	}
}

func TestElBloqueEsDeterministaYOrdenado(t *testing.T) {
	r := sampleReport()
	got := string(r.Block())
	want := "format: 1\n" +
		"generated_at: 2026-09-22T00:00:00Z\n" +
		"cause: scheduled\n" +
		"anchor: tenant=11111111-1111-4111-8111-111111111111 slug=- chain=audit_logs seq=5 hash=" + hashA + " hash_version=1 anchored_at=2026-09-21T09:46:00Z\n" +
		"anchor: tenant=22222222-2222-4222-8222-222222222222 slug=beta chain=audit_logs seq=120 hash=" + hashB + " hash_version=2 anchored_at=2026-09-21T09:45:00Z\n" +
		"anchor: tenant=22222222-2222-4222-8222-222222222222 slug=beta chain=security_events seq=7 hash=" + hashC + " hash_version=2 anchored_at=2026-09-21T09:45:00Z\n"
	if got != want {
		t.Fatalf("bloque:\n%s\nesperado:\n%s", got, want)
	}
	// El mismo contenido en otro orden da los mismos bytes: la firma no depende del orden de lectura.
	r.Tenants[0], r.Tenants[1] = r.Tenants[1], r.Tenants[0]
	if string(r.Block()) != want {
		t.Fatal("el bloque cambia con el orden de entrada")
	}
}

func TestElAsuntoEsFijoYLlevaLaFecha(t *testing.T) {
	r := sampleReport()
	if got := r.Subject(); got != "[Core Force Mail] Anclas de auditoria 2026-09-22" {
		t.Fatalf("asunto: %q", got)
	}
	r.Cause, r.Broken = ReportCauseChainBroken, &ChainBreak{TenantID: tenantA, Chain: ChainAuditLogs, Reason: ReasonHeadBehindAnchor}
	if got := r.Subject(); got != "[Core Force Mail] Anclas de auditoria 2026-09-22: cadena rota" {
		t.Fatalf("asunto de rotura: %q", got)
	}
}

func TestRenderYParseSonInversos(t *testing.T) {
	r := sampleReport()
	sig := &ReportSignature{KeyID: "0123456789abcdef", MAC: mac}
	body := r.Render(sig)
	p, err := ParseAnchorReport(body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p.Block, r.Block()) {
		t.Fatalf("el bloque leido no es el firmado:\n%s", p.Block)
	}
	if p.Format != AnchorReportFormat || !p.GeneratedAt.Equal(r.GeneratedAt) || p.Cause != ReportCauseScheduled || p.Broken != nil {
		t.Fatalf("cabecera: %+v", p)
	}
	if p.Signature == nil || p.Signature.KeyID != sig.KeyID || !bytes.Equal(p.Signature.MAC, mac) {
		t.Fatalf("firma: %+v", p.Signature)
	}
	if len(p.Anchors) != 3 {
		t.Fatalf("anclas: %d", len(p.Anchors))
	}
	first := p.Anchors[0]
	if first.Tenant.ID != tenantA || first.Tenant.Slug != "" || first.TenantID != tenantA || first.Chain != ChainAuditLogs ||
		first.HeadSeq != 5 || first.HeadHash != hashA || first.HashVersion != 1 || !first.AnchoredAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("primera ancla: %+v", first)
	}
	if p.Anchors[1].Tenant.Slug != "beta" || p.Anchors[2].Chain != ChainSecurityEvents || p.Anchors[2].HeadSeq != 7 {
		t.Fatalf("anclas de beta: %+v %+v", p.Anchors[1], p.Anchors[2])
	}
}

func TestLaRoturaViajaEnElBloqueFirmado(t *testing.T) {
	r := sampleReport()
	r.Cause = ReportCauseChainBroken
	r.Broken = &ChainBreak{TenantID: tenantB, Chain: ChainSecurityEvents, Reason: ReasonAnchorMismatch}
	p, err := ParseAnchorReport(r.Render(&ReportSignature{KeyID: "0123456789abcdef", MAC: mac}))
	if err != nil {
		t.Fatal(err)
	}
	if p.Cause != ReportCauseChainBroken || p.Broken == nil || *p.Broken != *r.Broken {
		t.Fatalf("rotura: %+v", p.Broken)
	}
	if !strings.Contains(string(p.Block), "broken: tenant="+tenantB.String()+" chain=security_events reason=anchor_mismatch\n") {
		t.Fatalf("la rotura no esta en el bloque:\n%s", p.Block)
	}
}

func TestSinLlaveElInformeLoDiceYNoLlevaFirma(t *testing.T) {
	body := sampleReport().Render(nil)
	if !strings.Contains(body, "signature: none\n") || !strings.Contains(body, "no tiene AUDIT_HASH_KEY") {
		t.Fatalf("cuerpo sin firma:\n%s", body)
	}
	p, err := ParseAnchorReport(body)
	if err != nil || p.Signature != nil {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestElParseoToleraCRLFYEspaciosFinales(t *testing.T) {
	r := sampleReport()
	body := r.Render(&ReportSignature{KeyID: "0123456789abcdef", MAC: mac})
	mangled := strings.ReplaceAll(body, "\n", "  \r\n")
	p, err := ParseAnchorReport(mangled)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p.Block, r.Block()) {
		t.Fatal("el bloque cambia con CRLF o espacios finales")
	}
}

// El informe es la copia externa del rastro: no puede llevar nada del contenido de los apuntes.
func TestElCuerpoSoloLlevaIdentificadoresPosicionesYHashes(t *testing.T) {
	body := sampleReport().Render(&ReportSignature{KeyID: "0123456789abcdef", MAC: mac})
	p, _ := ParseAnchorReport(body)
	anchorLine := regexp.MustCompile(`^anchor: tenant=[0-9a-f-]{36} slug=[^ ]+ chain=(audit_logs|security_events) seq=\d+ hash=[0-9a-f]+ hash_version=\d anchored_at=\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)
	for _, l := range strings.Split(strings.TrimRight(string(p.Block), "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "format: "), strings.HasPrefix(l, "generated_at: "), strings.HasPrefix(l, "cause: "):
		case anchorLine.MatchString(l):
		default:
			t.Fatalf("linea fuera del contrato: %q", l)
		}
	}
	for _, forbidden := range []string{"ip_address", "user_agent", "@", "before", "after", "changes"} {
		if strings.Contains(string(p.Block), forbidden) {
			t.Fatalf("el bloque lleva %q", forbidden)
		}
	}
}

func TestUnSlugConEspaciosNoRompeLaLinea(t *testing.T) {
	r := sampleReport()
	r.Tenants[0].Tenant.Slug = "con espacio"
	p, err := ParseAnchorReport(r.Render(nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range p.Anchors {
		if a.Tenant.ID == tenantB && a.Tenant.Slug != "" {
			t.Fatalf("slug: %q", a.Tenant.Slug)
		}
	}
}

func TestElParseoRechazaLoQueNoSabeLeer(t *testing.T) {
	good := sampleReport().Render(&ReportSignature{KeyID: "0123456789abcdef", MAC: mac})
	casos := map[string]struct {
		text string
		want error
	}{
		"sin bloque":               {"hola\n", ErrReportBlockMissing},
		"sin firma":                {strings.Split(good, reportEnd)[0] + reportEnd + "\n", ErrReportSignatureMissing},
		"otra cosa tras el bloque": {strings.Replace(good, "signature: ", "firma: ", 1), ErrReportSignatureMissing},
		"bloque repetido":          {good + good, nil},
		"linea desconocida":        {strings.Replace(good, "cause: ", "nota: x\ncause: ", 1), nil},
		"mac corto":                {strings.Replace(good, "mac=5a5a", "mac=5a", 1), nil},
		"key_id corto":             {strings.Replace(good, "key_id=0123456789abcdef", "key_id=0123", 1), nil},
		"formato futuro":           {strings.Replace(good, "format: 1", "format: 2", 1), nil},
		"ancla sin hash":           {strings.Replace(good, "hash="+hashA+" ", "", 1), nil},
		"campo repetido":           {strings.Replace(good, "seq=5 ", "seq=5 seq=6 ", 1), nil},
		"campo de mas":             {strings.Replace(good, "seq=5 ", "seq=5 ip=1.2.3.4 ", 1), nil},
	}
	for name, c := range casos {
		t.Run(name, func(t *testing.T) {
			_, err := ParseAnchorReport(c.text)
			if err == nil {
				t.Fatal("se acepto")
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Fatalf("error %v, esperado %v", err, c.want)
			}
		})
	}
}

func TestCompareAnchorDaElMismoVeredictoQueElVerificador(t *testing.T) {
	a := ChainAnchor{Chain: ChainAuditLogs, HeadSeq: 10, HeadHash: hashA}
	casos := []struct {
		nombre  string
		facts   ChainFacts
		reason  string
		warning bool
	}{
		{"la cadena la contiene", ChainFacts{HeadSeq: 12, HashAtSeq: hashA, AnchorRecorded: true}, "", false},
		{"cabeza justo en el ancla", ChainFacts{HeadSeq: 10, HashAtSeq: hashA, AnchorRecorded: true}, "", false},
		{"se borraron las ultimas filas", ChainFacts{HeadSeq: 9, HashAtSeq: "", AnchorRecorded: true}, ReasonHeadBehindAnchor, false},
		{"se vacio la cadena", ChainFacts{}, ReasonHeadBehindAnchor, true},
		{"se reescribio la fila", ChainFacts{HeadSeq: 12, HashAtSeq: hashB, AnchorRecorded: true}, ReasonAnchorMismatch, false},
		{"se borro la fila y se siguio escribiendo", ChainFacts{HeadSeq: 12, HashAtSeq: "", AnchorRecorded: true}, ReasonAnchorMismatch, false},
		{"tambien borraron el ancla de la tabla", ChainFacts{HeadSeq: 9, HashAtSeq: "", AnchorRecorded: false}, ReasonHeadBehindAnchor, true},
		{"cadena intacta pero sin el ancla en la tabla", ChainFacts{HeadSeq: 12, HashAtSeq: hashA, AnchorRecorded: false}, "", true},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			reason, warning := CompareAnchor(a, c.facts)
			if reason != c.reason || (warning != "") != c.warning {
				t.Fatalf("reason %q warning %q", reason, warning)
			}
		})
	}
}

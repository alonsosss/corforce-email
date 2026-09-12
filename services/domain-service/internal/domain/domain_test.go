package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const platformHost = "mail.plataforma.example"

func TestValidateDomainName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want error
	}{
		{"valido", "acme.com", nil},
		{"subdominio", "correo.acme.com.pe", nil},
		{"punycode", "xn--espaa-rta.com", nil},
		{"vacio", "", ErrInvalidDomainName},
		{"sin tld", "localhost", ErrInvalidDomainName},
		{"ipv4", "192.168.1.1", ErrInvalidDomainName},
		{"ipv6", "::1", ErrInvalidDomainName},
		{"tld numerico", "acme.123", ErrInvalidDomainName},
		{"guion inicial", "-acme.com", ErrInvalidDomainName},
		{"etiqueta vacia", "acme..com", ErrInvalidDomainName},
		{"mayusculas sin normalizar", "Acme.com", ErrInvalidDomainName},
		{"etiqueta larga", strings.Repeat("a", 64) + ".com", ErrInvalidDomainName},
		{"hostname de la plataforma", platformHost, ErrPlatformDomain},
		{"subdominio de la plataforma", "smtp." + platformHost, ErrPlatformDomain},
		{"dominio base de la plataforma", "plataforma.example", ErrPlatformDomain},
		{"subdominio del base de la plataforma", "clientes.plataforma.example", ErrPlatformDomain},
		{"parecido pero distinto", "miplataforma.example", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ValidateDomainName(c.in, platformHost); !errors.Is(got, c.want) {
				t.Errorf("ValidateDomainName(%q) = %v; want %v", c.in, got, c.want)
			}
		})
	}
}

func TestNormalizeDomainName(t *testing.T) {
	if got := NormalizeDomainName("  Acme.COM. "); got != "acme.com" {
		t.Errorf("got %q", got)
	}
}

func fixture(purpose Purpose) *Domain {
	return &Domain{
		ID: uuid.New(), TenantID: uuid.New(), Domain: "acme.com", Purpose: purpose,
		VerificationToken: "0123456789abcdef0123456789abcdef",
		DKIMSelector:      "cfm202609", DKIMPublicKey: "MIIBIjANBgkq", DMARCPolicy: DMARCQuarantine,
	}
}

var platform = PlatformDNS{MXHostname: "mx.plataforma.example", SPFInclude: "include:spf.plataforma.example", DMARCRUA: "dmarc@plataforma.example"}

func recordByKind(records []DNSRecord, kind RecordKind) (DNSRecord, bool) {
	for _, r := range records {
		if r.Record == kind {
			return r, true
		}
	}
	return DNSRecord{}, false
}

func TestExpectedRecordsCorporate(t *testing.T) {
	d := fixture(PurposeCorporate)
	records := ExpectedRecords(d, platform)
	if len(records) != 5 {
		t.Fatalf("records = %d; want 5", len(records))
	}
	want := map[RecordKind][3]string{
		RecordOwnershipTXT: {"TXT", "_cfm-verify.acme.com", "cfm-verify=" + d.VerificationToken},
		RecordMX:           {"MX", "acme.com", "mx.plataforma.example priority 10"},
		RecordSPF:          {"TXT", "acme.com", "v=spf1 include:spf.plataforma.example -all"},
		RecordDKIM:         {"TXT", "cfm202609._domainkey.acme.com", "v=DKIM1; k=rsa; p=MIIBIjANBgkq"},
		RecordDMARC:        {"TXT", "_dmarc.acme.com", "v=DMARC1; p=quarantine; rua=mailto:dmarc@plataforma.example"},
	}
	for kind, exp := range want {
		rec, ok := recordByKind(records, kind)
		if !ok {
			t.Fatalf("falta el registro %s", kind)
		}
		if rec.Type != exp[0] || rec.Host != exp[1] || rec.Value != exp[2] {
			t.Errorf("%s = %+v; want %v", kind, rec, exp)
		}
		if rec.Required == (kind == RecordDMARC) {
			t.Errorf("%s required = %v", kind, rec.Required)
		}
	}
}

func TestExpectedRecordsSendingOmitsMX(t *testing.T) {
	records := ExpectedRecords(fixture(PurposeSending), platform)
	if _, ok := recordByKind(records, RecordMX); ok {
		t.Error("un dominio solo de envio no debe pedir MX")
	}
}

func TestExpectedRecordsIncludePreviousDKIMDuringGrace(t *testing.T) {
	d := fixture(PurposeBoth)
	now := time.Now()
	d.DKIMPreviousSelector, d.DKIMPreviousPrivateKeyEnc, d.DKIMPreviousPublicKey, d.DKIMRotatedAt = "cfm202608", []byte("x"), "OLDKEY", &now
	rec, ok := recordByKind(ExpectedRecords(d, platform), RecordDKIMPrevious)
	if !ok || rec.Host != "cfm202608._domainkey.acme.com" || rec.Required {
		t.Errorf("registro anterior = %+v, ok=%v", rec, ok)
	}
}

// allOK devuelve observaciones completas y correctas para el dominio.
func allOK(d *Domain) map[RecordKind]Observation {
	return map[RecordKind]Observation{
		RecordOwnershipTXT: {TXT: []string{"cfm-verify=" + d.VerificationToken}},
		RecordMX:           {MX: []MXRecord{{Host: "MX.plataforma.example.", Priority: 10}}},
		RecordSPF:          {TXT: []string{"v=spf1 include:spf.plataforma.example -all", "google-site-verification=abc"}},
		RecordDKIM:         {TXT: []string{"v=DKIM1; k=rsa; p=" + d.DKIMPublicKey}},
		RecordDMARC:        {TXT: []string{"v=DMARC1; p=quarantine; rua=mailto:dmarc@plataforma.example"}},
	}
}

func checkByKind(checks []DNSCheck, kind RecordKind) DNSCheck {
	for _, c := range checks {
		if c.Record == kind {
			return c
		}
	}
	return DNSCheck{}
}

func TestEvaluateAllOK(t *testing.T) {
	d := fixture(PurposeCorporate)
	res := Evaluate(d, ExpectedRecords(d, platform), allOK(d), time.Now())
	if res.Outcome != OutcomeVerified {
		t.Fatalf("outcome = %s; checks %+v", res.Outcome, res.Checks)
	}
	for _, c := range res.Checks {
		if !c.OK {
			t.Errorf("%s no ok: %s", c.Record, c.Detail)
		}
	}
}

func TestEvaluateMissingSPFFails(t *testing.T) {
	d := fixture(PurposeCorporate)
	obs := allOK(d)
	obs[RecordSPF] = Observation{TXT: []string{"v=spf1 include:_spf.otro.example ~all"}}
	res := Evaluate(d, ExpectedRecords(d, platform), obs, time.Now())
	if res.Outcome != OutcomeFailed {
		t.Fatalf("outcome = %s; want failed", res.Outcome)
	}
	spf := checkByKind(res.Checks, RecordSPF)
	if spf.OK || !strings.Contains(spf.Detail, "include:spf.plataforma.example") {
		t.Errorf("spf = %+v", spf)
	}
	if spf.Observed != "v=spf1 include:_spf.otro.example ~all" {
		t.Errorf("observed = %q", spf.Observed)
	}
}

func TestEvaluateTwoSPFRecordsFail(t *testing.T) {
	d := fixture(PurposeSending)
	obs := allOK(d)
	obs[RecordSPF] = Observation{TXT: []string{"v=spf1 include:spf.plataforma.example -all", "v=spf1 -all"}}
	res := Evaluate(d, ExpectedRecords(d, platform), obs, time.Now())
	if res.Outcome != OutcomeFailed || !strings.Contains(checkByKind(res.Checks, RecordSPF).Detail, "mas de un") {
		t.Errorf("res = %+v", res)
	}
}

func TestEvaluateDMARCAbsentDoesNotBlock(t *testing.T) {
	d := fixture(PurposeCorporate)
	obs := allOK(d)
	obs[RecordDMARC] = Observation{}
	res := Evaluate(d, ExpectedRecords(d, platform), obs, time.Now())
	if res.Outcome != OutcomeVerified {
		t.Fatalf("outcome = %s; want verified", res.Outcome)
	}
	dmarc := checkByKind(res.Checks, RecordDMARC)
	if dmarc.OK || dmarc.Detail == "" {
		t.Errorf("dmarc = %+v", dmarc)
	}
}

func TestEvaluateDMARCWeakerPolicyIsOKWithDetail(t *testing.T) {
	d := fixture(PurposeCorporate)
	d.DMARCPolicy = DMARCReject
	obs := allOK(d)
	res := Evaluate(d, ExpectedRecords(d, platform), obs, time.Now())
	dmarc := checkByKind(res.Checks, RecordDMARC)
	if !dmarc.OK || !strings.Contains(dmarc.Detail, "quarantine") {
		t.Errorf("dmarc = %+v", dmarc)
	}
}

func TestEvaluateDKIMKeyMismatchAndSplitKey(t *testing.T) {
	d := fixture(PurposeSending)
	obs := allOK(d)
	obs[RecordDKIM] = Observation{TXT: []string{"v=DKIM1; k=rsa; p=OTRA"}}
	if res := Evaluate(d, ExpectedRecords(d, platform), obs, time.Now()); res.Outcome != OutcomeFailed {
		t.Errorf("clave distinta: outcome = %s", res.Outcome)
	}
	obs[RecordDKIM] = Observation{TXT: []string{"v=DKIM1; k=rsa; p=MIIBIjAN Bgkq"}}
	if res := Evaluate(d, ExpectedRecords(d, platform), obs, time.Now()); res.Outcome != OutcomeVerified {
		t.Errorf("clave partida en cadenas: outcome = %s", res.Outcome)
	}
}

func TestEvaluateTransientErrorIsInconclusive(t *testing.T) {
	d := fixture(PurposeCorporate)
	obs := allOK(d)
	obs[RecordMX] = Observation{Err: errors.New("i/o timeout")}
	obs[RecordSPF] = Observation{}
	res := Evaluate(d, ExpectedRecords(d, platform), obs, time.Now())
	if res.Outcome != OutcomeInconclusive {
		t.Errorf("outcome = %s; want inconclusive aunque falte el SPF", res.Outcome)
	}
	if mx := checkByKind(res.Checks, RecordMX); mx.OK || !strings.Contains(mx.Detail, "timeout") {
		t.Errorf("mx = %+v", mx)
	}
}

func TestEvaluateMXMissingBlocksOnlyCorporate(t *testing.T) {
	corp := fixture(PurposeBoth)
	obs := allOK(corp)
	obs[RecordMX] = Observation{MX: []MXRecord{{Host: "mx.otro.example", Priority: 5}}}
	if res := Evaluate(corp, ExpectedRecords(corp, platform), obs, time.Now()); res.Outcome != OutcomeFailed {
		t.Errorf("corporate sin MX: outcome = %s", res.Outcome)
	}
	send := fixture(PurposeSending)
	if res := Evaluate(send, ExpectedRecords(send, platform), obs, time.Now()); res.Outcome != OutcomeVerified {
		t.Errorf("sending sin MX: outcome = %s", res.Outcome)
	}
}

func TestEvaluateRotationGraceSignsWithPrevious(t *testing.T) {
	d := fixture(PurposeSending)
	now := time.Now()
	d.DKIMPreviousSelector, d.DKIMPreviousPrivateKeyEnc, d.DKIMPreviousPublicKey, d.DKIMRotatedAt = "cfm202608", []byte("x"), "OLDKEY", &now
	obs := allOK(d)
	obs[RecordDKIM] = Observation{}
	obs[RecordDKIMPrevious] = Observation{TXT: []string{"v=DKIM1; k=rsa; p=OLDKEY"}}
	res := Evaluate(d, ExpectedRecords(d, platform), obs, now)
	if res.Outcome != OutcomeVerified || !res.SignWithPrevious {
		t.Fatalf("res = outcome %s signWithPrevious %v", res.Outcome, res.SignWithPrevious)
	}
	if cur := checkByKind(res.Checks, RecordDKIM); cur.OK || !strings.Contains(cur.Detail, "cfm202608") {
		t.Errorf("dkim actual = %+v", cur)
	}

	// Sin el TXT anterior tampoco, no hay con que firmar: falla.
	obs[RecordDKIMPrevious] = Observation{}
	if res := Evaluate(d, ExpectedRecords(d, platform), obs, now); res.Outcome != OutcomeFailed || res.SignWithPrevious {
		t.Errorf("sin ningun TXT DKIM: %+v", res.Outcome)
	}
}

func TestPreviousDKIMExpired(t *testing.T) {
	d := fixture(PurposeSending)
	if d.PreviousDKIMExpired(time.Now(), time.Hour) {
		t.Error("sin clave anterior no hay gracia que vencer")
	}
	rotated := time.Now().Add(-2 * time.Hour)
	d.DKIMPreviousSelector, d.DKIMPreviousPrivateKeyEnc, d.DKIMPreviousPublicKey, d.DKIMRotatedAt = "old", []byte("x"), "k", &rotated
	if !d.PreviousDKIMExpired(time.Now(), time.Hour) || d.PreviousDKIMExpired(time.Now(), 3*time.Hour) {
		t.Error("la gracia se mide desde dkim_rotated_at")
	}
	d.ClearPreviousDKIM()
	if d.HasPreviousDKIM() {
		t.Error("ClearPreviousDKIM debe dejar los cuatro campos vacios")
	}
}

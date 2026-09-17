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

// La proteccion del dominio de la plataforma sigue la Public Suffix List: bajo un sufijo de dos
// etiquetas (com.pe, co.uk) protege solo el dominio registrable de la plataforma, nunca el
// sufijo entero.
func TestValidateDomainNamePlataformaPorSufijoPublico(t *testing.T) {
	cases := []struct {
		name     string
		platform string
		in       string
		want     error
	}{
		{".com: base", "mail.plataforma.com", "plataforma.com", ErrPlatformDomain},
		{".com: subdominio del base", "mail.plataforma.com", "clientes.plataforma.com", ErrPlatformDomain},
		{".com: otra empresa", "mail.plataforma.com", "acme.com", nil},
		{".com.pe: hostname", "mail.plataforma.com.pe", "mail.plataforma.com.pe", ErrPlatformDomain},
		{".com.pe: subdominio del hostname", "mail.plataforma.com.pe", "smtp.mail.plataforma.com.pe", ErrPlatformDomain},
		{".com.pe: base", "mail.plataforma.com.pe", "plataforma.com.pe", ErrPlatformDomain},
		{".com.pe: subdominio del base", "mail.plataforma.com.pe", "ventas.plataforma.com.pe", ErrPlatformDomain},
		{".com.pe: otra empresa", "mail.plataforma.com.pe", "acme.com.pe", nil},
		{".com.pe: subdominio de otra empresa", "mail.plataforma.com.pe", "correo.acme.com.pe", nil},
		{".com.pe: parecido pero distinto", "mail.plataforma.com.pe", "miplataforma.com.pe", nil},
		{".com.pe: el mismo nombre en .pe", "mail.plataforma.com.pe", "plataforma.pe", nil},
		{".co.uk: base", "mail.x.co.uk", "x.co.uk", ErrPlatformDomain},
		{".co.uk: otra empresa", "mail.x.co.uk", "acme.co.uk", nil},
		{".uk directo: base", "mail.plataforma.uk", "plataforma.uk", ErrPlatformDomain},
		{".uk directo: otra empresa", "mail.plataforma.uk", "acme.uk", nil},
		{".uk directo: bajo co.uk", "mail.plataforma.uk", "plataforma.co.uk", nil},
		{"hostname de la plataforma en dos etiquetas", "plataforma.com.pe", "clientes.plataforma.com.pe", ErrPlatformDomain},
		{"mayusculas y punto final en MAIL_HOSTNAME", " MAIL.Plataforma.COM.PE. ", "plataforma.com.pe", ErrPlatformDomain},
		{"mayusculas y punto final: otra empresa", " MAIL.Plataforma.COM.PE. ", "acme.com.pe", nil},
		{"sufijo privado: solo el registrable", "mail.plataforma.github.io", "plataforma.github.io", ErrPlatformDomain},
		{"sufijo privado: vecino del sufijo", "mail.plataforma.github.io", "acme.github.io", nil},
		{"TLD fuera de la lista", "mail.plataforma.example", "clientes.plataforma.example", ErrPlatformDomain},
		{"hostname que es un sufijo publico: el propio sufijo no se da de alta", "com.pe", "com.pe", ErrPublicSuffixDomain},
		{"hostname que es un sufijo publico: no bloquea el sufijo", "com.pe", "acme.com.pe", nil},
		{"sin hostname de plataforma", "", "acme.com.pe", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ValidateDomainName(c.in, c.platform); !errors.Is(got, c.want) {
				t.Errorf("ValidateDomainName(%q, %q) = %v; want %v", c.in, c.platform, got, c.want)
			}
		})
	}
}

// Un sufijo publico no es el dominio de una empresa; lo que cuelga de el, si. Un TLD fuera de la
// lista no convierte en sufijo al nombre de dos etiquetas que usa el e2e.
func TestValidateDomainNameSufijoPublico(t *testing.T) {
	cases := []struct {
		in   string
		want error
	}{
		{"com.pe", ErrPublicSuffixDomain},
		{"co.uk", ErrPublicSuffixDomain},
		{"gob.pe", ErrPublicSuffixDomain},
		{"github.io", ErrPublicSuffixDomain},
		{"blogspot.com", ErrPublicSuffixDomain},
		{"acme.ck", ErrPublicSuffixDomain},
		{"www.ck", nil},
		{"acme.com.pe", nil},
		{"correo.acme.com.pe", nil},
		{"acme.co.uk", nil},
		{"acme.uk", nil},
		{"acme.github.io", nil},
		{"acme.com", nil},
		{"cfm.test", nil},
		{"acme.test", nil},
		{"plataforma.example", nil},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := ValidateDomainName(c.in, ""); !errors.Is(got, c.want) {
				t.Errorf("ValidateDomainName(%q) = %v; want %v", c.in, got, c.want)
			}
		})
	}
}

func TestValidatePlatformHostname(t *testing.T) {
	cases := []struct {
		host string
		ok   bool
	}{
		{"mail.plataforma.com", true},
		{"mail.plataforma.com.pe", true},
		{"MAIL.Plataforma.co.uk.", true},
		{"plataforma.uk", true},
		{"mail.cfm.test", true},
		{"com.pe", false},
		{"co.uk", false},
		{"uk", false},
		{"github.io", false},
		{"localhost", false},
		{"", false},
		{"192.168.1.1", false},
		{"mail_x.plataforma.com", false},
	}
	for _, c := range cases {
		err := ValidatePlatformHostname(c.host)
		if c.ok && err != nil {
			t.Errorf("ValidatePlatformHostname(%q) = %v", c.host, err)
		}
		if !c.ok && !errors.Is(err, ErrInvalidPlatformHostname) {
			t.Errorf("ValidatePlatformHostname(%q) = %v; want ErrInvalidPlatformHostname", c.host, err)
		}
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
	now := time.Now()
	rotated := now.Add(-2 * time.Hour)
	d.DKIMPreviousSelector, d.DKIMPreviousPrivateKeyEnc, d.DKIMPreviousPublicKey, d.DKIMRotatedAt = "old", []byte("x"), "k", &rotated
	if !d.PreviousDKIMExpired(now, time.Hour) || d.PreviousDKIMExpired(now, 3*time.Hour) {
		t.Error("sin firmas posteriores la gracia se mide desde dkim_rotated_at")
	}
	signed := now.Add(-30 * time.Minute)
	d.DKIMPreviousSignedAt = &signed
	if d.PreviousDKIMExpired(now, time.Hour) || !d.PreviousDKIMRetireAfter(time.Hour).Equal(signed.Add(time.Hour)) {
		t.Error("la gracia se mide desde la ultima vez que la clave anterior pudo firmar")
	}
	earlier := rotated.Add(-time.Hour)
	d.DKIMPreviousSignedAt = &earlier
	if !d.PreviousDKIMSigningEnd().Equal(rotated) {
		t.Error("la anterior firmo al menos hasta la rotacion")
	}
	d.ClearPreviousDKIM()
	if d.HasPreviousDKIM() || d.DKIMPreviousSignedAt != nil || !d.PreviousDKIMRetireAfter(time.Hour).IsZero() {
		t.Error("ClearPreviousDKIM debe dejar vacios los campos de la clave anterior")
	}
	if got := d.DKIMSelectors(); len(got) != 1 || got[0] != d.DKIMSelector {
		t.Errorf("selectores sin anterior = %v", got)
	}
}

func TestNormalizeRevocationReason(t *testing.T) {
	if got, err := NormalizeRevocationReason("  expuesta en un respaldo\n(ticket 42)  "); err != nil || got != "expuesta en un respaldo\n(ticket 42)" {
		t.Errorf("valido: %q %v", got, err)
	}
	if got, err := NormalizeRevocationReason(strings.Repeat("ñ", MaxRevocationReasonLength)); err != nil || got == "" {
		t.Errorf("el limite cuenta caracteres, no bytes: %v", err)
	}
	for _, bad := range []string{"", "   ", "a\x00b", "a\x1bb", strings.Repeat("a", MaxRevocationReasonLength+1), "\xff"} {
		if _, err := NormalizeRevocationReason(bad); !errors.Is(err, ErrInvalidRevocationReason) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestDKIMRotationRevoked(t *testing.T) {
	var none *DKIMRotation
	scheduled := &DKIMRotation{Kind: RotationScheduled, PreviousSelector: "a", RevokedSelectors: []string{"a"}}
	revoked := &DKIMRotation{Kind: RotationCompromised, RevokedSelectors: []string{"a", "b"}}
	if none.Revoked("a") || scheduled.Revoked("a") || !revoked.Revoked("b") || revoked.Revoked("c") {
		t.Error("solo una revocacion revoca, y solo sus selectores")
	}
}

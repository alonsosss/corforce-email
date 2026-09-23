package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var sesPlatform = PlatformDNS{
	MXHostname: "mx.plataforma.example", SPFInclude: "include:spf.plataforma.example",
	DMARCRUA: "dmarc@plataforma.example", SESRegion: "eu-central-1",
}

func sesDomain(purpose Purpose) *Domain {
	return &Domain{ID: uuid.New(), TenantID: uuid.New(), Domain: "envio.com", Purpose: purpose,
		VerificationToken: "t", DKIMSelector: "cfm202609", DKIMPublicKey: "PUB", DMARCPolicy: DMARCReject}
}

func recordsOf(records []DNSRecord, kinds ...RecordKind) map[RecordKind]DNSRecord {
	out := map[RecordKind]DNSRecord{}
	for _, r := range records {
		for _, k := range kinds {
			if r.Record == k {
				out[k] = r
			}
		}
	}
	return out
}

func TestSoloLosDominiosDeEnvioPidenElMailFromDeSES(t *testing.T) {
	for _, tc := range []struct {
		purpose Purpose
		region  string
		want    bool
	}{
		{PurposeSending, "eu-central-1", true},
		{PurposeBoth, "eu-central-1", true},
		{PurposeCorporate, "eu-central-1", false},
		{PurposeSending, "", false},
	} {
		platform := sesPlatform
		platform.SESRegion = tc.region
		got := recordsOf(ExpectedRecords(sesDomain(tc.purpose), platform, ""), RecordSESMailFromMX, RecordSESMailFromSPF)
		if (len(got) == 2) != tc.want || (len(got) != 0 && len(got) != 2) {
			t.Fatalf("%s con region %q: %+v", tc.purpose, tc.region, got)
		}
		if !tc.want {
			continue
		}
		mx, spf := got[RecordSESMailFromMX], got[RecordSESMailFromSPF]
		if mx.Type != "MX" || mx.Host != "bounce.envio.com" || mx.Value != "feedback-smtp.eu-central-1.amazonses.com priority 10" || mx.Required {
			t.Errorf("MX: %+v", mx)
		}
		if spf.Type != "TXT" || spf.Host != "bounce.envio.com" || spf.Value != "v=spf1 include:amazonses.com ~all" || spf.Required {
			t.Errorf("SPF: %+v", spf)
		}
	}
}

func TestElMXDelMailFromNoBloqueaYSeEvaluaContraSES(t *testing.T) {
	d := sesDomain(PurposeSending)
	expected := ExpectedRecords(d, sesPlatform, "")
	observed := map[RecordKind]Observation{
		RecordOwnershipTXT:   {TXT: []string{OwnershipValue("t")}},
		RecordSPF:            {TXT: []string{SPFValue(sesPlatform.SPFInclude)}},
		RecordDKIM:           {TXT: []string{DKIMValue("PUB")}},
		RecordSESMailFromMX:  {MX: []MXRecord{{Host: "mx.plataforma.example.", Priority: 10}}},
		RecordSESMailFromSPF: {TXT: []string{"v=spf1 include:otro.com ~all"}},
	}
	res := Evaluate(d, expected, observed, time.Now())
	if res.Outcome != OutcomeVerified {
		t.Fatalf("el MAIL FROM es recomendado: %s", res.Outcome)
	}
	for _, c := range res.Checks {
		if (c.Record == RecordSESMailFromMX || c.Record == RecordSESMailFromSPF) && c.OK {
			t.Errorf("%s mal publicado cuenta como bien: %+v", c.Record, c)
		}
	}

	observed[RecordSESMailFromMX] = Observation{MX: []MXRecord{{Host: "Feedback-SMTP.eu-central-1.amazonses.com.", Priority: 10}}}
	observed[RecordSESMailFromSPF] = Observation{TXT: []string{SESMailFromSPFValue}}
	for _, c := range Evaluate(d, expected, observed, time.Now()).Checks {
		if (c.Record == RecordSESMailFromMX || c.Record == RecordSESMailFromSPF) && !c.OK {
			t.Errorf("%s publicado: %s", c.Record, c.Detail)
		}
	}
}

func TestElProveedorDNSPublicaElMXDelMailFromConSuDestino(t *testing.T) {
	var mx, corporate *DesiredRecord
	desired := DesiredRecords(sesDomain(PurposeBoth), sesPlatform, "")
	for i := range desired {
		switch desired[i].Kind {
		case RecordSESMailFromMX:
			mx = &desired[i]
		case RecordMX:
			corporate = &desired[i]
		}
	}
	if mx == nil || mx.Content != "feedback-smtp.eu-central-1.amazonses.com" || mx.Priority != 10 || mx.Name != "bounce.envio.com" {
		t.Fatalf("MX del MAIL FROM: %+v", mx)
	}
	if corporate == nil || corporate.Content != "mx.plataforma.example" {
		t.Fatalf("el MX corporativo sigue apuntando a la plataforma: %+v", corporate)
	}

	var spf DesiredRecord
	for _, dr := range desired {
		if dr.Kind == RecordSESMailFromSPF {
			spf = dr
		}
	}
	other := ProviderRecord{ID: "1", Type: "TXT", Name: "bounce.envio.com", Content: `"v=spf1 include:otro.com -all"`}
	if plan := PlanRecord(spf, []ProviderRecord{other}, false); plan.Action != RecordConflict {
		t.Errorf("un SPF del cliente en el MAIL FROM es un conflicto, no un segundo SPF: %s", plan.Action)
	}
}

func TestEstadosDeSES(t *testing.T) {
	for raw, want := range map[string]SESCheckStatus{
		"SUCCESS": SESCheckSuccess, "PENDING": SESCheckPending, "TEMPORARY_FAILURE": SESCheckTemporaryFailure,
		"FAILED": SESCheckFailed, "NOT_STARTED": SESCheckNotStarted, "NUEVO_DE_SES": "", "": "",
	} {
		if got := ParseSESCheckStatus(raw); got != want {
			t.Errorf("%q = %q; want %q", raw, got, want)
		}
	}
	now := time.Now()
	if s := (SESIdentityObservation{VerifiedForSending: true}).State(now); !s.VerifiedForSending() || !s.Checked() {
		t.Errorf("verificada: %+v", s)
	}
	if s := (SESIdentityObservation{DKIMStatus: SESCheckFailed}).State(now); s.IdentityStatus != SESIdentityFailed || s.VerifiedForSending() {
		t.Errorf("DKIM fallido: %+v", s)
	}
	if s := (SESIdentityObservation{DKIMStatus: SESCheckPending}).State(now); s.IdentityStatus != SESIdentityPending {
		t.Errorf("pendiente: %+v", s)
	}
}

func TestLaIdentidadEsDeLaEmpresaDeSuEtiquetaODeQuienLaAdopta(t *testing.T) {
	tenant := uuid.New()
	if (SESIdentityObservation{}).OwnedBy(tenant) {
		t.Error("una identidad sin etiqueta no es de nadie hasta adoptarla")
	}
	if !(SESIdentityObservation{ConfigurationSet: "cfm-transactional"}).Adoptable("cfm-transactional") {
		t.Error("sin etiqueta y con el conjunto de la plataforma se adopta")
	}
	if (SESIdentityObservation{ConfigurationSet: "my-first-configuration-set"}).Adoptable("cfm-transactional") {
		t.Error("sin etiqueta y con otro conjunto es de otro proyecto de la cuenta")
	}
	if (SESIdentityObservation{}).Adoptable("") {
		t.Error("sin conjunto de plataforma no se adopta nada")
	}
	if (SESIdentityObservation{TenantTag: uuid.NewString(), ConfigurationSet: "cfm-transactional"}).Adoptable("cfm-transactional") {
		t.Error("una etiquetada por otra empresa no se adopta")
	}
	if !(SESIdentityObservation{TenantTag: strings.ToUpper(tenant.String())}).OwnedBy(tenant) {
		t.Error("la etiqueta propia")
	}
	if (SESIdentityObservation{TenantTag: uuid.NewString()}).OwnedBy(tenant) {
		t.Error("la de otra empresa no")
	}
	if (SESIdentityObservation{DKIMOrigin: "AWS_SES", DKIMSelectors: []string{"cfm202609"}}).SignsWith("cfm202609") {
		t.Error("con Easy DKIM SES no firma con la clave de domain-service")
	}
}

func TestRegionYErrorDeSES(t *testing.T) {
	for region, want := range map[string]bool{
		"us-east-1": true, "eu-central-1": true, "ap-southeast-2": true, "us-gov-west-1": true,
		"": false, "US-EAST-1": false, "us-east": false, "us-east-1.evil.com": false, "us-*-1": false,
	} {
		if ValidSESRegion(region) != want {
			t.Errorf("%q: %v", region, !want)
		}
	}
	long := strings.Repeat("x", MaxSESErrorLength+10) + "\n"
	if got := TruncateSESError(long); len([]rune(got)) != MaxSESErrorLength {
		t.Errorf("largo %d", len([]rune(got)))
	}
	if got := TruncateSESError("  linea uno\n\tlinea\xffdos  "); got != "linea uno lineados" {
		t.Errorf("una linea valida: %q", got)
	}
}

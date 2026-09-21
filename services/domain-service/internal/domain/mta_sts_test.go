package domain

import (
	"strings"
	"testing"
	"time"
)

const testPolicyID = "a1b2c3d4e5f60718293a4b5c6d7e8f90"

var platformWithTLSRPT = PlatformDNS{
	MXHostname: "mx.plataforma.example", SPFInclude: "include:spf.plataforma.example",
	DMARCRUA: "dmarc@plataforma.example", TLSRPTRUA: "tlsrpt@plataforma.example",
}

func TestUnDominioConPoliticaYTLSRPTPideLosDosTXTSinExigirlos(t *testing.T) {
	d := fixture(PurposeCorporate)
	records := ExpectedRecords(d, platformWithTLSRPT, testPolicyID)
	sts, ok := recordByKind(records, RecordMTASTS)
	if !ok || sts.Type != "TXT" || sts.Host != "_mta-sts.acme.com" || sts.Value != "v=STSv1; id="+testPolicyID || sts.Required {
		t.Errorf("mta_sts = %+v ok=%v", sts, ok)
	}
	rpt, ok := recordByKind(records, RecordTLSRPT)
	if !ok || rpt.Type != "TXT" || rpt.Host != "_smtp._tls.acme.com" || rpt.Value != "v=TLSRPTv1; rua=mailto:tlsrpt@plataforma.example" || rpt.Required {
		t.Errorf("tls_rpt = %+v ok=%v", rpt, ok)
	}
	if len(records) != 7 {
		t.Errorf("registros = %d; se esperaban 7", len(records))
	}
}

func TestSinPoliticaOSinDireccionNoSePideElRegistro(t *testing.T) {
	d := fixture(PurposeCorporate)
	if _, ok := recordByKind(ExpectedRecords(d, platformWithTLSRPT, ""), RecordMTASTS); ok {
		t.Error("sin politica publicada no se pide _mta-sts")
	}
	if _, ok := recordByKind(ExpectedRecords(d, platform, testPolicyID), RecordTLSRPT); ok {
		t.Error("sin MAIL_TLSRPT_RUA no se pide _smtp._tls")
	}
	if _, ok := recordByKind(ExpectedRecords(d, platform, testPolicyID), RecordMTASTS); !ok {
		t.Error("MTA-STS no depende de la direccion de TLS-RPT")
	}
}

// Ambos protegen el correo que ENTRA por la celda: un dominio solo de envio no los pide.
func TestUnDominioSoloDeEnvioNoPideMTASTSNiTLSRPT(t *testing.T) {
	records := ExpectedRecords(fixture(PurposeSending), platformWithTLSRPT, testPolicyID)
	for _, kind := range []RecordKind{RecordMTASTS, RecordTLSRPT} {
		if _, ok := recordByKind(records, kind); ok {
			t.Errorf("un dominio de envio no debe pedir %s", kind)
		}
	}
}

func evaluateWith(t *testing.T, d *Domain, sts, rpt []string) (VerificationResult, DNSCheck, DNSCheck) {
	t.Helper()
	obs := allOK(d)
	obs[RecordMTASTS] = Observation{TXT: sts}
	obs[RecordTLSRPT] = Observation{TXT: rpt}
	res := Evaluate(d, ExpectedRecords(d, platformWithTLSRPT, testPolicyID), obs, time.Now())
	return res, checkByKind(res.Checks, RecordMTASTS), checkByKind(res.Checks, RecordTLSRPT)
}

func TestEvaluateMTASTSYTLSRPTPublicados(t *testing.T) {
	d := fixture(PurposeCorporate)
	res, sts, rpt := evaluateWith(t, d,
		[]string{"v=STSv1; id=" + testPolicyID},
		[]string{"v=TLSRPTv1; rua=mailto:otro@cliente.example, mailto:tlsrpt@plataforma.example"})
	if !sts.OK || !rpt.OK || res.Outcome != OutcomeVerified {
		t.Fatalf("mta_sts %+v, tls_rpt %+v, resultado %s", sts, rpt, res.Outcome)
	}
}

// Ninguno es requerido: faltar o estar mal no cambia el estado del dominio.
func TestEvaluateMTASTSYTLSRPTNoBloqueanLaVerificacion(t *testing.T) {
	d := fixture(PurposeCorporate)
	casos := map[string]struct{ sts, rpt []string }{
		"ausentes":                {nil, nil},
		"id de otra version":      {[]string{"v=STSv1; id=vieja"}, []string{"v=TLSRPTv1; rua=mailto:otro@cliente.example"}},
		"registros duplicados":    {[]string{"v=STSv1; id=" + testPolicyID, "v=STSv1; id=otra"}, []string{"v=TLSRPTv1; rua=mailto:tlsrpt@plataforma.example", "v=TLSRPTv1; rua=mailto:x@y.example"}},
		"otro TXT no relacionado": {[]string{"google-site-verification=abc"}, []string{"google-site-verification=abc"}},
	}
	for nombre, c := range casos {
		res, sts, rpt := evaluateWith(t, d, c.sts, c.rpt)
		if sts.OK || rpt.OK {
			t.Errorf("%s: no deberian estar ok: %+v %+v", nombre, sts, rpt)
		}
		if res.Outcome != OutcomeVerified {
			t.Errorf("%s: el resultado debe seguir verificado: %s", nombre, res.Outcome)
		}
		if sts.Detail == "" || rpt.Detail == "" {
			t.Errorf("%s: sin explicacion para el cliente", nombre)
		}
	}
}

func TestEvaluateMTASTSDiceCualEsElIdVigente(t *testing.T) {
	d := fixture(PurposeCorporate)
	_, sts, _ := evaluateWith(t, d, []string{"v=STSv1; id=vieja"}, nil)
	if !strings.Contains(sts.Detail, testPolicyID) || !strings.Contains(sts.Detail, "vieja") {
		t.Errorf("detalle: %q", sts.Detail)
	}
}

func TestUnFalloDelDNSEnMTASTSNoCambiaElEstado(t *testing.T) {
	d := fixture(PurposeCorporate)
	obs := allOK(d)
	obs[RecordMTASTS] = Observation{Err: timeoutError{}}
	res := Evaluate(d, ExpectedRecords(d, platformWithTLSRPT, testPolicyID), obs, time.Now())
	if res.Outcome != OutcomeVerified {
		t.Fatalf("un registro recomendado que no se pudo consultar no vuelve inconcluso el dominio: %s", res.Outcome)
	}
	if c := checkByKind(res.Checks, RecordMTASTS); c.OK || !strings.Contains(c.Detail, "no se pudo consultar") {
		t.Errorf("check: %+v", c)
	}
}

type timeoutError struct{}

func (timeoutError) Error() string { return "timeout" }

func TestElMTASTSYElTLSRPTOcupanSuPropioSitioEnElProveedor(t *testing.T) {
	d := dominioDePrueba(PurposeCorporate)
	var sts, rpt DesiredRecord
	for _, r := range DesiredRecords(d, platformWithTLSRPT, testPolicyID) {
		switch r.Kind {
		case RecordMTASTS:
			sts = r
		case RecordTLSRPT:
			rpt = r
		}
	}
	if sts.Name != "_mta-sts.acme.com" || rpt.Name != "_smtp._tls.acme.com" {
		t.Fatalf("nombres: %q %q", sts.Name, rpt.Name)
	}
	txt := func(name, content string) ProviderRecord {
		return ProviderRecord{Type: "TXT", Name: name, Content: content}
	}
	if !sts.sameFamily(txt(sts.Name, `"v=STSv1; id=vieja"`)) || rpt.sameFamily(txt(sts.Name, "v=STSv1; id=x")) {
		t.Error("el TXT de MTA-STS se reconoce por su version y no se confunde con el de TLS-RPT")
	}
	if sts.sameFamily(txt(sts.Name, "otra-cosa")) {
		t.Error("un TXT ajeno en _mta-sts no es un MTA-STS que la plataforma pueda reemplazar sin preguntar")
	}
	if !rpt.sameFamily(txt(rpt.Name, "v=TLSRPTv1; rua=mailto:x@y.example")) {
		t.Error("el TXT de TLS-RPT se reconoce por su version")
	}
}

func TestValidateReportAddress(t *testing.T) {
	for _, ok := range []string{"tlsrpt@plataforma.example", "informes+tls@dmarc.plataforma.example"} {
		if err := ValidateReportAddress(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "sin-arroba", "@plataforma.example", "a@", "a@b@c.example", "a b@plataforma.example",
		"a@plataforma.example,b@plataforma.example", "a@plataforma.example;", "a@no_es_dns", "a!@plataforma.example"} {
		if err := ValidateReportAddress(bad); err == nil {
			t.Errorf("%q debe rechazarse", bad)
		}
	}
}

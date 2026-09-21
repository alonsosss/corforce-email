package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/google/uuid"
)

// fakeMTASTS da la version de politica que se le fije por dominio, o el error que se le fije, y anota
// a quien se pregunto.
type fakeMTASTS struct {
	ids   map[string]string
	err   error
	asked []string
}

func (f *fakeMTASTS) PolicyID(_ context.Context, _ uuid.UUID, name string) (string, error) {
	f.asked = append(f.asked, name)
	return f.ids[name], f.err
}

func withMTASTS(h *harness, reader *fakeMTASTS, tlsRPT string) {
	platform := testPlatform
	platform.TLSRPTRUA = tlsRPT
	h.uc.mtaSTS, h.uc.platform = reader, platform
}

func recordOf(records []domain.DNSRecord, kind domain.RecordKind) (domain.DNSRecord, bool) {
	for _, r := range records {
		if r.Record == kind {
			return r, true
		}
	}
	return domain.DNSRecord{}, false
}

func TestElTXTDeMTASTSLlevaLaVersionQueDaElDirectorio(t *testing.T) {
	h := newHarness(t)
	reader := &fakeMTASTS{ids: map[string]string{"acme.com": "v1"}}
	withMTASTS(h, reader, "tlsrpt@plataforma.example")
	d := h.create(t, "acme.com", domain.PurposeCorporate)

	sts, ok := recordOf(h.uc.ExpectedRecords(context.Background(), d), domain.RecordMTASTS)
	if !ok || sts.Value != "v=STSv1; id=v1" || sts.Host != "_mta-sts.acme.com" || sts.Required {
		t.Fatalf("mta_sts = %+v ok=%v", sts, ok)
	}
	// La version cambia en el directorio y el registro esperado la sigue en la siguiente lectura.
	reader.ids["acme.com"] = "v2"
	if sts, _ = recordOf(h.uc.ExpectedRecords(context.Background(), d), domain.RecordMTASTS); sts.Value != "v=STSv1; id=v2" {
		t.Errorf("tras cambiar la politica: %+v", sts)
	}
	if reader.asked[0] != "acme.com" {
		t.Errorf("preguntado por %v", reader.asked)
	}
}

func TestSinPoliticaEnElDirectorioNoSePideElTXT(t *testing.T) {
	h := newHarness(t)
	withMTASTS(h, &fakeMTASTS{}, "")
	d := h.create(t, "acme.com", domain.PurposeCorporate)
	records := h.uc.ExpectedRecords(context.Background(), d)
	for _, kind := range []domain.RecordKind{domain.RecordMTASTS, domain.RecordTLSRPT} {
		if _, ok := recordOf(records, kind); ok {
			t.Errorf("no debe pedir %s", kind)
		}
	}
}

func TestUnDominioSoloDeEnvioNoPreguntaAlDirectorio(t *testing.T) {
	h := newHarness(t)
	reader := &fakeMTASTS{ids: map[string]string{"envio.com": "v1"}}
	withMTASTS(h, reader, "tlsrpt@plataforma.example")
	d := h.create(t, "envio.com", domain.PurposeSending)
	records := h.uc.ExpectedRecords(context.Background(), d)
	if _, ok := recordOf(records, domain.RecordMTASTS); ok || len(reader.asked) != 0 {
		t.Errorf("un dominio de envio no lleva MTA-STS ni pregunta: %v", reader.asked)
	}
}

// Sin respuesta del directorio se sigue con el resto de registros: MTA-STS es recomendado y su fallo no
// puede dejar sin verificar ni sin mostrar el dominio.
func TestUnFalloDelDirectorioDejaFueraSoloElTXTDeMTASTS(t *testing.T) {
	h := newHarness(t)
	withMTASTS(h, &fakeMTASTS{err: errors.New("celda caida")}, "tlsrpt@plataforma.example")
	d := h.create(t, "acme.com", domain.PurposeCorporate)
	records := h.uc.ExpectedRecords(context.Background(), d)
	if _, ok := recordOf(records, domain.RecordMTASTS); ok {
		t.Error("sin respuesta no se anuncia una version")
	}
	if _, ok := recordOf(records, domain.RecordSPF); !ok {
		t.Error("el resto de registros sigue")
	}
	if _, ok := recordOf(records, domain.RecordTLSRPT); !ok {
		t.Error("TLS-RPT no depende del directorio")
	}
}

func TestVerificarGuardaLosChecksDeMTASTSYTLSRPTSinBloquear(t *testing.T) {
	h := newHarness(t)
	withMTASTS(h, &fakeMTASTS{ids: map[string]string{"acme.com": "v1"}}, "tlsrpt@plataforma.example")
	d := h.create(t, "acme.com", domain.PurposeCorporate)
	h.dns.publishZone(h.uc, d)
	res := h.verify(t, d.ID)
	if res.Outcome != domain.OutcomeVerified {
		t.Fatalf("outcome = %s", res.Outcome)
	}
	saved := map[domain.RecordKind]domain.DNSCheck{}
	for _, c := range res.Checks {
		saved[c.Record] = c
	}
	if !saved[domain.RecordMTASTS].OK || !saved[domain.RecordTLSRPT].OK {
		t.Fatalf("checks: %+v", saved)
	}

	// La politica cambia de version y el cliente aun no actualizo el TXT: el check lo dice y el dominio
	// sigue verificado.
	h.uc.mtaSTS.(*fakeMTASTS).ids["acme.com"] = "v2"
	res = h.verify(t, d.ID)
	stale := checkOf(res.Checks, domain.RecordMTASTS)
	if res.Outcome != domain.OutcomeVerified || stale.OK || stale.Detail == "" {
		t.Errorf("TXT desactualizado: outcome %s, check %+v", res.Outcome, stale)
	}
}

func checkOf(checks []domain.DNSCheck, kind domain.RecordKind) domain.DNSCheck {
	for _, c := range checks {
		if c.Record == kind {
			return c
		}
	}
	return domain.DNSCheck{}
}

// El proveedor DNS automatico publica los TXT de MTA-STS y TLS-RPT como el resto de registros, con la
// marca de la plataforma, y una segunda publicacion no toca lo que ya esta igual.
func TestPublicarEnElProveedorIncluyeMTASTSYTLSRPT(t *testing.T) {
	h := newDNSHarness(t)
	withMTASTS(h.harness, &fakeMTASTS{ids: map[string]string{"acme.com": "v1"}}, "tlsrpt@plataforma.example")
	h.connect(t)
	d := h.automatic(t, "acme.com", domain.PurposeCorporate)
	res := h.publish(t, d.ID)

	got := actions(res.Publication)
	if got[domain.RecordMTASTS] != domain.RecordCreated || got[domain.RecordTLSRPT] != domain.RecordCreated {
		t.Fatalf("acciones %v", got)
	}
	found := false
	for _, r := range h.cf.records["zacme"] {
		if r.Name == "_mta-sts.acme.com" && strings.Contains(r.Content, "v=STSv1; id=v1") {
			found = r.Managed()
		}
	}
	if !found {
		t.Fatalf("TXT de MTA-STS con la marca de la plataforma no encontrado en la zona: %+v", h.cf.records["zacme"])
	}
	writes := len(h.cf.writes)
	for kind, action := range actions(h.publish(t, d.ID).Publication) {
		if action != domain.RecordUnchanged {
			t.Errorf("segunda publicacion de %s: %s", kind, action)
		}
	}
	if len(h.cf.writes) != writes {
		t.Error("la segunda publicacion escribio en la zona")
	}
}

func TestLosNuevosRegistrosSeAdmitenComoReemplazables(t *testing.T) {
	got, err := ParseRecordKinds([]string{"mta_sts", "tls_rpt"})
	if err != nil || !got[domain.RecordMTASTS] || !got[domain.RecordTLSRPT] {
		t.Fatalf("ParseRecordKinds: %v %v", got, err)
	}
}

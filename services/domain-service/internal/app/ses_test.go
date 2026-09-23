package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/google/uuid"
)

const (
	testSESRegion    = "us-east-1"
	testSESConfigSet = "cfm-transactional"
)

// newSESHarness es el harness con la integracion de SES activa.
func newSESHarness(t *testing.T) (*harness, *fakeSES) {
	t.Helper()
	h := newHarness(t)
	ses := newFakeSES()
	platform := testPlatform
	platform.SESRegion = testSESRegion
	h.uc = New(Deps{
		Repo: h.repo, DNS: h.dns, Cipher: h.keyRing,
		MailDirectory: h.directory, MailSecurity: h.security, DomainIndex: h.index, Events: h.events, KeyEvents: h.events,
		SES: ses, SendingEvents: h.events, SESConfigurationSet: testSESConfigSet,
		Platform: platform, PlatformHostname: platformHost,
		DKIMRotationGrace: 72 * time.Hour,
		Now:               func() time.Time { return h.now },
	})
	return h, ses
}

// verifiedSending da de alta un dominio, publica su zona y lo verifica.
func (h *harness) verifiedSending(t *testing.T, name string, purpose domain.Purpose) *domain.Domain {
	t.Helper()
	d := h.create(t, name, purpose)
	h.dns.publishZone(h.uc, d)
	if res := h.verify(t, d.ID); res.Domain.Status != domain.StatusVerified {
		t.Fatalf("no verifico: %+v", res.Checks)
	}
	return h.stored(t, d.ID)
}

func (h *harness) currentPEM(t *testing.T, d *domain.Domain) string {
	t.Helper()
	pem, err := h.keyRing.Decrypt(d.DKIMPrivateKeyEnc)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	return string(pem)
}

func TestAlVerificarseUnDominioDeEnvioSeDaDeAltaEnSESConSuMismaClaveDKIM(t *testing.T) {
	h, ses := newSESHarness(t)
	d := h.verifiedSending(t, "envio.com", domain.PurposeSending)

	id, ok := ses.identities["envio.com"]
	if !ok {
		t.Fatal("el dominio verificado no se dio de alta en SES")
	}
	if id.key.Selector != d.DKIMSelector || id.key.PrivateKeyPEM != h.currentPEM(t, d) {
		t.Fatalf("SES debe firmar con el selector y la clave que custodia domain-service: %s", id.key.Selector)
	}
	if id.obs.MailFromDomain != "bounce.envio.com" || id.obs.BehaviorOnMXFailure != domain.SESBehaviorOnMXFailure {
		t.Errorf("MAIL FROM = %q %q", id.obs.MailFromDomain, id.obs.BehaviorOnMXFailure)
	}
	if id.obs.ConfigurationSet != testSESConfigSet || id.obs.TenantTag != h.tenantID.String() {
		t.Errorf("conjunto %q, etiqueta %q", id.obs.ConfigurationSet, id.obs.TenantTag)
	}
	if d.SES.IdentityStatus != domain.SESIdentityPending || d.SES.CheckedAt == nil || d.SES.LastError != "" {
		t.Fatalf("estado de SES guardado: %+v", d.SES)
	}
	if len(h.events.sending) != 1 || h.events.sending[0] {
		t.Fatalf("la primera comprobacion anuncia que aun no es apto: %v", h.events.sending)
	}
	if len(h.events.verifiedReady) != 1 || h.events.verifiedReady[0] == nil || *h.events.verifiedReady[0] {
		t.Fatalf("domains.domain.verified dice que SES aun no acepta sus envios: %v", h.events.verifiedReady)
	}
	if h.directory.calls != nil {
		t.Errorf("un dominio solo de envio no se activa en la celda: %v", h.directory.calls)
	}
}

func TestSoloConSESVerificadoElDominioQuedaAptoYLaSincronizacionEsIdempotente(t *testing.T) {
	h, ses := newSESHarness(t)
	d := h.verifiedSending(t, "envio.com", domain.PurposeSending)
	calls := len(ses.calls)

	h.now = h.now.Add(time.Hour)
	h.uc.SweepTenant(context.Background(), h.tenantID)
	if len(ses.calls) != calls {
		t.Fatalf("con la identidad al dia no se cambia nada en SES: %v", ses.calls[calls:])
	}
	if len(h.events.sending) != 1 {
		t.Fatalf("sin cambio de aptitud no hay evento: %v", h.events.sending)
	}

	ses.verify("envio.com")
	h.now = h.now.Add(time.Hour)
	h.uc.SweepTenant(context.Background(), h.tenantID)
	got := h.stored(t, d.ID)
	if got.SES.IdentityStatus != domain.SESIdentityVerified || got.SES.DKIMStatus != domain.SESCheckSuccess {
		t.Fatalf("el barrido guarda lo que dice SES: %+v", got.SES)
	}
	if len(h.events.sending) != 2 || !h.events.sending[1] {
		t.Fatalf("al verificarse en SES se anuncia apto: %v", h.events.sending)
	}

	h.now = h.now.Add(time.Hour)
	h.uc.SweepTenant(context.Background(), h.tenantID)
	if len(h.events.sending) != 2 || len(ses.calls) != calls {
		t.Fatalf("otra pasada igual no anuncia ni cambia nada: %v %v", h.events.sending, ses.calls[calls:])
	}
}

func TestUnaIdentidadQueYaExisteSeCorrigeEnVezDeFallar(t *testing.T) {
	h, ses := newSESHarness(t)
	d := h.create(t, "envio.com", domain.PurposeBoth)
	ses.identities["envio.com"] = &sesIdentity{obs: domain.SESIdentityObservation{
		DKIMOrigin: domain.SESDKIMOriginExternal, DKIMSelectors: []string{"viejo"},
		MailFromDomain: "otro.envio.com", BehaviorOnMXFailure: "REJECT_MESSAGE", ConfigurationSet: "a-mano",
		TenantTag: h.tenantID.String(),
	}}
	h.dns.publishZone(h.uc, d)
	h.verify(t, d.ID)

	want := []string{
		"dkim envio.com " + d.DKIMSelector,
		"mailfrom envio.com bounce.envio.com",
		"configset envio.com " + testSESConfigSet,
	}
	if strings.Join(ses.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("llamadas = %v; want %v", ses.calls, want)
	}
}

func TestLaRotacionLlegaASESCuandoSeVePublicadoElTXTNuevo(t *testing.T) {
	h, ses := newSESHarness(t)
	d := h.verifiedSending(t, "envio.com", domain.PurposeBoth)
	ses.verify("envio.com")
	old := d.DKIMSelector

	h.now = h.now.Add(24 * time.Hour)
	rot, err := h.uc.RotateDKIM(context.Background(), h.tenantID, d.ID, h.actor)
	if err != nil {
		t.Fatalf("RotateDKIM: %v", err)
	}
	h.verify(t, d.ID)
	if got := ses.identities["envio.com"].key.Selector; got != old {
		t.Fatalf("sin el TXT nuevo publicado SES sigue con la clave anterior: %s", got)
	}

	h.dns.publishZone(h.uc, rot.Domain)
	h.verify(t, d.ID)
	id := ses.identities["envio.com"]
	if id.key.Selector != rot.Domain.DKIMSelector || id.key.PrivateKeyPEM != h.currentPEM(t, rot.Domain) {
		t.Fatalf("con el TXT nuevo visto SES firma con la clave nueva: %s", id.key.Selector)
	}
}

func TestUnaRevocacionSacaDeSESLaClaveRevocadaAlMomento(t *testing.T) {
	h, ses := newSESHarness(t)
	d := h.verifiedSending(t, "envio.com", domain.PurposeSending)
	ses.verify("envio.com")

	res, err := h.uc.RevokeDKIM(context.Background(), h.tenantID, d.ID, RevokeDKIMRequest{CurrentSelector: d.DKIMSelector, Reason: "clave filtrada"})
	if err != nil {
		t.Fatalf("RevokeDKIM: %v", err)
	}
	if len(res.IntegrationErrors) != 0 {
		t.Fatalf("errores: %v", res.IntegrationErrors)
	}
	if got := ses.identities["envio.com"].key.Selector; got != res.Domain.DKIMSelector || got == d.DKIMSelector {
		t.Fatalf("SES debe pasar a la clave nueva sin esperar al TXT: %s", got)
	}
}

func TestUnaRevocacionQueNoLlegaASESSeReintentaAunqueElDominioNoEsteVerificado(t *testing.T) {
	h, ses := newSESHarness(t)
	d := h.verifiedSending(t, "envio.com", domain.PurposeSending)
	stored := h.stored(t, d.ID)
	stored.Status = domain.StatusFailed
	if err := h.repo.Update(context.Background(), stored); err != nil {
		t.Fatal(err)
	}

	ses.err = errDown
	res, err := h.uc.RevokeDKIM(context.Background(), h.tenantID, d.ID, RevokeDKIMRequest{CurrentSelector: d.DKIMSelector, Reason: "clave filtrada"})
	if err != nil {
		t.Fatalf("RevokeDKIM: %v", err)
	}
	if len(res.IntegrationErrors) == 0 || h.stored(t, d.ID).SES.LastError == "" {
		t.Fatalf("el fallo de SES se informa y se guarda: %v", res.IntegrationErrors)
	}

	ses.err = nil
	rep := h.uc.SweepTenant(context.Background(), h.tenantID)
	if rep.SESRetried != 1 {
		t.Fatalf("el barrido reintenta el dominio no verificado: %+v", rep)
	}
	if got := ses.identities["envio.com"].key.Selector; got != res.Domain.DKIMSelector {
		t.Fatalf("SES firma con la clave nueva: %s", got)
	}
	if h.stored(t, d.ID).SES.LastError != "" {
		t.Error("completado, el error se borra")
	}
}

func TestLaBajaDelDominioBorraSuIdentidadDeSES(t *testing.T) {
	h, ses := newSESHarness(t)
	d := h.verifiedSending(t, "envio.com", domain.PurposeSending)

	ses.err = errDown
	if err := h.uc.Delete(context.Background(), h.tenantID, d.ID); !errors.Is(err, domain.ErrIntegrationUnavailable) {
		t.Fatalf("sin respuesta de SES la fila se queda: %v", err)
	}
	h.stored(t, d.ID)

	ses.err = nil
	if err := h.uc.Delete(context.Background(), h.tenantID, d.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := ses.identities["envio.com"]; ok {
		t.Fatal("la identidad de SES sigue tras la baja")
	}
	if err := h.uc.Delete(context.Background(), h.tenantID, d.ID); !errors.Is(err, domain.ErrDomainNotFound) {
		t.Fatalf("borrado: %v", err)
	}
}

func TestDejarDeEnviarBorraLaIdentidadYAnunciaQueYaNoEsApto(t *testing.T) {
	h, ses := newSESHarness(t)
	d := h.verifiedSending(t, "ambos.com", domain.PurposeBoth)
	ses.verify("ambos.com")
	h.uc.SweepTenant(context.Background(), h.tenantID)
	if !h.stored(t, d.ID).SES.VerifiedForSending() {
		t.Fatal("precondicion: apto en SES")
	}

	corporate := string(domain.PurposeCorporate)
	got, err := h.uc.Update(context.Background(), h.tenantID, d.ID, UpdateRequest{Purpose: &corporate})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, ok := ses.identities["ambos.com"]; ok {
		t.Fatal("un dominio solo corporativo no conserva identidad en SES")
	}
	if got.SES != (domain.SESState{}) || h.stored(t, d.ID).SES != (domain.SESState{}) {
		t.Fatalf("el estado de SES se olvida: %+v", h.stored(t, d.ID).SES)
	}
	if last := h.events.sending[len(h.events.sending)-1]; last {
		t.Fatalf("se anuncia que ya no es apto: %v", h.events.sending)
	}
	if got.Status != domain.StatusVerified || !h.directory.calls[len(h.directory.calls)-1].active {
		t.Error("el camino corporativo no cambia")
	}
}

func TestUnDominioCorporativoNoTocaSES(t *testing.T) {
	h, ses := newSESHarness(t)
	d := h.verifiedSending(t, "acme.com", domain.PurposeCorporate)
	h.uc.SweepTenant(context.Background(), h.tenantID)
	if err := h.uc.Delete(context.Background(), h.tenantID, d.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(ses.calls) != 0 || len(ses.identities) != 0 || len(h.events.sending) != 0 {
		t.Fatalf("un dominio corporativo no llama a SES: %v", ses.calls)
	}
	for _, rec := range h.uc.ExpectedRecords(context.Background(), d) {
		if rec.Record == domain.RecordSESMailFromMX || rec.Record == domain.RecordSESMailFromSPF {
			t.Fatalf("un dominio corporativo no pide el MAIL FROM de SES: %+v", rec)
		}
	}
}

func TestSiSESFallaElDominioBothSeActivaEnLaCeldaYSeReintentaEnElBarrido(t *testing.T) {
	h, ses := newSESHarness(t)
	ses.err = errDown
	d := h.create(t, "ambos.com", domain.PurposeBoth)
	h.dns.publishZone(h.uc, d)
	res := h.verify(t, d.ID)

	if res.Domain.Status != domain.StatusVerified || len(h.directory.calls) != 1 || !h.directory.calls[0].active {
		t.Fatalf("el dominio se activa en la celda aunque SES no responda: %s %v", res.Domain.Status, h.directory.calls)
	}
	if h.security.lastPublished().domain != "ambos.com" {
		t.Fatal("sus claves llegan a los motores")
	}
	if len(res.IntegrationErrors) != 1 || !strings.Contains(res.IntegrationErrors[0], "Amazon SES") {
		t.Fatalf("el fallo de SES se informa: %v", res.IntegrationErrors)
	}
	if got := h.stored(t, d.ID).SES; got.LastError == "" || got.Checked() {
		t.Fatalf("se guarda el error sin inventar un estado: %+v", got)
	}
	if ready := h.events.verifiedReady[0]; ready == nil || *ready {
		t.Fatal("no llega a transactional como apto")
	}

	ses.err = nil
	h.now = h.now.Add(time.Hour)
	h.uc.SweepTenant(context.Background(), h.tenantID)
	if _, ok := ses.identities["ambos.com"]; !ok {
		t.Fatal("el barrido crea la identidad que fallo")
	}
	if got := h.stored(t, d.ID).SES; got.LastError != "" || got.IdentityStatus != domain.SESIdentityPending {
		t.Fatalf("estado tras el reintento: %+v", got)
	}
}

func TestLaIdentidadDeOtraEmpresaNoSeTocaNiSeBorra(t *testing.T) {
	h, ses := newSESHarness(t)
	other := uuid.New().String()
	ses.identities["envio.com"] = &sesIdentity{obs: domain.SESIdentityObservation{
		VerifiedForSending: true, DKIMOrigin: domain.SESDKIMOriginExternal, DKIMSelectors: []string{"suyo"}, TenantTag: other,
	}}
	d := h.create(t, "envio.com", domain.PurposeSending)
	h.dns.publishZone(h.uc, d)
	res := h.verify(t, d.ID)
	if len(ses.calls) != 0 || len(res.IntegrationErrors) != 1 {
		t.Fatalf("no se cambia la identidad de otra empresa: %v %v", ses.calls, res.IntegrationErrors)
	}
	if ready := h.events.verifiedReady[0]; ready == nil || *ready {
		t.Fatal("con la identidad de otra empresa no es apto")
	}
	if err := h.uc.Delete(context.Background(), h.tenantID, d.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := ses.identities["envio.com"]; !ok {
		t.Fatal("la baja no borra la identidad de otra empresa")
	}
}

func TestUnaIdentidadConEasyDKIMQueYaEnviaConservaSuDKIM(t *testing.T) {
	h, ses := newSESHarness(t)
	ses.identities["avisos.com"] = &sesIdentity{obs: domain.SESIdentityObservation{
		VerifiedForSending: true, DKIMOrigin: "AWS_SES", DKIMSelectors: []string{"t1", "t2", "t3"},
		DKIMStatus: domain.SESCheckSuccess, MailFromDomain: "bounce.avisos.com", BehaviorOnMXFailure: domain.SESBehaviorOnMXFailure,
		MailFromStatus: domain.SESCheckSuccess, ConfigurationSet: testSESConfigSet,
	}}
	d := h.verifiedSending(t, "avisos.com", domain.PurposeSending)
	if len(ses.calls) != 1 || ses.calls[0] != "tag avisos.com" {
		t.Fatalf("solo se etiqueta: pasarla a BYODKIM la dejaria pendiente y sin enviar: %v", ses.calls)
	}
	if !d.SES.VerifiedForSending() || h.events.sending[0] != true {
		t.Fatalf("queda apta: %+v %v", d.SES, h.events.sending)
	}
}

func TestSinLaIntegracionDeSESNadaCambia(t *testing.T) {
	h := newHarness(t)
	d := h.verifiedSending(t, "envio.com", domain.PurposeSending)
	if len(h.events.verifiedReady) != 1 || h.events.verifiedReady[0] != nil {
		t.Fatalf("sin integracion domains.domain.verified no dice nada de SES: %v", h.events.verifiedReady)
	}
	if d.SES != (domain.SESState{}) || len(h.events.sending) != 0 {
		t.Fatalf("estado %+v eventos %v", d.SES, h.events.sending)
	}
	for _, rec := range h.uc.ExpectedRecords(context.Background(), d) {
		if rec.Record == domain.RecordSESMailFromMX {
			t.Fatal("sin region de SES no se pide el MAIL FROM")
		}
	}
}

func TestLosRegistrosDelMailFromSePidenYNoBloqueanLaVerificacion(t *testing.T) {
	h, _ := newSESHarness(t)
	d := h.create(t, "envio.com", domain.PurposeSending)
	var mx, spf *domain.DNSRecord
	records := h.uc.ExpectedRecords(context.Background(), d)
	for i := range records {
		switch records[i].Record {
		case domain.RecordSESMailFromMX:
			mx = &records[i]
		case domain.RecordSESMailFromSPF:
			spf = &records[i]
		}
	}
	if mx == nil || mx.Host != "bounce.envio.com" || mx.Value != "feedback-smtp.us-east-1.amazonses.com priority 10" || mx.Required {
		t.Fatalf("MX del MAIL FROM: %+v", mx)
	}
	if spf == nil || spf.Host != "bounce.envio.com" || spf.Value != "v=spf1 include:amazonses.com ~all" || spf.Required {
		t.Fatalf("SPF del MAIL FROM: %+v", spf)
	}

	h.dns.publishZone(h.uc, d)
	delete(h.dns.mx, "bounce.envio.com")
	delete(h.dns.txt, "bounce.envio.com")
	res := h.verify(t, d.ID)
	if res.Domain.Status != domain.StatusVerified {
		t.Fatal("sin el MAIL FROM el dominio verifica igual: SES envia con el suyo")
	}
	for _, c := range res.Checks {
		if (c.Record == domain.RecordSESMailFromMX || c.Record == domain.RecordSESMailFromSPF) && c.OK {
			t.Errorf("%s sin publicar no puede estar bien", c.Record)
		}
	}

	h.dns.publishZone(h.uc, d)
	res = h.verify(t, d.ID)
	for _, c := range res.Checks {
		if (c.Record == domain.RecordSESMailFromMX || c.Record == domain.RecordSESMailFromSPF) && !c.OK {
			t.Errorf("%s publicado: %s", c.Record, c.Detail)
		}
	}
}

// La cuenta de SES es compartida: una identidad sin etiqueta con otro conjunto (la de otro proyecto de
// la cuenta) no se adopta, no se modifica y la baja del dominio no la borra.
func TestUnaIdentidadSinEtiquetaDeOtroProyectoNoSeTocaNiSeBorra(t *testing.T) {
	h, ses := newSESHarness(t)
	ses.identities["ajena.com"] = &sesIdentity{obs: domain.SESIdentityObservation{
		VerifiedForSending: true, DKIMOrigin: "AWS_SES", DKIMStatus: domain.SESCheckSuccess,
		ConfigurationSet: "my-first-configuration-set",
	}}
	d := h.create(t, "ajena.com", domain.PurposeSending)
	h.dns.publishZone(h.uc, d)
	res := h.verify(t, d.ID)
	if len(ses.calls) != 0 || len(res.IntegrationErrors) != 1 {
		t.Fatalf("no se toca: %v %v", ses.calls, res.IntegrationErrors)
	}
	if ready := h.events.verifiedReady[0]; ready == nil || *ready {
		t.Fatal("una identidad ajena no deja el dominio apto")
	}
	if err := h.uc.Delete(context.Background(), h.tenantID, d.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := ses.identities["ajena.com"]; !ok {
		t.Fatal("la baja no borra una identidad sin etiqueta")
	}
}

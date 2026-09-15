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

const testRevocationReason = "clave privada expuesta en un respaldo"

func (h *harness) revoke(t *testing.T, id uuid.UUID, selector string) *RevokeDKIMResult {
	t.Helper()
	res, err := h.uc.RevokeDKIM(context.Background(), h.tenantID, id, RevokeDKIMRequest{
		CurrentSelector: selector, Reason: testRevocationReason, ActorID: h.actor,
	})
	if err != nil {
		t.Fatalf("RevokeDKIM(%s): %v", selector, err)
	}
	return res
}

// verifiedCorporate da de alta acme.com con su zona publicada y lo verifica.
func (h *harness) verifiedCorporate(t *testing.T) *domain.Domain {
	t.Helper()
	d := h.create(t, "acme.com", domain.PurposeCorporate)
	h.dns.publishZone(h.uc, d)
	if res := h.verify(t, d.ID); res.Domain.Status != domain.StatusVerified {
		t.Fatalf("status %s", res.Domain.Status)
	}
	return h.stored(t, d.ID)
}

func selectorsFromHosts(records []domain.DNSRecord, name string) []string {
	out := make([]string, 0, len(records))
	for _, r := range records {
		out = append(out, strings.TrimSuffix(r.Host, "._domainkey."+name))
	}
	return out
}

func signing(p published) string {
	if len(p.keys) == 0 {
		return ""
	}
	return p.keys[len(p.keys)-1].Selector
}

// Revocar retira de la celda la clave actual y la que seguia en gracia en la misma entrega que
// deja firmando la nueva: mail-security recibe solo la nueva y retira las demas.
func TestRevocarRetiraAlMomentoTodasLasClavesYFirmaConLaNueva(t *testing.T) {
	h := newHarness(t)
	d := h.verifiedCorporate(t)
	rotated, err := h.uc.RotateDKIM(context.Background(), h.tenantID, d.ID, h.actor)
	if err != nil {
		t.Fatal(err)
	}
	k1, k2 := d.DKIMSelector, rotated.Domain.DKIMSelector
	if got := signing(h.security.lastPublished()); got != k1 {
		t.Fatalf("tras rotar sigue firmando la anterior: %s", got)
	}

	res := h.revoke(t, d.ID, k2)
	k3 := res.Domain.DKIMSelector
	if k3 == k1 || k3 == k2 {
		t.Fatalf("selector nuevo %s repite uno revocado", k3)
	}
	if pub := h.security.lastPublished(); len(pub.keys) != 1 || pub.keys[0].Selector != k3 {
		t.Errorf("a la celda solo va la clave nueva: %v", selectorsOf(pub))
	}
	if !res.EnginesRetired || len(res.IntegrationErrors) != 0 {
		t.Errorf("confirmado %v, errores %v", res.EnginesRetired, res.IntegrationErrors)
	}
	stored := h.stored(t, d.ID)
	if stored.HasPreviousDKIM() || stored.DKIMRevocationPending || stored.DKIMConfirmedAt != nil {
		t.Errorf("fila tras revocar: anterior %v, pendiente %v, confirmada %v", stored.HasPreviousDKIM(), stored.DKIMRevocationPending, stored.DKIMConfirmedAt)
	}
	if got := selectorsFromHosts(res.RemoveRecords, "acme.com"); len(got) != 2 || got[0] != k2 || got[1] != k1 {
		t.Errorf("TXT que el cliente debe retirar = %v", got)
	}
	if res.Record.Host != domain.DKIMHost(k3, "acme.com") || res.Record.Value != domain.DKIMValue(stored.DKIMPublicKey) {
		t.Errorf("TXT que publicar = %+v", res.Record)
	}
	for _, rec := range h.uc.ExpectedRecords(stored) {
		if rec.Record == domain.RecordDKIMPrevious {
			t.Error("tras revocar no queda ningun TXT anterior que conservar")
		}
	}
	r := res.Rotation
	if r.Kind != domain.RotationCompromised || r.Reason != testRevocationReason || r.ActorID != h.actor ||
		len(r.RevokedSelectors) != 2 || r.RevokedSelectors[0] != k2 || r.RevokedSelectors[1] != k1 {
		t.Errorf("revocacion = %+v", r)
	}
	if h.events.count("domains.domain.dkim_revoked") != 1 || h.events.rotations[len(h.events.rotations)-1].ID != r.ID {
		t.Errorf("eventos = %v", h.events.subjects)
	}
	rotations, _ := h.uc.DKIMRotations(context.Background(), h.tenantID, d.ID)
	if len(rotations) != 2 || rotations[0].Kind != domain.RotationCompromised || rotations[1].Kind != domain.RotationScheduled {
		t.Errorf("historial = %+v", rotations)
	}
}

// El reintento de la misma revocacion (mismo selector) devuelve la que ya se hizo: ni otra clave,
// ni otra entrada del historial, ni otro evento. Revocar la clave nueva es otra revocacion.
func TestRevocarDosVecesConElMismoSelectorNoGeneraOtraClave(t *testing.T) {
	h := newHarness(t)
	d := h.verifiedCorporate(t)
	first := h.revoke(t, d.ID, d.DKIMSelector)
	published := len(h.security.published)

	again := h.revoke(t, d.ID, d.DKIMSelector)
	if again.Domain.DKIMSelector != first.Domain.DKIMSelector || again.Rotation.ID != first.Rotation.ID || !again.EnginesRetired {
		t.Fatalf("reintento: %s/%s, confirmado %v", again.Domain.DKIMSelector, again.Rotation.ID, again.EnginesRetired)
	}
	if len(h.security.published) != published || h.events.count("domains.domain.dkim_revoked") != 1 {
		t.Errorf("sin nada pendiente no se repite nada: publicaciones %d, eventos %v", len(h.security.published)-published, h.events.subjects)
	}

	third := h.revoke(t, d.ID, first.Domain.DKIMSelector)
	if third.Domain.DKIMSelector == first.Domain.DKIMSelector || third.Domain.DKIMSelector == d.DKIMSelector {
		t.Errorf("revocar la nueva genera otra: %s", third.Domain.DKIMSelector)
	}
	if rotations, _ := h.uc.DKIMRotations(context.Background(), h.tenantID, d.ID); len(rotations) != 2 {
		t.Errorf("historial = %d", len(rotations))
	}
}

// Sin respuesta de la celda la revocacion queda guardada (la fila ya no tiene la clave revocada)
// y pendiente; ni la verificacion ni el barrido la dan por hecha, y el reintento de la misma
// peticion la completa en cuanto la celda responde.
func TestRevocarSinCeldaQuedaPendienteHastaQueLaCeldaConfirma(t *testing.T) {
	h := newHarness(t)
	d := h.verifiedCorporate(t)
	published := len(h.security.published)

	h.security.err = errCelda
	res := h.revoke(t, d.ID, d.DKIMSelector)
	if res.EnginesRetired || len(res.IntegrationErrors) != 1 {
		t.Fatalf("sin celda: confirmado %v, errores %v", res.EnginesRetired, res.IntegrationErrors)
	}
	stored := h.stored(t, d.ID)
	if !stored.DKIMRevocationPending || stored.DKIMSelector == d.DKIMSelector || stored.DKIMSelector != res.Domain.DKIMSelector {
		t.Fatalf("guardada y pendiente: %v %s", stored.DKIMRevocationPending, stored.DKIMSelector)
	}
	h.uc.SweepTenant(context.Background(), h.tenantID)
	if !h.stored(t, d.ID).DKIMRevocationPending || len(h.security.published) != published {
		t.Fatal("con la celda caida el barrido no la da por hecha")
	}

	h.security.err = nil
	retry := h.revoke(t, d.ID, d.DKIMSelector)
	if !retry.EnginesRetired || retry.Domain.DKIMSelector != res.Domain.DKIMSelector {
		t.Fatalf("reintento: confirmado %v, selector %s", retry.EnginesRetired, retry.Domain.DKIMSelector)
	}
	if pub := h.security.lastPublished(); len(pub.keys) != 1 || pub.keys[0].Selector != res.Domain.DKIMSelector {
		t.Errorf("entrega = %v", selectorsOf(pub))
	}
	if h.stored(t, d.ID).DKIMRevocationPending || h.events.count("domains.domain.dkim_revoked") != 1 {
		t.Error("confirmada una vez y con un solo evento")
	}
}

func TestElBarridoCompletaUnaRevocacionPendiente(t *testing.T) {
	h := newHarness(t)
	d := h.verifiedCorporate(t)
	h.security.err = errCelda
	res := h.revoke(t, d.ID, d.DKIMSelector)

	h.security.err = nil
	rep := h.uc.SweepTenant(context.Background(), h.tenantID)
	if rep.Revoked != 1 || h.stored(t, d.ID).DKIMRevocationPending {
		t.Fatalf("barrido: %+v", rep)
	}
	if pub := h.security.lastPublished(); signing(pub) != res.Domain.DKIMSelector {
		t.Errorf("firma %s", signing(pub))
	}
}

func TestRevocarExigeElSelectorActualYUnMotivo(t *testing.T) {
	h := newHarness(t)
	d := h.verifiedCorporate(t)
	rotated, err := h.uc.RotateDKIM(context.Background(), h.tenantID, d.ID, h.actor)
	if err != nil {
		t.Fatal(err)
	}
	for nombre, c := range map[string]struct {
		selector, reason string
		want             error
	}{
		"selector que nunca tuvo":          {"cfm199901", testRevocationReason, domain.ErrDKIMSelectorNotCurrent},
		"la anterior en gracia, no actual": {d.DKIMSelector, testRevocationReason, domain.ErrDKIMSelectorNotCurrent},
		"motivo en blanco":                 {rotated.Domain.DKIMSelector, "   ", domain.ErrInvalidRevocationReason},
		"motivo con control":               {rotated.Domain.DKIMSelector, "filtrada\x00", domain.ErrInvalidRevocationReason},
		"motivo demasiado largo":           {rotated.Domain.DKIMSelector, strings.Repeat("a", domain.MaxRevocationReasonLength+1), domain.ErrInvalidRevocationReason},
	} {
		_, err := h.uc.RevokeDKIM(context.Background(), h.tenantID, d.ID, RevokeDKIMRequest{CurrentSelector: c.selector, Reason: c.reason, ActorID: h.actor})
		if !errors.Is(err, c.want) {
			t.Errorf("%s: %v", nombre, err)
		}
	}
	stored := h.stored(t, d.ID)
	if stored.DKIMSelector != rotated.Domain.DKIMSelector || stored.DKIMPreviousSelector != d.DKIMSelector || h.events.count("domains.domain.dkim_revoked") != 0 {
		t.Errorf("nada cambia: %s/%s, eventos %v", stored.DKIMSelector, stored.DKIMPreviousSelector, h.events.subjects)
	}
}

// Una verificacion que leyo el dominio antes de la revocacion no devuelve la clave revocada a los
// motores ni a la fila.
func TestUnaVerificacionConLasClavesDeAntesNoDevuelveLaRevocada(t *testing.T) {
	h := newHarness(t)
	stale := h.verifiedCorporate(t)
	res := h.revoke(t, stale.ID, stale.DKIMSelector)
	published := len(h.security.published)

	if failures := h.uc.syncVerified(context.Background(), stale, true); len(failures) != 0 {
		t.Fatalf("fallos: %v", failures)
	}
	if len(h.security.published) != published {
		t.Errorf("publico con las claves de antes: %v", selectorsOf(h.security.lastPublished()))
	}
	stale.Status = domain.StatusVerified
	if err := h.repo.Update(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	if got := h.stored(t, stale.ID); got.DKIMSelector != res.Domain.DKIMSelector {
		t.Errorf("Update devolvio la clave revocada: %s", got.DKIMSelector)
	}
}

// Tras revocar, el TXT de la clave nueva aun no esta: verificar a mano no apaga el correo del
// dominio. Una clave que ya se vio publicada y desaparece si lo marca failed.
func TestTrasRevocarVerificarAManoNoApagaElDominio(t *testing.T) {
	h := newHarness(t)
	d := h.verifiedCorporate(t)
	res := h.revoke(t, d.ID, d.DKIMSelector)
	calls := len(h.directory.calls)

	vr := h.verify(t, d.ID)
	if vr.Outcome != domain.OutcomeFailed || vr.Domain.Status != domain.StatusVerified {
		t.Fatalf("outcome %s, status %s", vr.Outcome, vr.Domain.Status)
	}
	for _, c := range h.directory.calls[calls:] {
		if !c.active {
			t.Fatal("se desactivo el dominio en el directorio")
		}
	}

	h.dns.publishZone(h.uc, res.Domain)
	if vr := h.verify(t, d.ID); vr.Outcome != domain.OutcomeVerified || h.stored(t, d.ID).DKIMConfirmedAt == nil {
		t.Fatalf("publicado el TXT nuevo: %s", vr.Outcome)
	}
	h.dns.txt[domain.DKIMHost(res.Domain.DKIMSelector, "acme.com")] = nil
	if vr := h.verify(t, d.ID); vr.Domain.Status != domain.StatusFailed {
		t.Errorf("una clave confirmada que desaparece: %s", vr.Domain.Status)
	}
}

// La gracia se cuenta desde la ultima vez que la clave anterior pudo firmar: si el cliente tarda
// en publicar el TXT nuevo, la anterior firmo hasta entonces y lo que firmo sigue en cola.
func TestLaGraciaSeCuentaDesdeLaUltimaFirmaDeLaClaveAnterior(t *testing.T) {
	h := newHarness(t)
	d := h.verifiedCorporate(t)
	t0 := h.now
	rotated, err := h.uc.RotateDKIM(context.Background(), h.tenantID, d.ID, h.actor)
	if err != nil {
		t.Fatal(err)
	}
	if !rotated.GraceUntil.Equal(t0.Add(h.uc.rotationGrace)) {
		t.Errorf("grace_until = %s", rotated.GraceUntil)
	}

	h.now = t0.Add(100 * time.Hour)
	h.verify(t, d.ID)
	stored := h.stored(t, d.ID)
	if stored.DKIMPreviousSignedAt == nil || !stored.DKIMPreviousSignedAt.Equal(h.now) {
		t.Fatalf("sin el TXT nuevo sigue firmando la anterior: %v", stored.DKIMPreviousSignedAt)
	}
	if until := h.uc.PreviousDKIMRetireAfter(stored); until == nil || !until.Equal(h.now.Add(h.uc.rotationGrace)) {
		t.Errorf("hasta = %v", until)
	}

	h.dns.publishZone(h.uc, rotated.Domain)
	h.now = t0.Add(101 * time.Hour)
	if rep := h.uc.SweepTenant(context.Background(), h.tenantID); rep.Retired != 0 {
		t.Fatal("retiraria la anterior con correo firmado hace una hora aun en cola")
	}
	if got := signing(h.security.lastPublished()); got != rotated.Domain.DKIMSelector {
		t.Fatalf("con el TXT nuevo publicado firma la nueva: %s", got)
	}
	switched := h.now
	h.now = switched.Add(h.uc.rotationGrace + time.Minute)
	if rep := h.uc.SweepTenant(context.Background(), h.tenantID); rep.Retired != 1 || h.stored(t, d.ID).HasPreviousDKIM() {
		t.Errorf("vencida la gracia desde el cambio de firma: %+v", rep)
	}
}

// Ningun selector se repite, tampoco el de una clave revocada ni varias claves en el mismo segundo.
func TestLosSelectoresNoSeRepiten(t *testing.T) {
	h := newHarness(t)
	d := h.verifiedCorporate(t)
	seen := map[string]bool{d.DKIMSelector: true}
	current := d.DKIMSelector
	for i := 0; i < 4; i++ {
		res := h.revoke(t, d.ID, current)
		current = res.Domain.DKIMSelector
		if seen[current] {
			t.Fatalf("selector repetido %s", current)
		}
		seen[current] = true
	}
	rotated, err := h.uc.RotateDKIM(context.Background(), h.tenantID, d.ID, h.actor)
	if err != nil {
		t.Fatal(err)
	}
	if seen[rotated.Domain.DKIMSelector] {
		t.Errorf("la rotacion repite %s", rotated.Domain.DKIMSelector)
	}
}

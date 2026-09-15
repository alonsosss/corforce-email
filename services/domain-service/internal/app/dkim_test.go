package app

import (
	"context"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
)

// Un dominio solo de envio no se sirve en la celda: sus claves no van a los motores al verificar,
// ni al rotar, ni al retirar la anterior, ni al revocar, y nada de eso depende de que la celda
// responda.
func TestUnDominioSoloDeEnvioNoLlevaClavesALaCelda(t *testing.T) {
	h := newHarness(t)
	d := h.create(t, "envio.com", domain.PurposeSending)
	h.dns.publishZone(h.uc, d)
	h.security.err = errCelda

	if res := h.verify(t, d.ID); res.Domain.Status != domain.StatusVerified || len(res.IntegrationErrors) != 0 {
		t.Fatalf("verificado sin tocar la celda: %s %v", res.Domain.Status, res.IntegrationErrors)
	}
	rotated, err := h.uc.RotateDKIM(context.Background(), h.tenantID, d.ID, h.actor)
	if err != nil {
		t.Fatalf("rotar no depende de la celda: %v", err)
	}

	h.dns.publishZone(h.uc, rotated.Domain)
	h.verify(t, d.ID)
	h.now = h.now.Add(73 * time.Hour)
	rep := h.uc.SweepTenant(context.Background(), h.tenantID)
	if stored := h.stored(t, d.ID); rep.Retired != 1 || stored.HasPreviousDKIM() {
		t.Fatalf("la anterior se retira solo de la fila: %+v, anterior %v", rep, stored.HasPreviousDKIM())
	}

	res, err := h.uc.RevokeDKIM(context.Background(), h.tenantID, d.ID, RevokeDKIMRequest{
		CurrentSelector: rotated.Domain.DKIMSelector, Reason: "clave expuesta", ActorID: h.actor,
	})
	if err != nil {
		t.Fatalf("revocar no depende de la celda: %v", err)
	}
	if !res.EnginesRetired || len(res.IntegrationErrors) != 0 || h.stored(t, d.ID).DKIMRevocationPending {
		t.Errorf("sin claves en la celda no queda nada pendiente: %v %v", res.EnginesRetired, res.IntegrationErrors)
	}
}

// Un dominio que cae con la desactivacion sin confirmar sigue activo en la celda y sus claves
// siguen en los motores: revocar las retira todas alli, sin entregarle la nueva, porque la celda
// ya no debe firmar por el.
func TestRevocarConLaDesactivacionPendienteRetiraTodasLasClavesDeLaCelda(t *testing.T) {
	h := newHarness(t)
	d := h.create(t, "acme.com", domain.PurposeCorporate)
	h.dns.publishZone(h.uc, d)
	h.verify(t, d.ID)
	rotated, err := h.uc.RotateDKIM(context.Background(), h.tenantID, d.ID, h.actor)
	if err != nil {
		t.Fatal(err)
	}

	h.dns.txt["acme.com"] = []string{"v=spf1 include:_spf.google.com ~all"}
	h.directory.err = errCelda
	if res := h.verify(t, d.ID); res.Domain.Status != domain.StatusFailed || !res.Domain.DirectoryDeactivationPending {
		t.Fatalf("cae con la desactivacion pendiente: %s %v", res.Domain.Status, res.Domain.DirectoryDeactivationPending)
	}
	published := len(h.security.published)

	res, err := h.uc.RevokeDKIM(context.Background(), h.tenantID, d.ID, RevokeDKIMRequest{
		CurrentSelector: rotated.Domain.DKIMSelector, Reason: "clave expuesta", ActorID: h.actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.security.deleted) != 1 || h.security.deleted[0] != "acme.com" || !res.EnginesRetired {
		t.Errorf("retiradas en los motores: %v, confirmado %v", h.security.deleted, res.EnginesRetired)
	}
	if len(h.security.published) != published {
		t.Error("un dominio caido no recibe claves nuevas en los motores")
	}
	if got := selectorsFromHosts(res.RemoveRecords, "acme.com"); len(got) != 2 || got[0] != rotated.Domain.DKIMSelector || got[1] != d.DKIMSelector {
		t.Errorf("TXT que retirar = %v", got)
	}
}

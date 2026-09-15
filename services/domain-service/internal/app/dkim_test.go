package app

import (
	"context"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
)

// Un dominio solo de envio no se sirve en la celda: sus claves no van a los motores al verificar,
// ni al rotar, ni al retirar la anterior, y nada de eso depende de que la celda responda.
func TestUnDominioSoloDeEnvioNoLlevaClavesALaCelda(t *testing.T) {
	h := newHarness(t)
	d := h.create(t, "envio.com", domain.PurposeSending)
	h.dns.publishZone(h.uc, d)
	h.security.err = errCelda

	if res := h.verify(t, d.ID); res.Domain.Status != domain.StatusVerified || len(res.IntegrationErrors) != 0 {
		t.Fatalf("verificado sin tocar la celda: %s %v", res.Domain.Status, res.IntegrationErrors)
	}
	if _, err := h.uc.RotateDKIM(context.Background(), h.tenantID, d.ID); err != nil {
		t.Fatalf("primera rotacion: %v", err)
	}
	second, err := h.uc.RotateDKIM(context.Background(), h.tenantID, d.ID)
	if err != nil {
		t.Fatalf("la clave en gracia no esta en los motores: rotar no depende de la celda: %v", err)
	}

	h.dns.publishZone(h.uc, second.Domain)
	h.now = h.now.Add(73 * time.Hour)
	rep := h.uc.SweepTenant(context.Background(), h.tenantID)
	if stored := h.stored(t, d.ID); rep.Retired != 1 || stored.HasPreviousDKIM() {
		t.Fatalf("la anterior se retira solo de la fila: %+v, anterior %v", rep, stored.HasPreviousDKIM())
	}
}

// Un dominio que cae con la desactivacion sin confirmar sigue activo en la celda y sus claves
// siguen en los motores: rotar retira alli la clave en gracia antes de olvidarla, y no le
// entrega claves nuevas.
func TestRotarConLaDesactivacionPendienteRetiraLaClaveEnGracia(t *testing.T) {
	h := newHarness(t)
	d := h.create(t, "acme.com", domain.PurposeCorporate)
	h.dns.publishZone(h.uc, d)
	h.verify(t, d.ID)
	first, err := h.uc.RotateDKIM(context.Background(), h.tenantID, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	inGrace := first.Domain.DKIMPreviousSelector

	h.dns.txt["acme.com"] = []string{"v=spf1 include:_spf.google.com ~all"}
	h.directory.err = errCelda
	if res := h.verify(t, d.ID); res.Domain.Status != domain.StatusFailed || !res.Domain.DirectoryDeactivationPending {
		t.Fatalf("cae con la desactivacion pendiente: %s %v", res.Domain.Status, res.Domain.DirectoryDeactivationPending)
	}
	published := len(h.security.published)

	if _, err := h.uc.RotateDKIM(context.Background(), h.tenantID, d.ID); err != nil {
		t.Fatal(err)
	}
	if len(h.security.retired) != 1 || h.security.retired[0] != "acme.com/"+inGrace {
		t.Errorf("retiradas en los motores: %v", h.security.retired)
	}
	if len(h.security.published) != published {
		t.Error("un dominio caido no recibe claves nuevas en los motores")
	}
}

package app

import (
	"context"
	"fmt"
	"testing"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
)

// Una empresa a la que aun le faltan migraciones del servicio no es un fallo del barrido: se salta esa pasada
// (con un aviso) y se retoma en la siguiente, sin tocar sus dominios a medias.
func TestElBarridoSaltaUnaEmpresaSinMigrarYLaRetomaDespues(t *testing.T) {
	h := newHarness(t)
	d := h.create(t, "envio.com", domain.PurposeSending)
	h.dns.publishZone(h.uc, d)
	h.verify(t, d.ID)
	before := h.stored(t, d.ID).LastCheckedAt

	h.repo.listErr = fmt.Errorf("%w: column \"dkim_previous_signed_at\" does not exist", domain.ErrTenantSchemaNotReady)
	rep := h.uc.SweepTenant(context.Background(), h.tenantID)
	if rep != (SweepReport{}) {
		t.Fatalf("una empresa sin migrar no se barre: %+v", rep)
	}
	if got := h.stored(t, d.ID).LastCheckedAt; (got == nil) != (before == nil) || (got != nil && !got.Equal(*before)) {
		t.Fatalf("no debe reverificar nada en esa pasada: %v -> %v", before, got)
	}

	h.repo.listErr = nil
	h.now = h.now.Add(1)
	if rep := h.uc.SweepTenant(context.Background(), h.tenantID); rep.Rechecked != 1 {
		t.Fatalf("migrada, la siguiente pasada la barre: %+v", rep)
	}
}

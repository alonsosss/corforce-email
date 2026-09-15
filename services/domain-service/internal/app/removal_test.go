package app

import (
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
)

// Una empresa con la baja en curso no activa su dominio aunque siga verificado: organization no le
// deja reclamarlo, o la celda ya la dio de baja. No se llama a la celda si el reclamo se rechaza, y
// sus claves DKIM no van a los motores de una celda que no sirve el dominio.
func TestUnaEmpresaEnBajaNoActivaSuDominio(t *testing.T) {
	for nombre, preparar := range map[string]func(h *harness){
		"organization rechaza el reclamo":      func(h *harness) { h.index.claimErr = domain.ErrTenantBeingRemoved },
		"la celda ya la dio de baja":           func(h *harness) { h.directory.err = domain.ErrTenantBeingRemoved },
		"el barrido tampoco lo vuelve a subir": func(h *harness) { h.index.claimErr = domain.ErrTenantBeingRemoved },
	} {
		t.Run(nombre, func(t *testing.T) {
			h := newHarness(t)
			preparar(h)
			d := h.create(t, "acme.com", domain.PurposeCorporate)
			h.dns.publishZone(h.uc, d)

			res := h.verify(t, d.ID)
			if res.Domain.Status != domain.StatusVerified || len(res.IntegrationErrors) != 1 ||
				!strings.Contains(res.IntegrationErrors[0], domain.ErrTenantBeingRemoved.Error()) {
				t.Fatalf("status %s, errores %v", res.Domain.Status, res.IntegrationErrors)
			}
			if len(h.directory.calls) != 0 || len(h.security.published) != 0 {
				t.Fatalf("activaciones %v, publicaciones DKIM %d", h.directory.calls, len(h.security.published))
			}
		})
	}
}

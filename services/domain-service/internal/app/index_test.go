package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/google/uuid"
)

// errIndice hace de organization sin respuesta.
var errIndice = errors.New("organization: no responde")

// Un dominio verificado con uso corporativo se reclama en el indice global antes de activarse en
// el directorio de la celda, y el barrido lo confirma en cada pasada: es lo que hace converger el
// indice, tambien para los dominios activados antes de que existiera.
func TestVerificarReclamaElDominioAntesDeActivarlo(t *testing.T) {
	h := newHarness(t)
	var seq []string
	h.directory.seq, h.index.seq = &seq, &seq
	d := h.create(t, "acme.com", domain.PurposeCorporate)
	h.dns.publishZone(h.uc, d)

	if res := h.verify(t, d.ID); res.Domain.Status != domain.StatusVerified || len(res.IntegrationErrors) != 0 {
		t.Fatalf("status %s, errores %v", res.Domain.Status, res.IntegrationErrors)
	}
	if got := strings.Join(seq, ","); got != "reclamar acme.com,activar acme.com true" {
		t.Fatalf("orden: %s", got)
	}
	if h.index.owners["acme.com"] != h.tenantID {
		t.Fatalf("indice: %v", h.index.owners)
	}
	seq = nil
	h.uc.SweepTenant(context.Background(), h.tenantID)
	if got := strings.Join(seq, ","); got != "reclamar acme.com,activar acme.com true" {
		t.Fatalf("el barrido confirma el reclamo y la activacion: %s", got)
	}
}

// Un dominio solo de envio no entra en el directorio de la celda ni en el indice.
func TestUnDominioDeSoloEnvioNoSeReclama(t *testing.T) {
	h := newHarness(t)
	d := h.create(t, "envios.com", domain.PurposeSending)
	h.dns.publishZone(h.uc, d)
	h.verify(t, d.ID)
	if len(h.index.owners) != 0 || len(h.directory.calls) != 0 {
		t.Fatalf("indice %v, activaciones %v", h.index.owners, h.directory.calls)
	}
}

// Un dominio activo en otra empresa no se activa en esta celda: la verificacion lo dice y el
// estado sigue saliendo del DNS; sus claves DKIM no van a los motores de una celda que no lo
// sirve. Cuando la otra empresa lo suelta, el barrido lo reclama, lo activa y publica sus claves.
func TestUnDominioActivoEnOtraEmpresaNoSeActiva(t *testing.T) {
	h := newHarness(t)
	h.index.owners["acme.com"] = uuid.New()
	d := h.create(t, "acme.com", domain.PurposeCorporate)
	h.dns.publishZone(h.uc, d)

	res := h.verify(t, d.ID)
	if res.Domain.Status != domain.StatusVerified || len(res.IntegrationErrors) != 1 ||
		!strings.Contains(res.IntegrationErrors[0], domain.ErrDomainClaimedElsewhere.Error()) {
		t.Fatalf("status %s, errores %v", res.Domain.Status, res.IntegrationErrors)
	}
	if len(h.directory.calls) != 0 || len(h.security.published) != 0 {
		t.Fatalf("activaciones %v, publicaciones DKIM %d", h.directory.calls, len(h.security.published))
	}

	delete(h.index.owners, "acme.com")
	h.uc.SweepTenant(context.Background(), h.tenantID)
	if h.index.owners["acme.com"] != h.tenantID || len(h.directory.calls) != 1 || !h.directory.calls[0].active ||
		len(h.security.published) != 1 {
		t.Fatalf("liberado: indice %v, activaciones %v, publicaciones DKIM %d", h.index.owners, h.directory.calls, len(h.security.published))
	}
}

// Sin respuesta de organization no se activa nada; el barrido lo repite hasta que responde.
func TestSinIndiceNoSeActivaYElBarridoReintenta(t *testing.T) {
	h := newHarness(t)
	h.index.claimErr = errIndice
	d := h.create(t, "acme.com", domain.PurposeCorporate)
	h.dns.publishZone(h.uc, d)

	if res := h.verify(t, d.ID); res.Domain.Status != domain.StatusVerified || len(res.IntegrationErrors) != 1 || len(h.directory.calls) != 0 {
		t.Fatalf("status %s, errores %v, activaciones %v", res.Domain.Status, res.IntegrationErrors, h.directory.calls)
	}
	h.uc.SweepTenant(context.Background(), h.tenantID)
	if len(h.directory.calls) != 0 {
		t.Fatalf("sin organization el barrido tampoco activa: %v", h.directory.calls)
	}
	h.index.claimErr = nil
	h.uc.SweepTenant(context.Background(), h.tenantID)
	if h.index.owners["acme.com"] != h.tenantID || len(h.directory.calls) != 1 {
		t.Fatalf("con organization de vuelta: indice %v, activaciones %v", h.index.owners, h.directory.calls)
	}
}

// Un dominio que deja de recibir se desactiva y despues se suelta del indice. Si organization no
// responde, la desactivacion pendiente queda y el barrido repite las dos llamadas hasta que ambas
// se confirman.
func TestUnDominioCaidoSeSueltaDelIndiceDespuesDeDesactivarse(t *testing.T) {
	h := newHarness(t)
	d := h.create(t, "acme.com", domain.PurposeCorporate)
	h.dns.publishZone(h.uc, d)
	h.verify(t, d.ID)

	var seq []string
	h.directory.seq, h.index.seq = &seq, &seq
	h.index.releaseErr = errIndice
	h.dns.txt[d.Domain] = nil
	res := h.verify(t, d.ID)
	if res.Domain.Status != domain.StatusFailed || len(res.IntegrationErrors) != 1 || !h.stored(t, d.ID).DirectoryDeactivationPending {
		t.Fatalf("status %s, errores %v, pendiente %v", res.Domain.Status, res.IntegrationErrors, h.stored(t, d.ID).DirectoryDeactivationPending)
	}
	if got := strings.Join(seq, ","); got != "activar acme.com false,soltar acme.com" || h.index.owners["acme.com"] != h.tenantID {
		t.Fatalf("orden %s, indice %v", got, h.index.owners)
	}

	h.index.releaseErr = nil
	if rep := h.uc.SweepTenant(context.Background(), h.tenantID); rep.Deactivated != 1 || h.stored(t, d.ID).DirectoryDeactivationPending {
		t.Fatalf("barrido: %+v", rep)
	}
	if len(h.index.owners) != 0 {
		t.Fatalf("indice tras el barrido: %v", h.index.owners)
	}
}

// Quitar el uso corporativo y borrar el dominio lo sueltan del indice; si organization no
// responde, no se guarda nada y el dominio sigue reclamado.
func TestQuitarElUsoCorporativoYBorrarSueltanElDominio(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	d := h.create(t, "acme.com", domain.PurposeCorporate)
	h.dns.publishZone(h.uc, d)
	h.verify(t, d.ID)
	sending := string(domain.PurposeSending)

	h.index.releaseErr = errIndice
	if _, err := h.uc.Update(ctx, h.tenantID, d.ID, UpdateRequest{Purpose: &sending}); !errors.Is(err, domain.ErrIntegrationUnavailable) {
		t.Fatalf("sin organization: %v", err)
	}
	if got := h.stored(t, d.ID); got.Purpose != domain.PurposeCorporate {
		t.Fatalf("guardado sin soltar el dominio: %s", got.Purpose)
	}
	h.index.releaseErr = nil
	if _, err := h.uc.Update(ctx, h.tenantID, d.ID, UpdateRequest{Purpose: &sending}); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.index.owners["acme.com"]; ok {
		t.Fatalf("solo envio y sigue reclamado: %v", h.index.owners)
	}

	b := h.create(t, "beta.com", domain.PurposeBoth)
	h.dns.publishZone(h.uc, b)
	h.verify(t, b.ID)
	h.index.releaseErr = errIndice
	if err := h.uc.Delete(ctx, h.tenantID, b.ID); !errors.Is(err, domain.ErrIntegrationUnavailable) {
		t.Fatalf("borrar sin organization: %v", err)
	}
	h.stored(t, b.ID)
	if len(h.security.deleted) != 0 {
		t.Fatalf("las claves se retiran despues de soltar el dominio: %v", h.security.deleted)
	}
	h.index.releaseErr = nil
	if err := h.uc.Delete(ctx, h.tenantID, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.index.owners["beta.com"]; ok {
		t.Fatalf("borrado y sigue reclamado: %v", h.index.owners)
	}
}

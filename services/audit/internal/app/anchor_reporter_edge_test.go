package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/google/uuid"
)

func TestCollectLlevaLasDosCadenasCuandoLasDosTienenAncla(t *testing.T) {
	rig := newReporterRig("ops@example.org")
	tenant := uuid.New()
	rig.anchors.found[domain.ChainAuditLogs] = domain.AnchorFindings{Last: fullAnchor(tenant, domain.ChainAuditLogs, 40, hex40)}
	rig.anchors.found[domain.ChainSecurityEvents] = domain.AnchorFindings{Last: fullAnchor(tenant, domain.ChainSecurityEvents, 3, hex03)}
	got, err := rig.r.Collect(context.Background(), domain.TenantRef{ID: tenant})
	if err != nil || len(got.Anchors) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	// Una empresa sin ninguna ancla (cadenas vacias) tambien se recoge: sale sin lineas, y su
	// ausencia de anclas queda dicha por omision en el informe.
	rig.anchors.found = map[domain.ChainName]domain.AnchorFindings{}
	empty, err := rig.r.Collect(context.Background(), domain.TenantRef{ID: uuid.New()})
	if err != nil || len(empty.Anchors) != 0 {
		t.Fatalf("%+v %v", empty, err)
	}
}

func TestElInformeLlevaLaHoraDelRelojDelServicio(t *testing.T) {
	rig := newReporterRig("ops@example.org")
	rig.clock = time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC)
	if err := rig.r.SendScheduled(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	mail := rig.sender.last()
	if mail.subject != "[Core Force Mail] Anclas de auditoria 2026-12-31" {
		t.Fatalf("asunto: %q", mail.subject)
	}
	parsed, err := domain.ParseAnchorReport(mail.text)
	if err != nil || !parsed.GeneratedAt.Equal(rig.clock) {
		t.Fatalf("%+v %v", parsed, err)
	}
	if _, successes := rig.metrics.snapshot(); successes != 1 || !rig.metrics.success[0].Equal(rig.clock) {
		t.Fatalf("el exito se anota con el reloj del servicio: %v", rig.metrics.success)
	}
}

func TestElAvisoDeRoturaSaleAunqueElRegistroNoDeElSlug(t *testing.T) {
	rig := newReporterRig("ops@example.org")
	rig.dir.err = errors.New("registro caido")
	tenant := uuid.New()
	rig.anchors.found[domain.ChainAuditLogs] = domain.AnchorFindings{Last: fullAnchor(tenant, domain.ChainAuditLogs, 40, hex40)}
	rig.r.ChainBroken(context.Background(), tenant, domain.ChainAuditLogs, domain.ReasonAnchorMismatch)
	rig.r.inflight.Wait()
	if rig.sender.count() != 1 {
		t.Fatalf("envios: %d", rig.sender.count())
	}
	parsed, err := domain.ParseAnchorReport(rig.sender.last().text)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Anchors[0].Tenant.Slug != "" || parsed.Anchors[0].Tenant.ID != tenant {
		t.Fatalf("%+v", parsed.Anchors[0])
	}
	// Una empresa que el registro no lista (dada de baja entre medias) tampoco frena el aviso.
	rig.dir.err = nil
	rig.dir.tenants = []domain.TenantRef{{ID: uuid.New(), Slug: "otra"}}
	other := uuid.New()
	rig.r.ChainBroken(context.Background(), other, domain.ChainAuditLogs, domain.ReasonAnchorMismatch)
	rig.r.inflight.Wait()
	if rig.sender.count() != 2 {
		t.Fatalf("envios: %d", rig.sender.count())
	}
}

func TestElAvisoDeRoturaVaACadaDireccionYCuentaSusResultados(t *testing.T) {
	rig := newReporterRig("ops@example.org", "down@example.org")
	tenant := uuid.New()
	rig.r.ChainBroken(context.Background(), tenant, domain.ChainSecurityEvents, domain.ReasonChainBroken)
	rig.r.inflight.Wait()
	results, successes := rig.metrics.snapshot()
	if strings.Join(results, ",") != "sent,failed" || successes != 1 {
		t.Fatalf("metricas: %v %d", results, successes)
	}
}

func TestUnEnvioFallidoNoReabreElFrenoDeLaHora(t *testing.T) {
	rig := newReporterRig("down@example.org")
	tenant := uuid.New()
	rig.r.ChainBroken(context.Background(), tenant, domain.ChainAuditLogs, domain.ReasonChainBroken)
	rig.r.inflight.Wait()
	rig.r.ChainBroken(context.Background(), tenant, domain.ChainAuditLogs, domain.ReasonChainBroken)
	rig.r.inflight.Wait()
	if rig.sender.count() != 1 {
		t.Fatalf("un fallo no convierte cada verificacion en un intento; envios: %d", rig.sender.count())
	}
	rig.clock = rig.clock.Add(chainBreakNoticeEvery)
	rig.r.ChainBroken(context.Background(), tenant, domain.ChainAuditLogs, domain.ReasonChainBroken)
	rig.r.inflight.Wait()
	if rig.sender.count() != 2 {
		t.Fatalf("pasada la hora se reintenta; envios: %d", rig.sender.count())
	}
}

func TestElCorreoDeRoturaNoLlevaMasQueLaEmpresaAfectada(t *testing.T) {
	rig := newReporterRig("ops@example.org")
	tenant := uuid.New()
	rig.dir.tenants = []domain.TenantRef{{ID: tenant, Slug: "acme"}, {ID: uuid.New(), Slug: "otra"}}
	rig.anchors.found[domain.ChainAuditLogs] = domain.AnchorFindings{Last: fullAnchor(tenant, domain.ChainAuditLogs, 40, hex40)}
	rig.r.ChainBroken(context.Background(), tenant, domain.ChainAuditLogs, domain.ReasonHeadBehindAnchor)
	rig.r.inflight.Wait()
	parsed, err := domain.ParseAnchorReport(rig.sender.last().text)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range parsed.Anchors {
		if a.Tenant.ID != tenant {
			t.Fatalf("el aviso lleva otra empresa: %+v", a)
		}
	}
	if strings.Contains(rig.sender.last().text, "otra") {
		t.Fatal("el aviso nombra a otra empresa")
	}
}

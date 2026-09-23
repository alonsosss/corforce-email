package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
)

// duro es un limite de plan que no admite excederse; blando se factura y no restringe.
func duro(n int64) ports.PlanAllowance   { return ports.PlanAllowance{Limit: n, HardLimit: true} }
func blando(n int64) ports.PlanAllowance { return ports.PlanAllowance{Limit: n} }

func conPlan(h *harness, limits map[string]ports.PlanAllowance) { h.plan.limits = limits }

// deBaja: la empresa tiene plan pero su suscripcion no esta vigente.
func deBaja() ports.PlanAllowance { return ports.PlanAllowance{SubscriptionInactive: true} }

func crear(h *harness, tenant uuid.UUID, local string, quota *int64) error {
	_, err := h.uc.CreateMailbox(context.Background(), tenant, CreateMailboxRequest{
		LocalPart: local, Domain: "acme.com", Password: testPassword, QuotaBytes: quota,
	})
	return err
}

func TestElPlanLimitaElNumeroDeBuzones(t *testing.T) {
	h, tenant := newHarness(), uuid.New()
	h.addDomain(tenant, "acme.com", domain.DomainLimits{})
	conPlan(h, map[string]ports.PlanAllowance{planResourceMailboxes: duro(2)})

	if err := crear(h, tenant, "ana", nil); err != nil {
		t.Fatalf("primer buzon dentro del plan: %v", err)
	}
	if err := crear(h, tenant, "bea", nil); err != nil {
		t.Fatalf("segundo buzon, justo en el limite: %v", err)
	}
	if err := crear(h, tenant, "carlos", nil); !errors.Is(err, domain.ErrPlanMailboxesExceeded) {
		t.Fatalf("el tercero supera el plan: err = %v", err)
	}
}

// El limite del plan se cuenta por EMPRESA: dos dominios de la misma empresa suman.
func TestElLimiteDeBuzonesEsDeLaEmpresaNoDelDominio(t *testing.T) {
	h, tenant := newHarness(), uuid.New()
	h.addDomain(tenant, "acme.com", domain.DomainLimits{})
	h.addDomain(tenant, "otro.com", domain.DomainLimits{})
	conPlan(h, map[string]ports.PlanAllowance{planResourceMailboxes: duro(1)})

	if err := crear(h, tenant, "ana", nil); err != nil {
		t.Fatalf("primer buzon: %v", err)
	}
	_, err := h.uc.CreateMailbox(context.Background(), tenant, CreateMailboxRequest{
		LocalPart: "bea", Domain: "otro.com", Password: testPassword,
	})
	if !errors.Is(err, domain.ErrPlanMailboxesExceeded) {
		t.Fatalf("otro dominio de la misma empresa no da buzones de mas: err = %v", err)
	}
}

func TestElPlanLimitaElEspacioAsignado(t *testing.T) {
	h, tenant := newHarness(), uuid.New()
	h.addDomain(tenant, "acme.com", domain.DomainLimits{})
	conPlan(h, map[string]ports.PlanAllowance{planResourceStorage: duro(100)})

	q := func(n int64) *int64 { return &n }
	if err := crear(h, tenant, "ana", q(60)); err != nil {
		t.Fatalf("60 de 100: %v", err)
	}
	if err := crear(h, tenant, "bea", q(40)); err != nil {
		t.Fatalf("40 mas, justo en el limite: %v", err)
	}
	if err := crear(h, tenant, "carlos", q(1)); !errors.Is(err, domain.ErrPlanStorageExceeded) {
		t.Fatalf("un byte mas supera el plan: err = %v", err)
	}
}

// Un buzon sin limite no cabe en un plan con espacio acotado: seria saltarselo entero.
func TestUnBuzonIlimitadoNoCabeEnUnPlanConEspacioAcotado(t *testing.T) {
	h, tenant := newHarness(), uuid.New()
	h.addDomain(tenant, "acme.com", domain.DomainLimits{})
	conPlan(h, map[string]ports.PlanAllowance{planResourceStorage: duro(100)})

	if err := crear(h, tenant, "ana", new(int64)); !errors.Is(err, domain.ErrPlanStorageExceeded) {
		t.Fatalf("cuota 0 es ilimitada y no cabe: err = %v", err)
	}
}

func TestSubirLaCuotaDeUnBuzonRespetaElPlan(t *testing.T) {
	h, tenant := newHarness(), uuid.New()
	h.addDomain(tenant, "acme.com", domain.DomainLimits{})
	conPlan(h, map[string]ports.PlanAllowance{planResourceStorage: duro(100)})
	m := h.addMailbox(tenant, "ana@acme.com", 50)

	subir := func(n int64) error {
		_, err := h.uc.UpdateMailbox(context.Background(), tenant, m.ID, UpdateMailboxRequest{QuotaBytes: &n})
		return err
	}
	if err := subir(100); err != nil {
		t.Fatalf("subir hasta el tope del plan: %v", err)
	}
	if err := subir(101); !errors.Is(err, domain.ErrPlanStorageExceeded) {
		t.Fatalf("pasarse del tope del plan: err = %v", err)
	}
}

// Un billing caido no puede dejar a una empresa sin poder crear buzones: se sigue sin el
// limite del plan y el del dominio se aplica igual.
func TestSinBillingElAltaSigueYSeAplicaElLimiteDelDominio(t *testing.T) {
	h, tenant := newHarness(), uuid.New()
	h.addDomain(tenant, "acme.com", domain.DomainLimits{MaxMailboxes: 1})
	h.plan.err = errors.New("billing caido")

	if err := crear(h, tenant, "ana", nil); err != nil {
		t.Fatalf("sin billing el alta sigue: %v", err)
	}
	if h.plan.calls == 0 {
		t.Fatal("se pregunto a billing")
	}
	if err := crear(h, tenant, "bea", nil); !errors.Is(err, domain.ErrMaxMailboxesReached) {
		t.Fatalf("el limite del dominio se aplica igual: err = %v", err)
	}
}

// Una empresa sin plan no queda restringida por el plan.
func TestSinPlanNoSeRestringe(t *testing.T) {
	h, tenant := newHarness(), uuid.New()
	h.addDomain(tenant, "acme.com", domain.DomainLimits{})
	conPlan(h, map[string]ports.PlanAllowance{})

	q := int64(1 << 40)
	if err := crear(h, tenant, "ana", &q); err != nil {
		t.Fatalf("sin plan no se restringe: %v", err)
	}
}

// Un limite blando lo factura billing como exceso: no bloquea el alta.
func TestUnLimiteBlandoNoBloquea(t *testing.T) {
	h, tenant := newHarness(), uuid.New()
	h.addDomain(tenant, "acme.com", domain.DomainLimits{})
	conPlan(h, map[string]ports.PlanAllowance{planResourceMailboxes: blando(1)})

	if err := crear(h, tenant, "ana", nil); err != nil {
		t.Fatalf("primer buzon: %v", err)
	}
	if err := crear(h, tenant, "bea", nil); err != nil {
		t.Fatalf("un limite blando no bloquea: %v", err)
	}
}

// Una empresa dada de baja conserva lo que tiene pero no crece: ni un buzon mas ni un byte
// mas. Es el mismo criterio con el que billing le deniega el envio (ADR 0010).
func TestUnaEmpresaDeBajaNoCreceNiEnBuzonesNiEnEspacio(t *testing.T) {
	h, tenant := newHarness(), uuid.New()
	h.addDomain(tenant, "acme.com", domain.DomainLimits{})
	conPlan(h, map[string]ports.PlanAllowance{planResourceMailboxes: deBaja(), planResourceStorage: deBaja()})

	if err := crear(h, tenant, "ana", nil); !errors.Is(err, domain.ErrSubscriptionInactive) {
		t.Fatalf("no deberia crear un buzon estando de baja: err = %v", err)
	}
	m := h.addMailbox(tenant, "bea@acme.com", 50)
	q := int64(60)
	if _, err := h.uc.UpdateMailbox(context.Background(), tenant, m.ID, UpdateMailboxRequest{QuotaBytes: &q}); !errors.Is(err, domain.ErrSubscriptionInactive) {
		t.Fatalf("no deberia subir la cuota estando de baja: err = %v", err)
	}
}

// La baja gana sobre un plan sin limite: si no, una empresa cancelada con plan ilimitado
// creceria sin tope, que es justo lo contrario de darse de baja.
func TestLaBajaGanaSobreUnPlanSinLimite(t *testing.T) {
	h, tenant := newHarness(), uuid.New()
	h.addDomain(tenant, "acme.com", domain.DomainLimits{})
	conPlan(h, map[string]ports.PlanAllowance{
		planResourceMailboxes: {Limit: -1, HardLimit: true, SubscriptionInactive: true},
		planResourceStorage:   {Limit: -1, HardLimit: true, SubscriptionInactive: true},
	})
	if err := crear(h, tenant, "ana", nil); !errors.Is(err, domain.ErrSubscriptionInactive) {
		t.Fatalf("un plan sin limite no libra de la baja: err = %v", err)
	}
}

// Un buzon ilimitado pedido por una empresa de baja se rechaza POR LA BAJA, no por el espacio:
// el motivo que ve quien lo pide tiene que ser el de verdad.
func TestElMotivoDeLaBajaNoSeConfundeConElDelEspacio(t *testing.T) {
	h, tenant := newHarness(), uuid.New()
	h.addDomain(tenant, "acme.com", domain.DomainLimits{})
	conPlan(h, map[string]ports.PlanAllowance{planResourceMailboxes: deBaja(), planResourceStorage: deBaja()})
	if err := crear(h, tenant, "ana", new(int64)); !errors.Is(err, domain.ErrSubscriptionInactive) {
		t.Fatalf("el motivo debe ser la baja: err = %v", err)
	}
}

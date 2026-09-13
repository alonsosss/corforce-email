package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/google/uuid"
)

var ctx = context.Background()

func TestAuthorizeSuspendidoNoConsultaNadaMas(t *testing.T) {
	h := newHarness()
	tenant, by := uuid.New(), uuid.New()
	h.states.set(domain.Record{TenantID: tenant, Class: domain.ClassMarketing, State: domain.StateSuspended, Manual: true, ChangedBy: &by})

	dec, err := h.uc.Authorize(ctx, AuthorizeInput{TenantID: tenant, Class: domain.ClassMarketing, Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Allowed || dec.Reason != domain.ReasonSuspended || dec.State != domain.StateSuspended {
		t.Fatalf("decision: %+v", dec)
	}
	if h.billing.calls != 0 || h.rate.calls != 0 {
		t.Fatalf("un suspendido no consulta billing ni reserva tasa: billing=%d rate=%d", h.billing.calls, h.rate.calls)
	}
	if dec.Hourly.Limit != 500 || dec.Hourly.Used != nil || dec.Monthly != nil {
		t.Fatalf("los pasos no alcanzados van sin uso: %+v", dec)
	}
	if h.metrics.authorize["marketing/suspended"] != 1 {
		t.Fatalf("metrica: %+v", h.metrics.authorize)
	}
}

func TestAuthorizeRestringido(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.states.set(domain.Record{TenantID: tenant, Class: domain.ClassTransactional, State: domain.StateRestricted})

	dec, err := h.uc.Authorize(ctx, AuthorizeInput{TenantID: tenant, Class: domain.ClassTransactional, Count: 3})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Allowed || dec.Reason != domain.ReasonReputationRestricted || h.billing.calls != 0 || h.rate.calls != 0 {
		t.Fatalf("decision: %+v billing=%d rate=%d", dec, h.billing.calls, h.rate.calls)
	}
}

func TestAuthorizeWarningNoRestringe(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.states.set(domain.Record{TenantID: tenant, Class: domain.ClassMarketing, State: domain.StateWarning})
	dec, err := h.uc.Authorize(ctx, AuthorizeInput{TenantID: tenant, Class: domain.ClassMarketing, Count: 1})
	if err != nil || !dec.Allowed || dec.State != domain.StateWarning {
		t.Fatalf("warning avisa pero no frena: %+v %v", dec, err)
	}
}

func TestAuthorizeBillingDeniega(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.billing.ent = domain.Entitlement{Allowed: false, HardLimit: true, Limit: ptr(1000), Used: 1000, Remaining: ptr(0), Reason: "monthly_quota_exceeded"}

	dec, err := h.uc.Authorize(ctx, AuthorizeInput{TenantID: tenant, Class: domain.ClassMarketing, Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Allowed || dec.Reason != "monthly_quota_exceeded" {
		t.Fatalf("billing debe denegar con su motivo: %+v", dec)
	}
	if dec.Monthly == nil || *dec.Monthly.Limit != 1000 || dec.Monthly.Used != 1000 || *dec.Monthly.Remaining != 0 {
		t.Fatalf("monthly: %+v", dec.Monthly)
	}
	if h.rate.calls != 0 {
		t.Fatal("una denegacion del plan no debe reservar cupo de tasa")
	}
	if h.metrics.authorize["marketing/"+ResultPlanDenied] != 1 {
		t.Fatalf("el motivo de billing no va a la etiqueta: %+v", h.metrics.authorize)
	}
}

func TestAuthorizeBillingCaidoSeAutoriza(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.billing.err = errBoom

	dec, err := h.uc.Authorize(ctx, AuthorizeInput{TenantID: tenant, Class: domain.ClassTransactional, Count: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Allowed || dec.Monthly != nil {
		t.Fatalf("sin billing se autoriza y monthly va a null: %+v", dec)
	}
	if h.metrics.degraded[DependencyBilling] != 1 {
		t.Fatalf("degradacion: %+v", h.metrics.degraded)
	}
	if dec.Hourly.Used == nil || *dec.Hourly.Used != 2 || *dec.Daily.Used != 2 {
		t.Fatalf("la tasa se sigue reservando: %+v", dec)
	}
}

func TestAuthorizeTasaAgotadaEnLaHora(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.rate.used[key(tenant, domain.ClassTransactional)] = [2]int64{100, 100}

	dec, err := h.uc.Authorize(ctx, AuthorizeInput{TenantID: tenant, Class: domain.ClassTransactional, Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Allowed || dec.Reason != domain.ReasonRateLimited {
		t.Fatalf("decision: %+v", dec)
	}
	if dec.RetryAfterSeconds == nil || *dec.RetryAfterSeconds != 1800 {
		t.Fatalf("de 10:30 a 11:00 son 1800 s: %v", dec.RetryAfterSeconds)
	}
	if *dec.Hourly.Used != 100 || dec.Hourly.Limit != 100 {
		t.Fatalf("uso de la hora: %+v", dec.Hourly)
	}
	if h.metrics.authorize["transactional/rate_limited"] != 1 {
		t.Fatalf("metrica: %+v", h.metrics.authorize)
	}
}

func TestAuthorizeTasaAgotadaEnElDia(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.rate.used[key(tenant, domain.ClassTransactional)] = [2]int64{0, 1000}

	dec, err := h.uc.Authorize(ctx, AuthorizeInput{TenantID: tenant, Class: domain.ClassTransactional, Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Allowed || dec.RetryAfterSeconds == nil || *dec.RetryAfterSeconds != int64((13*time.Hour+30*time.Minute)/time.Second) {
		t.Fatalf("de 10:30 a medianoche: %+v", dec)
	}
}

func TestAuthorizeCantidadQueNuncaCabeNoSugiereReintento(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	dec, err := h.uc.Authorize(ctx, AuthorizeInput{TenantID: tenant, Class: domain.ClassTransactional, Count: 101})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Allowed || dec.Reason != domain.ReasonRateLimited || dec.RetryAfterSeconds != nil {
		t.Fatalf("101 no cabe en una hora de 100: %+v", dec)
	}
}

func TestAuthorizeLimitesPropiosDeLaEmpresa(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.limits.overrides[key(tenant, domain.ClassMarketing)] = domain.LimitOverride{TenantID: tenant, Class: domain.ClassMarketing, Hourly: ptr(5)}

	dec, err := h.uc.Authorize(ctx, AuthorizeInput{TenantID: tenant, Class: domain.ClassMarketing, Count: 5})
	if err != nil || !dec.Allowed || dec.Hourly.Limit != 5 || dec.Daily.Limit != 5000 {
		t.Fatalf("limites efectivos: %+v %v", dec, err)
	}
	dec, err = h.uc.Authorize(ctx, AuthorizeInput{TenantID: tenant, Class: domain.ClassMarketing, Count: 1})
	if err != nil || dec.Allowed || dec.RetryAfterSeconds == nil {
		t.Fatalf("sexto mensaje de la hora: %+v %v", dec, err)
	}
}

func TestAuthorizeRedisCaidoSeAutorizaSinReserva(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.rate.err = errBoom

	dec, err := h.uc.Authorize(ctx, AuthorizeInput{TenantID: tenant, Class: domain.ClassMarketing, Count: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Allowed || dec.Hourly.Used != nil || dec.Daily.Used != nil || dec.Hourly.Limit != 500 {
		t.Fatalf("fail-open en la tasa: %+v", dec)
	}
	if h.metrics.degraded[DependencyRedis] != 1 || h.metrics.authorize["marketing/allowed"] != 1 {
		t.Fatalf("metricas: %+v %+v", h.metrics.degraded, h.metrics.authorize)
	}
}

func TestAuthorizeReutilizaBillingYDescuentaLoAutorizado(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.billing.ent = domain.Entitlement{Allowed: true, HardLimit: true, Limit: ptr(100), Used: 90, Remaining: ptr(10)}

	dec, err := h.uc.Authorize(ctx, AuthorizeInput{TenantID: tenant, Class: domain.ClassMarketing, Count: 6})
	if err != nil || !dec.Allowed {
		t.Fatalf("primer lote: %+v %v", dec, err)
	}
	if dec.Monthly == nil || dec.Monthly.Used != 96 || *dec.Monthly.Remaining != 4 {
		t.Fatalf("monthly tras autorizar: %+v", dec.Monthly)
	}
	dec, err = h.uc.Authorize(ctx, AuthorizeInput{TenantID: tenant, Class: domain.ClassMarketing, Count: 5})
	if err != nil || dec.Allowed || dec.Reason != domain.ReasonPlanLimitExceeded {
		t.Fatalf("el remanente local ya no alcanza: %+v %v", dec, err)
	}
	if h.billing.calls != 1 {
		t.Fatalf("dentro de la cache no se vuelve a consultar: %d", h.billing.calls)
	}
	// Otra clase es otra entrada de la cache.
	if _, err := h.uc.Authorize(ctx, AuthorizeInput{TenantID: tenant, Class: domain.ClassTransactional, Count: 1}); err != nil {
		t.Fatal(err)
	}
	if h.billing.calls != 2 {
		t.Fatalf("la cache es por empresa y clase: %d", h.billing.calls)
	}
	h.now = h.now.Add(EntitlementTTL + time.Second)
	if _, err := h.uc.Authorize(ctx, AuthorizeInput{TenantID: tenant, Class: domain.ClassMarketing, Count: 1}); err != nil {
		t.Fatal(err)
	}
	if h.billing.calls != 3 {
		t.Fatalf("caducada la cache se vuelve a consultar: %d", h.billing.calls)
	}
}

func TestAuthorizeEntradaInvalida(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	if _, err := h.uc.Authorize(ctx, AuthorizeInput{TenantID: tenant, Class: domain.ClassMarketing, Count: 0}); !errors.Is(err, domain.ErrInvalidCount) {
		t.Fatalf("count 0: %v", err)
	}
	if _, err := h.uc.Authorize(ctx, AuthorizeInput{TenantID: tenant, Class: domain.ClassMarketing, Count: domain.MaxAuthorizeCount + 1}); !errors.Is(err, domain.ErrInvalidCount) {
		t.Fatalf("count excesivo: %v", err)
	}
	if _, err := h.uc.Authorize(ctx, AuthorizeInput{TenantID: tenant, Class: "newsletter", Count: 1}); !errors.Is(err, domain.ErrInvalidClass) {
		t.Fatalf("clase: %v", err)
	}
}

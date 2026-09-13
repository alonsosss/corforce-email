package app

import (
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/alonsosss/corforce-email/services/reputation/internal/ports"
	"github.com/google/uuid"
)

func TestSuspensionManualNoLaPisaLaEvaluacion(t *testing.T) {
	h := newHarness()
	tenant, admin := uuid.New(), uuid.New()
	h.tenants.active = []uuid.UUID{tenant}

	rec, err := h.uc.Suspend(ctx, tenant, domain.ClassMarketing, "  campana con listas compradas ", admin)
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != domain.StateSuspended || !rec.Manual || rec.Reason != "campana con listas compradas" || *rec.ChangedBy != admin {
		t.Fatalf("suspension: %+v", rec)
	}
	if len(h.events.changes) != 1 || !h.events.changes[0].Manual || *h.events.changes[0].ChangedBy != admin {
		t.Fatalf("evento de la suspension: %+v", h.events.changes)
	}

	// Tasas limpias y volumen suficiente: la evaluacion diria ok, pero no toca lo manual.
	if err := h.stats.AddDaily(ctx, tenant, domain.ClassMarketing, h.today(), domain.Counts{Sent: 5000}); err != nil {
		t.Fatal(err)
	}
	got, changed, err := h.uc.Reevaluate(ctx, tenant, domain.ClassMarketing)
	if err != nil || changed || got.State != domain.StateSuspended {
		t.Fatalf("la evaluacion no levanta una suspension: %+v changed=%v err=%v", got, changed, err)
	}
	dec, err := h.uc.Authorize(ctx, AuthorizeInput{TenantID: tenant, Class: domain.ClassMarketing, Count: 1})
	if err != nil || dec.Allowed || dec.Reason != domain.ReasonSuspended {
		t.Fatalf("suspendida no envia: %+v %v", dec, err)
	}

	rel, err := h.uc.Release(ctx, tenant, domain.ClassMarketing, admin)
	if err != nil {
		t.Fatal(err)
	}
	if rel.State != domain.StateOK || rel.Manual || rel.Reason != domain.ReasonWithinThresholds {
		t.Fatalf("liberacion: %+v", rel)
	}
	if len(h.states.history) != 2 || !h.states.history[1].Manual || h.states.history[1].From != domain.StateSuspended {
		t.Fatalf("historial: %+v", h.states.history)
	}
}

func TestReleaseDejaLaClaseEnLoQueDigaLaVentana(t *testing.T) {
	h := newHarness()
	tenant, admin := uuid.New(), uuid.New()
	h.tenants.active = []uuid.UUID{tenant}
	if _, err := h.uc.Suspend(ctx, tenant, domain.ClassMarketing, "revision", admin); err != nil {
		t.Fatal(err)
	}
	if err := h.stats.AddDaily(ctx, tenant, domain.ClassMarketing, h.today(), domain.Counts{Sent: 1000, Bounced: 50}); err != nil {
		t.Fatal(err)
	}
	rel, err := h.uc.Release(ctx, tenant, domain.ClassMarketing, admin)
	if err != nil || rel.State != domain.StateRestricted || rel.Manual {
		t.Fatalf("liberar con 5 %% de rebotes deja la clase restringida: %+v %v", rel, err)
	}
}

func TestOperacionesDePlataformaValidan(t *testing.T) {
	h := newHarness()
	tenant, admin := uuid.New(), uuid.New()
	h.tenants.active = []uuid.UUID{tenant}

	if _, err := h.uc.Suspend(ctx, uuid.New(), domain.ClassMarketing, "x", admin); !errors.Is(err, domain.ErrTenantNotFound) {
		t.Fatalf("empresa desconocida: %v", err)
	}
	if _, err := h.uc.Suspend(ctx, tenant, domain.ClassMarketing, "   ", admin); !errors.Is(err, domain.ErrInvalidReason) {
		t.Fatalf("motivo vacio: %v", err)
	}
	if _, err := h.uc.Release(ctx, tenant, "newsletter", admin); !errors.Is(err, domain.ErrInvalidClass) {
		t.Fatalf("clase: %v", err)
	}
	if len(h.states.records) != 0 {
		t.Fatal("una operacion rechazada no toca la base")
	}
}

func TestSetLimits(t *testing.T) {
	h := newHarness()
	tenant, admin := uuid.New(), uuid.New()
	h.tenants.active = []uuid.UUID{tenant}

	res, err := h.uc.SetLimits(ctx, SetLimitsInput{TenantID: tenant, Class: domain.ClassMarketing, Hourly: ptr(50), UpdatedBy: admin})
	if err != nil {
		t.Fatal(err)
	}
	if res.Effective != (domain.Limits{Hourly: 50, Daily: 5000}) || res.Override == nil || res.Override.UpdatedBy != admin {
		t.Fatalf("limites: %+v", res)
	}
	if _, err := h.uc.SetLimits(ctx, SetLimitsInput{TenantID: tenant, Class: domain.ClassMarketing, Hourly: ptr(6000), UpdatedBy: admin}); !errors.Is(err, domain.ErrInvalidLimit) {
		t.Fatalf("hourly por encima del daily por defecto: %v", err)
	}
	if _, err := h.uc.SetLimits(ctx, SetLimitsInput{TenantID: uuid.New(), Class: domain.ClassMarketing, Hourly: ptr(5), UpdatedBy: admin}); !errors.Is(err, domain.ErrTenantNotFound) {
		t.Fatalf("empresa desconocida: %v", err)
	}
	res, err = h.uc.SetLimits(ctx, SetLimitsInput{TenantID: tenant, Class: domain.ClassMarketing, UpdatedBy: admin})
	if err != nil || res.Override != nil || res.Effective != (domain.Limits{Hourly: 500, Daily: 5000}) {
		t.Fatalf("sin valores se vuelve a los de la configuracion: %+v %v", res, err)
	}
	if len(h.limits.overrides) != 0 {
		t.Fatal("los limites propios deben retirarse")
	}
}

func TestListTenantsFiltraPorEstado(t *testing.T) {
	h := newHarness()
	a, b := uuid.New(), uuid.New()
	h.tenants.active = []uuid.UUID{a, b}
	h.states.set(domain.Record{TenantID: b, Class: domain.ClassMarketing, State: domain.StateRestricted})
	if err := h.stats.AddDaily(ctx, b, domain.ClassMarketing, h.today(), domain.Counts{Sent: 1000, Bounced: 45}); err != nil {
		t.Fatal(err)
	}

	all, err := h.uc.ListTenants(ctx, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("todas: %d %v", len(all), err)
	}
	restricted, err := h.uc.ListTenants(ctx, domain.StateRestricted)
	if err != nil || len(restricted) != 1 || restricted[0].TenantID != b {
		t.Fatalf("restringidas: %+v %v", restricted, err)
	}
	mk := restricted[0].Classes[1]
	if mk.Record.Class != domain.ClassMarketing || !mk.BounceRate.Equal(dec("0.045")) || mk.Counts.Sent != 1000 {
		t.Fatalf("resumen de la clase: %+v", mk)
	}
	if _, err := h.uc.ListTenants(ctx, "blocked"); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("estado desconocido: %v", err)
	}
}

func TestBarridoRecuperaSolaUnaClaseRestringida(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.tenants.active = []uuid.UUID{tenant}
	// Restringida por la evaluacion hace mas de una ventana; sin envios desde entonces.
	h.states.set(domain.Record{TenantID: tenant, Class: domain.ClassMarketing, State: domain.StateRestricted, Reason: domain.ReasonBounceBlock})
	if err := h.stats.AddDaily(ctx, tenant, domain.ClassMarketing, h.today().AddDate(0, 0, -10), domain.Counts{Sent: 1000, Bounced: 80}); err != nil {
		t.Fatal(err)
	}

	rep, err := h.uc.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Tenants != 1 || rep.Changed != 1 || rep.Failed != 0 {
		t.Fatalf("informe: %+v", rep)
	}
	if rec := h.states.get(tenant, domain.ClassMarketing); rec.State != domain.StateOK || rec.Reason != domain.ReasonInsufficientVolume {
		t.Fatalf("la ventana se desplazo y la clase vuelve a ok: %+v", rec)
	}
}

func TestStatusYHistorial(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.tenants.active = []uuid.UUID{tenant}
	if err := h.stats.AddDaily(ctx, tenant, domain.ClassMarketing, h.today(), domain.Counts{Sent: 1000, Bounced: 20}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.uc.Reevaluate(ctx, tenant, domain.ClassMarketing); err != nil {
		t.Fatal(err)
	}
	h.rate.used[key(tenant, domain.ClassMarketing)] = [2]int64{7, 70}
	h.limits.overrides[key(tenant, domain.ClassMarketing)] = domain.LimitOverride{TenantID: tenant, Class: domain.ClassMarketing, Daily: ptr(900)}

	st, err := h.uc.Status(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if st.WindowDays != 7 || st.MinVolume != 200 || len(st.Classes) != 2 {
		t.Fatalf("status: %+v", st)
	}
	tx, mk := st.Classes[0], st.Classes[1]
	if tx.Record.State != domain.StateOK || tx.Record.Reason != domain.ReasonInitial || !tx.Thresholds.BounceBlock.Equal(dec("0.08")) {
		t.Fatalf("transaccional sin historia: %+v", tx)
	}
	if mk.Record.State != domain.StateWarning || !mk.BounceRate.Equal(dec("0.02")) || mk.Daily.Limit != 900 || *mk.Hourly.Used != 7 || *mk.Daily.Used != 70 {
		t.Fatalf("marketing: %+v", mk)
	}

	h.rate.err = errBoom
	st, err = h.uc.Status(ctx, tenant)
	if err != nil || st.Classes[1].Hourly.Used != nil || st.Classes[1].Hourly.Limit != 500 {
		t.Fatalf("sin Redis el estado se informa sin uso: %+v %v", st.Classes[1], err)
	}

	items, total, err := h.uc.History(ctx, tenant, ports.HistoryFilter{Class: domain.ClassMarketing, Page: 1, PerPage: 20})
	if err != nil || total != 1 || items[0].To != domain.StateWarning {
		t.Fatalf("historial: %+v %d %v", items, total, err)
	}
	if _, _, err := h.uc.History(ctx, tenant, ports.HistoryFilter{Class: "newsletter"}); !errors.Is(err, domain.ErrInvalidClass) {
		t.Fatalf("clase del filtro: %v", err)
	}
}

package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

func setMode(t *testing.T, h *harness, tenant uuid.UUID, name, mode string) *domain.MTASTSState {
	t.Helper()
	state, err := h.uc.SetMTASTSMode(context.Background(), tenant, name, mode)
	if err != nil {
		t.Fatalf("pasar %s a %s: %v", name, mode, err)
	}
	return state
}

func TestUnDominioSinPoliticaEstaEnNoneYSoloOfreceTesting(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.addDomain(tenant, "acme.test", domain.DomainLimits{})
	state, err := h.uc.GetMTASTS(context.Background(), tenant, "ACME.test")
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != domain.MTASTSNone || state.PolicyID != "" || state.UpdatedAt != nil || !state.DomainActive {
		t.Fatalf("estado inicial: %+v", state)
	}
	if len(state.AllowedModes) != 1 || state.AllowedModes[0] != domain.MTASTSTesting {
		t.Fatalf("desde none solo se ofrece testing: %v", state.AllowedModes)
	}
}

func TestActivarLaPoliticaLaDejaEnTestingConVersion(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.addDomain(tenant, "acme.test", domain.DomainLimits{})
	state := setMode(t, h, tenant, "acme.test", "testing")
	if state.Mode != domain.MTASTSTesting || state.MaxAge != domain.MTASTSTestingMaxAge || len(state.PolicyID) != 32 || state.UpdatedAt == nil {
		t.Fatalf("activada: %+v", state)
	}
	stored := h.mtaSTS.items["acme.test"]
	if stored == nil || stored.TenantID != tenant || stored.PolicyID != state.PolicyID {
		t.Fatalf("fila: %+v", stored)
	}
}

func TestCadaCambioRenuevaLaVersionYRepetirElModoNo(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.addDomain(tenant, "acme.test", domain.DomainLimits{})
	first := setMode(t, h, tenant, "acme.test", "testing")
	again := setMode(t, h, tenant, "acme.test", "testing")
	if again.PolicyID != first.PolicyID || h.mtaSTS.upserts != 1 {
		t.Fatalf("repetir el modo no cambia nada: %s -> %s, escrituras %d", first.PolicyID, again.PolicyID, h.mtaSTS.upserts)
	}
	enforced := setMode(t, h, tenant, "acme.test", "enforce")
	if enforced.PolicyID == first.PolicyID || enforced.MaxAge != domain.MTASTSEnforceMaxAge {
		t.Fatalf("enforce renueva la version y su max_age: %+v", enforced)
	}
	back := setMode(t, h, tenant, "acme.test", "testing")
	if back.PolicyID == enforced.PolicyID || back.PolicyID == first.PolicyID || back.MaxAge != domain.MTASTSTestingMaxAge {
		t.Fatalf("volver a testing tambien renueva: %+v", back)
	}
}

func TestSoloSeEntraYSaleDeEnforcePorTesting(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.addDomain(tenant, "acme.test", domain.DomainLimits{})
	if _, err := h.uc.SetMTASTSMode(context.Background(), tenant, "acme.test", "enforce"); !errors.Is(err, domain.ErrMTASTSTransition) {
		t.Fatalf("de none a enforce: %v", err)
	}
	if h.mx.calls != 0 || len(h.mtaSTS.items) != 0 {
		t.Fatalf("un cambio que no procede no consulta el DNS ni escribe: consultas %d, filas %d", h.mx.calls, len(h.mtaSTS.items))
	}
	setMode(t, h, tenant, "acme.test", "testing")
	setMode(t, h, tenant, "acme.test", "enforce")
	if _, err := h.uc.SetMTASTSMode(context.Background(), tenant, "acme.test", "none"); !errors.Is(err, domain.ErrMTASTSTransition) {
		t.Fatalf("de enforce a none: %v", err)
	}
	if got := h.mtaSTS.items["acme.test"].Mode; got != domain.MTASTSEnforce {
		t.Fatalf("el modo no debe cambiar tras el rechazo: %s", got)
	}
}

func TestEnforceExigeElDominioActivo(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	d := h.addDomain(tenant, "acme.test", domain.DomainLimits{})
	setMode(t, h, tenant, "acme.test", "testing")
	d.Active = false
	if _, err := h.uc.SetMTASTSMode(context.Background(), tenant, "acme.test", "enforce"); !errors.Is(err, domain.ErrMTASTSDomainNotActive) {
		t.Fatalf("dominio inactivo: %v", err)
	}
	if h.mx.calls != 0 {
		t.Fatalf("un dominio inactivo no consulta el DNS: %d", h.mx.calls)
	}
	state, err := h.uc.GetMTASTS(context.Background(), tenant, "acme.test")
	if err != nil || state.DomainActive || len(state.AllowedModes) != 1 || state.AllowedModes[0] != domain.MTASTSNone {
		t.Fatalf("un dominio inactivo no ofrece enforce: %+v %v", state, err)
	}
}

func TestEnforceExigeQueTodosLosMXSeanLosDeLaPlataforma(t *testing.T) {
	casos := map[string][]string{
		"sin MX":                  nil,
		"solo uno ajeno":          {"mail.otro.example."},
		"uno propio y otro ajeno": {"MX.plataforma.example.", "mail.otro.example."},
	}
	for nombre, mx := range casos {
		t.Run(nombre, func(t *testing.T) {
			h := newHarness()
			tenant := uuid.New()
			h.addDomain(tenant, "acme.test", domain.DomainLimits{})
			setMode(t, h, tenant, "acme.test", "testing")
			h.mx.hosts = mx
			if _, err := h.uc.SetMTASTSMode(context.Background(), tenant, "acme.test", "enforce"); !errors.Is(err, domain.ErrMTASTSMXMismatch) {
				t.Fatalf("MX %v: %v", mx, err)
			}
			if h.mtaSTS.items["acme.test"].Mode != domain.MTASTSTesting {
				t.Fatal("el modo no debe cambiar si el MX no casa")
			}
		})
	}
}

func TestEnforceAceptaElMXDeLaPlataformaSinDistinguirMayusculasNiPuntoFinal(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.addDomain(tenant, "acme.test", domain.DomainLimits{})
	setMode(t, h, tenant, "acme.test", "testing")
	h.mx.hosts = []string{"MX.Plataforma.Example."}
	if got := setMode(t, h, tenant, "acme.test", "enforce"); got.Mode != domain.MTASTSEnforce {
		t.Fatalf("enforce: %+v", got)
	}
}

func TestUnFalloDelDNSNoEsUnMXQueNoCasa(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.addDomain(tenant, "acme.test", domain.DomainLimits{})
	setMode(t, h, tenant, "acme.test", "testing")
	h.mx.err = errors.New("servidor caido")
	_, err := h.uc.SetMTASTSMode(context.Background(), tenant, "acme.test", "enforce")
	if !errors.Is(err, domain.ErrMTASTSDNSUnavailable) || errors.Is(err, domain.ErrMTASTSMXMismatch) {
		t.Fatalf("fallo de DNS: %v", err)
	}
}

func TestLaPoliticaNoCruzaEmpresas(t *testing.T) {
	h := newHarness()
	mine, other := uuid.New(), uuid.New()
	h.addDomain(mine, "acme.test", domain.DomainLimits{})
	setMode(t, h, mine, "acme.test", "testing")
	if _, err := h.uc.GetMTASTS(context.Background(), other, "acme.test"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("otra empresa leyendo: %v", err)
	}
	if _, err := h.uc.SetMTASTSMode(context.Background(), other, "acme.test", "testing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("otra empresa escribiendo: %v", err)
	}
	if h.mtaSTS.items["acme.test"].TenantID != mine {
		t.Fatal("la fila cambio de empresa")
	}
}

func TestUnDominioDeLaEmpresaQueNoEstaEnElDirectorioNoExiste(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	if _, err := h.uc.SetMTASTSMode(context.Background(), tenant, "acme.test", "testing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("dominio sin alta: %v", err)
	}
	if len(h.mtaSTS.items) != 0 {
		t.Fatal("no debe crear una politica de un dominio que el directorio no tiene")
	}
}

func TestEntradasInvalidasNoAbrenTransaccion(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.addDomain(tenant, "acme.test", domain.DomainLimits{})
	if _, err := h.uc.SetMTASTSMode(context.Background(), tenant, "acme.test", "strict"); !errors.Is(err, domain.ErrInvalidMTASTSMode) {
		t.Fatalf("modo: %v", err)
	}
	if _, err := h.uc.SetMTASTSMode(context.Background(), tenant, "no es un dominio", "testing"); !errors.Is(err, domain.ErrInvalidDomainName) {
		t.Fatalf("dominio: %v", err)
	}
	if h.tx.calls != 0 {
		t.Fatalf("transacciones: %d", h.tx.calls)
	}
}

func TestUnaEmpresaDadaDeBajaNoCambiaSuPolitica(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.addDomain(tenant, "acme.test", domain.DomainLimits{})
	if _, err := h.uc.RetireTenant(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := h.uc.SetMTASTSMode(context.Background(), tenant, "acme.test", "testing"); !errors.Is(err, domain.ErrTenantRetired) {
		t.Fatalf("empresa dada de baja: %v", err)
	}
}

func TestSoloSeServeLaPoliticaDeUnDominioActivoYPublicado(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	d := h.addDomain(tenant, "acme.test", domain.DomainLimits{})
	ctx := context.Background()
	if _, err := h.uc.PublishedMTASTS(ctx, "acme.test"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("sin politica: %v", err)
	}
	setMode(t, h, tenant, "acme.test", "testing")
	body, err := h.uc.PublishedMTASTS(ctx, "ACME.test.")
	want := "version: STSv1\r\nmode: testing\r\nmx: mx.plataforma.example\r\nmax_age: 86400\r\n"
	if err != nil || body != want {
		t.Fatalf("cuerpo: %q %v", body, err)
	}
	d.Active = false
	if _, err := h.uc.PublishedMTASTS(ctx, "acme.test"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("dominio inactivo: %v", err)
	}
	d.Active = true
	setMode(t, h, tenant, "acme.test", "none")
	if _, err := h.uc.PublishedMTASTS(ctx, "acme.test"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("modo none: %v", err)
	}
	if _, err := h.uc.PublishedMTASTS(ctx, "../etc/passwd"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("nombre invalido: %v", err)
	}
}

func TestBorrarUnDominioBorraSuPolitica(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	d := h.addDomain(tenant, "acme.test", domain.DomainLimits{})
	setMode(t, h, tenant, "acme.test", "testing")
	if err := h.uc.DeleteDomain(context.Background(), tenant, d.ID); err != nil {
		t.Fatal(err)
	}
	if len(h.mtaSTS.items) != 0 || !strings.Contains(strings.Join(h.mtaSTS.deleted, ","), "acme.test") {
		t.Fatalf("la politica sobrevivio al dominio: %v", h.mtaSTS.items)
	}
}

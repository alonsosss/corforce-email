package tenantcell

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/tenantcell/tenantcelltest"
	"github.com/google/uuid"
)

const (
	envDirectorio = "MAIL_DIRECTORY_CELL_HOSTS"
	envSeguridad  = "MAIL_SECURITY_CELL_HOSTS"
)

func entorno(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

func TestInstanciasPorCeldaDelEntorno(t *testing.T) {
	got, err := ParseInstances("X", " pe-01=mail-security-pe-01:8042 , pe-02=10.0.2.15:9042,eu-west-1=[fd00::5]:8042")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"pe-01": "http://mail-security-pe-01:8042", "pe-02": "http://10.0.2.15:9042", "eu-west-1": "http://[fd00::5]:8042"}
	if len(got) != len(want) {
		t.Fatalf("instancias: %v", got)
	}
	for code, target := range want {
		if got[code] != target {
			t.Errorf("%s: %q, se esperaba %q", code, got[code], target)
		}
	}
	if got, err := ParseInstances("X", "  "); err != nil || len(got) != 0 {
		t.Fatalf("vacia: %v %v", got, err)
	}

	for name, raw := range map[string]string{
		"sin igual":             "pe-01",
		"sin puerto":            "pe-01=mail-security",
		"puerto no numerico":    "pe-01=mail-security:http",
		"puerto fuera de rango": "pe-01=mail-security:70000",
		"puerto cero":           "pe-01=mail-security:0",
		"con esquema":           "pe-01=http://mail-security:8042",
		"con ruta":              "pe-01=mail-security/x:8042",
		"sin host":              "pe-01=:8042",
		"celda repetida":        "pe-01=a:1,pe-01=b:2",
		"celda en mayusculas":   "PE-01=a:1",
		"celda con guion final": "pe-=a:1",
		"celda vacia":           "=a:1",
		"entrada vacia":         "pe-01=a:1,,pe-02=b:2",
	} {
		if _, err := ParseInstances("X", raw); err == nil || !strings.Contains(err.Error(), "X") {
			t.Errorf("%s: %q se esperaba error con la variable: %v", name, raw, err)
		}
	}
}

// La celda de los destinos base es obligatoria en cuanto se declara una instancia, bien formada y
// no repetida como instancia; sin nada declarado el despliegue es de una celda.
func TestCeldaBaseEInstancias(t *testing.T) {
	for nombre, c := range map[string]struct{ base, directory, security, err string }{
		"instancias sin celda base":         {"", "", "pe-02=ms-pe-02:8042", BaseCellEnv},
		"celda base mal formada":            {"PE-01", "", "", BaseCellEnv},
		"celda base tambien como instancia": {"pe-01", "pe-01=md-pe-01:8040", "", envDirectorio},
		"instancia mal formada":             {"pe-01", "", "pe-02", envSeguridad},
		"celda base con instancias de otra": {"pe-01", "pe-02=md-pe-02:8040", "pe-02=ms-pe-02:8042", ""},
		"solo celda base, sin otras celdas": {"pe-01", "", "", ""},
		"una celda: nada declarado":         {"", "", "", ""},
		"celda base con espacios alrededor": {" pe-01 ", "", "", ""},
	} {
		inst, err := LoadInstances(entorno(map[string]string{BaseCellEnv: c.base, envDirectorio: c.directory, envSeguridad: c.security}),
			BaseCellEnv, envSeguridad, envDirectorio)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: se esperaba error con %s: %v", nombre, c.err, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", nombre, err)
			continue
		}
		if want := strings.TrimSpace(c.base); inst.BaseCell != want {
			t.Errorf("%s: celda base %q, se esperaba %q", nombre, inst.BaseCell, want)
		}
		if len(inst.ByEnv) != 2 {
			t.Errorf("%s: variables cargadas %v", nombre, inst.ByEnv)
		}
	}
}

// nuevosDestinos: mail-directory con destino base en pe-01 y una instancia en pe-02, contra una
// organization de prueba que conoce las empresas dadas.
func nuevosDestinos(t *testing.T, empresas map[string]string) (*Targets, *tenantcelltest.Organization, *relojFijo) {
	t.Helper()
	org, url := tenantcelltest.New(t, tokenDePrueba, empresas)
	resolver, reloj := nuevoResolver(url)
	inst, err := LoadInstances(entorno(map[string]string{BaseCellEnv: "pe-01", envDirectorio: "pe-02=md-pe-02:8040"}), BaseCellEnv, envDirectorio)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := inst.Targets(envDirectorio, "http://md-pe-01:8040/", resolver)
	if err != nil {
		t.Fatal(err)
	}
	return targets, org, reloj
}

func TestCadaEmpresaVaALaInstanciaDeSuCelda(t *testing.T) {
	base, otra, sinInstancia := uuid.NewString(), uuid.NewString(), uuid.NewString()
	targets, _, _ := nuevosDestinos(t, map[string]string{base: "pe-01", otra: "pe-02", sinInstancia: "pe-03"})
	ctx := context.Background()

	for _, c := range []struct {
		tenant, target, cell string
		err                  error
	}{
		{base, "http://md-pe-01:8040", "pe-01", nil},
		{otra, "http://md-pe-02:8040", "pe-02", nil},
		{sinInstancia, "", "pe-03", ErrNotServed},
		{uuid.NewString(), "", "", ErrUnknownTenant},
		{"no-es-un-uuid", "", "", ErrUnknownTenant},
	} {
		target, cell, err := targets.For(ctx, c.tenant)
		if target != c.target || cell != c.cell || !errors.Is(err, c.err) || (c.err == nil) != (err == nil) {
			t.Errorf("%s: %q %q %v, se esperaba %q %q %v", c.tenant, target, cell, err, c.target, c.cell, c.err)
		}
	}
	if _, _, err := targets.For(ctx, sinInstancia); err == nil || !strings.Contains(err.Error(), "pe-03") {
		t.Errorf("el error de una celda sin instancia nombra la celda: %v", err)
	}
}

// Con organization caido vale la ultima celda conocida dentro del margen del resolvedor; una
// empresa sin respuesta previa, o fuera del margen, no tiene destino.
func TestOrganizationCaidoConYSinCache(t *testing.T) {
	conocida, nueva := uuid.NewString(), uuid.NewString()
	targets, org, reloj := nuevosDestinos(t, map[string]string{conocida: "pe-02", nueva: "pe-02"})
	ctx := context.Background()
	if target, _, err := targets.For(ctx, conocida); err != nil || target != "http://md-pe-02:8040" {
		t.Fatalf("con organization arriba: %q %v", target, err)
	}

	org.SetDown(true)
	reloj.avanzar(cacheTTL + time.Minute)
	if target, cell, err := targets.For(ctx, conocida); err != nil || target != "http://md-pe-02:8040" || cell != "pe-02" {
		t.Errorf("caido, con la celda en cache dentro del margen: %q %q %v", target, cell, err)
	}
	if target, _, err := targets.For(ctx, nueva); !errors.Is(err, ErrUnresolved) || target != "" {
		t.Errorf("caido, sin cache: %q %v", target, err)
	}
	reloj.avanzar(staleGrace)
	if target, _, err := targets.For(ctx, conocida); !errors.Is(err, ErrUnresolved) || target != "" {
		t.Errorf("caido, fuera del margen: %q %v", target, err)
	}
}

func TestUnaCeldaVaAlDestinoBaseSinPreguntar(t *testing.T) {
	org, url := tenantcelltest.New(t, tokenDePrueba, nil)
	resolver, _ := nuevoResolver(url)
	inst, err := LoadInstances(entorno(nil), BaseCellEnv, envDirectorio)
	if err != nil || inst.BaseCell != "" {
		t.Fatalf("una celda: %+v %v", inst, err)
	}
	for _, r := range []*Resolver{nil, resolver} {
		targets, err := inst.Targets(envDirectorio, "http://mail-directory:8040", r)
		if err != nil {
			t.Fatal(err)
		}
		target, cell, err := targets.For(context.Background(), uuid.NewString())
		if err != nil || target != "http://mail-directory:8040" || cell != "" {
			t.Errorf("una celda: %q %q %v", target, cell, err)
		}
		if got := targets.URLs(); len(got) != 1 || got[0] != "http://mail-directory:8040" {
			t.Errorf("destinos: %v", got)
		}
	}
	if org.Calls() != 0 {
		t.Errorf("con una celda no se pregunta a organization: %d consultas", org.Calls())
	}
}

func TestDestinosMalConfigurados(t *testing.T) {
	varias := Instances{BaseCell: "pe-01", ByEnv: map[string]map[string]string{envDirectorio: {"pe-02": "http://md-pe-02:8040"}}}
	if _, err := varias.Targets(envDirectorio, "http://md:8040", nil); err == nil {
		t.Error("varias celdas sin resolvedor")
	}
	if _, err := varias.Targets(envSeguridad, "http://ms:8042", &Resolver{}); err == nil {
		t.Error("una variable que no se cargo")
	}
	if _, err := varias.Targets(envDirectorio, " ", &Resolver{}); err == nil {
		t.Error("sin destino base")
	}
	targets, err := varias.Targets(envDirectorio, "http://md:8040", &Resolver{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(targets.URLs(), ","); got != "http://md:8040,http://md-pe-02:8040" || targets.BaseCell() != "pe-01" {
		t.Errorf("destinos %s, celda base %q", got, targets.BaseCell())
	}
}

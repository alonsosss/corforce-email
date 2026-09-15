package config

import (
	"strings"
	"testing"
)

const (
	upstreamHostEnv = "CONFIG_TEST_UPSTREAM_HOST"
	upstreamPortEnv = upstreamHostEnv + "_PORT"
)

func TestUpstreamURL(t *testing.T) {
	cases := []struct {
		name, host, port string
		want             string
		errVar           string
	}{
		{"sin entorno valen los defectos", "", "", "http://identity:8001", ""},
		{"en blanco valen los defectos", " \t", "  ", "http://identity:8001", ""},
		{"host y puerto del entorno", "10.0.0.5", "9001", "http://10.0.0.5:9001", ""},
		{"espacios en los extremos", " identity-2 ", " 9001 ", "http://identity-2:9001", ""},
		{"IPv6 sin corchetes", "fd00::5", "9001", "http://[fd00::5]:9001", ""},
		{"puerto minimo", "", "1", "http://identity:1", ""},
		{"puerto maximo", "", "65535", "http://identity:65535", ""},
		{"puerto cero", "", "0", "", upstreamPortEnv},
		{"puerto por encima", "", "65536", "", upstreamPortEnv},
		{"puerto negativo", "", "-1", "", upstreamPortEnv},
		{"puerto ilegible", "", "ocho", "", upstreamPortEnv},
		{"puerto decimal", "", "80.5", "", upstreamPortEnv},
		{"puerto que desborda", "", "99999999999999999999", "", upstreamPortEnv},
		{"host con espacio", "iden tity", "", "", upstreamHostEnv},
		{"host con tabulador", "iden\ttity", "", "", upstreamHostEnv},
		{"host con esquema", "http://identity", "", "", upstreamHostEnv},
		{"host con ruta", "identity/x", "", "", upstreamHostEnv},
		{"host con usuario", "u@identity", "", "", upstreamHostEnv},
		{"host con consulta", "identity?x", "", "", upstreamHostEnv},
		{"host con puerto", "identity:9001", "", "", upstreamHostEnv},
		{"IPv6 con corchetes", "[fd00::5]", "", "", upstreamHostEnv},
		{"IPv6 con zona", "fe80::1%eth0", "", "", upstreamHostEnv},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(upstreamHostEnv, c.host)
			t.Setenv(upstreamPortEnv, c.port)
			got, err := UpstreamURL(upstreamHostEnv, "identity", 8001)
			if c.errVar != "" {
				if err == nil || !strings.Contains(err.Error(), c.errVar+"=") {
					t.Fatalf("%q:%q: %q, %v; se esperaba un error que nombre %s", c.host, c.port, got, err, c.errVar)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("%q:%q: %q, %v; se esperaba %q", c.host, c.port, got, err, c.want)
			}
		})
	}
	unsetEnv(t, upstreamHostEnv)
	unsetEnv(t, upstreamPortEnv)
	if got, err := UpstreamURL(upstreamHostEnv, "identity", 8001); err != nil || got != "http://identity:8001" {
		t.Fatalf("sin definir: %q, %v; se esperaban los defectos", got, err)
	}
}

// Un defecto invalido es un error de programacion: sale aunque el entorno traiga un valor bueno.
func TestUpstreamURLValidaLosDefectos(t *testing.T) {
	t.Setenv(upstreamHostEnv, "identity")
	t.Setenv(upstreamPortEnv, "8001")
	for name, c := range map[string]struct {
		host string
		port int
	}{
		"host vacio":           {"", 8001},
		"host con espacio":     {"iden tity", 8001},
		"puerto cero":          {"identity", 0},
		"puerto por encima":    {"identity", MaxPort + 1},
		"puerto negativo":      {"identity", -80},
		"host con dos puertos": {"identity:1", 8001},
	} {
		if got, err := UpstreamURL(upstreamHostEnv, c.host, c.port); err == nil || !strings.Contains(err.Error(), upstreamHostEnv) {
			t.Errorf("%s: %q, %v; se esperaba un error que nombre %s", name, got, err, upstreamHostEnv)
		}
	}
}

const serviceURLEnv = "CONFIG_TEST_SERVICE_URL"

func TestServiceURL(t *testing.T) {
	valid := map[string]string{
		"http://organization:8003":        "http://organization:8003",
		"http://organization:8003/":       "http://organization:8003",
		" http://organization:8003 ":      "http://organization:8003",
		"https://mail-auth":               "https://mail-auth",
		"HTTP://Organization:8003":        "http://Organization:8003",
		"http://127.0.0.1:18003":          "http://127.0.0.1:18003",
		"http://host.docker.internal:1":   "http://host.docker.internal:1",
		"http://svc.cell.internal:65535":  "http://svc.cell.internal:65535",
		"http://[fd00::5]:8003":           "http://[fd00::5]:8003",
		"http://[::1]":                    "http://[::1]",
		"https://organization.pe-01:8443": "https://organization.pe-01:8443",
	}
	for raw, want := range valid {
		t.Setenv(serviceURLEnv, raw)
		if got, err := ServiceURL(serviceURLEnv, ""); err != nil || got != want {
			t.Errorf("%q: %q, %v; se esperaba %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{
		"organization:8003", "organization", "//organization:8003", "ftp://organization", "file:///etc/passwd",
		"http:organization", "http://", "http:///x", "http://:8003",
		"http://organization:8003/api", "http://organization:8003//", "http://organization:8003/%2F",
		"http://organization:8003?x=1", "http://organization:8003/?", "http://organization:8003#f", "http://organization:8003/#",
		"http://u@organization:8003", "http://u:p@organization:8003", "http://@organization",
		"http://organization:0", "http://organization:65536", "http://organization:99999999999999999999",
		"http://organization:", "http://organization:ocho", "http://organization:-1",
		"http://organ ization:8003", "http://organization\t:8003", "http://organization:8003/ x",
		"http://fd00::5:8003", "http://[fe80::1%25eth0]:8003", "http://organization%20x",
	} {
		t.Setenv(serviceURLEnv, raw)
		if got, err := ServiceURL(serviceURLEnv, "http://organization:8003"); err == nil || !strings.Contains(err.Error(), serviceURLEnv+"=") {
			t.Errorf("%q: %q, %v; se esperaba un error que nombre %s", raw, got, err, serviceURLEnv)
		}
	}
}

// Ausente o en blanco vale el defecto, "" en una variable opcional; RequiredServiceURL no tiene.
func TestServiceURLAusente(t *testing.T) {
	for _, value := range []string{"", " \t"} {
		t.Setenv(serviceURLEnv, value)
		if got, err := ServiceURL(serviceURLEnv, "http://access-control:8002/"); err != nil || got != "http://access-control:8002" {
			t.Errorf("%q con defecto: %q, %v", value, got, err)
		}
		if got, err := ServiceURL(serviceURLEnv, ""); err != nil || got != "" {
			t.Errorf("%q opcional: %q, %v", value, got, err)
		}
		if got, err := RequiredServiceURL(serviceURLEnv); err == nil || !strings.Contains(err.Error(), serviceURLEnv+" is required") {
			t.Errorf("%q obligatoria: %q, %v", value, got, err)
		}
	}
	unsetEnv(t, serviceURLEnv)
	if got, err := RequiredServiceURL(serviceURLEnv); err == nil || got != "" {
		t.Errorf("sin definir: %q, %v; se esperaba un error", got, err)
	}
	t.Setenv(serviceURLEnv, "http://organization:8003/")
	if got, err := RequiredServiceURL(serviceURLEnv); err != nil || got != "http://organization:8003" {
		t.Errorf("obligatoria presente: %q, %v", got, err)
	}
	t.Setenv(serviceURLEnv, "organization:8003")
	if got, err := RequiredServiceURL(serviceURLEnv); err == nil || !strings.Contains(err.Error(), serviceURLEnv+"=") {
		t.Errorf("obligatoria mal formada: %q, %v", got, err)
	}
}

// Un defecto invalido es un error de programacion: sale aunque el entorno traiga un valor bueno.
func TestServiceURLValidaElDefecto(t *testing.T) {
	t.Setenv(serviceURLEnv, "http://organization:8003")
	for _, def := range []string{"organization:8003", "http://organization:8003/api", "http://organization:0", " "} {
		if got, err := ServiceURL(serviceURLEnv, def); err == nil || !strings.Contains(err.Error(), serviceURLEnv+": default") {
			t.Errorf("defecto %q: %q, %v; se esperaba un error que nombre %s", def, got, err, serviceURLEnv)
		}
	}
}

func TestParsePort(t *testing.T) {
	for raw, want := range map[string]int{"1": 1, "80": 80, "8001": 8001, "65535": MaxPort} {
		if got, err := ParsePort(raw); err != nil || got != want {
			t.Errorf("%q: %d, %v; se esperaba %d", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", " ", "0", "-1", "65536", "99999999999999999999", "80.0", "http", " 80", "80 ", "0x50"} {
		if got, err := ParsePort(raw); err == nil || !strings.Contains(err.Error(), "between 1 and 65535") {
			t.Errorf("%q: %d, %v; se esperaba un error con el rango", raw, got, err)
		}
	}
}

func TestValidHost(t *testing.T) {
	for _, host := range []string{"identity", "mail-security-pe-01", "10.0.2.15", "fd00::5", "::1", "svc.cell.internal"} {
		if !ValidHost(host) {
			t.Errorf("%q deberia valer como host", host)
		}
	}
	for _, host := range []string{"", " ", "a b", "a\nb", "a\x00b", "http://a", "a/b", `a\b`, "a?b", "a#b", "u@a", "[::1]", "a:80", "fe80::1%eth0"} {
		if ValidHost(host) {
			t.Errorf("%q no deberia valer como host", host)
		}
	}
}

package mailcell

import (
	"strings"
	"testing"
)

func TestCodigoDeCelda(t *testing.T) {
	for code, want := range map[string]bool{
		"pe-01": true, "eu-west-1": true, "a": true, strings.Repeat("a", MaxCodeLen): true,
		"": false, "PE-01": false, "pe_01": false, "pe.01": false, "-pe": false, "pe-": false, "pe--01": false,
		" pe-01": false, strings.Repeat("a", MaxCodeLen+1): false,
	} {
		if got := ValidCode(code); got != want {
			t.Errorf("ValidCode(%q) = %v", code, got)
		}
	}
}

func TestDominioDeCorreo(t *testing.T) {
	for raw, want := range map[string]string{
		"acme.test":          "acme.test",
		"  Beta.TEST ":       "beta.test",
		"xn--bcher-kva.test": "xn--bcher-kva.test",
		"a-b.c-d.pe":         "a-b.c-d.pe",
		"sub.acme.test":      "sub.acme.test",
	} {
		if got, ok := NormalizeDomain(raw); !ok || got != want {
			t.Errorf("NormalizeDomain(%q) = %q, %v; se esperaba %q", raw, got, ok, want)
		}
	}
	for _, raw := range []string{
		"", "acme", "acme.test.", ".acme.test", "acme..test", "-acme.test", "acme-.test",
		"acme_x.test", "acme.test/x", "ana@acme.test", "acme .test", "acme.test%2F",
		strings.Repeat("a", 64) + ".test", strings.Repeat("abcdefghi.", 26) + "test",
	} {
		if got, ok := NormalizeDomain(raw); ok {
			t.Errorf("NormalizeDomain(%q) = %q; se esperaba rechazo", raw, got)
		}
	}
}

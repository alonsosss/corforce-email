package domain

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestNormalizarCodigos(t *testing.T) {
	cases := map[string]string{
		" 123 456 ":     "123456",
		"abcde-fghjk":   "ABCDEFGHJK",
		"ab cde - fgh":  "ABCDEFGH",
		"\t12-34 56\t ": "123456",
		"":              "",
	}
	for in, want := range cases {
		if got := NormalizeMFACode(in); got != want {
			t.Errorf("NormalizeMFACode(%q) = %q; se esperaba %q", in, got, want)
		}
	}
}

func TestFormaDeLosCodigos(t *testing.T) {
	for code, want := range map[string]bool{"123456": true, "12345": false, "1234567": false, "12345a": false, "": false} {
		if IsTOTPCode(code) != want {
			t.Errorf("IsTOTPCode(%q) != %v", code, want)
		}
	}
	for code, want := range map[string]bool{
		"ABCDEFGHJK": true, "2345678923": true, "ABCDEFGHJ": false, "ABCDEFGHIK": false,
		"ABCDEFGH0K": false, "ABCDEFGH1K": false, "ABCDEFGHOK": false, "abcdefghjk": false,
	} {
		if IsRecoveryCode(code) != want {
			t.Errorf("IsRecoveryCode(%q) != %v", code, want)
		}
	}
	if got := FormatRecoveryCode("ABCDEFGHJK"); got != "ABCDE-FGHJK" {
		t.Fatalf("formato: %q", got)
	}
	if HashRecoveryCode("ABCDEFGHJK") == HashRecoveryCode("ABCDEFGHJM") || len(HashRecoveryCode("ABCDEFGHJK")) != 64 {
		t.Fatal("hash SHA-256 en hexadecimal")
	}
	if len(RecoveryCodeAlphabet) != 32 || strings.ContainsAny(RecoveryCodeAlphabet, "IO01") {
		t.Fatal("alfabeto base32 sin ambiguos")
	}
}

func TestSecretoTOTP(t *testing.T) {
	got, err := NormalizeTOTPSecret(" jbswy3dpehpk3pxp jbswy3dpehpk3pxp== ")
	if err != nil || got != "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP" {
		t.Fatalf("%q %v", got, err)
	}
	for _, bad := range []string{"", "JBSWY3DP", "no-base32!", strings.Repeat("A", 200)} {
		if _, err := NormalizeTOTPSecret(bad); err == nil {
			t.Errorf("%q se acepto", bad)
		}
	}
}

func TestEstadoDeLaVerificacion(t *testing.T) {
	var none *MailboxMFA
	if s := none.Status(); s.Enabled || s.EnabledAt != nil {
		t.Fatalf("sin fila: %+v", s)
	}
	m := &MailboxMFA{RecoveryHashes: []string{"a", "b"}}
	if s := m.Status(); !s.Enabled || s.EnabledAt == nil || s.RecoveryRemaining != 2 {
		t.Fatalf("con fila: %+v", s)
	}
}

func filtrosDePrueba() *MailboxFilters {
	return &MailboxFilters{
		ID: uuid.New(), Username: "ana@acme.test",
		Rules: []FilterRule{
			{Name: "fuera", Enabled: true, Conditions: []FilterCondition{{Field: "from", Op: "contains", Value: "x"}},
				Actions: []FilterAction{{Type: FilterActionForward, Address: "a@gmail.test"}}},
			{Name: "mixta", Enabled: false, Conditions: []FilterCondition{{Field: "from", Op: "contains", Value: "y"}},
				Actions: []FilterAction{{Type: FilterActionFlag}, {Type: FilterActionForward, Address: "b@yahoo.test"}, {Type: FilterActionForward, Address: "luis@acme.test"}}},
		},
		Forwarding: Forwarding{Enabled: true, Addresses: []string{"c@gmail.test", "eva@acme.test"}},
	}
}

func TestDireccionesDeReenvio(t *testing.T) {
	f := filtrosDePrueba()
	if got := f.ForwardAddresses(); !slices.Equal(got, []string{"a@gmail.test", "b@yahoo.test", "c@gmail.test", "eva@acme.test", "luis@acme.test"}) {
		t.Fatalf("todas: %v", got)
	}
	if got := f.ActiveForwardAddresses(); !slices.Equal(got, []string{"a@gmail.test", "c@gmail.test", "eva@acme.test"}) {
		t.Fatalf("activas: %v", got)
	}
	owned := map[string]bool{"acme.test": true}
	if got := ExternalAddresses(f.ForwardAddresses(), owned); !slices.Equal(got, []string{"a@gmail.test", "b@yahoo.test", "c@gmail.test"}) {
		t.Fatalf("externas: %v", got)
	}
	if got := AddressDomains(f.ForwardAddresses()); !slices.Equal(got, []string{"acme.test", "gmail.test", "yahoo.test"}) {
		t.Fatalf("dominios: %v", got)
	}
}

func TestCambioDelReenvioExterno(t *testing.T) {
	owned := map[string]bool{"acme.test": true}
	before := filtrosDePrueba()
	after := filtrosDePrueba()
	if _, changed := ExternalForwardingChange(before, after, owned); changed {
		t.Fatal("lo mismo no cambia")
	}
	after.Rules[1].Enabled = true
	after.Forwarding.Addresses = []string{"eva@acme.test"}
	c, changed := ExternalForwardingChange(before, after, owned)
	if !changed || !slices.Equal(c.ExternalAdded, []string{"b@yahoo.test"}) || !slices.Equal(c.ExternalRemoved, []string{"c@gmail.test"}) || !c.ForwardingEnabled {
		t.Fatalf("cambio: %+v", c)
	}
	internalOnly := filtrosDePrueba()
	internalOnly.Forwarding.Enabled = false
	c, changed = ExternalForwardingChange(before, internalOnly, owned)
	if !changed || c.ForwardingEnabled || !slices.Equal(c.ExternalRemoved, []string{"c@gmail.test"}) {
		t.Fatalf("apagar el reenvio: %+v", c)
	}
}

func TestRetirarElReenvioExterno(t *testing.T) {
	f := filtrosDePrueba()
	if err := f.Normalize(); err != nil {
		t.Fatal(err)
	}
	changed, err := f.StripExternalForwarding(map[string]bool{"acme.test": true})
	if err != nil || !changed {
		t.Fatalf("%v %v", changed, err)
	}
	if len(f.Rules) != 1 || f.Rules[0].Name != "mixta" || len(f.Rules[0].Actions) != 2 {
		t.Fatalf("reglas: %+v", f.Rules)
	}
	if !f.Forwarding.Enabled || !slices.Equal(f.Forwarding.Addresses, []string{"eva@acme.test"}) {
		t.Fatalf("reenvio: %+v", f.Forwarding)
	}
	if strings.Contains(f.ScriptData, "gmail") || !strings.Contains(f.ScriptData, `redirect "eva@acme.test"`) {
		t.Fatalf("script: %s", f.ScriptData)
	}
	if changed, _ := f.StripExternalForwarding(map[string]bool{"acme.test": true}); changed {
		t.Fatal("dos veces no cambia nada")
	}

	solo := &MailboxFilters{Username: "ana@acme.test", Forwarding: Forwarding{Enabled: true, Addresses: []string{"x@gmail.test"}}}
	if changed, err := solo.StripExternalForwarding(map[string]bool{}); !changed || err != nil || solo.Forwarding.Enabled || len(solo.Forwarding.Addresses) != 0 || solo.ScriptData != "" {
		t.Fatalf("sin direcciones el reenvio se apaga: %+v %v", solo, err)
	}
}

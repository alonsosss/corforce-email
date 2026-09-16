package domain

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestValidateRateLimitValue(t *testing.T) {
	for _, ok := range []string{"100 / 1h", "1 / 1s", "5000 / 1d", "30 / 1m"} {
		if err := ValidateRateLimitValue(ok); err != nil {
			t.Errorf("%q deberia aceptarse: %v", ok, err)
		}
	}
	for _, bad := range []string{"100/1h", "100 / 2h", "100 / 1w", "x / 1h", "", " 100 / 1h", "100 / 1h "} {
		if err := ValidateRateLimitValue(bad); !errors.Is(err, ErrValidation) {
			t.Errorf("%q deberia rechazarse con ErrValidation, obtuve %v", bad, err)
		}
	}
}

func TestNormalizeAddressQuitaEtiquetaYMinusculas(t *testing.T) {
	cases := map[string]string{
		"Ana+Promo@Acme.COM": "ana@acme.com",
		"ana@acme.com":       "ana@acme.com",
		"+raro@acme.com":     "+raro@acme.com",
		"sin-arroba":         "sin-arroba",
	}
	for in, want := range cases {
		if got := NormalizeAddress(in); got != want {
			t.Errorf("NormalizeAddress(%q) = %q, quiero %q", in, got, want)
		}
	}
}

func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"10.1.2.3":      "10.1.2.3/32",
		"10.1.2.0/24":   "10.1.2.0/24",
		"10.1.2.7/24":   "10.1.2.0/24",
		"2001:db8::1":   "2001:db8::1/128",
		"2001:db8::/32": "2001:db8::/32",
	}
	for in, want := range cases {
		got, err := NormalizeHost(in)
		if err != nil || got != want {
			t.Errorf("NormalizeHost(%q) = %q, %v; quiero %q", in, got, err, want)
		}
	}
	if _, err := NormalizeHost("no-es-ip"); !errors.Is(err, ErrValidation) {
		t.Errorf("un host invalido debe rechazarse, obtuve %v", err)
	}
}

func TestValidateObject(t *testing.T) {
	for _, ok := range []string{"acme.com", "ana@acme.com", "a.b@sub.acme.co"} {
		if err := ValidateObject(ok); err != nil {
			t.Errorf("%q deberia aceptarse: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "@acme.com", "ana@", "*@acme.com", "acme", "ana@acme..com"} {
		if err := ValidateObject(bad); err == nil {
			t.Errorf("%q deberia rechazarse", bad)
		}
	}
}

func TestValidateQuarantineMaxSize(t *testing.T) {
	for _, ok := range []int64{1, DefaultQuarantineMaxSizeBytes, MaxQuarantineMaxSizeBytes} {
		if err := ValidateQuarantineMaxSize(ok); err != nil {
			t.Errorf("%d deberia aceptarse: %v", ok, err)
		}
	}
	for _, bad := range []int64{0, -1, MaxQuarantineMaxSizeBytes + 1, 1 << 40} {
		if err := ValidateQuarantineMaxSize(bad); !errors.Is(err, ErrValidation) {
			t.Errorf("%d deberia rechazarse con ErrValidation, obtuve %v", bad, err)
		}
	}
}

func TestCellQuarantineTopEsElMaximoYLaUnion(t *testing.T) {
	a := DefaultQuarantineSettings(uuid.New())
	a.MaxSizeBytes = 3 * 1024 * 1024
	a.MaxAgeDays = 30
	a.RetentionSize = 50
	a.ExcludeDomains = []string{"b.com", "a.com"}
	b := DefaultQuarantineSettings(uuid.New())
	b.MaxSizeBytes = 20*1024*1024 + 1
	b.MaxAgeDays = 400
	b.RetentionSize = 10
	b.ExcludeDomains = []string{"a.com", "c.com"}

	top := ComputeCellQuarantineTop([]QuarantineSettings{a, b})
	if top.MaxSizeMiB != 21 {
		t.Errorf("MaxSizeMiB = %d, quiero 21 (redondeo hacia arriba)", top.MaxSizeMiB)
	}
	if top.MaxAgeDays != 400 || top.RetentionSize != 100 {
		t.Errorf("edad/retencion = %d/%d, quiero 400/100 (el defecto tambien cuenta)", top.MaxAgeDays, top.RetentionSize)
	}
	if got := top.ExcludeDomains; len(got) != 3 || got[0] != "a.com" || got[1] != "b.com" || got[2] != "c.com" {
		t.Errorf("ExcludeDomains = %v, quiero la union ordenada", got)
	}

	empty := ComputeCellQuarantineTop(nil)
	if empty.MaxSizeMiB != 10 || empty.MaxAgeDays != 365 || empty.RetentionSize != 100 || len(empty.ExcludeDomains) != 0 {
		t.Errorf("sin filas deben regir los defectos, obtuve %+v", empty)
	}
}

func TestParseRateLimitInfo(t *testing.T) {
	name, hash := ParseRateLimitInfo("user(abc123)")
	if name != "user" || hash != "abc123" {
		t.Errorf("obtuve %q/%q", name, hash)
	}
	name, hash = ParseRateLimitInfo("sinhash")
	if name != "sinhash" || hash != "" {
		t.Errorf("obtuve %q/%q", name, hash)
	}
}

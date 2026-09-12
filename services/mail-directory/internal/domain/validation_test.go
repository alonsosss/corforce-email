package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNormalizeDomain(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"Example.COM", "example.com", true},
		{" mail.example.org. ", "mail.example.org", true},
		{"xn--espaa-rta.com", "xn--espaa-rta.com", true},
		{"localhost", "", false},
		{"-bad.com", "", false},
		{"bad-.com", "", false},
		{"bad..com", "", false},
		{"example.c0m", "", false},
		{"", "", false},
		{strings.Repeat("a", 64) + ".com", "", false},
		{"a b.com", "", false},
	}
	for _, c := range cases {
		got, err := NormalizeDomain(c.in)
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("NormalizeDomain(%q) = %q, %v; quiero %q", c.in, got, err, c.want)
		}
		if !c.ok && !errors.Is(err, ErrInvalidDomainName) {
			t.Errorf("NormalizeDomain(%q) debia fallar, dio %q", c.in, got)
		}
	}
}

func TestNormalizeLocalPart(t *testing.T) {
	ok := []string{"juan.perez", "Ventas+Lima", "a_b-c", "123"}
	for _, in := range ok {
		got, err := NormalizeLocalPart(in)
		if err != nil || got != strings.ToLower(in) {
			t.Errorf("NormalizeLocalPart(%q) = %q, %v", in, got, err)
		}
	}
	bad := []string{"", ".juan", "juan.", "ju..an", "juan perez", "juan@x", "ñandu", strings.Repeat("a", 65)}
	for _, in := range bad {
		if _, err := NormalizeLocalPart(in); !errors.Is(err, ErrInvalidLocalPart) {
			t.Errorf("NormalizeLocalPart(%q) debia fallar", in)
		}
	}
}

func TestNormalizeAddress(t *testing.T) {
	addr, dom, err := NormalizeAddress(" Ventas@Example.com ")
	if err != nil || addr != "ventas@example.com" || dom != "example.com" {
		t.Fatalf("direccion completa: %q %q %v", addr, dom, err)
	}
	addr, dom, err = NormalizeAddress("@Example.com")
	if err != nil || addr != "@example.com" || dom != "example.com" {
		t.Fatalf("catch-all: %q %q %v", addr, dom, err)
	}
	for _, in := range []string{"", "@", "ventas", "ventas@", "@-x.com", "a@b"} {
		if _, _, err := NormalizeAddress(in); !errors.Is(err, ErrInvalidAddress) {
			t.Errorf("NormalizeAddress(%q) debia fallar", in)
		}
	}
}

func TestNormalizeGoto(t *testing.T) {
	got, err := NormalizeGoto(" Uno@Example.com , dos@ext.org,uno@example.com,, ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "uno@example.com,dos@ext.org" {
		t.Fatalf("goto normalizado: %q", got)
	}
	for _, in := range []string{"", " , ", "sin-arroba", "a@b.com, mal"} {
		if _, err := NormalizeGoto(in); !errors.Is(err, ErrInvalidGoto) {
			t.Errorf("NormalizeGoto(%q) debia fallar", in)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	if err := ValidatePassword("corta"); !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("corta: %v", err)
	}
	if err := ValidatePassword(strings.Repeat("x", MaxPasswordLength+1)); !errors.Is(err, ErrPasswordTooLong) {
		t.Errorf("larga: %v", err)
	}
	if err := ValidatePassword("doce-caracteres!"); err != nil {
		t.Errorf("valida: %v", err)
	}
}

func TestCheckMailboxQuota(t *testing.T) {
	limits := DomainLimits{MaxQuotaBytes: 100, QuotaBytes: 250}
	if err := CheckMailboxQuota(100, limits, 150); err != nil {
		t.Errorf("justo en el limite: %v", err)
	}
	if err := CheckMailboxQuota(101, limits, 0); !errors.Is(err, ErrQuotaExceedsMax) {
		t.Errorf("supera max por buzon: %v", err)
	}
	if err := CheckMailboxQuota(0, limits, 0); !errors.Is(err, ErrQuotaExceedsMax) {
		t.Errorf("ilimitado con maximo: %v", err)
	}
	if err := CheckMailboxQuota(100, limits, 151); !errors.Is(err, ErrDomainQuotaExceeded) {
		t.Errorf("supera cuota del dominio: %v", err)
	}
	if err := CheckMailboxQuota(0, DomainLimits{}, 1<<40); err != nil {
		t.Errorf("sin limites todo vale: %v", err)
	}
	if err := CheckMailboxQuota(-1, DomainLimits{}, 0); !errors.Is(err, ErrInvalidLimit) {
		t.Errorf("negativa: %v", err)
	}
}

func TestDomainLimitsValidate(t *testing.T) {
	if err := (DomainLimits{MaxAliases: -1}).Validate(); !errors.Is(err, ErrInvalidLimit) {
		t.Errorf("negativo: %v", err)
	}
	if err := (DomainLimits{DefaultQuotaBytes: 10, MaxQuotaBytes: 5}).Validate(); !errors.Is(err, ErrQuotaExceedsMax) {
		t.Errorf("default > max: %v", err)
	}
	if err := (DomainLimits{DefaultQuotaBytes: 10}).Validate(); err != nil {
		t.Errorf("valido: %v", err)
	}
}

func TestCheckLimit(t *testing.T) {
	if err := CheckLimit(0, 1000, ErrMaxMailboxesReached); err != nil {
		t.Errorf("0 es sin limite: %v", err)
	}
	if err := CheckLimit(3, 2, ErrMaxMailboxesReached); err != nil {
		t.Errorf("por debajo: %v", err)
	}
	if err := CheckLimit(3, 3, ErrMaxMailboxesReached); !errors.Is(err, ErrMaxMailboxesReached) {
		t.Errorf("alcanzado: %v", err)
	}
}

func TestValidateSieveScript(t *testing.T) {
	if err := ValidateSieveScript("  \n"); !errors.Is(err, ErrSieveEmpty) {
		t.Errorf("vacio: %v", err)
	}
	if err := ValidateSieveScript(strings.Repeat("#", MaxSieveScriptBytes+1)); !errors.Is(err, ErrSieveTooLarge) {
		t.Errorf("grande: %v", err)
	}
	if err := ValidateSieveScript(`require ["fileinto"]; fileinto "Spam";`); err != nil {
		t.Errorf("valido: %v", err)
	}
}

func TestValidateSpamAliasValidity(t *testing.T) {
	if err := ValidateSpamAliasValidity(nil, false); !errors.Is(err, ErrValidityRequired) {
		t.Errorf("sin caducidad: %v", err)
	}
	until := time.Now().Add(time.Hour)
	if err := ValidateSpamAliasValidity(&until, false); err != nil {
		t.Errorf("con fecha: %v", err)
	}
	if err := ValidateSpamAliasValidity(nil, true); err != nil {
		t.Errorf("permanente: %v", err)
	}
}

func TestNormalizeHostname(t *testing.T) {
	for _, in := range []string{"smtp.relay.net", "SMTP.relay.net:587", "[10.0.0.1]:25"} {
		if _, err := NormalizeHostname(in); err != nil {
			t.Errorf("NormalizeHostname(%q): %v", in, err)
		}
	}
	for _, in := range []string{"", "smtp relay", "host:abc"} {
		if _, err := NormalizeHostname(in); !errors.Is(err, ErrInvalidHostname) {
			t.Errorf("NormalizeHostname(%q) debia fallar", in)
		}
	}
}

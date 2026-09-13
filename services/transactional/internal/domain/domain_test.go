package domain

import (
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
)

const testKey = "0123456789abcdef0123456789abcdef-signing"

func TestLinkSignerRoundTrip(t *testing.T) {
	s, err := NewLinkSigner(testKey, "https://app.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	c := UnsubscribeClaims{TenantID: uuid.New(), MessageID: uuid.New(), Email: "ana+promo@example.com"}
	sig := s.Sign(c)
	if len(sig) != 64 {
		t.Fatalf("la firma debe ir completa (64 hex), tiene %d", len(sig))
	}
	if !s.Verify(c, sig) {
		t.Fatal("una firma recien emitida debe verificar")
	}

	raw := s.UnsubscribeURL(c)
	if !strings.HasPrefix(raw, "https://app.example.com"+UnsubscribePath+"?") {
		t.Fatalf("enlace inesperado: %s", raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	parsed := UnsubscribeClaims{
		TenantID:  uuid.MustParse(q.Get("t")),
		MessageID: uuid.MustParse(q.Get("m")),
		Email:     q.Get("e"),
	}
	if parsed != c {
		t.Fatalf("las claims no sobreviven al enlace: %+v", parsed)
	}
	if !s.Verify(parsed, q.Get("sig")) {
		t.Fatal("la firma del enlace debe verificar tras parsearlo")
	}
}

func TestLinkSignerRejectsTampering(t *testing.T) {
	s, _ := NewLinkSigner(testKey, "https://app.example.com")
	c := UnsubscribeClaims{TenantID: uuid.New(), MessageID: uuid.New(), Email: "ana@example.com"}
	sig := s.Sign(c)

	other, _ := NewLinkSigner(testKey+"-otra", "https://app.example.com")
	cases := map[string]struct {
		signer *LinkSigner
		claims UnsubscribeClaims
		sig    string
	}{
		"otro email":      {s, UnsubscribeClaims{c.TenantID, c.MessageID, "eva@example.com"}, sig},
		"otro mensaje":    {s, UnsubscribeClaims{c.TenantID, uuid.New(), c.Email}, sig},
		"otra empresa":    {s, UnsubscribeClaims{uuid.New(), c.MessageID, c.Email}, sig},
		"firma truncada":  {s, c, sig[:32]},
		"firma vacia":     {s, c, ""},
		"firma alterada":  {s, c, strings.Repeat("0", 64)},
		"otra clave":      {other, c, sig},
		"firma con ruido": {s, c, sig + "00"},
	}
	for name, tc := range cases {
		if tc.signer.Verify(tc.claims, tc.sig) {
			t.Errorf("%s: la verificacion debia fallar", name)
		}
	}
}

func TestNewLinkSignerRejectsShortKey(t *testing.T) {
	if _, err := NewLinkSigner("corta", "https://app.example.com"); err == nil {
		t.Fatal("una clave de menos de 32 caracteres debe rechazarse")
	}
}

func TestValidEmailAndNormalize(t *testing.T) {
	valid := []string{"ana@example.com", "a.b+c@sub.example.co", "X_Y-z%1@example.io"}
	invalid := []string{"", "ana", "ana@", "@example.com", "ana@example", "ana @example.com",
		"ana@example.com\r\nBcc: x@evil.com", "\"ana\"@example.com", "ana@-example.com"}
	for _, e := range valid {
		if !ValidEmail(e) {
			t.Errorf("%q deberia ser valida", e)
		}
	}
	for _, e := range invalid {
		if ValidEmail(e) {
			t.Errorf("%q deberia ser invalida", e)
		}
	}
	if got := NormalizeEmail("  Ana@EXAMPLE.Com "); got != "Ana@example.com" {
		t.Errorf("NormalizeEmail = %q", got)
	}
}

func TestFormatAddressEncodesName(t *testing.T) {
	if got := FormatAddress("", "ana@example.com"); got != "ana@example.com" {
		t.Errorf("sin nombre = %q", got)
	}
	if got := FormatAddress("Core Force", "no-reply@example.com"); got != `"Core Force" <no-reply@example.com>` {
		t.Errorf("con nombre = %q", got)
	}
	got := FormatAddress("Peña", "no-reply@example.com")
	if !strings.HasPrefix(got, "=?utf-8?") || !strings.HasSuffix(got, "<no-reply@example.com>") {
		t.Errorf("un nombre no ASCII debe ir codificado (RFC 2047): %q", got)
	}
}

func TestValidateHeaders(t *testing.T) {
	if err := ValidateHeaders(map[string]string{"X-Order-Id": "A-100"}); err != nil {
		t.Fatalf("cabecera X- valida rechazada: %v", err)
	}
	rejected := []map[string]string{
		{"From": "x@example.com"},
		{"List-Unsubscribe": "<https://evil.example>"},
		{"X-SES-CONFIGURATION-SET": "otro"},
		{"X-Order-Id": "A\r\nBcc: victima@example.com"},
		{"X-Vacia": ""},
		{"X-Con Espacio": "v"},
	}
	for _, h := range rejected {
		if err := ValidateHeaders(h); err == nil || !IsValidation(err) {
			t.Errorf("%v deberia rechazarse con error de validacion", h)
		}
	}
}

func TestValidateTagsReserved(t *testing.T) {
	if err := ValidateTags(map[string]string{"campaign": "welcome-1"}); err != nil {
		t.Fatalf("etiqueta valida rechazada: %v", err)
	}
	for _, tags := range []map[string]string{{"tenant_id": "x"}, {"Message_ID": "x"}, {"con espacio": "x"}} {
		if err := ValidateTags(tags); err == nil {
			t.Errorf("%v deberia rechazarse", tags)
		}
	}
}

func TestStatusForEventNeverRegresses(t *testing.T) {
	status, from := StatusForEvent(EventDelivery)
	if status != StatusDelivered {
		t.Fatalf("delivery fija %q", status)
	}
	for _, s := range from {
		if s == StatusBounced || s == StatusComplained {
			t.Fatalf("un delivery tardio no puede pisar %q", s)
		}
	}
	if s, _ := StatusForEvent(EventOpen); s != "" {
		t.Fatalf("open no cambia el estado, fijo %q", s)
	}
}

func TestSendingDomainCanSend(t *testing.T) {
	cases := []struct {
		d    *SendingDomain
		want bool
	}{
		{&SendingDomain{Status: "verified", Purpose: "sending"}, true},
		{&SendingDomain{Status: "verified", Purpose: "both"}, true},
		{&SendingDomain{Status: "verified", Purpose: "corporate"}, false},
		{&SendingDomain{Status: "failed", Purpose: "sending"}, false},
		{&SendingDomain{Status: "pending", Purpose: "both"}, false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := tc.d.CanSend(); got != tc.want {
			t.Errorf("%+v: CanSend = %v", tc.d, got)
		}
	}
}

package oauth2

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestStateRoundTrip(t *testing.T) {
	signer := NewStateSigner([]byte("clave-de-plataforma"), "google-ads")
	tenant, user := uuid.New(), uuid.New()

	raw, issued, err := signer.Sign(tenant, user)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	got, err := signer.Verify(raw, tenant)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.TenantID != tenant || got.UserID != user || got.Nonce != issued.Nonce {
		t.Fatalf("state alterado en el viaje: %+v vs %+v", got, issued)
	}
}

func TestStateRejectsOtherTenant(t *testing.T) {
	signer := NewStateSigner([]byte("clave"), "google-ads")
	raw, _, err := signer.Sign(uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := signer.Verify(raw, uuid.New()); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("una empresa no puede completar la autorizacion de otra, err=%v", err)
	}
}

func TestStateRejectsOtherPurpose(t *testing.T) {
	key := []byte("clave-compartida")
	raw, _, err := NewStateSigner(key, "google-ads").Sign(uuid.Nil, uuid.Nil)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := NewStateSigner(key, "meta-ads").Verify(raw, uuid.Nil); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("un state de otro proveedor no debe valer, err=%v", err)
	}
}

func TestStateRejectsTamperedPayload(t *testing.T) {
	signer := NewStateSigner([]byte("clave"), "google-ads")
	tenant := uuid.New()
	raw, _, err := signer.Sign(tenant, uuid.New())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	payload, sig, _ := strings.Cut(raw, ".")
	// Se altera el primer caracter por otro distinto del que hubiera: cambiarlo
	// por una constante fija dejaria el test sin efecto cuando coincidieran.
	replacement := "A"
	if payload[0] == 'A' {
		replacement = "B"
	}
	tampered := replacement + payload[1:] + "." + sig
	if _, err := signer.Verify(tampered, tenant); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("la firma debe detectar la manipulacion, err=%v", err)
	}
}

func TestStateExpires(t *testing.T) {
	signer := NewStateSigner([]byte("clave"), "google-ads").WithTTL(time.Second)
	tenant := uuid.New()
	raw, _, err := signer.Sign(tenant, uuid.New())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := signer.Verify(raw, tenant); !errors.Is(err, ErrStateExpired) {
		t.Fatalf("el state debe caducar, err=%v", err)
	}
}

func TestPKCEChallengeIsDerivedFromVerifier(t *testing.T) {
	p, err := NewPKCE()
	if err != nil {
		t.Fatalf("pkce: %v", err)
	}
	if err := ValidateVerifier(p.Verifier); err != nil {
		t.Fatalf("verifier fuera de rango: %v", err)
	}
	if p.Method != MethodS256 {
		t.Fatalf("solo se admite S256, se obtuvo %q", p.Method)
	}
	if ChallengeFor(p.Verifier) != p.Challenge {
		t.Fatal("el challenge no corresponde al verifier")
	}
	other, _ := NewPKCE()
	if other.Verifier == p.Verifier {
		t.Fatal("dos verifier consecutivos no pueden coincidir")
	}
}

func TestCipherIsolatesTenants(t *testing.T) {
	key := strings.Repeat("ab", 32)
	c, err := NewCipherFromHex(key, "google-ads-token")
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	tenantA, tenantB := uuid.New(), uuid.New()
	secret := []byte("1//refresh-token-de-la-empresa")

	sealed, err := c.EncryptFor(tenantA, secret)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	opened, err := c.DecryptFor(tenantA, sealed)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(opened, secret) {
		t.Fatal("el texto descifrado no coincide")
	}
	if _, err := c.DecryptFor(tenantB, sealed); err == nil {
		t.Fatal("otra empresa no debe poder descifrar el token")
	}
}

func TestCipherRejectsShortKey(t *testing.T) {
	if _, err := NewCipherFromHex("abcd", "google-ads-token"); err == nil {
		t.Fatal("una clave corta debe rechazarse al arrancar")
	}
}

func TestValidateRedirectURI(t *testing.T) {
	valid := []string{
		"https://app.example.com/google-ads/callback",
		"http://localhost:5173/google-ads/callback",
		"http://127.0.0.1:5173/google-ads/callback",
	}
	for _, uri := range valid {
		if err := ValidateRedirectURI(uri); err != nil {
			t.Fatalf("%s deberia ser valido: %v", uri, err)
		}
	}
	invalid := []string{
		"",
		"http://app.example.com/google-ads/callback",
		"https://app.example.com/google-ads/callback#done",
		"https://app.example.com/google-ads/callback?next=https://otro.com",
		"https://*.example.com/callback",
		"https://user:pass@app.example.com/callback",
		"https://203.0.113.10/callback",
		"https://app/callback",
		"ftp://app.example.com/callback",
		"https://app.example.com/google-ads/../callback",
	}
	for _, uri := range invalid {
		if err := ValidateRedirectURI(uri); err == nil {
			t.Fatalf("%s deberia rechazarse", uri)
		}
	}
}

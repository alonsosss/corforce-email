package secrets

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/crypto"
	"github.com/alonsosss/corforce-email/pkg/totp"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
)

func TestLosCodigosDeRecuperacionUsanSoloElAlfabetoSinAmbiguos(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		code, err := New().GenerateRecoveryCode()
		if err != nil {
			t.Fatal(err)
		}
		if !domain.IsRecoveryCode(code) {
			t.Fatalf("%q no es un codigo de recuperacion", code)
		}
		if strings.ContainsAny(code, "IO01") {
			t.Fatalf("%q lleva caracteres ambiguos", code)
		}
		seen[code] = true
	}
	if len(seen) < 199 {
		t.Fatalf("solo %d codigos distintos de 200", len(seen))
	}
}

func TestElSelladoAtaElSecretoASuBuzon(t *testing.T) {
	t.Setenv("MFA_TEST_KEY", strings.Repeat("ab", 32))
	ring, err := crypto.LoadKeyRing("MFA_TEST_KEY", "MFA_TEST_KEYS_OLD")
	if err != nil {
		t.Fatal(err)
	}
	s := NewKeyRingSealer(ring)
	sealed, err := s.Seal([]byte("SECRETO"), []byte("buzon-a"))
	if err != nil || bytes.Contains(sealed, []byte("SECRETO")) {
		t.Fatalf("sellado: %v", err)
	}
	if plain, err := s.Open(sealed, []byte("buzon-a")); err != nil || string(plain) != "SECRETO" {
		t.Fatalf("abrir: %q %v", plain, err)
	}
	if _, err := s.Open(sealed, []byte("buzon-b")); err == nil {
		t.Fatal("el secreto de un buzon se abrio como el de otro")
	}
}

func TestTOTPDevuelveElPaso(t *testing.T) {
	secret, _ := totp.GenerateSecret()
	now := time.Unix(1_900_000_000, 0)
	code, _ := totp.Generate(secret, now)
	step, ok := TOTP{}.ValidateStep(secret, code, now)
	if !ok || step != now.Unix()/30 {
		t.Fatalf("paso %d ok=%v", step, ok)
	}
}

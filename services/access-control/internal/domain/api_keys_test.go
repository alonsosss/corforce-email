package domain

import (
	"bytes"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTokenDeClave(t *testing.T) {
	prefix, secret, err := NewAPIKeyToken(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	token := FormatAPIKeyToken(prefix, secret)
	if !strings.HasPrefix(token, APIKeyTokenPrefix) || len(prefix) != APIKeyPrefixLen || len(secret) != APIKeySecretLen {
		t.Fatalf("forma: %q", token)
	}
	p, s, err := ParseAPIKeyToken(token)
	if err != nil || p != prefix || s != secret || !ValidAPIKeyPrefix(p) {
		t.Fatalf("ida y vuelta: %v", err)
	}
	for _, bad := range []string{"", prefix, "cfm_" + prefix, "cfm_" + prefix + "_" + secret[:10], "xyz_" + prefix + "_" + secret,
		"cfm_" + strings.ToUpper(prefix) + "_" + secret, "cfm_" + prefix + "_" + secret + "1", "cfm_" + prefix + "__" + secret[1:]} {
		if _, _, err := ParseAPIKeyToken(bad); !errors.Is(err, ErrAPIKeyInvalid) {
			t.Errorf("%q deberia rechazarse", bad)
		}
	}
	if _, _, err := NewAPIKeyToken(bytes.NewReader(nil)); err == nil {
		t.Fatal("sin aleatoriedad no hay clave")
	}
	p2, _, _ := NewAPIKeyToken(rand.Reader)
	if p2 == prefix {
		t.Fatal("dos claves no comparten prefijo")
	}
	if bytes.Equal(APIKeyHashInput(prefix, secret), APIKeyHashInput(p2, secret)) {
		t.Fatal("el prefijo forma parte de lo firmado")
	}
}

func TestEstadoDeClave(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Minute), now.Add(time.Minute)
	if (APIKey{}).Status(now) != APIKeyActive || (APIKey{ExpiresAt: &future}).Status(now) != APIKeyActive {
		t.Fatal("vigente")
	}
	if (APIKey{ExpiresAt: &past}).Status(now) != APIKeyExpired || (APIKey{ExpiresAt: &now}).Status(now) != APIKeyExpired {
		t.Fatal("caducada")
	}
	if (APIKey{ExpiresAt: &past, RevokedAt: &past}).Status(now) != APIKeyRevoked {
		t.Fatal("la revocacion manda sobre la caducidad")
	}
}

func TestNombreYCaducidad(t *testing.T) {
	if n, err := NormalizeAPIKeyName("  Tienda  "); err != nil || n != "Tienda" {
		t.Fatalf("nombre: %q %v", n, err)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("a", MaxAPIKeyNameLen+1), "linea\nnueva"} {
		if _, err := NormalizeAPIKeyName(bad); err == nil {
			t.Errorf("%q deberia rechazarse", bad)
		}
	}
	now := time.Now()
	ok := now.Add(30 * 24 * time.Hour)
	soon, far := now.Add(time.Minute), now.Add(MaxAPIKeyLifetime+time.Hour)
	if ValidateAPIKeyExpiry(nil, now) != nil || ValidateAPIKeyExpiry(&ok, now) != nil {
		t.Fatal("caducidades validas")
	}
	if ValidateAPIKeyExpiry(&soon, now) == nil || ValidateAPIKeyExpiry(&far, now) == nil {
		t.Fatal("caducidades fuera de rango")
	}
}

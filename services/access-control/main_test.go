package main

import (
	"errors"
	"strings"
	"testing"

	hmacadapter "github.com/alonsosss/corforce-email/services/access-control/internal/adapters/hmac"
)

// Sin llave del hash, fuera de development y test access-control no arranca; dentro arranca y las
// claves responden como no disponibles.
func TestLlaveDelHashDeLasClaves(t *testing.T) {
	t.Setenv("API_KEY_HASH_KEYS_OLD", "")
	t.Setenv("API_KEY_HASH_KEY", "")
	for _, env := range []string{"production", "staging", ""} {
		t.Setenv("ENVIRONMENT", env)
		if _, err := apiKeyHasherFromEnv(); err == nil || !strings.Contains(err.Error(), "API_KEY_HASH_KEY") {
			t.Errorf("ENVIRONMENT=%q sin llave: %v", env, err)
		}
	}
	t.Setenv("ENVIRONMENT", "development")
	h, err := apiKeyHasherFromEnv()
	if err != nil {
		t.Fatalf("development sin llave: %v", err)
	}
	if _, _, err := h.Hash([]byte("x")); !errors.Is(err, hmacadapter.ErrNoKey) {
		t.Fatalf("sin llave no se firma nada: %v", err)
	}
	if _, _, err := h.Verify([]byte("x"), nil, "k"); !errors.Is(err, hmacadapter.ErrNoKey) {
		t.Fatalf("sin llave no se verifica nada: %v", err)
	}
	t.Setenv("API_KEY_HASH_KEY", "corta")
	if _, err := apiKeyHasherFromEnv(); err == nil {
		t.Fatal("una llave mal formada no arranca ni en development")
	}
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("API_KEY_HASH_KEY", strings.Repeat("ab", 32))
	if _, err := apiKeyHasherFromEnv(); err != nil {
		t.Fatalf("con llave: %v", err)
	}
}

func TestDireccionPublicaDelRelay(t *testing.T) {
	t.Setenv("SMTP_RELAY_PUBLIC_HOST", "")
	if s, err := smtpSettingsFromEnv(); err != nil || s.Host != "" {
		t.Fatalf("sin relay: %+v %v", s, err)
	}
	t.Setenv("SMTP_RELAY_PUBLIC_HOST", "smtp.ejemplo.test")
	t.Setenv("SMTP_RELAY_PUBLIC_STARTTLS_PORT", "")
	t.Setenv("SMTP_RELAY_PUBLIC_TLS_PORT", "10465")
	s, err := smtpSettingsFromEnv()
	if err != nil || s.Host != "smtp.ejemplo.test" || s.StartTLSPort != 2525 || s.TLSPort != 10465 {
		t.Fatalf("con relay: %+v %v", s, err)
	}
	t.Setenv("SMTP_RELAY_PUBLIC_HOST", "no es un host")
	if _, err := smtpSettingsFromEnv(); err == nil {
		t.Fatal("host invalido")
	}
	t.Setenv("SMTP_RELAY_PUBLIC_HOST", "smtp.ejemplo.test")
	t.Setenv("SMTP_RELAY_PUBLIC_TLS_PORT", "70000")
	if _, err := smtpSettingsFromEnv(); err == nil {
		t.Fatal("puerto fuera de rango")
	}
}

func TestStepUpDeCrearClaves(t *testing.T) {
	t.Setenv("STEP_UP_MODE", "")
	if mw, err := stepUpFromEnv(); err != nil || mw == nil {
		t.Fatalf("sin enforce: %v", err)
	}
	t.Setenv("STEP_UP_MODE", "enforce")
	t.Setenv("JWT_PUBLIC_KEYS", "")
	if _, err := stepUpFromEnv(); err == nil || !strings.Contains(err.Error(), "STEP_UP_MODE=enforce") {
		t.Fatalf("enforce sin claves publicas no arranca: %v", err)
	}
}

package hmac

import (
	"errors"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/crypto"
)

func TestHashConRotacion(t *testing.T) {
	oldKey, newKey := strings.Repeat("aa", 32), strings.Repeat("bb", 32)
	t.Setenv("API_KEY_HASH_KEY", oldKey)
	t.Setenv("API_KEY_HASH_KEYS_OLD", "")
	before, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	input := []byte("core-force-mail/api-key/v1\nprefijo\nsecreto")
	hash, id, err := before.Hash(input)
	if err != nil {
		t.Fatal(err)
	}
	if ok, current, err := before.Verify(input, hash, id); !ok || !current || err != nil {
		t.Fatalf("recien firmado: %v %v %v", ok, current, err)
	}
	if ok, _, _ := before.Verify([]byte("otro"), hash, id); ok {
		t.Fatal("otro secreto no verifica")
	}

	t.Setenv("API_KEY_HASH_KEY", newKey)
	t.Setenv("API_KEY_HASH_KEYS_OLD", oldKey)
	after, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if ok, current, err := after.Verify(input, hash, id); !ok || current || err != nil {
		t.Fatalf("con la llave retirada verifica pero pide rehacerse: %v %v %v", ok, current, err)
	}

	t.Setenv("API_KEY_HASH_KEYS_OLD", "")
	gone, _ := FromEnv()
	if _, _, err := gone.Verify(input, hash, id); !errors.Is(err, crypto.ErrUnknownKeyID) {
		t.Fatalf("sin la llave retirada: %v", err)
	}
}

func TestSinLlave(t *testing.T) {
	t.Setenv("API_KEY_HASH_KEY", "")
	t.Setenv("API_KEY_HASH_KEYS_OLD", "")
	if _, err := FromEnv(); !errors.Is(err, ErrNoKey) {
		t.Fatalf("sin llave: %v", err)
	}
	t.Setenv("API_KEY_HASH_KEY", "corta")
	if _, err := FromEnv(); err == nil {
		t.Fatal("una llave mal formada no arranca")
	}
}

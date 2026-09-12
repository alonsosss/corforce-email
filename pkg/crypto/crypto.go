// Package crypto provides AES-256-GCM helpers for encrypting sensitive
// credentials at rest (e.g. email provider secrets), and a key loader that
// supports key rotation. Shared across services so the encryption format is
// uniform and a single key can be rotated without losing access to old data.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// ErrUndecryptable: ningun juego de llaves configurado abre el dato. Es el error que
// distingue "llave retirada antes de tiempo" de un fallo de cifrado.
var ErrUndecryptable = errors.New("crypto: unable to decrypt with any configured key")

// Encrypt encrypts plaintext using AES-256-GCM. The returned slice is
// nonce || ciphertext. key must be exactly 32 bytes.
func Encrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt decrypts a nonce || ciphertext slice produced by Encrypt with the
// given key.
func Decrypt(key, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(data) < ns {
		return nil, errors.New("crypto: ciphertext too short")
	}
	nonce, ciphertext := data[:ns], data[ns:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}

// KeyRing holds the active key (used to encrypt) plus optional old keys (only
// used to decrypt), enabling key rotation without losing access to data
// encrypted under a previous key.
type KeyRing struct {
	active []byte
	old    [][]byte
}

// LoadKeyRing builds a KeyRing from env vars. activeEnv must hold 64 hex chars
// (32 bytes). oldEnv (optional) holds a comma-separated list of 64-hex keys.
func LoadKeyRing(activeEnv, oldEnv string) (*KeyRing, error) {
	active, err := parseHexKey(os.Getenv(activeEnv))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", activeEnv, err)
	}
	kr := &KeyRing{active: active}
	for _, hk := range strings.Split(os.Getenv(oldEnv), ",") {
		hk = strings.TrimSpace(hk)
		if hk == "" {
			continue
		}
		k, err := parseHexKey(hk)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", oldEnv, err)
		}
		kr.old = append(kr.old, k)
	}
	return kr, nil
}

// Encrypt encrypts with the active key.
func (kr *KeyRing) Encrypt(plaintext []byte) ([]byte, error) {
	return Encrypt(kr.active, plaintext)
}

// Decrypt tries the active key first, then any old keys (rotation support).
func (kr *KeyRing) Decrypt(data []byte) ([]byte, error) {
	if out, err := Decrypt(kr.active, data); err == nil {
		return out, nil
	}
	for _, k := range kr.old {
		if out, err := Decrypt(k, data); err == nil {
			return out, nil
		}
	}
	return nil, ErrUndecryptable
}

// Rotate deja data cifrada bajo la llave activa. Si ya lo esta, la devuelve tal cual con
// rotated=false; si solo la abre una llave vieja, la re-cifra y devuelve rotated=true; si
// no la abre ninguna, ErrUndecryptable. Es la pieza que permite RETIRAR una llave vieja:
// sin re-cifrar lo guardado, el anillo solo servia para seguir leyendo.
func (kr *KeyRing) Rotate(data []byte) (out []byte, rotated bool, err error) {
	if _, err := Decrypt(kr.active, data); err == nil {
		return data, false, nil
	}
	for _, k := range kr.old {
		plain, err := Decrypt(k, data)
		if err != nil {
			continue
		}
		out, err := Encrypt(kr.active, plain)
		if err != nil {
			return nil, false, err
		}
		return out, true, nil
	}
	return nil, false, ErrUndecryptable
}

// HasOldKeys dice si hay llaves retiradas en el anillo, es decir, si queda una rotacion
// por terminar.
func (kr *KeyRing) HasOldKeys() bool { return len(kr.old) > 0 }

func parseHexKey(hexKey string) ([]byte, error) {
	hexKey = strings.TrimSpace(hexKey)
	if len(hexKey) != 64 {
		return nil, errors.New("must be 64 hex chars (32 bytes)")
	}
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, err
	}
	return key, nil
}

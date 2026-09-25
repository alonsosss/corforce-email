// Package crypto provides AES-256-GCM helpers for encrypting sensitive
// credentials at rest (e.g. email provider secrets), and a key loader that
// supports key rotation. Shared across services so the encryption format is
// uniform and a single key can be rotated without losing access to old data.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
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
	return EncryptWithAAD(key, plaintext, nil)
}

// EncryptWithAAD es Encrypt con datos adicionales autenticados: el dato solo abre con los mismos
// aad, asi que un cifrado copiado a otra fila (otro dueno, otro id) no se lee en su nuevo sitio.
// aad no se guarda ni se cifra.
func EncryptWithAAD(key, plaintext, aad []byte) ([]byte, error) {
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
	return gcm.Seal(nonce, nonce, plaintext, aad), nil
}

// Decrypt decrypts a nonce || ciphertext slice produced by Encrypt with the
// given key.
func Decrypt(key, data []byte) ([]byte, error) {
	return DecryptWithAAD(key, data, nil)
}

// DecryptWithAAD abre lo cifrado por EncryptWithAAD con los mismos aad.
func DecryptWithAAD(key, data, aad []byte) ([]byte, error) {
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
	return gcm.Open(nil, nonce, ciphertext, aad)
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
	return kr.DecryptWithAAD(data, nil)
}

// EncryptWithAAD encrypts with the active key, binding the result to aad.
func (kr *KeyRing) EncryptWithAAD(plaintext, aad []byte) ([]byte, error) {
	return EncryptWithAAD(kr.active, plaintext, aad)
}

// DecryptWithAAD is Decrypt for data produced by EncryptWithAAD with the same aad.
func (kr *KeyRing) DecryptWithAAD(data, aad []byte) ([]byte, error) {
	if out, err := DecryptWithAAD(kr.active, data, aad); err == nil {
		return out, nil
	}
	for _, k := range kr.old {
		if out, err := DecryptWithAAD(k, data, aad); err == nil {
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
	return kr.RotateWithAAD(data, nil)
}

// RotateWithAAD es Rotate para lo cifrado con EncryptWithAAD: abre y vuelve a cifrar con los
// mismos aad, de modo que el dato re-cifrado sigue atado a su fila.
func (kr *KeyRing) RotateWithAAD(data, aad []byte) (out []byte, rotated bool, err error) {
	if _, err := DecryptWithAAD(kr.active, data, aad); err == nil {
		return data, false, nil
	}
	for _, k := range kr.old {
		plain, err := DecryptWithAAD(k, data, aad)
		if err != nil {
			continue
		}
		out, err := EncryptWithAAD(kr.active, plain, aad)
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

// ErrUnknownKeyID: el identificador de llave no esta en el anillo. Es lo que distingue una
// llave retirada antes de tiempo de un dato manipulado.
var ErrUnknownKeyID = errors.New("crypto: unknown mac key id")

// macKeyLabel separa el identificador de la llave de cualquier otro uso del mismo HMAC.
const macKeyLabel = "core-force-mail/mac-key-id/v1"

type macKey struct {
	id  string
	key []byte
}

// MACKeyRing es el anillo de llaves HMAC-SHA256: la activa firma y las viejas solo sirven
// para volver a calcular lo firmado con ellas. Cada llave se identifica por un id derivado
// de la propia llave (HMAC de una etiqueta fija, no reversible), asi que el id se guarda junto
// a lo firmado sin exponerla ni depender de un nombre que alguien tenga que mantener.
//
// A diferencia del anillo AES, una firma no se re-cifra al rotar: lo firmado con una llave
// vieja solo se verifica con ella, y retirarla lo deja sin verificar.
type MACKeyRing struct {
	active macKey
	old    []macKey
}

// LoadMACKeyRing lee la llave activa (64 hex) de activeEnv y las retiradas (64 hex separadas
// por coma) de oldEnv. Devuelve nil, nil si no hay ninguna de las dos: el anillo es opcional
// para quien lo trata asi. Retiradas sin activa, una llave repetida o una mal formada son
// error, nunca un anillo a medias.
func LoadMACKeyRing(activeEnv, oldEnv string) (*MACKeyRing, error) {
	rawActive := strings.TrimSpace(os.Getenv(activeEnv))
	rawOld := strings.TrimSpace(os.Getenv(oldEnv))
	if rawActive == "" {
		if rawOld != "" {
			return nil, fmt.Errorf("%s: hay llaves retiradas pero falta la activa en %s", oldEnv, activeEnv)
		}
		return nil, nil
	}
	activeKey, err := parseHexKey(rawActive)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", activeEnv, err)
	}
	kr := &MACKeyRing{active: newMACKey(activeKey)}
	seen := map[string]bool{kr.active.id: true}
	for _, hk := range strings.Split(rawOld, ",") {
		hk = strings.TrimSpace(hk)
		if hk == "" {
			continue
		}
		k, err := parseHexKey(hk)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", oldEnv, err)
		}
		mk := newMACKey(k)
		if seen[mk.id] {
			return nil, fmt.Errorf("%s: llave repetida", oldEnv)
		}
		seen[mk.id] = true
		kr.old = append(kr.old, mk)
	}
	return kr, nil
}

func newMACKey(key []byte) macKey {
	return macKey{id: hex.EncodeToString(macSum(key, []byte(macKeyLabel))[:8]), key: key}
}

// ActiveID es el identificador de la llave con la que se firma.
func (kr *MACKeyRing) ActiveID() string { return kr.active.id }

// SignWith firma data con la llave de ese id, activa o retirada: con ActiveID se firma y con
// el id guardado junto a lo firmado se vuelve a calcular para verificarlo. ErrUnknownKeyID si
// el anillo no la tiene.
func (kr *MACKeyRing) SignWith(keyID string, data []byte) ([]byte, error) {
	if kr.active.id == keyID {
		return macSum(kr.active.key, data), nil
	}
	for _, k := range kr.old {
		if k.id == keyID {
			return macSum(k.key, data), nil
		}
	}
	return nil, ErrUnknownKeyID
}

// HasOldKeys dice si quedan llaves retiradas en el anillo.
func (kr *MACKeyRing) HasOldKeys() bool { return len(kr.old) > 0 }

func macSum(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func parseHexKey(hexKey string) ([]byte, error) {
	hexKey = strings.TrimSpace(hexKey)
	if len(hexKey) != 64 {
		return nil, errors.New("must be 64 hex chars (32 bytes)")
	}
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		// El error de hex nombra el caracter invalido, que es un caracter de la llave.
		return nil, errors.New("must be 64 hex chars (0-9, a-f)")
	}
	return key, nil
}

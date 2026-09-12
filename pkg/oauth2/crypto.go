package oauth2

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
	"golang.org/x/crypto/hkdf"
)

// Cipher cifra credenciales OAuth en reposo con AES-256-GCM bajo una clave
// DERIVADA POR EMPRESA (HKDF-SHA256) a partir de la clave maestra del servicio.
// Que la fuga del texto cifrado de una empresa no sirva para descifrar el de
// otra es la unica forma de que un backup o un volcado parcial no comprometa a
// todo el padron de clientes.
type Cipher struct {
	master  []byte
	purpose string
}

// NewCipherFromHex construye el cifrador desde una clave maestra en hexadecimal
// de 64 caracteres (32 bytes). Se exige el largo exacto para que una variable
// mal cargada falle al arrancar y no al guardar el primer token.
func NewCipherFromHex(hexKey, purpose string) (*Cipher, error) {
	if len(hexKey) != 64 {
		return nil, fmt.Errorf("oauth2: la clave de cifrado debe tener 64 caracteres hexadecimales")
	}
	master, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("oauth2: clave de cifrado invalida: %w", err)
	}
	if purpose == "" {
		return nil, errors.New("oauth2: falta el proposito de la clave derivada")
	}
	return &Cipher{master: master, purpose: purpose}, nil
}

func (c *Cipher) deriveKey(tenantID uuid.UUID) ([]byte, error) {
	reader := hkdf.New(sha256.New, c.master, nil, []byte(c.purpose+":"+tenantID.String()))
	key := make([]byte, 32)
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, err
	}
	return key, nil
}

// EncryptFor cifra para una empresa concreta. Un valor vacio no se cifra: se
// devuelve nil para poder distinguir en base "no hay credencial" de "hay una
// credencial que resulta ser la cadena vacia".
func (c *Cipher) EncryptFor(tenantID uuid.UUID, plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, nil
	}
	key, err := c.deriveKey(tenantID)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func (c *Cipher) DecryptFor(tenantID uuid.UUID, data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	key, err := c.deriveKey(tenantID)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(data) < gcm.NonceSize() {
		return nil, errors.New("oauth2: texto cifrado demasiado corto")
	}
	return gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

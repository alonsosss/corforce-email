// Package hmac calcula el hash de los secretos de las claves de API con HMAC-SHA256 y el anillo
// de llaves del almacen (API_KEY_HASH_KEY y API_KEY_HASH_KEYS_OLD, pkg/crypto.MACKeyRing).
//
// Un HMAC y no argon2id: el secreto son 256 bits aleatorios, asi que no hay diccionario que
// frenar, y el hash se calcula en cada resolucion (API y SMTP), donde un hash lento seria una
// palanca de denegacion de servicio. La llave del almacen hace que una copia de la tabla no
// sirva para probar secretos fuera de linea.
package hmac

import (
	"crypto/hmac"
	"errors"

	"github.com/alonsosss/corforce-email/pkg/crypto"
)

// ErrNoKey: sin llave activa no se crea ni se resuelve ninguna clave.
var ErrNoKey = errors.New("API_KEY_HASH_KEY no esta configurada")

// Hasher implementa ports.APIKeyHasher.
type Hasher struct {
	ring *crypto.MACKeyRing
}

// FromEnv lee el anillo. Devuelve error si falta la llave activa: el servicio no arranca sin ella
// fuera de desarrollo y prueba (lo decide main).
func FromEnv() (*Hasher, error) {
	ring, err := crypto.LoadMACKeyRing("API_KEY_HASH_KEY", "API_KEY_HASH_KEYS_OLD")
	if err != nil {
		return nil, err
	}
	if ring == nil {
		return nil, ErrNoKey
	}
	return &Hasher{ring: ring}, nil
}

func New(ring *crypto.MACKeyRing) *Hasher { return &Hasher{ring: ring} }

func (h *Hasher) Hash(input []byte) ([]byte, string, error) {
	id := h.ring.ActiveID()
	sum, err := h.ring.SignWith(id, input)
	return sum, id, err
}

func (h *Hasher) Verify(input, hash []byte, keyID string) (bool, bool, error) {
	sum, err := h.ring.SignWith(keyID, input)
	if err != nil {
		return false, false, err
	}
	return hmac.Equal(sum, hash), keyID == h.ring.ActiveID(), nil
}

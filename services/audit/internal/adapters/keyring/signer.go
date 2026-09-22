// Package keyring firma el informe de anclas con la llave activa de la cadena de hash
// (AUDIT_HASH_KEY): quien recibe el correo fuera del servidor lo comprueba con la misma llave.
package keyring

import "github.com/alonsosss/corforce-email/pkg/crypto"

type Signer struct{ ring *crypto.MACKeyRing }

// NewSigner devuelve nil sin anillo: el informe sale entonces sin firma y lo dice.
func NewSigner(ring *crypto.MACKeyRing) *Signer {
	if ring == nil {
		return nil
	}
	return &Signer{ring: ring}
}

func (s *Signer) KeyID() string { return s.ring.ActiveID() }

func (s *Signer) Sign(data []byte) []byte {
	// La llave activa siempre esta en el anillo: SignWith solo falla con un id ajeno.
	mac, _ := s.ring.SignWith(s.ring.ActiveID(), data)
	return mac
}

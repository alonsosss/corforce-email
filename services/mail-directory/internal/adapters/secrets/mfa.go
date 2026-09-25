package secrets

import (
	"time"

	"github.com/alonsosss/corforce-email/pkg/crypto"
	"github.com/alonsosss/corforce-email/pkg/totp"
)

// KeyRingSealer implementa ports.SecretSealer con el anillo de MAIL_ENCRYPTION_KEY (AES-256-GCM): la
// llave activa cifra y las retiradas (MAIL_ENCRYPTION_KEYS_OLD) siguen abriendo lo cifrado antes.
type KeyRingSealer struct{ ring *crypto.KeyRing }

func NewKeyRingSealer(ring *crypto.KeyRing) *KeyRingSealer { return &KeyRingSealer{ring: ring} }

func (s *KeyRingSealer) Seal(plain, aad []byte) ([]byte, error) {
	return s.ring.EncryptWithAAD(plain, aad)
}

func (s *KeyRingSealer) Open(sealed, aad []byte) ([]byte, error) {
	return s.ring.DecryptWithAAD(sealed, aad)
}

// TOTP implementa ports.TOTPVerifier con pkg/totp (RFC 6238, SHA-1, 6 cifras, 30 s, una ventana de
// margen a cada lado).
type TOTP struct{}

func (TOTP) ValidateStep(secret, code string, now time.Time) (int64, bool) {
	return totp.ValidateStep(secret, code, now)
}

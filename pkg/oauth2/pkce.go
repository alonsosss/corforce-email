package oauth2

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

// MethodS256 es el unico metodo de challenge que emitimos. "plain" esta
// permitido por el RFC 7636 pero no protege de nada cuando el codigo se
// intercepta, asi que no se ofrece.
const MethodS256 = "S256"

// PKCE es el par verifier/challenge del RFC 7636. Protege el canje del codigo
// de autorizacion aunque el codigo se filtre en el redirect: sin el verifier,
// que nunca sale del servidor, el codigo no vale nada.
type PKCE struct {
	Verifier  string
	Challenge string
	Method    string
}

// NewPKCE genera un verifier de 43 caracteres (32 bytes en base64url sin
// relleno), dentro del rango 43-128 que exige el RFC.
func NewPKCE() (PKCE, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return PKCE{}, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	return PKCE{Verifier: verifier, Challenge: ChallengeFor(verifier), Method: MethodS256}, nil
}

// ChallengeFor deriva el challenge S256 de un verifier ya existente.
func ChallengeFor(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ValidateVerifier comprueba la longitud que exige el RFC antes de usarlo.
func ValidateVerifier(verifier string) error {
	if len(verifier) < 43 || len(verifier) > 128 {
		return errors.New("oauth2: code_verifier fuera del rango 43-128")
	}
	return nil
}

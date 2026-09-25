// Package secrets implementa ports.Secrets: bcrypt para los hashes y crypto/rand para las
// contrasenas de aplicacion y los codigos de recuperacion que genera el servidor.
package secrets

import (
	"crypto/rand"
	"math/big"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"golang.org/x/crypto/bcrypt"
)

// appPasswordLength supera el minimo de 24 que exige el contrato; el alfabeto excluye
// caracteres ambiguos para que la persona pueda teclearla en un cliente de correo.
const (
	appPasswordLength   = 32
	appPasswordAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

type Bcrypt struct{}

func New() *Bcrypt { return &Bcrypt{} }

func (*Bcrypt) HashPassword(plain string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func (*Bcrypt) GenerateAppPassword() (string, error) {
	return randomString(appPasswordAlphabet, appPasswordLength)
}

func (*Bcrypt) GenerateRecoveryCode() (string, error) {
	return randomString(domain.RecoveryCodeAlphabet, domain.RecoveryCodeLength)
}

// randomString elige cada caracter de alphabet de forma uniforme con crypto/rand.
func randomString(alphabet string, length int) (string, error) {
	out := make([]byte, length)
	max := big.NewInt(int64(len(alphabet)))
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = alphabet[n.Int64()]
	}
	return string(out), nil
}

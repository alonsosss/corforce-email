// Package bcrypt implementa ports.PasswordVerifier con los hashes que escribe
// mail-directory al crear buzones y contrasenas de aplicacion.
package bcrypt

import (
	"crypto/rand"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// Verifier compara contrasenas con bcrypt y guarda un hash ficticio del mismo coste
// para las comparaciones contra usuarios inexistentes.
type Verifier struct {
	dummy string
}

// New genera el hash ficticio a partir de bytes aleatorios: ninguna contrasena real
// puede coincidir con el y su coste iguala al de produccion (DefaultCost, el mismo con
// el que identity y mail-directory generan los suyos).
func New() (*Verifier, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generar secreto del hash ficticio: %w", err)
	}
	hash, err := bcrypt.GenerateFromPassword(secret, bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("generar hash ficticio: %w", err)
	}
	return &Verifier{dummy: string(hash)}, nil
}

func (v *Verifier) Verify(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func (v *Verifier) DummyHash() string { return v.dummy }

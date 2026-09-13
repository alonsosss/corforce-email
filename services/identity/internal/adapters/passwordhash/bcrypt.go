// Package passwordhash calcula y compara los hashes de contrasena de las cuentas de identity.
package passwordhash

import (
	"fmt"

	"github.com/alonsosss/corforce-email/services/identity/internal/ports"
	"golang.org/x/crypto/bcrypt"
)

// BcryptCost es el coste de todo hash que escribe identity y del superadmin que siembra
// ops/db/bootstrap-platform.sh. Un solo coste para todas las cuentas: con dos, el tiempo de
// un inicio de sesion fallido distingue unas de otras y del correo que no existe.
const BcryptCost = bcrypt.DefaultCost

// Bcrypt es el hasher de contrasenas de identity, con un coste fijo.
type Bcrypt struct {
	cost int
}

// NewBcrypt rechaza un coste fuera de rango: bcrypt cambiaria en silencio uno demasiado bajo
// por su coste por defecto, y el hash de relleno del inicio de sesion dejaria de costar lo
// mismo que los reales.
func NewBcrypt(cost int) (*Bcrypt, error) {
	if cost < bcrypt.MinCost || cost > bcrypt.MaxCost {
		return nil, fmt.Errorf("coste bcrypt %d fuera de [%d, %d]", cost, bcrypt.MinCost, bcrypt.MaxCost)
	}
	return &Bcrypt{cost: cost}, nil
}

func (b *Bcrypt) Hash(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), b.cost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

func (b *Bcrypt) Compare(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

var _ ports.PasswordHasher = (*Bcrypt)(nil)

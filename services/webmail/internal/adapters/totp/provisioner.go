// Package totp prepara los secretos de la verificacion en dos pasos del webmail con pkg/totp (RFC
// 6238, SHA-1, 6 digitos, 30 s): los mismos parametros que valida mail-directory.
package totp

import (
	"errors"
	"strings"
	"unicode"

	pkgtotp "github.com/alonsosss/corforce-email/pkg/totp"
)

// MaxIssuerLen acota el emisor que muestran las aplicaciones de autenticacion.
const MaxIssuerLen = 64

// Provisioner implementa ports.TOTPProvisioner con el emisor del despliegue.
type Provisioner struct {
	issuer string
}

// New valida el emisor: va en la etiqueta de la URI otpauth://, donde ':' separa emisor y cuenta.
func New(issuer string) (*Provisioner, error) {
	issuer = strings.TrimSpace(issuer)
	if issuer == "" || len(issuer) > MaxIssuerLen || strings.Contains(issuer, ":") ||
		strings.IndexFunc(issuer, unicode.IsControl) >= 0 {
		return nil, errors.New("el emisor de la verificación en dos pasos debe tener de 1 a 64 caracteres, sin ':' ni caracteres de control")
	}
	return &Provisioner{issuer: issuer}, nil
}

func (p *Provisioner) NewSecret() (string, error) { return pkgtotp.GenerateSecret() }

func (p *Provisioner) ProvisioningURI(secret, account string) string {
	return pkgtotp.ProvisioningURI(secret, account, p.issuer)
}

package domain

import "github.com/alonsosss/corforce-email/pkg/mailcell"

// NormalizeMailDomain deja el nombre de un dominio de correo como lo guarda el indice global
// dominio -> empresa (minusculas, sin espacios) o responde ErrInvalidMailDomain. La regla es la
// misma con la que el gateway extrae el dominio del buzon en el inicio de sesion del webmail.
func NormalizeMailDomain(raw string) (string, error) {
	name, ok := mailcell.NormalizeDomain(raw)
	if !ok {
		return "", ErrInvalidMailDomain
	}
	return name, nil
}

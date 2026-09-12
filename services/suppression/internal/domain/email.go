package domain

import (
	"regexp"
	"strings"
)

// maxEmailLength es el limite de una direccion completa (RFC 5321: 64 de parte local,
// 255 de dominio y la arroba).
const maxEmailLength = 320

// emailRegex es deliberadamente la misma forma que acepta pkg/validate: parte local
// ASCII sin espacios, dominio con al menos un punto y TLD alfabetico. Se repite aqui y
// no se importa porque el dominio no depende de paquetes de infraestructura.
var emailRegex = regexp.MustCompile(`^[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}$`)

// NormalizeEmail deja la direccion como se guarda y se compara: sin espacios alrededor
// y en minusculas. La parte local es, en teoria, sensible a mayusculas, pero ningun
// proveedor real la distingue y una lista de supresion que si lo hiciera dejaria pasar
// envios a la misma persona escritos con otra capitalizacion.
func NormalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" || len(email) > maxEmailLength || !emailRegex.MatchString(email) {
		return "", ErrInvalidEmail
	}
	return email, nil
}

// NormalizeEmails normaliza una lista, descarta las direcciones invalidas y las
// repetidas, y devuelve cuantas se descartaron. El orden de las validas se conserva.
func NormalizeEmails(raw []string) (valid []string, discarded int) {
	seen := make(map[string]struct{}, len(raw))
	for _, r := range raw {
		email, err := NormalizeEmail(r)
		if err != nil {
			discarded++
			continue
		}
		if _, dup := seen[email]; dup {
			discarded++
			continue
		}
		seen[email] = struct{}{}
		valid = append(valid, email)
	}
	return valid, discarded
}

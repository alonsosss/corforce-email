package domain

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
)

// MaxTestRecipients acota el envio de prueba de una version. Es el mismo tope que aplica
// transactional, que es quien envia; aqui se comprueba antes para no llamarle en balde.
const MaxTestRecipients = 5

var (
	// ErrInvalidTestSend envuelve los defectos de la peticion de prueba: destinatarios,
	// remitente o respuesta.
	ErrInvalidTestSend = errors.New("envío de prueba no válido")
	// ErrTestSendUnavailable: el servicio arranco sin TRANSACTIONAL_URL o transactional no
	// respondio; la prueba no salio.
	ErrTestSendUnavailable = errors.New("el envío de prueba no está disponible")
)

// TestSendRejectedError es el rechazo de la prueba por transactional (remitente sin
// verificar, supresion no disponible, tope de pruebas, denegacion de reputation): se
// devuelve tal cual lo decidio, con su estado, su codigo y la espera si la dio.
type TestSendRejectedError struct {
	Status     int
	Code       string
	Message    string
	RetryAfter string
}

func (e *TestSendRejectedError) Error() string {
	return fmt.Sprintf("transactional rechazó la prueba (%d %s): %s", e.Status, e.Code, e.Message)
}

// NormalizeTestRecipients recorta y valida los destinatarios de una prueba: entre 1 y
// MaxTestRecipients direcciones validas y sin repetir (sin distinguir mayusculas).
func NormalizeTestRecipients(to []string) ([]string, error) {
	if len(to) == 0 || len(to) > MaxTestRecipients {
		return nil, fmt.Errorf("%w: indique entre 1 y %d destinatarios", ErrInvalidTestSend, MaxTestRecipients)
	}
	out := make([]string, 0, len(to))
	seen := make(map[string]bool, len(to))
	for i, raw := range to {
		email := strings.TrimSpace(raw)
		if !ValidEmailAddress(email) {
			return nil, fmt.Errorf("%w: to[%d] no es un correo válido", ErrInvalidTestSend, i)
		}
		key := strings.ToLower(email)
		if seen[key] {
			return nil, fmt.Errorf("%w: %s está repetido", ErrInvalidTestSend, email)
		}
		seen[key] = true
		out = append(out, email)
	}
	return out, nil
}

// ValidEmailAddress dice si s es una direccion sola, sin nombre ni corchetes.
func ValidEmailAddress(s string) bool {
	if len(s) > 320 {
		return false
	}
	addr, err := mail.ParseAddress(s)
	return err == nil && addr.Address == s && strings.Contains(s, "@")
}

package domain

import (
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// MaxPrimarySenders acota los remitentes que un buzon fija en Principal: el webmail los traduce
	// a una busqueda IMAP, que no admite una lista sin limite.
	MaxPrimarySenders = 200
	// MaxPrimarySenderAddressBytes es el tope de una direccion de correo (RFC 5321).
	MaxPrimarySenderAddressBytes = 320
)

// PrimarySender es una direccion que el buzon quiere siempre en Principal. Address va en minusculas.
type PrimarySender struct {
	ID        uuid.UUID `json:"id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	Username  string    `json:"username"`
	Address   string    `json:"address"`
	CreatedAt time.Time `json:"created_at"`
}

// NormalizePrimarySenderAddress devuelve la direccion en minusculas o un error de validacion. Solo
// admite una direccion simple: sin nombre, sin varias y sin grupos.
func NormalizePrimarySenderAddress(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fieldErr("address", "es obligatoria")
	}
	if len(raw) > MaxPrimarySenderAddressBytes {
		return "", fieldErr("address", "es demasiado larga")
	}
	parsed, err := mail.ParseAddress(raw)
	if err != nil || parsed.Name != "" || !strings.EqualFold(parsed.Address, raw) {
		return "", fieldErr("address", "no es una dirección de correo válida")
	}
	return strings.ToLower(parsed.Address), nil
}

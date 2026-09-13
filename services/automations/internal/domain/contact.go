package domain

import (
	"encoding/json"
	"strings"

	"github.com/google/uuid"
)

// Contact es lo que contacts entrega de un contacto enviable: lo justo para personalizar.
type Contact struct {
	ID         uuid.UUID
	Email      string
	FirstName  string
	LastName   string
	Attributes map[string]json.RawMessage
}

// DisplayName es el nombre para la cabecera To.
func (c Contact) DisplayName() string {
	return strings.TrimSpace(strings.TrimSpace(c.FirstName) + " " + strings.TrimSpace(c.LastName))
}

// Variables son las del destinatario: sus atributos y, por encima, first_name, last_name y
// email, que un atributo con el mismo nombre no puede pisar. Es el mismo contrato que usan
// las campanas, asi que una plantilla de marketing sirve para las dos.
func (c Contact) Variables() map[string]json.RawMessage {
	vars := make(map[string]json.RawMessage, len(c.Attributes)+3)
	for k, v := range c.Attributes {
		vars[k] = v
	}
	vars["first_name"] = jsonString(c.FirstName)
	vars["last_name"] = jsonString(c.LastName)
	vars["email"] = jsonString(c.Email)
	return vars
}

func jsonString(s string) json.RawMessage {
	b, _ := json.Marshal(strings.TrimSpace(s))
	return b
}

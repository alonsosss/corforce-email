package app

import (
	"context"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
)

const (
	DefaultDirectorySearchLimit = 20
	MaxDirectorySearchLimit     = 50
)

// DirectoryEntry es lo unico de un buzon que se comparte con sus companeros de empresa: la
// direccion y el nombre visible. Nada de cuota, accesos ni credenciales.
type DirectoryEntry struct {
	Address     string
	DisplayName string
}

// SearchDirectoryByUsername sirve la libreta de direcciones compartida del webmail: los buzones
// activos de la empresa de quien pregunta. La empresa sale del buzon con el que el webmail abrio la
// sesion (no del cuerpo ni de la consulta), asi que nadie ve el directorio de otra empresa.
func (uc *UseCase) SearchDirectoryByUsername(ctx context.Context, username, search string, limit int) ([]DirectoryEntry, error) {
	tenantID, _, err := uc.locate(ctx, username)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = DefaultDirectorySearchLimit
	}
	if limit > MaxDirectorySearchLimit {
		limit = MaxDirectorySearchLimit
	}
	boxes, _, err := uc.ListMailboxes(ctx, tenantID, ports.MailboxFilter{Search: search, ActiveOnly: true}, ports.Page{Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]DirectoryEntry, 0, len(boxes))
	for _, m := range boxes {
		out = append(out, DirectoryEntry{Address: m.Username, DisplayName: m.DisplayName})
	}
	return out, nil
}

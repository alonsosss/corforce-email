// Package catalog lee el catalogo de manejadores del scheduler (handlers.json): la lista
// blanca de manejadores a los que puede apuntar un trabajo, con el servicio que los ejecuta.
// Vive en datos, como las rutas del gateway, para que sumar un ejecutor no obligue a tocar
// codigo del scheduler.
package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
)

type document struct {
	Handlers []entry `json:"handlers"`
}

type entry struct {
	Name              string   `json:"name"`
	Service           string   `json:"service"`
	Description       string   `json:"description,omitempty"`
	MaxTimeoutSeconds int      `json:"max_timeout_seconds"`
	Scopes            []string `json:"scopes"`
}

// Parse lee y valida el catalogo. Rechaza claves desconocidas: una errata en el fichero
// debe impedir el arranque, no dejar un manejador a medias.
func Parse(raw []byte) (*domain.HandlerCatalog, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var doc document
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("catalogo de manejadores: %w", err)
	}
	if dec.More() {
		return nil, errors.New("catalogo de manejadores: el fichero debe contener un solo objeto")
	}
	if doc.Handlers == nil {
		return nil, errors.New(`catalogo de manejadores: falta la lista "handlers" (vacia si no hay ninguno)`)
	}
	specs := make([]domain.HandlerSpec, 0, len(doc.Handlers))
	for _, e := range doc.Handlers {
		scopes := make([]domain.HandlerScope, 0, len(e.Scopes))
		for _, s := range e.Scopes {
			scopes = append(scopes, domain.HandlerScope(s))
		}
		specs = append(specs, domain.HandlerSpec{
			Name:              e.Name,
			Service:           e.Service,
			Description:       e.Description,
			MaxTimeoutSeconds: e.MaxTimeoutSeconds,
			Scopes:            scopes,
		})
	}
	return domain.NewHandlerCatalog(specs)
}

// Package templatesclient averigua la version publicada de una plantilla.
//
// templates no expone aun una lectura interna de plantillas; lo unico interno que hay es
// el renderizado (POST /internal/templates/{id}/render), que sin version usa la
// publicada y devuelve su numero. Se renderiza sin variables: si la plantilla declara
// alguna requerida sin valor por defecto, templates responde 422 y el numero no se puede
// saber desde aqui (domain.ErrTemplateVersionRequired). Si la respuesta trae el tipo de
// la plantilla (kind), se exige marketing; si no lo trae, lo valida transactional en el
// primer lote o en el envio de prueba.
package templatesclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/adapters/internalapi"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/ports"
	"github.com/google/uuid"
)

// kindMarketing es el tipo de plantilla que admite una campana.
const kindMarketing = "marketing"

type Client struct {
	caller *internalapi.Caller
}

// New: renderizar no tiene efectos; es seguro repetirlo.
func New(baseURL, token string) *Client {
	return &Client{caller: internalapi.NewCaller("templates", baseURL, token,
		httpclient.Options{Timeout: 10 * time.Second, MaxAttempts: 2})}
}

func (c *Client) PublishedVersion(ctx context.Context, tenantID, templateID uuid.UUID) (int, error) {
	var out struct {
		Data struct {
			Version int    `json:"version"`
			Kind    string `json:"kind"`
		} `json:"data"`
	}
	err := c.caller.Post(ctx, tenantID, "/internal/templates/"+templateID.String()+"/render", map[string]any{
		"variables": map[string]any{},
		"reserved":  map[string]string{},
	}, true, &out)
	if err != nil {
		var se *internalapi.StatusError
		if !errors.As(err, &se) {
			return 0, err
		}
		switch se.Status {
		case http.StatusNotFound:
			return 0, domain.ErrTemplateNotFound
		case http.StatusConflict:
			return 0, fmt.Errorf("%w (templates: %s)", domain.ErrNoPublishedVersion, se.Message)
		case http.StatusBadRequest, http.StatusUnprocessableEntity:
			return 0, domain.ErrTemplateVersionRequired
		}
		return 0, fmt.Errorf("%w: templates: %v", ports.ErrUnavailable, se)
	}
	if out.Data.Version < 1 {
		return 0, fmt.Errorf("%w: templates no devolvió el número de versión", ports.ErrUnavailable)
	}
	if out.Data.Kind != "" && out.Data.Kind != kindMarketing {
		return 0, domain.ErrTemplateNotMarketing
	}
	return out.Data.Version, nil
}

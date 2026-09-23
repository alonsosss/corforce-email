// Package transactionalclient entrega los lotes de una campana a la via de marketing de
// transactional (POST /internal/transactional/batch).
package transactionalclient

import (
	"context"
	"encoding/json"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/adapters/internalapi"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/ports"
	"github.com/google/uuid"
)

const (
	batchPath = "/internal/transactional/batch"
	// classMarketing selecciona el carril de marketing de transactional: su cola, su
	// configuration set y su tasa, separados de los del correo transaccional.
	classMarketing = "marketing"
)

type Client struct {
	caller *internalapi.Caller
}

// New: sin reintentos dentro de la llamada. El orquestador reintenta el lote en el
// siguiente tick con la misma clave, respetando Retry-After y la espera creciente; un
// reintento inmediato aqui solo apretaria a un transactional que ya pidio aire.
func New(baseURL, token string) *Client {
	return &Client{caller: internalapi.NewCaller("transactional", baseURL, token,
		httpclient.Options{Timeout: 60 * time.Second, MaxAttempts: 1})}
}

type fromAddress struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type batchRequest struct {
	Class           string             `json:"class"`
	CampaignID      uuid.UUID          `json:"campaign_id"`
	IdempotencyKey  string             `json:"idempotency_key"`
	From            fromAddress        `json:"from"`
	ReplyTo         string             `json:"reply_to,omitempty"`
	TemplateID      uuid.UUID          `json:"template_id"`
	TemplateVersion int                `json:"template_version"`
	Recipients      []domain.Recipient `json:"recipients"`
	Tags            map[string]string  `json:"tags,omitempty"`
	UTM             *utmSettings       `json:"utm,omitempty"`
}

// utmSettings es el objeto utm del lote: transactional normaliza los valores y deriva
// utm_source del remitente.
type utmSettings struct {
	Campaign string `json:"campaign,omitempty"`
}

func (c *Client) SendBatch(ctx context.Context, tenantID uuid.UUID, r ports.BatchRequest) (*ports.BatchResult, error) {
	recipients := make([]domain.Recipient, len(r.Recipients))
	for i, rc := range r.Recipients {
		if rc.Variables == nil {
			rc.Variables = map[string]json.RawMessage{}
		}
		recipients[i] = rc
	}
	var out struct {
		Data ports.BatchResult `json:"data"`
	}
	err := c.caller.Post(ctx, tenantID, batchPath, batchRequest{
		Class:           classMarketing,
		CampaignID:      r.CampaignID,
		IdempotencyKey:  r.IdempotencyKey,
		From:            fromAddress{Email: r.FromEmail, Name: r.FromName},
		ReplyTo:         r.ReplyTo,
		TemplateID:      r.TemplateID,
		TemplateVersion: r.TemplateVersion,
		Recipients:      recipients,
		Tags:            r.Tags,
		UTM:             &utmSettings{Campaign: r.CampaignName},
	}, true, &out)
	if err != nil {
		return nil, internalapi.Classify(err)
	}
	if out.Data.Suppressed == nil {
		out.Data.Suppressed = []ports.SuppressedRecipient{}
	}
	if out.Data.MessageIDs == nil {
		out.Data.MessageIDs = []uuid.UUID{}
	}
	return &out.Data, nil
}

// Package transactionalclient envia por transactional: el correo del doble opt-in por su
// envio interno (POST /internal/transactional/messages, proposito double_opt_in) y los
// pasos de envio de los flujos por la via de marketing (POST /internal/transactional/batch).
package transactionalclient

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/services/automations/internal/adapters/internalapi"
	"github.com/alonsosss/corforce-email/services/automations/internal/ports"
	"github.com/google/uuid"
)

const (
	messagesPath = "/internal/transactional/messages"
	batchPath    = "/internal/transactional/batch"
	// purposeDoubleOptIn: transactional no bloquea este correo por una baja voluntaria.
	purposeDoubleOptIn = "double_opt_in"
	// classMarketing selecciona el carril de marketing de transactional.
	classMarketing = "marketing"
)

type Client struct {
	caller *internalapi.Caller
}

// New: sin reintentos dentro de la llamada. Reintenta quien llama (el consumidor con la
// reentrega del evento, el ejecutor en el siguiente tick) con la misma clave, respetando
// Retry-After; repetir aqui solo apretaria a un transactional que ya pidio aire.
func New(baseURL, token string) *Client {
	return &Client{caller: internalapi.NewCaller("transactional", baseURL, token,
		httpclient.Options{Timeout: 30 * time.Second, MaxAttempts: 1})}
}

type address struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type messageRequest struct {
	From       address           `json:"from"`
	ReplyTo    string            `json:"reply_to,omitempty"`
	To         []address         `json:"to"`
	TemplateID uuid.UUID         `json:"template_id"`
	Variables  map[string]any    `json:"variables,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
	Purpose    string            `json:"purpose"`
}

type messageResponse struct {
	Data struct {
		Messages []struct {
			ID     uuid.UUID `json:"id"`
			Status string    `json:"status"`
		} `json:"messages"`
		Suppressed []ports.Suppressed `json:"suppressed"`
	} `json:"data"`
}

func (c *Client) SendDOI(ctx context.Context, tenantID uuid.UUID, m ports.DOIMessage) (*ports.DOIResult, error) {
	header := http.Header{}
	header.Set("Idempotency-Key", m.IdempotencyKey)
	var out messageResponse
	err := c.caller.Post(ctx, tenantID, messagesPath, messageRequest{
		From:       address{Email: m.FromEmail, Name: m.FromName},
		ReplyTo:    m.ReplyTo,
		To:         []address{{Email: m.ToEmail, Name: m.ToName}},
		TemplateID: m.TemplateID,
		Variables:  m.Variables,
		Tags:       m.Tags,
		Purpose:    purposeDoubleOptIn,
	}, false, header, &out)
	if err != nil {
		return nil, internalapi.Classify(err)
	}
	res := &ports.DOIResult{Suppressed: out.Data.Suppressed}
	if len(out.Data.Messages) > 0 {
		id := out.Data.Messages[0].ID
		res.MessageID = &id
		res.Status = out.Data.Messages[0].Status
	}
	return res, nil
}

type batchRecipient struct {
	Email     string                     `json:"email"`
	Name      string                     `json:"name,omitempty"`
	ContactID uuid.UUID                  `json:"contact_id"`
	Variables map[string]json.RawMessage `json:"variables"`
}

type batchRequest struct {
	Class           string            `json:"class"`
	CampaignID      uuid.UUID         `json:"campaign_id"`
	IdempotencyKey  string            `json:"idempotency_key"`
	From            address           `json:"from"`
	ReplyTo         string            `json:"reply_to,omitempty"`
	TemplateID      uuid.UUID         `json:"template_id"`
	TemplateVersion int               `json:"template_version"`
	Recipients      []batchRecipient  `json:"recipients"`
	Tags            map[string]string `json:"tags,omitempty"`
}

// SendMarketing entrega un lote de un destinatario con campaign_id = id del flujo: para
// transactional, analytics y reputation un flujo es una campana mas.
func (c *Client) SendMarketing(ctx context.Context, tenantID uuid.UUID, m ports.MarketingMessage) (*ports.BatchResult, error) {
	var out struct {
		Data struct {
			Accepted   int                `json:"accepted"`
			Suppressed []ports.Suppressed `json:"suppressed"`
			MessageIDs []uuid.UUID        `json:"message_ids"`
		} `json:"data"`
	}
	err := c.caller.Post(ctx, tenantID, batchPath, batchRequest{
		Class:           classMarketing,
		CampaignID:      m.WorkflowID,
		IdempotencyKey:  m.IdempotencyKey,
		From:            address{Email: m.FromEmail, Name: m.FromName},
		ReplyTo:         m.ReplyTo,
		TemplateID:      m.TemplateID,
		TemplateVersion: m.TemplateVersion,
		Recipients: []batchRecipient{{
			Email: m.Contact.Email, Name: m.Contact.DisplayName(), ContactID: m.Contact.ID, Variables: m.Contact.Variables(),
		}},
		Tags: m.Tags,
	}, false, nil, &out)
	if err != nil {
		return nil, internalapi.Classify(err)
	}
	return &ports.BatchResult{Accepted: out.Data.Accepted, Suppressed: out.Data.Suppressed, MessageIDs: out.Data.MessageIDs}, nil
}

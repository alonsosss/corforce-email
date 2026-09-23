package ports

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

// TestSendRequest es la prueba de una version que se pide a transactional
// (POST /internal/transactional/test-send).
type TestSendRequest struct {
	TemplateID  uuid.UUID
	Version     int
	FromEmail   string
	FromName    string
	ReplyTo     string
	To          []string
	Variables   map[string]json.RawMessage
	RequestedBy uuid.UUID
}

// TestSendMessage es un mensaje de prueba encolado.
type TestSendMessage struct {
	ID     uuid.UUID `json:"id"`
	Status string    `json:"status"`
	Email  string    `json:"email"`
}

// TestSendSuppressed es un destinatario que la lista de supresion retiro.
type TestSendSuppressed struct {
	Email  string `json:"email"`
	Reason string `json:"reason"`
}

// TestSendResult es lo que respondio transactional.
type TestSendResult struct {
	Messages   []TestSendMessage    `json:"messages"`
	Suppressed []TestSendSuppressed `json:"suppressed"`
}

// TestSender envia la prueba por transactional. Un rechazo de transactional llega como
// *domain.TestSendRejectedError; una caida, como domain.ErrTestSendUnavailable.
type TestSender interface {
	SendTest(ctx context.Context, tenantID uuid.UUID, req TestSendRequest) (*TestSendResult, error)
}

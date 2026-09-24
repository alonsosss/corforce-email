// Package mime lee el DATA con pkg/rawmail, el mismo lector que usa transactional, para rechazar
// en la sesion lo que transactional rechazaria y saber si el mensaje lleva adjuntos.
package mime

import (
	"errors"
	"fmt"

	"github.com/alonsosss/corforce-email/pkg/rawmail"
	"github.com/alonsosss/corforce-email/services/smtp-relay/internal/domain"
)

// Inspector implementa ports.Inspector.
type Inspector struct {
	limits rawmail.Limits
}

func New(maxBytes int) *Inspector {
	return &Inspector{limits: rawmail.Limits{MaxBytes: maxBytes}}
}

func (i *Inspector) Inspect(raw []byte) (domain.Inspection, error) {
	msg, err := rawmail.Parse(raw, i.limits)
	switch {
	case err == nil:
		return domain.Inspection{HasAttachments: msg.HasAttachments, MessageID: msg.MessageID}, nil
	case errors.Is(err, rawmail.ErrTooLarge):
		return domain.Inspection{}, domain.ErrTooLarge
	default:
		return domain.Inspection{}, fmt.Errorf("%w: %v", domain.ErrMalformed, err)
	}
}

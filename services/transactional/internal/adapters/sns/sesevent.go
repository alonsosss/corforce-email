package sns

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/google/uuid"
)

// ErrNotOurs: la notificacion no lleva las etiquetas tenant_id y message_id que este
// servicio pone en cada envio.
var ErrNotOurs = errors.New("ses: el evento no lleva las etiquetas del servicio")

// sesEvent es el JSON que SES publica en el topic del configuration set. Solo se leen
// los campos que el servicio usa.
type sesEvent struct {
	EventType        string `json:"eventType"`
	NotificationType string `json:"notificationType"`
	Mail             struct {
		Timestamp   string              `json:"timestamp"`
		MessageID   string              `json:"messageId"`
		Source      string              `json:"source"`
		Destination []string            `json:"destination"`
		Tags        map[string][]string `json:"tags"`
	} `json:"mail"`
	Bounce *struct {
		BounceType        string `json:"bounceType"`
		BounceSubType     string `json:"bounceSubType"`
		Timestamp         string `json:"timestamp"`
		BouncedRecipients []struct {
			EmailAddress   string `json:"emailAddress"`
			Status         string `json:"status"`
			DiagnosticCode string `json:"diagnosticCode"`
		} `json:"bouncedRecipients"`
	} `json:"bounce"`
	Complaint *struct {
		ComplaintFeedbackType string `json:"complaintFeedbackType"`
		Timestamp             string `json:"timestamp"`
		ComplainedRecipients  []struct {
			EmailAddress string `json:"emailAddress"`
		} `json:"complainedRecipients"`
	} `json:"complaint"`
	Delivery *struct {
		Timestamp  string   `json:"timestamp"`
		Recipients []string `json:"recipients"`
	} `json:"delivery"`
	Reject *struct {
		Reason string `json:"reason"`
	} `json:"reject"`
	DeliveryDelay *struct {
		DelayType         string `json:"delayType"`
		Timestamp         string `json:"timestamp"`
		DelayedRecipients []struct {
			EmailAddress string `json:"emailAddress"`
		} `json:"delayedRecipients"`
	} `json:"deliveryDelay"`
	Open *struct {
		Timestamp string `json:"timestamp"`
		IPAddress string `json:"ipAddress"`
		UserAgent string `json:"userAgent"`
	} `json:"open"`
	Click *struct {
		Timestamp string `json:"timestamp"`
		Link      string `json:"link"`
	} `json:"click"`
	Failure *struct {
		ErrorMessage string `json:"errorMessage"`
		TemplateName string `json:"templateName"`
	} `json:"failure"`
	Subscription *struct {
		Timestamp string `json:"timestamp"`
		Source    string `json:"source"`
	} `json:"subscription"`
}

// ParseSESEvent convierte el Message de una Notification en un evento del dominio.
func ParseSESEvent(message []byte, snsMessageID string) (domain.InboundEvent, error) {
	var raw sesEvent
	if err := json.Unmarshal(message, &raw); err != nil {
		return domain.InboundEvent{}, fmt.Errorf("ses: evento ilegible: %w", err)
	}
	sesType := raw.EventType
	if sesType == "" {
		sesType = raw.NotificationType
	}
	eventType := domain.NormalizeSESEventType(sesType)
	if eventType == "" {
		return domain.InboundEvent{}, fmt.Errorf("ses: eventType %q no reconocido", sesType)
	}
	tenantID, err := tagUUID(raw.Mail.Tags, "tenant_id")
	if err != nil {
		return domain.InboundEvent{}, err
	}
	messageID, err := tagUUID(raw.Mail.Tags, "message_id")
	if err != nil {
		return domain.InboundEvent{}, err
	}

	ev := domain.InboundEvent{
		Type:         eventType,
		TenantID:     tenantID,
		MessageID:    messageID,
		SNSMessageID: snsMessageID,
		Recipients:   raw.Mail.Destination,
		OccurredAt:   parseTime(raw.Mail.Timestamp),
		Detail:       map[string]any{"ses_message_id": raw.Mail.MessageID},
	}
	switch eventType {
	case domain.EventBounce:
		if raw.Bounce != nil {
			ev.Recipients = nil
			for _, r := range raw.Bounce.BouncedRecipients {
				ev.Recipients = append(ev.Recipients, r.EmailAddress)
			}
			ev.BounceType = domain.BounceTypeTransient
			if strings.EqualFold(raw.Bounce.BounceType, "Permanent") {
				ev.BounceType = domain.BounceTypePermanent
			}
			ev.Detail["bounce_type"] = raw.Bounce.BounceType
			ev.Detail["bounce_sub_type"] = raw.Bounce.BounceSubType
			ev.Detail["reason"] = raw.Bounce.BounceType + "/" + raw.Bounce.BounceSubType
			if len(raw.Bounce.BouncedRecipients) > 0 {
				ev.Detail["diagnostic_code"] = raw.Bounce.BouncedRecipients[0].DiagnosticCode
				ev.Detail["status"] = raw.Bounce.BouncedRecipients[0].Status
			}
			ev.OccurredAt = firstTime(raw.Bounce.Timestamp, ev.OccurredAt)
		}
	case domain.EventComplaint:
		if raw.Complaint != nil {
			ev.Recipients = nil
			for _, r := range raw.Complaint.ComplainedRecipients {
				ev.Recipients = append(ev.Recipients, r.EmailAddress)
			}
			ev.Detail["feedback_type"] = raw.Complaint.ComplaintFeedbackType
			ev.Detail["reason"] = raw.Complaint.ComplaintFeedbackType
			ev.OccurredAt = firstTime(raw.Complaint.Timestamp, ev.OccurredAt)
		}
	case domain.EventDelivery:
		if raw.Delivery != nil {
			if len(raw.Delivery.Recipients) > 0 {
				ev.Recipients = raw.Delivery.Recipients
			}
			ev.OccurredAt = firstTime(raw.Delivery.Timestamp, ev.OccurredAt)
		}
	case domain.EventReject:
		if raw.Reject != nil {
			ev.Detail["reason"] = raw.Reject.Reason
		}
	case domain.EventDeliveryDelay:
		if raw.DeliveryDelay != nil {
			ev.Recipients = nil
			for _, r := range raw.DeliveryDelay.DelayedRecipients {
				ev.Recipients = append(ev.Recipients, r.EmailAddress)
			}
			ev.Detail["delay_type"] = raw.DeliveryDelay.DelayType
			ev.OccurredAt = firstTime(raw.DeliveryDelay.Timestamp, ev.OccurredAt)
		}
	case domain.EventOpen:
		if raw.Open != nil {
			ev.Detail["ip_address"] = raw.Open.IPAddress
			ev.Detail["user_agent"] = raw.Open.UserAgent
			ev.OccurredAt = firstTime(raw.Open.Timestamp, ev.OccurredAt)
		}
	case domain.EventClick:
		if raw.Click != nil {
			ev.Detail["link"] = raw.Click.Link
			ev.OccurredAt = firstTime(raw.Click.Timestamp, ev.OccurredAt)
		}
	case domain.EventRenderingFailure:
		if raw.Failure != nil {
			ev.Detail["error"] = raw.Failure.ErrorMessage
			ev.Detail["template"] = raw.Failure.TemplateName
		}
	case domain.EventSubscription:
		if raw.Subscription != nil {
			ev.Detail["source"] = raw.Subscription.Source
			ev.OccurredAt = firstTime(raw.Subscription.Timestamp, ev.OccurredAt)
		}
	}
	if len(ev.Recipients) == 0 {
		ev.Recipients = raw.Mail.Destination
	}
	return ev, nil
}

func tagUUID(tags map[string][]string, name string) (uuid.UUID, error) {
	values := tags[name]
	if len(values) == 0 {
		return uuid.Nil, fmt.Errorf("%w: falta %s", ErrNotOurs, name)
	}
	id, err := uuid.Parse(values[0])
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: %s inválido", ErrNotOurs, name)
	}
	return id, nil
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func firstTime(s string, fallback time.Time) time.Time {
	if t := parseTime(s); !t.IsZero() {
		return t
	}
	return fallback
}

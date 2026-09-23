package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// DeliveryKind es el hecho de entrega que transactional publica como
// transactional.email.<kind> y que suma uno al contador del mismo nombre.
type DeliveryKind string

const (
	KindSent         DeliveryKind = "sent"
	KindDelivered    DeliveryKind = "delivered"
	KindBounced      DeliveryKind = "bounced"
	KindComplained   DeliveryKind = "complained"
	KindOpened       DeliveryKind = "opened"
	KindClicked      DeliveryKind = "clicked"
	KindUnsubscribed DeliveryKind = "unsubscribed"
	KindFailed       DeliveryKind = "failed"
)

const deliveryEventPrefix = "transactional.email."

// ParseDeliveryKind extrae el tipo de un transactional.email.<kind>. ok=false si el
// evento no alimenta ningun contador.
func ParseDeliveryKind(eventType string) (DeliveryKind, bool) {
	if !strings.HasPrefix(eventType, deliveryEventPrefix) {
		return "", false
	}
	k := DeliveryKind(strings.TrimPrefix(eventType, deliveryEventPrefix))
	switch k {
	case KindSent, KindDelivered, KindBounced, KindComplained, KindOpened, KindClicked, KindUnsubscribed, KindFailed:
		return k, true
	}
	return "", false
}

// UniquePerMessage: las aperturas y los clics se cuentan por mensaje, no por evento. Un
// destinatario que abre cinco veces es una apertura.
func (k DeliveryKind) UniquePerMessage() bool {
	return k == KindOpened || k == KindClicked
}

// DeliveryEvent es un evento de transactional atribuido a una campana.
type DeliveryEvent struct {
	EventID    string
	TenantID   uuid.UUID
	CampaignID uuid.UUID
	MessageID  *uuid.UUID
	ContactID  *uuid.UUID
	Kind       DeliveryKind
	OccurredAt time.Time
}

// PhaseEngagement son los mensajes aceptados de una fase (y variante) y cuantos se
// entregaron, abrieron y recibieron clic, contados una vez por mensaje.
type PhaseEngagement struct {
	Kind      PhaseKind
	Variant   *int
	Accepted  int64
	Delivered int64
	Opened    int64
	Clicked   int64
}

// SampleResults extrae de las filas de interaccion los resultados de cada variante de la
// muestra.
func SampleResults(rows []PhaseEngagement) []VariantResult {
	var out []VariantResult
	for _, r := range rows {
		if r.Kind != PhaseSample || r.Variant == nil {
			continue
		}
		out = append(out, VariantResult{Variant: *r.Variant, Accepted: r.Accepted, Delivered: r.Delivered,
			Opened: r.Opened, Clicked: r.Clicked})
	}
	return out
}

// MaxEventIDLength acota la clave de deduplicacion que se guarda.
const MaxEventIDLength = 200

// ProcessedEventRetention es cuanto se recuerda un evento ya contado. Supera con
// holgura la retencion del stream TRANSACTIONAL (7 dias): JetStream no puede reentregar
// un evento que ya se olvido aqui.
const ProcessedEventRetention = 30 * 24 * time.Hour

// Rates son las tasas de la campana como fraccion decimal con cuatro cifras ("0.2512").
// Entrega y rebote se miden sobre lo enviado; apertura, clic, queja y baja, sobre lo
// entregado. Un denominador en cero da "0.0000".
type Rates struct {
	DeliveryRate    string `json:"delivery_rate"`
	OpenRate        string `json:"open_rate"`
	ClickRate       string `json:"click_rate"`
	BounceRate      string `json:"bounce_rate"`
	ComplaintRate   string `json:"complaint_rate"`
	UnsubscribeRate string `json:"unsubscribe_rate"`
}

const rateDecimals = 4

func (c Counters) Rates() Rates {
	return Rates{
		DeliveryRate:    ratio(c.Delivered, c.Sent),
		OpenRate:        ratio(c.Opened, c.Delivered),
		ClickRate:       ratio(c.Clicked, c.Delivered),
		BounceRate:      ratio(c.Bounced, c.Sent),
		ComplaintRate:   ratio(c.Complained, c.Delivered),
		UnsubscribeRate: ratio(c.Unsubscribed, c.Delivered),
	}
}

// Ratio es n/d con cuatro cifras ("0.2512"); "0.0000" si el denominador es cero.
func Ratio(n, d int64) string { return ratio(n, d) }

func ratio(n, d int64) string {
	if n <= 0 || d <= 0 {
		return decimal.Zero.StringFixed(rateDecimals)
	}
	return decimal.NewFromInt(n).DivRound(decimal.NewFromInt(d), rateDecimals).StringFixed(rateDecimals)
}

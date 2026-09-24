package domain

import (
	"crypto/sha256"
	"encoding/binary"
	"math/big"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// ABCriterion es la metrica con la que se elige la variante ganadora.
type ABCriterion string

const (
	// CriterionOpens: aperturas unicas sobre entregados.
	CriterionOpens ABCriterion = "opens"
	// CriterionClicks: clics unicos sobre entregados.
	CriterionClicks ABCriterion = "clicks"
)

func ABCriteria() []ABCriterion { return []ABCriterion{CriterionOpens, CriterionClicks} }

const (
	MinABVariants     = 2
	MaxABVariants     = 4
	MinSamplePercent  = 10
	MaxSamplePercent  = 50
	MinDecisionWindow = time.Hour
	MaxDecisionWindow = 72 * time.Hour
	// MaxSubjectLength acota un asunto alternativo (variante o reenvio): termina en la
	// cabecera Subject, y RFC 5322 recomienda lineas de 78 caracteres y admite 998.
	MaxSubjectLength = 250

	// sampleBuckets es la resolucion del reparto: el porcentaje de muestra se aplica en
	// centesimas de punto.
	sampleBuckets = 10000
)

// ABVariant es una variante de la prueba. TemplateID nil usa la plantilla de la campana;
// Subject vacio, el asunto que renderiza la plantilla. TemplateVersion es la version pedida
// (nil = la publicada al programar o iniciar); PinnedVersion, la que se fijo entonces.
type ABVariant struct {
	Subject         string     `json:"subject"`
	TemplateID      *uuid.UUID `json:"template_id"`
	TemplateVersion *int       `json:"template_version"`
	PinnedVersion   *int       `json:"pinned_version,omitempty"`
}

// ABTest es la configuracion de la prueba A/B de una campana.
type ABTest struct {
	Criterion             ABCriterion `json:"criterion"`
	SamplePercent         int         `json:"sample_percent"`
	DecisionWindowMinutes int         `json:"decision_window_minutes"`
	Variants              []ABVariant `json:"variants"`
}

func (a *ABTest) normalize() {
	for i := range a.Variants {
		a.Variants[i].Subject = strings.TrimSpace(a.Variants[i].Subject)
	}
}

// DecisionWindow es la espera entre el cierre de la muestra y la eleccion.
func (a *ABTest) DecisionWindow() time.Duration {
	return time.Duration(a.DecisionWindowMinutes) * time.Minute
}

// VariantTemplate es la plantilla efectiva de la variante i.
func (a *ABTest) VariantTemplate(i int, campaignTemplate uuid.UUID) uuid.UUID {
	if t := a.Variants[i].TemplateID; t != nil {
		return *t
	}
	return campaignTemplate
}

func (a *ABTest) Validate(campaignTemplate uuid.UUID) error {
	if a.Criterion != CriterionOpens && a.Criterion != CriterionClicks {
		return NewValidationError("ab_test.criterion: valor no válido %q", a.Criterion)
	}
	if a.SamplePercent < MinSamplePercent || a.SamplePercent > MaxSamplePercent {
		return NewValidationError("ab_test.sample_percent: debe estar entre %d y %d", MinSamplePercent, MaxSamplePercent)
	}
	w := a.DecisionWindow()
	if w < MinDecisionWindow || w > MaxDecisionWindow {
		return NewValidationError("ab_test.decision_window_minutes: debe estar entre %d y %d",
			int(MinDecisionWindow.Minutes()), int(MaxDecisionWindow.Minutes()))
	}
	if len(a.Variants) < MinABVariants || len(a.Variants) > MaxABVariants {
		return NewValidationError("ab_test.variants: indique entre %d y %d variantes", MinABVariants, MaxABVariants)
	}
	type key struct {
		template uuid.UUID
		version  int
		subject  string
	}
	seen := make(map[key]bool, len(a.Variants))
	for i, v := range a.Variants {
		if err := validateSubject("ab_test.variants.subject", v.Subject, false); err != nil {
			return err
		}
		if v.TemplateID != nil && *v.TemplateID == uuid.Nil {
			return NewValidationError("ab_test.variants.template_id: identificador vacío")
		}
		if v.TemplateVersion != nil && *v.TemplateVersion < 1 {
			return NewValidationError("ab_test.variants.template_version: debe ser mayor que cero")
		}
		k := key{template: a.VariantTemplate(i, campaignTemplate), subject: v.Subject}
		if v.TemplateVersion != nil {
			k.version = *v.TemplateVersion
		}
		if seen[k] {
			return NewValidationError("ab_test.variants: la variante %d repite el contenido de otra", i+1)
		}
		seen[k] = true
	}
	return nil
}

// validateSubject: el asunto termina en una cabecera, asi que no admite saltos de linea
// ni otros caracteres de control.
func validateSubject(field, v string, required bool) error {
	if v == "" {
		if required {
			return NewValidationError("%s: es obligatorio", field)
		}
		return nil
	}
	if utf8.RuneCountInString(v) > MaxSubjectLength {
		return NewValidationError("%s: admite como maximo %d caracteres", field, MaxSubjectLength)
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return NewValidationError("%s: contiene caracteres de control", field)
		}
	}
	return nil
}

// SampleBucket es la posicion estable (0..9999) del contacto en el reparto de la
// campana: depende solo de los dos ids, asi que un reinicio, otra replica o una segunda
// pasada por la audiencia asignan siempre lo mismo sin guardar nada.
func SampleBucket(campaignID, contactID uuid.UUID) int {
	h := sha256.New()
	h.Write(campaignID[:])
	h.Write(contactID[:])
	sum := h.Sum(nil)
	return int(binary.BigEndian.Uint64(sum[:8]) % sampleBuckets)
}

// Assign dice si el contacto entra en la muestra y con que variante. Los primeros
// SamplePercent % de posiciones forman la muestra, repartida en tramos contiguos de igual
// tamano por variante; el resto espera a la ganadora.
func (a *ABTest) Assign(campaignID, contactID uuid.UUID) (variant int, inSample bool) {
	threshold := a.SamplePercent * sampleBuckets / 100
	b := SampleBucket(campaignID, contactID)
	if b >= threshold {
		return 0, false
	}
	return b * len(a.Variants) / threshold, true
}

// VariantResult son los contadores de una variante en la muestra, por mensaje unico.
type VariantResult struct {
	Variant   int   `json:"variant"`
	Accepted  int64 `json:"accepted"`
	Delivered int64 `json:"delivered"`
	Opened    int64 `json:"opened"`
	Clicked   int64 `json:"clicked"`
}

// DecisionReason explica por que gano la variante.
type DecisionReason string

const (
	// ReasonCriterion: la mejor tasa del criterio.
	ReasonCriterion DecisionReason = "criterion"
	// ReasonSecondary: empate en el criterio; gana la mejor tasa de la otra metrica.
	ReasonSecondary DecisionReason = "secondary"
	// ReasonDelivered: empate en las dos tasas; gana la que mas entregas tuvo.
	ReasonDelivered DecisionReason = "delivered"
	// ReasonFirst: empate total (o sin datos); gana la primera variante, la de la campana.
	ReasonFirst DecisionReason = "first"
)

func DecisionReasons() []DecisionReason {
	return []DecisionReason{ReasonCriterion, ReasonSecondary, ReasonDelivered, ReasonFirst}
}

// ABDecision es la eleccion y la foto de los contadores con que se tomo.
type ABDecision struct {
	Winner    int             `json:"winner"`
	Criterion ABCriterion     `json:"criterion"`
	Reason    DecisionReason  `json:"reason"`
	Results   []VariantResult `json:"results"`
}

// SelectWinner elige la variante con la mayor tasa del criterio (unicos sobre entregados;
// sin entregas la tasa es cero). Desempate, en este orden: la mayor tasa de la otra
// metrica, el mayor numero de entregados (mas evidencia) y la variante de menor indice.
// Las tasas se comparan en exacto (productos cruzados), sin redondeo ni coma flotante.
func SelectWinner(criterion ABCriterion, variants int, results []VariantResult) ABDecision {
	byVariant := make([]VariantResult, variants)
	for i := range byVariant {
		byVariant[i].Variant = i
	}
	for _, r := range results {
		if r.Variant >= 0 && r.Variant < variants {
			byVariant[r.Variant] = r
		}
	}
	primary := func(r VariantResult) int64 { return r.Opened }
	secondary := func(r VariantResult) int64 { return r.Clicked }
	if criterion == CriterionClicks {
		primary, secondary = secondary, primary
	}
	winner, reason := 0, ReasonFirst
	for i := 1; i < variants; i++ {
		w, c := byVariant[winner], byVariant[i]
		if cmp := compareRates(primary(c), c.Delivered, primary(w), w.Delivered); cmp != 0 {
			if cmp > 0 {
				winner = i
			}
			continue
		}
		if cmp := compareRates(secondary(c), c.Delivered, secondary(w), w.Delivered); cmp != 0 {
			if cmp > 0 {
				winner = i
			}
			continue
		}
		if c.Delivered > w.Delivered {
			winner = i
		}
	}
	reason = decisionReason(byVariant, winner, primary, secondary)
	return ABDecision{Winner: winner, Criterion: criterion, Reason: reason, Results: byVariant}
}

// decisionReason es el primer criterio que separa a la ganadora de TODAS las demas.
func decisionReason(rs []VariantResult, winner int, primary, secondary func(VariantResult) int64) DecisionReason {
	w := rs[winner]
	reason := ReasonCriterion
	for i, c := range rs {
		if i == winner {
			continue
		}
		var r DecisionReason
		switch {
		case compareRates(primary(w), w.Delivered, primary(c), c.Delivered) != 0:
			r = ReasonCriterion
		case compareRates(secondary(w), w.Delivered, secondary(c), c.Delivered) != 0:
			r = ReasonSecondary
		case w.Delivered != c.Delivered:
			r = ReasonDelivered
		default:
			r = ReasonFirst
		}
		if rank(r) > rank(reason) {
			reason = r
		}
	}
	return reason
}

func rank(r DecisionReason) int {
	for i, x := range DecisionReasons() {
		if x == r {
			return i
		}
	}
	return 0
}

// compareRates compara n1/d1 con n2/d2 (denominador cero = tasa cero): -1, 0 o 1.
func compareRates(n1, d1, n2, d2 int64) int {
	if d1 <= 0 || n1 < 0 {
		n1, d1 = 0, 1
	}
	if d2 <= 0 || n2 < 0 {
		n2, d2 = 0, 1
	}
	a := new(big.Int).Mul(big.NewInt(n1), big.NewInt(d2))
	b := new(big.Int).Mul(big.NewInt(n2), big.NewInt(d1))
	return a.Cmp(b)
}

// VariantLabel es la letra con la que la interfaz y los eventos nombran la variante.
func VariantLabel(i int) string {
	if i < 0 || i >= MaxABVariants {
		return ""
	}
	return string(rune('A' + i))
}

// Resend es el reenvio a quien no abrio: otro asunto y un retraso desde que la ronda
// inicial termino. Sale una sola vez por campana.
type Resend struct {
	Subject      string `json:"subject"`
	DelayMinutes int    `json:"delay_minutes"`
}

const (
	MinResendDelay = 24 * time.Hour
	MaxResendDelay = 7 * 24 * time.Hour
)

func (r *Resend) normalize() { r.Subject = strings.TrimSpace(r.Subject) }

func (r *Resend) Delay() time.Duration { return time.Duration(r.DelayMinutes) * time.Minute }

func (r *Resend) Validate() error {
	if err := validateSubject("resend.subject", r.Subject, true); err != nil {
		return err
	}
	if d := r.Delay(); d < MinResendDelay || d > MaxResendDelay {
		return NewValidationError("resend.delay_minutes: debe estar entre %d y %d",
			int(MinResendDelay.Minutes()), int(MaxResendDelay.Minutes()))
	}
	return nil
}

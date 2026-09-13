package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type BillingPeriod string

const (
	PeriodMonthly BillingPeriod = "monthly"
	PeriodYearly  BillingPeriod = "yearly"
)

func ParseBillingPeriod(s string) (BillingPeriod, error) {
	switch BillingPeriod(s) {
	case PeriodMonthly, PeriodYearly:
		return BillingPeriod(s), nil
	}
	return "", fmt.Errorf("%w: billing_period debe ser monthly o yearly", ErrInvalidPlan)
}

// Months es la duracion del periodo en meses de calendario.
func (p BillingPeriod) Months() int {
	if p == PeriodYearly {
		return 12
	}
	return 1
}

type PlanStatus string

const (
	PlanActive  PlanStatus = "active"
	PlanRetired PlanStatus = "retired"
)

func ParsePlanStatus(s string) (PlanStatus, error) {
	switch PlanStatus(s) {
	case PlanActive, PlanRetired:
		return PlanStatus(s), nil
	}
	return "", fmt.Errorf("%w: status debe ser active o retired", ErrInvalidPlan)
}

// PlanLimit es lo que un plan incluye de un recurso. OverageUnitPrice solo existe en un
// limite blando con cantidad incluida: es el precio de cada unidad por encima.
type PlanLimit struct {
	Resource         Resource
	Included         int64
	HardLimit        bool
	OverageUnitPrice *decimal.Decimal
}

// ReachedBy indica si una cantidad deja un limite duro en su tope o por encima.
func (l PlanLimit) ReachedBy(quantity int64) bool {
	return l.HardLimit && l.Included != Unlimited && quantity >= l.Included
}

type Plan struct {
	ID            uuid.UUID
	Code          string
	Name          string
	Description   string
	Currency      string
	BasePrice     decimal.Decimal
	BillingPeriod BillingPeriod
	Status        PlanStatus
	Limits        []PlanLimit
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Escalas y topes de las columnas numeric(15,2) y numeric(15,6).
const (
	PriceScale           int32 = 2
	UnitPriceScale       int32 = 6
	MaxNameLength              = 120
	MaxDescriptionLength       = 1000
	maxAmountLength            = 32
)

var (
	planCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,39}$`)
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
	amountPattern   = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
	maxPrice        = decimal.New(1, 13)
	maxUnitPrice    = decimal.New(1, 9)
)

// ParsePrice lee el precio base de un plan ("49.00") sin pasar por float.
func ParsePrice(s string) (decimal.Decimal, error) {
	return parseAmount(s, PriceScale, maxPrice)
}

// ParseUnitPrice lee el precio por unidad de excedente ("0.001500").
func ParseUnitPrice(s string) (decimal.Decimal, error) {
	return parseAmount(s, UnitPriceScale, maxUnitPrice)
}

// parseAmount admite solo digitos y un punto decimal: sin signo, sin exponente y sin
// separadores de miles, que son las formas en que un importe se malinterpreta.
func parseAmount(s string, scale int32, limit decimal.Decimal) (decimal.Decimal, error) {
	if len(s) > maxAmountLength || !amountPattern.MatchString(s) {
		return decimal.Decimal{}, fmt.Errorf("%w: %q no es un importe (digitos y punto decimal)", ErrInvalidAmount, s)
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("%w: %q", ErrInvalidAmount, s)
	}
	if err := checkAmount(d, scale, limit); err != nil {
		return decimal.Decimal{}, err
	}
	return d, nil
}

func checkAmount(d decimal.Decimal, scale int32, limit decimal.Decimal) error {
	if d.IsNegative() {
		return fmt.Errorf("%w: no puede ser negativo", ErrInvalidAmount)
	}
	if !d.Equal(d.Round(scale)) {
		return fmt.Errorf("%w: admite como maximo %d decimales", ErrInvalidAmount, scale)
	}
	if d.GreaterThanOrEqual(limit) {
		return fmt.Errorf("%w: supera el maximo admitido", ErrInvalidAmount)
	}
	return nil
}

// Validate comprueba el plan completo antes de guardarlo.
func (p *Plan) Validate() error {
	if !planCodePattern.MatchString(p.Code) {
		return invalidPlan("code debe empezar por una letra minuscula y tener de 2 a 40 caracteres a-z, 0-9, _ o -")
	}
	if err := ValidatePlanText(p.Name, p.Description); err != nil {
		return err
	}
	if !currencyPattern.MatchString(p.Currency) {
		return invalidPlan("currency debe ser un codigo ISO 4217 de tres letras mayusculas")
	}
	if err := checkAmount(p.BasePrice, PriceScale, maxPrice); err != nil {
		return invalidPlan("base_price: %v", err)
	}
	if _, err := ParseBillingPeriod(string(p.BillingPeriod)); err != nil {
		return err
	}
	return ValidateLimits(p.Limits)
}

// ValidatePlanText comprueba nombre y descripcion, lo unico editable de un plan que ya
// tiene suscripciones.
func ValidatePlanText(name, description string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || utf8.RuneCountInString(trimmed) > MaxNameLength {
		return invalidPlan("name es obligatorio y admite hasta %d caracteres", MaxNameLength)
	}
	if utf8.RuneCountInString(description) > MaxDescriptionLength {
		return invalidPlan("description admite hasta %d caracteres", MaxDescriptionLength)
	}
	return nil
}

// ValidateLimits exige exactamente un limite por recurso: un plan que calla sobre un
// recurso obligaria a adivinar si lo incluye.
func ValidateLimits(limits []PlanLimit) error {
	seen := make(map[Resource]bool, len(limits))
	for _, l := range limits {
		if _, err := ParseResource(string(l.Resource)); err != nil {
			return invalidPlan("limits: recurso %q desconocido", l.Resource)
		}
		if seen[l.Resource] {
			return invalidPlan("limits: %s aparece mas de una vez", l.Resource)
		}
		seen[l.Resource] = true
		if l.Included < Unlimited {
			return invalidPlan("limits: included de %s debe ser -1 (ilimitado) o mayor o igual que 0", l.Resource)
		}
		if l.OverageUnitPrice != nil {
			if l.HardLimit || l.Included == Unlimited {
				return invalidPlan("limits: overage_unit_price de %s solo aplica a un limite blando con cantidad incluida", l.Resource)
			}
			if err := checkAmount(*l.OverageUnitPrice, UnitPriceScale, maxUnitPrice); err != nil {
				return invalidPlan("limits: overage_unit_price de %s: %v", l.Resource, err)
			}
		}
	}
	for _, r := range resources {
		if !seen[r] {
			return invalidPlan("limits: falta el limite de %s", r)
		}
	}
	return nil
}

// SortLimits ordena los limites en el orden canonico de los recursos.
func SortLimits(limits []PlanLimit) {
	order := make(map[Resource]int, len(resources))
	for i, r := range resources {
		order[r] = i
	}
	sort.SliceStable(limits, func(i, j int) bool { return order[limits[i].Resource] < order[limits[j].Resource] })
}

// EffectiveLimit devuelve el limite del recurso. Un plan anterior a un recurso nuevo no
// tiene fila para el: se trata como nada incluido y limite duro, que es fallar cerrado.
func (p *Plan) EffectiveLimit(r Resource) PlanLimit {
	for _, l := range p.Limits {
		if l.Resource == r {
			return l
		}
	}
	return PlanLimit{Resource: r, Included: 0, HardLimit: true}
}

// Assignable indica si el plan admite empresas nuevas.
func (p *Plan) Assignable() bool { return p.Status == PlanActive }

// PlanPatch es un cambio parcial de un plan. Las condiciones (moneda, precio, periodo y
// limites) solo cambian en un plan sin suscripciones: cambiarlas bajo una empresa ya
// suscrita alteraria lo que contrato. Nombre y descripcion no alteran nada y cambian siempre.
type PlanPatch struct {
	Name          *string
	Description   *string
	Currency      *string
	BasePrice     *decimal.Decimal
	BillingPeriod *BillingPeriod
	Limits        []PlanLimit
	ReplaceLimits bool
}

// ChangesTerms indica si el cambio toca las condiciones del plan.
func (c PlanPatch) ChangesTerms() bool {
	return c.Currency != nil || c.BasePrice != nil || c.BillingPeriod != nil || c.ReplaceLimits
}

// Empty indica que el cambio no pide nada.
func (c PlanPatch) Empty() bool {
	return c.Name == nil && c.Description == nil && !c.ChangesTerms()
}

// Apply vuelca el cambio sobre el plan; la validacion la hace Validate despues.
func (p *Plan) Apply(c PlanPatch) {
	if c.Name != nil {
		p.Name = strings.TrimSpace(*c.Name)
	}
	if c.Description != nil {
		p.Description = *c.Description
	}
	if c.Currency != nil {
		p.Currency = *c.Currency
	}
	if c.BasePrice != nil {
		p.BasePrice = *c.BasePrice
	}
	if c.BillingPeriod != nil {
		p.BillingPeriod = *c.BillingPeriod
	}
	if c.ReplaceLimits {
		p.Limits = append([]PlanLimit(nil), c.Limits...)
	}
}

func invalidPlan(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidPlan, fmt.Sprintf(format, args...))
}

package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Thresholds son los umbrales de la evaluacion, como fraccion de los envios de la ventana.
type Thresholds struct {
	BounceWarn     decimal.Decimal
	BounceBlock    decimal.Decimal
	ComplaintWarn  decimal.Decimal
	ComplaintBlock decimal.Decimal
	// MinVolume: por debajo de estos envios en la ventana no se juzga a la empresa. Con
	// poco volumen un solo rebote dispara la tasa y no dice nada de su practica.
	MinVolume int64
}

// transactionalBlockFactor multiplica los umbrales de bloqueo de la clase transaccional.
// Los correos de acceso, verificacion y recibo (un OTP) no pueden dejar de salir por una
// campana mal hecha de la misma empresa: la clase transaccional solo se restringe sola
// cuando sus tasas doblan el umbral de bloqueo; entre una y dos veces queda en warning.
var transactionalBlockFactor = decimal.NewFromInt(2)

// For devuelve los umbrales efectivos de una clase.
func (t Thresholds) For(class Class) Thresholds {
	if class != ClassTransactional {
		return t
	}
	t.BounceBlock = t.BounceBlock.Mul(transactionalBlockFactor)
	t.ComplaintBlock = t.ComplaintBlock.Mul(transactionalBlockFactor)
	return t
}

// MaxLimit acota cualquier limite de tasa: por encima deja de ser un limite.
const MaxLimit int64 = 1_000_000_000

// Limits son los topes de envio por hora y por dia (UTC) de una clase.
type Limits struct {
	Hourly int64
	Daily  int64
}

// Validate exige topes positivos y acotados, y que el de la hora no supere al del dia.
func (l Limits) Validate() error {
	if l.Hourly < 1 || l.Hourly > MaxLimit || l.Daily < 1 || l.Daily > MaxLimit {
		return fmt.Errorf("%w: hourly y daily deben estar entre 1 y %d", ErrInvalidLimit, MaxLimit)
	}
	if l.Hourly > l.Daily {
		return fmt.Errorf("%w: hourly no puede superar a daily", ErrInvalidLimit)
	}
	return nil
}

// LimitOverride son los limites que el superadmin fija a una empresa. Un valor nil toma el
// de la configuracion.
type LimitOverride struct {
	TenantID  uuid.UUID
	Class     Class
	Hourly    *int64
	Daily     *int64
	UpdatedBy uuid.UUID
	UpdatedAt time.Time
}

// MaxWindowDays acota la ventana movil; los contadores diarios se conservan mas tiempo.
const MaxWindowDays = 365

var one = decimal.NewFromInt(1)

// Policy es la configuracion con la que opera el servicio: umbrales, ventana y limites por
// defecto de cada clase.
type Policy struct {
	Thresholds Thresholds
	// WindowDays son los dias naturales (UTC) de la ventana movil, el de hoy incluido.
	WindowDays int
	Defaults   map[Class]Limits
}

// Validate comprueba que la configuracion sea coherente; el servicio no arranca si no lo es.
func (p Policy) Validate() error {
	t := p.Thresholds
	fractions := []struct {
		name  string
		value decimal.Decimal
	}{
		{"bounce_warn", t.BounceWarn},
		{"bounce_block", t.BounceBlock},
		{"complaint_warn", t.ComplaintWarn},
		{"complaint_block", t.ComplaintBlock},
	}
	for _, f := range fractions {
		if !f.value.IsPositive() || !f.value.LessThan(one) {
			return fmt.Errorf("%w: %s debe ser una fraccion mayor que 0 y menor que 1", ErrInvalidPolicy, f.name)
		}
	}
	if !t.BounceWarn.LessThan(t.BounceBlock) {
		return fmt.Errorf("%w: bounce_warn debe ser menor que bounce_block", ErrInvalidPolicy)
	}
	if !t.ComplaintWarn.LessThan(t.ComplaintBlock) {
		return fmt.Errorf("%w: complaint_warn debe ser menor que complaint_block", ErrInvalidPolicy)
	}
	if t.MinVolume < 1 {
		return fmt.Errorf("%w: min_volume debe ser mayor que 0", ErrInvalidPolicy)
	}
	if p.WindowDays < 1 || p.WindowDays > MaxWindowDays {
		return fmt.Errorf("%w: la ventana debe tener entre 1 y %d dias", ErrInvalidPolicy, MaxWindowDays)
	}
	for _, c := range Classes() {
		l, ok := p.Defaults[c]
		if !ok {
			return fmt.Errorf("%w: faltan los limites por defecto de %s", ErrInvalidPolicy, c)
		}
		if err := l.Validate(); err != nil {
			return fmt.Errorf("%w: limites por defecto de %s: %v", ErrInvalidPolicy, c, err)
		}
	}
	return nil
}

// LimitsFor devuelve los limites efectivos de la clase: el valor fijado por el superadmin
// donde lo hay y el de la configuracion donde no.
func (p Policy) LimitsFor(class Class, o *LimitOverride) Limits {
	l := p.Defaults[class]
	if o != nil {
		if o.Hourly != nil {
			l.Hourly = *o.Hourly
		}
		if o.Daily != nil {
			l.Daily = *o.Daily
		}
	}
	return l
}

// ValidateOverride comprueba cada valor fijado y el resultado efectivo junto con los
// valores por defecto: un tope por hora mayor que el diario no tiene sentido.
func (p Policy) ValidateOverride(o LimitOverride) error {
	for _, v := range []*int64{o.Hourly, o.Daily} {
		if v != nil && (*v < 1 || *v > MaxLimit) {
			return fmt.Errorf("%w: hourly y daily deben estar entre 1 y %d", ErrInvalidLimit, MaxLimit)
		}
	}
	return p.LimitsFor(o.Class, &o).Validate()
}

// WindowStart es el primer dia (UTC) que cuenta en la ventana que termina hoy.
func (p Policy) WindowStart(now time.Time) time.Time {
	y, m, d := now.UTC().Date()
	return time.Date(y, m, d-(p.WindowDays-1), 0, 0, 0, 0, time.UTC)
}

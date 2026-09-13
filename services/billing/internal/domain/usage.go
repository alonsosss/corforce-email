package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

const (
	MaxEventIDLength = 200
	MaxItemKeyLength = 320
	MaxSourceLength  = 64
)

// Counter es un contador de consumo. LimitReachedPeriod es el periodo de suscripcion en
// que se aviso que un limite duro llego a su tope.
type Counter struct {
	TenantID           uuid.UUID
	Resource           Resource
	PeriodStart        time.Time
	Quantity           int64
	LimitReachedPeriod *time.Time
}

// NotifiedIn indica si el aviso de limite ya salio en el periodo dado.
func (c *Counter) NotifiedIn(period time.Time) bool {
	return c.LimitReachedPeriod != nil && c.LimitReachedPeriod.Equal(Date(period))
}

// UsageChange es el efecto de un hecho consumado (un buzon creado, un correo enviado)
// sobre un contador. ItemKey y Source identifican el objeto cuando varias fuentes informan
// del mismo: el contador sube con la primera fuente y baja con la ultima.
type UsageChange struct {
	EventID  string
	Subject  string
	TenantID uuid.UUID
	Resource Resource
	Delta    int64
	ItemKey  string
	Source   string
}

// Validate rechaza lo que ningun reintento va a arreglar.
func (c UsageChange) Validate() error {
	if strings.TrimSpace(c.EventID) == "" || len(c.EventID) > MaxEventIDLength {
		return fmt.Errorf("%w: id de evento vacio o demasiado largo", ErrInvalidEvent)
	}
	if c.TenantID == uuid.Nil {
		return fmt.Errorf("%w: sin tenant_id", ErrInvalidEvent)
	}
	if _, err := ParseResource(string(c.Resource)); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidEvent, err)
	}
	// Un envio suma tantas unidades como destinatarios; un objeto de stock existe o no.
	if c.Resource.IsFlow() {
		if c.Delta < 1 || c.ItemKey != "" {
			return fmt.Errorf("%w: un recurso de flujo solo suma y no identifica objetos", ErrInvalidEvent)
		}
	} else if c.Delta != 1 && c.Delta != -1 {
		return fmt.Errorf("%w: un evento de stock suma o resta una unidad", ErrInvalidEvent)
	}
	if (c.ItemKey == "") != (c.Source == "") {
		return fmt.Errorf("%w: objeto y fuente van juntos", ErrInvalidEvent)
	}
	if len(c.ItemKey) > MaxItemKeyLength || len(c.Source) > MaxSourceLength {
		return fmt.Errorf("%w: objeto o fuente demasiado largos", ErrInvalidEvent)
	}
	return nil
}

// UsageLine es el consumo de un recurso frente a su limite. Percent es nil cuando el plan
// no limita el recurso o no incluye nada de el.
type UsageLine struct {
	Resource  Resource
	Flow      bool
	Used      int64
	Included  int64
	HardLimit bool
	Overage   int64
	Percent   *decimal.Decimal
}

func NewUsageLine(limit PlanLimit, used int64) UsageLine {
	line := UsageLine{
		Resource: limit.Resource, Flow: limit.Resource.IsFlow(), Used: used,
		Included: limit.Included, HardLimit: limit.HardLimit,
	}
	if limit.Included == Unlimited {
		return line
	}
	if used > limit.Included {
		line.Overage = used - limit.Included
	}
	if limit.Included > 0 {
		pct := decimal.NewFromInt(used).Mul(decimal.NewFromInt(100)).DivRound(decimal.NewFromInt(limit.Included), 2)
		line.Percent = &pct
	}
	return line
}

// UsageReport es el consumo de una empresa en su periodo vigente.
type UsageReport struct {
	TenantID    uuid.UUID
	PlanCode    string
	PeriodStart time.Time
	PeriodEnd   time.Time
	Lines       []UsageLine
}

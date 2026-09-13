package domain

import (
	"fmt"
	"time"
)

// Resource es cada magnitud que un plan limita y que billing cuenta.
type Resource string

const (
	ResourceUsers                 Resource = "users"
	ResourceDomains               Resource = "domains"
	ResourceMailboxes             Resource = "mailboxes"
	ResourceStorageBytes          Resource = "storage_bytes"
	ResourceContacts              Resource = "contacts"
	ResourceTransactionalMessages Resource = "transactional_messages"
	ResourceMarketingMessages     Resource = "marketing_messages"
)

// Unlimited en included significa que el plan no limita el recurso.
const Unlimited int64 = -1

// StockPeriodStart es el period_start del unico contador vivo de un recurso de stock: lo
// que existe no vuelve a cero al cambiar de periodo.
var StockPeriodStart = time.Date(1970, time.January, 1, 0, 0, 0, 0, time.UTC)

var resources = []Resource{
	ResourceUsers,
	ResourceDomains,
	ResourceMailboxes,
	ResourceStorageBytes,
	ResourceContacts,
	ResourceTransactionalMessages,
	ResourceMarketingMessages,
}

// Resources devuelve todos los recursos en el orden en que se presentan.
func Resources() []Resource { return append([]Resource(nil), resources...) }

func ParseResource(s string) (Resource, error) {
	for _, r := range resources {
		if string(r) == s {
			return r, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrInvalidResource, s)
}

// IsFlow distingue lo que se consume por periodo (mensajes enviados) de lo que existe
// (buzones, dominios): el primero empieza en cero en cada periodo, el segundo no.
func (r Resource) IsFlow() bool {
	return r == ResourceTransactionalMessages || r == ResourceMarketingMessages
}

// CounterPeriod es el period_start del contador del recurso en el periodo de suscripcion
// que empieza en periodStart.
func (r Resource) CounterPeriod(periodStart time.Time) time.Time {
	if r.IsFlow() {
		return Date(periodStart)
	}
	return StockPeriodStart
}

// NextQuantity aplica un alta o una baja a un contador sin bajar de cero: la baja de algo
// cuya alta ocurrio antes de que billing existiera no deja el contador en negativo.
func NextQuantity(current, delta int64) int64 {
	if n := current + delta; n > 0 {
		return n
	}
	return 0
}

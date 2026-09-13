package domain

import (
	"sort"
	"time"

	"github.com/google/uuid"
)

// Reason es la causa por la que una direccion queda fuera de todo envio.
type Reason string

const (
	ReasonHardBounce  Reason = "hard_bounce"
	ReasonComplaint   Reason = "complaint"
	ReasonUnsubscribe Reason = "unsubscribe"
	ReasonInvalid     Reason = "invalid"
	ReasonManual      Reason = "manual"
)

// Reasons enumera las causas en orden de gravedad descendente. Las consultas que eligen
// la causa principal de una direccion ordenan por la posicion en esta lista.
func Reasons() []Reason {
	return []Reason{ReasonComplaint, ReasonHardBounce, ReasonUnsubscribe, ReasonInvalid, ReasonManual}
}

// ParseReason valida el texto recibido por API o evento.
func ParseReason(s string) (Reason, error) {
	r := Reason(s)
	for _, known := range Reasons() {
		if r == known {
			return r, nil
		}
	}
	return "", ErrInvalidReason
}

// Severity ordena las causas: una direccion registrada por una causa grave no se
// degrada cuando llega la misma direccion por una causa menor. Una queja pesa mas que
// un rebote duro porque afecta a la reputacion de envio; la baja pesa mas que el resto
// porque la pidio la persona; lo manual es lo unico que un operador puede revertir a
// voluntad.
func (r Reason) Severity() int {
	switch r {
	case ReasonComplaint:
		return 5
	case ReasonHardBounce:
		return 4
	case ReasonUnsubscribe:
		return 3
	case ReasonInvalid:
		return 2
	case ReasonManual:
		return 1
	}
	return 0
}

// Removable indica si un operador puede retirar la exclusion por API. La baja pedida por
// la persona solo la levanta un nuevo consentimiento registrado por contacts.
func (r Reason) Removable() bool {
	return r != ReasonUnsubscribe
}

// Entry es UNA causa de exclusion de una direccion en una empresa: hay una fila por
// (empresa, direccion, causa). Una direccion puede tener varias a la vez (se dio de baja
// y ademas la empresa la excluyo a mano) y cada una se registra y se retira por separado,
// de modo que retirar una no libera a la direccion de las demas.
type Entry struct {
	ID         uuid.UUID  `json:"id"`
	TenantID   uuid.UUID  `json:"tenant_id"`
	Email      string     `json:"email"`
	Reason     Reason     `json:"reason"`
	Source     string     `json:"source"`
	Detail     string     `json:"detail"`
	MessageID  *uuid.UUID `json:"message_id,omitempty"`
	CampaignID *uuid.UUID `json:"campaign_id,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Active indica si la exclusion sigue vigente en el instante dado. Solo las manuales
// pueden caducar; el resto no lleva fecha.
func (e Entry) Active(now time.Time) bool {
	return e.ExpiresAt == nil || e.ExpiresAt.After(now)
}

// Address es una direccion excluida con todas sus causas: lo que devuelven el listado, la
// consulta de una exclusion y las altas. Los campos de Entry son los de la causa
// principal, que es lo que los consumidores leen como reason; Reasons son las causas
// vigentes de mas a menos grave y Causes todas las filas de la direccion (tambien una
// manual caducada), cada una con el id con que se retira.
type Address struct {
	Entry
	Reasons []Reason `json:"reasons"`
	Causes  []Entry  `json:"causes"`
}

// Active indica si la direccion tiene alguna causa vigente.
func (a Address) Active() bool { return len(a.Reasons) > 0 }

type addressKey struct {
	tenantID uuid.UUID
	email    string
}

// Aggregate agrupa las causas por direccion, en el orden en que aparece cada direccion
// por primera vez. La causa principal es la vigente mas grave; si ninguna esta vigente
// (solo una exclusion manual caducada), la mas grave de las que hay.
func Aggregate(entries []Entry, now time.Time) []Address {
	index := make(map[addressKey]int, len(entries))
	var out []Address
	for _, e := range entries {
		key := addressKey{tenantID: e.TenantID, email: e.Email}
		i, ok := index[key]
		if !ok {
			i = len(out)
			index[key] = i
			out = append(out, Address{})
		}
		out[i].Causes = append(out[i].Causes, e)
	}
	for i := range out {
		a := &out[i]
		sort.SliceStable(a.Causes, func(x, y int) bool {
			return a.Causes[x].Reason.Severity() > a.Causes[y].Reason.Severity()
		})
		a.Reasons = []Reason{}
		primary := -1
		for j, c := range a.Causes {
			if !c.Active(now) {
				continue
			}
			a.Reasons = append(a.Reasons, c.Reason)
			if primary < 0 {
				primary = j
			}
		}
		if primary < 0 {
			primary = 0
		}
		a.Entry = a.Causes[primary]
	}
	return out
}

// Import es el rastro de una carga masiva de exclusiones.
type Import struct {
	ID        uuid.UUID `json:"id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	Total     int       `json:"total"`
	Added     int       `json:"added"`
	Skipped   int       `json:"skipped"`
	CreatedBy uuid.UUID `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// Suppressed es la respuesta de la consulta previa al envio para una direccion. Reason es
// la causa vigente mas grave; Reasons, todas las vigentes de mas a menos grave: quien
// relaja la supresion por una causa (el doble opt-in ignora las bajas) necesita saber que
// no hay otra detras.
type Suppressed struct {
	Email   string   `json:"email"`
	Reason  Reason   `json:"reason"`
	Reasons []Reason `json:"reasons"`
}

// ReasonCount es el numero de direcciones con alguna exclusion vigente cuya causa
// principal es Reason.
type ReasonCount struct {
	Reason Reason
	Count  int64
}

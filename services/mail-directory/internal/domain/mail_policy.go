package domain

import (
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// MailPolicy es la politica de correo de una empresa (mail.mail_policy). Sin fila, todo permitido:
// es el comportamiento que habia antes de que existiera.
type MailPolicy struct {
	TenantID                  uuid.UUID
	ExternalForwardingAllowed bool
	// UpdatedBy es el usuario de la plataforma que hizo el ultimo cambio; nil si nunca se cambio.
	UpdatedBy *uuid.UUID
	UpdatedAt time.Time
}

// NewMailPolicy es la politica de una empresa que nunca la cambio.
func NewMailPolicy(tenantID uuid.UUID) *MailPolicy {
	return &MailPolicy{TenantID: tenantID, ExternalForwardingAllowed: true}
}

var (
	// ErrReauthRequired: el cambio anade un reenvio a direcciones externas y la peticion no llega
	// reautenticada. Una sesion robada no debe poder sacar el correo del buzon.
	ErrReauthRequired = errors.New("reenviar a direcciones externas exige volver a identificarse")
	// ErrExternalForwardingDisabled: la empresa no permite reenviar a direcciones externas.
	ErrExternalForwardingDisabled = errors.New("la empresa no permite reenviar a direcciones externas")
)

// ForwardingError es un rechazo de reenvio con las direcciones que lo provocan: la interfaz las
// muestra. Kind es ErrReauthRequired o ErrExternalForwardingDisabled.
type ForwardingError struct {
	Kind      error
	Addresses []string
}

func (e *ForwardingError) Error() string {
	return e.Kind.Error() + ": " + strings.Join(e.Addresses, ", ")
}
func (e *ForwardingError) Unwrap() error { return e.Kind }

// AddressDomain es el dominio de una direccion ya normalizada.
func AddressDomain(addr string) string {
	if at := strings.LastIndexByte(addr, '@'); at >= 0 {
		return addr[at+1:]
	}
	return ""
}

// ForwardAddresses son todas las direcciones a las que las reglas y el reenvio podrian mandar el
// correo, activas o no, ordenadas y sin repetir.
func (f *MailboxFilters) ForwardAddresses() []string {
	return f.forwardAddresses(false)
}

// ActiveForwardAddresses son las direcciones a las que el correo sale hoy: el reenvio si esta
// encendido y las acciones forward de las reglas activas. Ordenadas y sin repetir.
func (f *MailboxFilters) ActiveForwardAddresses() []string {
	return f.forwardAddresses(true)
}

func (f *MailboxFilters) forwardAddresses(activeOnly bool) []string {
	var out []string
	if !activeOnly || f.Forwarding.Enabled {
		out = append(out, f.Forwarding.Addresses...)
	}
	for _, r := range f.Rules {
		if activeOnly && !r.Enabled {
			continue
		}
		for _, a := range r.Actions {
			if a.Type == FilterActionForward {
				out = append(out, a.Address)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// ExternalAddresses deja de addrs las que no son de un dominio de la empresa. owned son los dominios
// propios y alias de la empresa.
func ExternalAddresses(addrs []string, owned map[string]bool) []string {
	out := []string{}
	for _, a := range addrs {
		if !owned[AddressDomain(a)] {
			out = append(out, a)
		}
	}
	return out
}

// AddressDomains son los dominios distintos de las direcciones, ordenados.
func AddressDomains(addrs []string) []string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, AddressDomain(a))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// SetDifference son los elementos de a que no estan en b; a y b vienen ordenados y sin repetir.
func SetDifference(a, b []string) []string {
	out := []string{}
	for _, x := range a {
		if _, found := slices.BinarySearch(b, x); !found {
			out = append(out, x)
		}
	}
	return out
}

// ForwardingChange es el cambio de reenvio externo de un buzon que se anuncia a auditoria.
type ForwardingChange struct {
	ExternalAdded     []string
	ExternalRemoved   []string
	ForwardingEnabled bool
}

// ExternalForwardingChange compara el reenvio externo activo de before y after. ok es false si ni
// las direcciones externas activas ni el interruptor del reenvio cambiaron: no hay nada que anunciar.
func ExternalForwardingChange(before, after *MailboxFilters, owned map[string]bool) (ForwardingChange, bool) {
	oldExt := ExternalAddresses(before.ActiveForwardAddresses(), owned)
	newExt := ExternalAddresses(after.ActiveForwardAddresses(), owned)
	c := ForwardingChange{
		ExternalAdded: SetDifference(newExt, oldExt), ExternalRemoved: SetDifference(oldExt, newExt),
		ForwardingEnabled: after.Forwarding.Enabled,
	}
	changed := len(c.ExternalAdded) > 0 || len(c.ExternalRemoved) > 0 || before.Forwarding.Enabled != after.Forwarding.Enabled
	return c, changed
}

// StripExternalForwarding quita de las reglas y del reenvio toda direccion externa, activa o no, y
// regenera el script. Lo aplica apagar la politica de reenvio externo a los filtros ya guardados:
//   - el reenvio pierde las direcciones externas y se apaga si no le queda ninguna;
//   - una regla pierde sus acciones forward externas; si no le queda ninguna accion se retira
//     entera, porque una regla sin acciones no es valida y solo reenviaba fuera: el correo que
//     casaba se entrega en el buzon como cualquier otro.
//
// Devuelve si cambio algo.
func (f *MailboxFilters) StripExternalForwarding(owned map[string]bool) (bool, error) {
	changed := false
	kept := f.Forwarding.Addresses[:0:0]
	for _, a := range f.Forwarding.Addresses {
		if owned[AddressDomain(a)] {
			kept = append(kept, a)
		} else {
			changed = true
		}
	}
	f.Forwarding.Addresses = kept
	if f.Forwarding.Enabled && len(kept) == 0 {
		f.Forwarding.Enabled = false
	}
	rules := f.Rules[:0:0]
	for _, r := range f.Rules {
		actions := r.Actions[:0:0]
		for _, a := range r.Actions {
			if a.Type == FilterActionForward && !owned[AddressDomain(a.Address)] {
				changed = true
				continue
			}
			actions = append(actions, a)
		}
		if len(actions) == 0 {
			continue
		}
		r.Actions = actions
		rules = append(rules, r)
	}
	f.Rules = rules
	if !changed {
		return false, nil
	}
	return true, f.Normalize()
}

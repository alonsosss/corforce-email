package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// State es el estado de reputacion de una clase de una empresa.
type State string

const (
	StateOK         State = "ok"
	StateWarning    State = "warning"
	StateRestricted State = "restricted"
	// StateSuspended solo lo fija el superadmin; ninguna evaluacion automatica llega a el.
	StateSuspended State = "suspended"
)

// States devuelve los estados de menor a mayor gravedad.
func States() []State {
	return []State{StateOK, StateWarning, StateRestricted, StateSuspended}
}

// StateNames devuelve los estados como texto, para validar entradas.
func StateNames() []string {
	states := States()
	out := make([]string, len(states))
	for i, s := range states {
		out[i] = string(s)
	}
	return out
}

// ParseState valida un estado que llega de fuera.
func ParseState(s string) (State, error) {
	for _, st := range States() {
		if string(st) == s {
			return st, nil
		}
	}
	return "", ErrInvalidState
}

// MaxReasonLength acota el motivo que escribe el superadmin al suspender.
const MaxReasonLength = 500

// NormalizeReason valida el motivo de una accion manual: obligatorio y acotado.
func NormalizeReason(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > MaxReasonLength {
		return "", ErrInvalidReason
	}
	return s, nil
}

// Record es el estado vigente de una clase de una empresa. Las tasas son las del momento
// del ultimo cambio; las de la ventana actual se calculan al consultar.
type Record struct {
	TenantID      uuid.UUID
	Class         Class
	State         State
	Reason        string
	BounceRate    decimal.Decimal
	ComplaintRate decimal.Decimal
	// Manual: el estado lo fijo el superadmin y la evaluacion automatica no lo cambia
	// hasta que lo libere.
	Manual    bool
	ChangedAt time.Time
	ChangedBy *uuid.UUID
}

// DefaultRecord es el estado de una clase que aun no tiene fila: sin historia no hay
// motivo para restringir.
func DefaultRecord(tenantID uuid.UUID, class Class) Record {
	return Record{
		TenantID:      tenantID,
		Class:         class,
		State:         StateOK,
		Reason:        ReasonInitial,
		BounceRate:    decimal.Zero,
		ComplaintRate: decimal.Zero,
	}
}

// Change es una transicion de estado: state_history la guarda y el evento
// reputation.tenant.state_changed la anuncia.
type Change struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	Class         Class
	From          State
	To            State
	Reason        string
	BounceRate    decimal.Decimal
	ComplaintRate decimal.Decimal
	// Manual: el cambio lo provoco una persona (suspension o liberacion por el
	// superadmin), no la evaluacion automatica.
	Manual    bool
	ChangedBy *uuid.UUID
	CreatedAt time.Time
}

// ChangeOf arma la transicion entre el estado anterior y el nuevo.
func ChangeOf(from, to Record, manual bool) Change {
	return Change{
		TenantID:      to.TenantID,
		Class:         to.Class,
		From:          from.State,
		To:            to.State,
		Reason:        to.Reason,
		BounceRate:    to.BounceRate,
		ComplaintRate: to.ComplaintRate,
		Manual:        manual,
		ChangedBy:     to.ChangedBy,
		CreatedAt:     to.ChangedAt,
	}
}

package domain

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// RotationKind distingue una rotacion programada, que conserva la clave anterior durante la
// gracia, de una revocacion por clave comprometida, que la retira de los motores al momento.
type RotationKind string

const (
	RotationScheduled   RotationKind = "scheduled"
	RotationCompromised RotationKind = "compromised"
)

// MaxRevocationReasonLength acota el motivo de una revocacion (caracteres).
const MaxRevocationReasonLength = 500

// DKIMRotation es una entrada del historial de claves del dominio.
type DKIMRotation struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	DomainID uuid.UUID
	Kind     RotationKind
	// Selector es la clave que queda como actual.
	Selector string
	// PreviousSelector es la clave que queda en gracia (solo scheduled).
	PreviousSelector string
	// RevokedSelectors son las claves retiradas al momento (solo compromised).
	RevokedSelectors []string
	Reason           string
	// ActorID es el usuario que la pidio; uuid.Nil si no hubo persona.
	ActorID   uuid.UUID
	RotatedAt time.Time
}

// Revoked dice si la entrada es una revocacion que retiro el selector.
func (r *DKIMRotation) Revoked(selector string) bool {
	if r == nil || r.Kind != RotationCompromised {
		return false
	}
	for _, s := range r.RevokedSelectors {
		if s == selector {
			return true
		}
	}
	return false
}

// NormalizeRevocationReason recorta el motivo y exige texto plano no vacio y acotado: acaba en
// el historial, en el evento y en la bitacora de auditoria.
func NormalizeRevocationReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || !utf8.ValidString(reason) || utf8.RuneCountInString(reason) > MaxRevocationReasonLength {
		return "", ErrInvalidRevocationReason
	}
	for _, r := range reason {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return "", ErrInvalidRevocationReason
		}
	}
	return reason, nil
}

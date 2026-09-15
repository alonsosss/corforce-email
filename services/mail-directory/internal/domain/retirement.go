package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrTenantRetired: la empresa esta dada de baja en la celda. Su directorio no admite cambios y no
// activa ningun dominio; solo se puede apagar uno que ya tiene.
var ErrTenantRetired = errors.New("la empresa esta dada de baja en la celda: su directorio no admite cambios")

// TenantRetirement es la baja de una empresa en el directorio de la celda: desde RetiredAt nada
// suyo recibe, reenvia ni autentica en la celda. Deactivated cuenta lo que apago la llamada que la
// devuelve: cero al repetirla.
type TenantRetirement struct {
	TenantID    uuid.UUID        `json:"tenant_id"`
	RetiredAt   time.Time        `json:"retired_at"`
	Deactivated RetirementCounts `json:"deactivated"`
}

// RetirementCounts son las filas que apago una baja, por tabla del directorio.
type RetirementCounts struct {
	Domains       int `json:"domains"`
	AliasDomains  int `json:"alias_domains"`
	Mailboxes     int `json:"mailboxes"`
	Aliases       int `json:"aliases"`
	AppPasswords  int `json:"app_passwords"`
	Relayhosts    int `json:"relayhosts"`
	Transports    int `json:"transports"`
	TLSPolicies   int `json:"tls_policies"`
	RecipientMaps int `json:"recipient_maps"`
	BCCMaps       int `json:"bcc_maps"`
}

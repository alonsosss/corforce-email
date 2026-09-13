// Package tenantcell dice en que celda vive una empresa y cierra los servicios de celda a las
// empresas de las demas.
//
// El token de acceso lleva la empresa y no su celda (organization.v_tenants excluye cell_id a
// proposito). La celda la sabe organization, que la sirve por su API interna
// (GET /internal/organization/tenants/{id}/cell, token interno y sin usuario). Resolver la
// pregunta y la guarda en una cache acotada. La usan el gateway, para llevar cada peticion con
// sesion a la instancia de la celda de su empresa, y cada servicio de celda (Membership), para
// rechazar la que llegue a una celda que no es la suya (Modelo_de_Datos_y_Celdas.md, 5.4).
package tenantcell

import (
	"errors"
	"regexp"
)

// Codigo de celda: el mismo formato que admite organization al darla de alta.
var codeRe = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// MaxCodeLen es la longitud maxima de un codigo de celda.
const MaxCodeLen = 63

// ValidCode dice si code tiene forma de codigo de celda.
func ValidCode(code string) bool {
	return len(code) <= MaxCodeLen && codeRe.MatchString(code)
}

var (
	// ErrUnknownTenant: organization respondio de forma definitiva que la empresa no existe, o
	// la peticion no trae una empresa valida.
	ErrUnknownTenant = errors.New("organization no conoce la empresa")
	// ErrUnresolved: no hay respuesta aplicable de organization.
	ErrUnresolved = errors.New("no se pudo resolver la celda de la empresa")
)

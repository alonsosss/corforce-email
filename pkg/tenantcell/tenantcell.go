// Package tenantcell dice en que celda vive una empresa o un dominio de correo y cierra los
// servicios de celda a las empresas de las demas.
//
// El token de acceso lleva la empresa y no su celda (organization.v_tenants excluye cell_id a
// proposito). La celda la sabe organization, que la sirve por su API interna (token interno y
// sin usuario): la de una empresa en GET /internal/organization/tenants/{id}/cell y la de un
// dominio de correo activo en GET /internal/organization/mail-domains/{dominio}/cell. Resolver
// la pregunta y la guarda en una cache acotada. La usan el gateway, para llevar cada peticion
// con sesion a la instancia de la celda de su empresa y el inicio de sesion del webmail a la del
// dominio del buzon, y cada servicio de celda (Membership), para rechazar la que llegue a una
// celda que no es la suya (Modelo_de_Datos_y_Celdas.md, 5.4 y 5.5).
package tenantcell

import (
	"errors"

	"github.com/alonsosss/corforce-email/pkg/mailcell"
)

// MaxCodeLen es la longitud maxima de un codigo de celda.
const MaxCodeLen = mailcell.MaxCodeLen

// ValidCode dice si code tiene forma de codigo de celda.
func ValidCode(code string) bool { return mailcell.ValidCode(code) }

var (
	// ErrUnknownTenant: organization respondio de forma definitiva que la empresa no existe, o
	// la peticion no trae una empresa valida.
	ErrUnknownTenant = errors.New("organization no conoce la empresa")
	// ErrUnknownDomain: organization respondio de forma definitiva que el dominio no esta activo
	// en ninguna celda, o el nombre no es un dominio de correo.
	ErrUnknownDomain = errors.New("organization no conoce el dominio de correo")
	// ErrUnresolved: no hay respuesta aplicable de organization.
	ErrUnresolved = errors.New("no se pudo resolver la celda")
)

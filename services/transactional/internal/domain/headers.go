package domain

import (
	"regexp"
	"strings"
)

// Solo se admiten cabeceras propias X-: cualquier cabecera estandar (From, To,
// List-Unsubscribe, Content-Type...) la fija el servicio y aceptarla del cliente
// permitiria suplantar remitentes o romper el correo.
var (
	headerNamePattern = regexp.MustCompile(`^X-[A-Za-z0-9\-]{1,60}$`)
	tagPattern        = regexp.MustCompile(`^[A-Za-z0-9_\-]{1,256}$`)
)

// reservedHeaderPrefixes son cabeceras X- que Amazon SES usa para si.
var reservedHeaderPrefixes = []string{"x-ses-", "x-amz-", "x-mailer"}

// ValidateHeaders comprueba nombres y valores de las cabeceras del cliente.
func ValidateHeaders(headers map[string]string) error {
	if len(headers) > MaxHeaders {
		return NewValidationError("headers: no se admiten mas de %d cabeceras", MaxHeaders)
	}
	for name, value := range headers {
		if !headerNamePattern.MatchString(name) {
			return NewValidationError("headers: la cabecera %q no es una cabecera X- valida", name)
		}
		lower := strings.ToLower(name)
		for _, p := range reservedHeaderPrefixes {
			if strings.HasPrefix(lower, p) {
				return NewValidationError("headers: la cabecera %q esta reservada", name)
			}
		}
		if err := validHeaderValue(name, value); err != nil {
			return err
		}
	}
	return nil
}

func validHeaderValue(name, value string) error {
	if len(value) == 0 || len(value) > 998 {
		return NewValidationError("headers: el valor de %q debe tener entre 1 y 998 caracteres", name)
	}
	for _, c := range value {
		if c < 32 || c > 126 {
			return NewValidationError("headers: el valor de %q solo admite ASCII imprimible", name)
		}
	}
	return nil
}

// ReservedTagNames son las etiquetas de SES con las que el servicio atribuye cada evento
// a su empresa y a su mensaje; el cliente no puede fijarlas.
var ReservedTagNames = []string{"tenant_id", "message_id"}

// ValidateTags comprueba las etiquetas del mensaje con el formato que exige SES.
func ValidateTags(tags map[string]string) error {
	if len(tags) > MaxTags {
		return NewValidationError("tags: no se admiten mas de %d etiquetas", MaxTags)
	}
	for k, v := range tags {
		for _, reserved := range ReservedTagNames {
			if strings.EqualFold(k, reserved) {
				return NewValidationError("tags: %q esta reservada", k)
			}
		}
		if !tagPattern.MatchString(k) || !tagPattern.MatchString(v) {
			return NewValidationError("tags: %q solo admite letras, numeros, guion y guion bajo (1..256)", k)
		}
	}
	return nil
}

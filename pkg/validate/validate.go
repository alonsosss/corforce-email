package validate

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)
	uuidRegex  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	slugRegex  = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

type Validator struct {
	errors []string
}

func New() *Validator {
	return &Validator{}
}

// Add anota una regla que no cabe en las genericas: un rango, una coherencia
// entre dos campos, un valor que depende del negocio. Sin esto habia que
// devolver el error a mano y el mensaje salia con otro formato, o dejarselo al
// CHECK de la base -- que responde "error inesperado" y no dice que corregir.
func (v *Validator) Add(field, message string) {
	v.errors = append(v.errors, fmt.Sprintf("%s: %s", field, message))
}

func (v *Validator) Required(field, value string) {
	if strings.TrimSpace(value) == "" {
		v.errors = append(v.errors, fmt.Sprintf("%s is required", field))
	}
}

func (v *Validator) MinLength(field, value string, min int) {
	if utf8.RuneCountInString(value) < min {
		v.errors = append(v.errors, fmt.Sprintf("%s must be at least %d characters", field, min))
	}
}

func (v *Validator) MaxLength(field, value string, max int) {
	if utf8.RuneCountInString(value) > max {
		v.errors = append(v.errors, fmt.Sprintf("%s must be at most %d characters", field, max))
	}
}

func (v *Validator) Email(field, value string) {
	if value != "" && !emailRegex.MatchString(value) {
		v.errors = append(v.errors, fmt.Sprintf("%s must be a valid email", field))
	}
}

func (v *Validator) UUID(field, value string) {
	if value != "" && !uuidRegex.MatchString(strings.ToLower(value)) {
		v.errors = append(v.errors, fmt.Sprintf("%s must be a valid UUID", field))
	}
}

func (v *Validator) Slug(field, value string) {
	if value != "" && !slugRegex.MatchString(value) {
		v.errors = append(v.errors, fmt.Sprintf("%s must be a valid slug", field))
	}
}

func (v *Validator) OneOf(field, value string, allowed []string) {
	if value == "" {
		return
	}
	for _, a := range allowed {
		if value == a {
			return
		}
	}
	v.errors = append(v.errors, fmt.Sprintf("%s must be one of: %s", field, strings.Join(allowed, ", ")))
}

func (v *Validator) Valid() bool {
	return len(v.errors) == 0
}

func (v *Validator) Error() string {
	return strings.Join(v.errors, "; ")
}

func DecodeJSON(r *http.Request, dst interface{}) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if dec.More() {
		return fmt.Errorf("request body must contain a single JSON object")
	}
	return nil
}

// DecodeJSONLimit es DecodeJSON con un tope explicito de bytes.
//
// Sin tope, el limite real de un cuerpo lo pone la memoria del proceso: basta un
// envio grande para degradar el servicio. Conserva las dos protecciones de
// DecodeJSON -- rechazo de campos desconocidos y de un segundo objeto en el
// mismo cuerpo -- porque decodificar a pelo para poner el limite las pierde.
//
// Necesita el ResponseWriter porque MaxBytesReader cierra la conexion cuando el
// cliente sigue enviando despues de pasarse.
func DecodeJSONLimit(w http.ResponseWriter, r *http.Request, dst interface{}, maxBytes int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return fmt.Errorf("el contenido supera el limite de %d KB", maxBytes/1024)
		}
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if dec.More() {
		return fmt.Errorf("request body must contain a single JSON object")
	}
	return nil
}

package response

import (
	"encoding/json"
	"go.uber.org/zap"
	"net/http"
	"reflect"
)

// nilSafeData converts nil slices to an empty slice so JSON serializes as []
// instead of null, preventing frontend crashes on DataGrid components.
func nilSafeData(data interface{}) interface{} {
	if data == nil {
		return []struct{}{}
	}
	v := reflect.ValueOf(data)
	if v.Kind() == reflect.Slice && v.IsNil() {
		return []struct{}{}
	}
	return data
}

type Envelope struct {
	Data  interface{} `json:"data,omitempty"`
	Error *APIError   `json:"error,omitempty"`
	Meta  *Meta       `json:"meta,omitempty"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Details son datos con los que el cliente explica el error en su idioma (el campo que
	// fallo, la direccion rechazada). Solo lo que el cliente envio o ya puede ver.
	Details map[string]string `json:"details,omitempty"`
}

type Meta struct {
	Page       int   `json:"page,omitempty"`
	PerPage    int   `json:"per_page,omitempty"`
	Total      int64 `json:"total,omitempty"`
	TotalPages int   `json:"total_pages,omitempty"`
}

// PageMeta arma la meta de un listado paginado CON total_pages.
//
// Existe porque omitirlo no es inocuo: el cliente lo usa para saber si quedan paginas
// (frontend/packages/api-client/src/pagination.ts) y, ausente, da por terminado el
// recorrido en la primera. Rellenar los campos a mano dejo dos endpoints sin el y sus
// pantallas mostrando solo las 100 primeras filas.
func PageMeta(total int64, page, perPage int) *Meta {
	totalPages := 0
	if perPage > 0 {
		totalPages = int(total) / perPage
		if int(total)%perPage > 0 {
			totalPages++
		}
	}
	return &Meta{Page: page, PerPage: perPage, Total: total, TotalPages: totalPages}
}

func JSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(Envelope{Data: nilSafeData(data)})
}

func JSONWithMeta(w http.ResponseWriter, status int, data interface{}, meta *Meta) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(Envelope{Data: nilSafeData(data), Meta: meta})
}

func Err(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(Envelope{
		Error: &APIError{Code: code, Message: message},
	})
}

func ErrBadRequest(w http.ResponseWriter, message string) {
	Err(w, http.StatusBadRequest, "BAD_REQUEST", message)
}

func ErrUnauthorized(w http.ResponseWriter, message string) {
	Err(w, http.StatusUnauthorized, "UNAUTHORIZED", message)
}

func ErrForbidden(w http.ResponseWriter, message string) {
	Err(w, http.StatusForbidden, "FORBIDDEN", message)
}

func ErrNotFound(w http.ResponseWriter, message string) {
	Err(w, http.StatusNotFound, "NOT_FOUND", message)
}

func ErrConflict(w http.ResponseWriter, message string) {
	Err(w, http.StatusConflict, "CONFLICT", message)
}

func ErrInternal(w http.ResponseWriter) {
	Err(w, http.StatusInternalServerError, "INTERNAL_ERROR", "ocurrió un error inesperado")
}

// unexpectedLogger registra los fallos que ningún servicio supo clasificar.
var unexpectedLogger *zap.Logger

// SetUnexpectedLogger conecta el registro de fallos inesperados. Lo llama cada servicio al
// arrancar; sin él, Unexpected se comporta como ErrInternal.
func SetUnexpectedLogger(l *zap.Logger) { unexpectedLogger = l }

// Unexpected responde 500 DEJANDO CONSTANCIA del motivo.
//
// Existe porque el patrón contrario aparecía en todos los servicios: writeError traducía los
// errores de dominio conocidos y mandaba el resto a un 500 mudo. Un fallo así obliga a
// reproducir el caso para saber qué pasó, y hay casos que no se reproducen -una restricción
// de la base con datos concretos, una integración que responde distinto de lo esperado-.
// Durante una sola auditoría del módulo de producción, cuatro servicios distintos escondieron
// así un error real: una columna JSON mal alimentada, una restricción de tipo de componente,
// un rechazo del proveedor de envio y un buzon sin cuota.
//
// El mensaje al cliente no cambia: quien llama sigue viendo "ocurrió un error inesperado",
// porque el detalle de un fallo interno no es suyo. Lo que cambia es que queda escrito.
func Unexpected(w http.ResponseWriter, err error) {
	if unexpectedLogger != nil && err != nil {
		unexpectedLogger.Error("fallo inesperado", zap.Error(err))
	}
	ErrInternal(w)
}

func ErrValidation(w http.ResponseWriter, message string) {
	Err(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", message)
}

// ErrWithDetails es Err con datos del error en error.details.
func ErrWithDetails(w http.ResponseWriter, status int, code, message string, details map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(Envelope{
		Error: &APIError{Code: code, Message: message, Details: details},
	})
}

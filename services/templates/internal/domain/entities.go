package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Kind separa las plantillas por clase de envio: las transaccionales las usa el
// servicio transactional y las de marketing, campaigns. Nunca comparten salida.
const (
	KindTransactional = "transactional"
	KindMarketing     = "marketing"
)

// Estados de la plantilla. Archivada es el paso previo obligatorio al borrado.
const (
	TemplateStatusActive   = "active"
	TemplateStatusArchived = "archived"
)

// Estados de una version. Solo una esta publicada por plantilla; al publicar otra, la
// anterior pasa a supersedida y conserva su fecha de publicacion para poder volver a ella.
const (
	VersionStatusDraft      = "draft"
	VersionStatusPublished  = "published"
	VersionStatusSuperseded = "superseded"
)

// Limites de contenido. El asunto respeta la longitud maxima de linea de RFC 5322; el
// HTML y la salida acotan la memoria por peticion.
const (
	MaxSubjectBytes = 998
	MaxHTMLBytes    = 512 * 1024
	MaxOutputBytes  = 1024 * 1024
	MaxNameLength   = 120
	MaxDescription  = 1000
	MaxVariables    = 100
)

// Marcado estructurado que la plataforma anade a una version al renderizarla. MarkupOrder es la
// tarjeta de pedido de Gmail (schema.org Order en JSON-LD); solo en plantillas transaccionales.
const MarkupOrder = "order"

func Markups() []string { return []string{MarkupOrder} }

// MaxTemplateKey es el tope de la clave estable de una plantilla.
const MaxTemplateKey = 64

// templateKeyRegex: segmentos en minusculas separados por punto, como pedido.confirmado. La
// clave viaja en el API de envio, asi que no admite nada que haya que escapar.
var templateKeyRegex = regexp.MustCompile(`^[a-z][a-z0-9_-]*(\.[a-z][a-z0-9_-]*)*$`)

// NormalizeTemplateKey valida una clave de plantilla. Vacia significa sin clave (nil).
func NormalizeTemplateKey(key string) (*string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, nil
	}
	if len(key) > MaxTemplateKey || !templateKeyRegex.MatchString(key) {
		return nil, fmt.Errorf("%w: use minúsculas, dígitos, _ y - en segmentos separados por punto (p. ej. pedido.confirmado), hasta %d caracteres", ErrInvalidTemplateKey, MaxTemplateKey)
	}
	return &key, nil
}

func Kinds() []string            { return []string{KindTransactional, KindMarketing} }
func TemplateStatuses() []string { return []string{TemplateStatusActive, TemplateStatusArchived} }
func VersionStatuses() []string {
	return []string{VersionStatusDraft, VersionStatusPublished, VersionStatusSuperseded}
}

// Template es la cabecera estable de una plantilla. CurrentVersion es 0 mientras no haya
// ninguna version publicada.
type Template struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	Name        string
	Description string
	// Key es la clave estable con la que otro producto nombra la plantilla; nil sin clave.
	Key            *string
	Kind           string
	Status         string
	CurrentVersion int
	CreatedBy      uuid.UUID
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Version es el contenido inmutable de una plantilla. Text nil significa que la parte de
// texto se genera desde el HTML renderizado.
type Version struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	TemplateID uuid.UUID
	Version    int
	Subject    string
	HTML       string
	Text       *string
	Variables  []Variable
	Editor     *EditorDocument
	// Markup es el marcado estructurado que se anade al renderizar (MarkupOrder) o vacio.
	Markup      string
	Status      string
	PublishedAt *time.Time
	CreatedBy   uuid.UUID
	CreatedAt   time.Time
}

// WasPublished indica que la version estuvo publicada en algun momento y por tanto puede
// renderizarse en un envio real (aunque hoy este supersedida).
func (v *Version) WasPublished() bool {
	return v.Status == VersionStatusPublished || v.Status == VersionStatusSuperseded
}

// VersionSummary es una version sin su contenido, para los listados y el detalle de la
// plantilla.
type VersionSummary struct {
	ID          uuid.UUID
	Version     int
	Status      string
	PublishedAt *time.Time
	CreatedBy   uuid.UUID
	CreatedAt   time.Time
}

// TemplateDetail es la plantilla con su version publicada (nil si no hay) y el resumen
// de todas sus versiones.
type TemplateDetail struct {
	Template *Template
	Current  *Version
	Versions []VersionSummary
}

// Content es lo que se guarda en una version. Editor no se compila ni se renderiza: es el
// diseno del editor visual, para volver a editarla.
type Content struct {
	Subject   string
	HTML      string
	Text      *string
	Variables []Variable
	Editor    *EditorDocument
	Markup    string
}

// Rendered es la salida de un renderizado. Kind es el tipo de la plantilla renderizada.
type Rendered struct {
	Subject string
	HTML    string
	Text    string
	Version int
	Kind    string
}

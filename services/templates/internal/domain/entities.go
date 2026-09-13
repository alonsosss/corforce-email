package domain

import (
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

func Kinds() []string            { return []string{KindTransactional, KindMarketing} }
func TemplateStatuses() []string { return []string{TemplateStatusActive, TemplateStatusArchived} }

// Template es la cabecera estable de una plantilla. CurrentVersion es 0 mientras no haya
// ninguna version publicada.
type Template struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	Name           string
	Description    string
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
	ID          uuid.UUID
	TenantID    uuid.UUID
	TemplateID  uuid.UUID
	Version     int
	Subject     string
	HTML        string
	Text        *string
	Variables   []Variable
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

// Content es lo que se compila y valida al guardar una version.
type Content struct {
	Subject   string
	HTML      string
	Text      *string
	Variables []Variable
}

// Rendered es la salida de un renderizado. Kind es el tipo de la plantilla renderizada.
type Rendered struct {
	Subject string
	HTML    string
	Text    string
	Version int
	Kind    string
}

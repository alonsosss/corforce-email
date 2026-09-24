package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// EditorKindGrapesJSWeb es el documento de GrapesJS en modo web (sin MJML) de una pagina.
const EditorKindGrapesJSWeb = "grapesjs-web"

func PageEditorKinds() []string { return []string{EditorKindGrapesJSWeb} }

// Topes de una pagina de aterrizaje. Los de tamano se repiten en la migracion.
const (
	MaxPageHTMLBytes      = 512 * 1024
	MaxPageCSSBytes       = 256 * 1024
	MaxPageTitleLength    = 200
	MaxPageDescriptionLen = 500
	MaxPageSlugLength     = 80
	PageSlugPattern       = `^[a-z0-9]([a-z0-9-]{0,78}[a-z0-9])?$`
	PageStatusActive      = "active"
	PageStatusArchived    = "archived"
)

var pageSlugRegex = regexp.MustCompile(PageSlugPattern)

func PageStatuses() []string { return []string{PageStatusActive, PageStatusArchived} }

var (
	ErrPageNotFound        = errors.New("pagina no encontrada")
	ErrPageVersionNotFound = errors.New("version de pagina no encontrada")
	ErrPageNameTaken       = errors.New("ya existe una pagina con ese nombre")
	ErrPageSlugTaken       = errors.New("ya existe una pagina con esa direccion")
	ErrPageNotArchived     = errors.New("la pagina debe estar archivada para borrarse")
	ErrPageArchived        = errors.New("la pagina esta archivada")
	ErrPageNotPublished    = errors.New("la pagina no tiene ninguna version publicada")
	// ErrInvalidPage envuelve cualquier defecto de la cabecera o del contenido de una pagina.
	ErrInvalidPage = errors.New("pagina no valida")
)

func invalidPage(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidPage, fmt.Sprintf(format, args...))
}

// LandingPage es la cabecera estable de una pagina. CurrentVersion es 0 si no hay version
// publicada: entonces la direccion publica responde 404.
type LandingPage struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	Name           string
	Slug           string
	Status         string
	NoIndex        bool
	CurrentVersion int
	CreatedBy      uuid.UUID
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// PageEditorDocument es el diseno de GrapesJS para volver a editar la version.
type PageEditorDocument struct {
	Kind    string          `json:"kind"`
	Project json.RawMessage `json:"project"`
}

// PageContent es lo que guarda una version: el HTML y el CSS ya saneados y el diseno.
type PageContent struct {
	Title       string
	Description string
	HTML        string
	CSS         string
	Editor      *PageEditorDocument
}

// LandingVersion es una version inmutable (una vez publicada) de una pagina.
type LandingVersion struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	PageID      uuid.UUID
	Version     int
	Content     PageContent
	Status      string
	PublishedAt *time.Time
	CreatedBy   uuid.UUID
	CreatedAt   time.Time
}

// LandingDetail es la pagina con su version publicada y el resumen de todas.
type LandingDetail struct {
	Page     *LandingPage
	Current  *LandingVersion
	Versions []VersionSummary
}

// NormalizePageName recorta el nombre y comprueba su longitud.
func NormalizePageName(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" || utf8.RuneCountInString(s) > MaxNameLength || strings.ContainsAny(s, "\x00\r\n") {
		return "", invalidPage("name es obligatorio y admite como maximo %d caracteres", MaxNameLength)
	}
	return s, nil
}

// NormalizePageSlug exige minusculas, digitos y guiones, sin guion al principio ni al final.
func NormalizePageSlug(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if !pageSlugRegex.MatchString(s) || strings.Contains(s, "--") {
		return "", invalidPage("slug admite de 1 a %d minusculas, digitos y guiones sueltos, sin guion al principio ni al final", MaxPageSlugLength)
	}
	return s, nil
}

// NormalizePageMeta valida el titulo y la descripcion del documento.
func NormalizePageMeta(title, description string) (string, string, error) {
	t := strings.TrimSpace(title)
	if t == "" || utf8.RuneCountInString(t) > MaxPageTitleLength || strings.ContainsAny(t, "\x00\r\n") {
		return "", "", invalidPage("title es obligatorio y admite una linea de como maximo %d caracteres", MaxPageTitleLength)
	}
	d := strings.TrimSpace(description)
	if utf8.RuneCountInString(d) > MaxPageDescriptionLen || strings.ContainsAny(d, "\x00\r\n") {
		return "", "", invalidPage("description admite una linea de como maximo %d caracteres", MaxPageDescriptionLen)
	}
	return t, d, nil
}

// NormalizePageEditor valida el documento del editor y compacta el proyecto. nil = sin diseno.
func NormalizePageEditor(e *PageEditorDocument) (*PageEditorDocument, error) {
	if e == nil {
		return nil, nil
	}
	if !contains(PageEditorKinds(), e.Kind) {
		return nil, invalidPage("editor.kind debe ser uno de: %s", strings.Join(PageEditorKinds(), ", "))
	}
	project := bytes.TrimSpace(e.Project)
	if len(project) == 0 || project[0] != '{' {
		return nil, invalidPage("editor.project debe ser un objeto")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, project); err != nil {
		return nil, invalidPage("editor.project no es JSON valido")
	}
	out := &PageEditorDocument{Kind: e.Kind, Project: compact.Bytes()}
	serialized, err := json.Marshal(out)
	if err != nil || len(serialized) > MaxEditorBytes {
		return nil, invalidPage("el documento del editor supera %d bytes", MaxEditorBytes)
	}
	return out, nil
}

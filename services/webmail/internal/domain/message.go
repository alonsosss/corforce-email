package domain

import (
	"mime"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Address es una direccion de un sobre o de un mensaje.
type Address struct {
	Name  string
	Email string
}

// Flag es un flag de sistema IMAP.
type Flag string

const (
	FlagSeen     Flag = `\Seen`
	FlagAnswered Flag = `\Answered`
	FlagFlagged  Flag = `\Flagged`
	FlagDraft    Flag = `\Draft`
	FlagDeleted  Flag = `\Deleted`
)

// SystemFlags son los flags que se muestran al cliente. Las palabras clave propias de
// otros clientes no salen: son texto arbitrario sin significado para el webmail.
var SystemFlags = []Flag{FlagSeen, FlagAnswered, FlagFlagged, FlagDraft, FlagDeleted}

// MutableFlags son los unicos que el cliente puede cambiar. \Deleted y \Draft los
// gobiernan borrar y guardar borrador: dejarlos sueltos permitiria marcar para borrar sin
// pasar por la papelera o disfrazar un mensaje recibido de borrador propio.
var MutableFlags = []Flag{FlagSeen, FlagFlagged, FlagAnswered}

// mutableFlag reconoce un flag modificable sin distinguir mayusculas (RFC 3501).
func mutableFlag(raw string) (Flag, bool) {
	raw = strings.TrimSpace(raw)
	for _, f := range MutableFlags {
		if strings.EqualFold(raw, string(f)) {
			return f, true
		}
	}
	return "", false
}

// maxFlagChanges acota la peticion: solo hay tres flags validos.
const maxFlagChanges = 6

// FlagChange son los flags que se anaden y los que se quitan en una sola operacion.
type FlagChange struct {
	Add    []Flag
	Remove []Flag
}

// NewFlagChange valida y deduplica el cambio pedido.
func NewFlagChange(add, remove []string) (FlagChange, error) {
	if len(add)+len(remove) == 0 {
		return FlagChange{}, invalid("flags", "no hay cambios")
	}
	if len(add)+len(remove) > maxFlagChanges {
		return FlagChange{}, invalid("flags", "demasiados cambios")
	}
	parse := func(field string, raw []string) ([]Flag, map[Flag]bool, error) {
		seen := map[Flag]bool{}
		var out []Flag
		for _, r := range raw {
			f, ok := mutableFlag(r)
			if !ok {
				return nil, nil, invalid(field, `solo se admiten \Seen, \Flagged y \Answered`)
			}
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
		return out, seen, nil
	}
	addFlags, addSet, err := parse("add", add)
	if err != nil {
		return FlagChange{}, err
	}
	removeFlags, _, err := parse("remove", remove)
	if err != nil {
		return FlagChange{}, err
	}
	for _, f := range removeFlags {
		if addSet[f] {
			return FlagChange{}, invalid("flags", "un mismo flag no puede añadirse y quitarse a la vez")
		}
	}
	return FlagChange{Add: addFlags, Remove: removeFlags}, nil
}

// Envelope es la fila de un listado de mensajes.
type Envelope struct {
	UID            uint32
	From           []Address
	To             []Address
	Cc             []Address
	Subject        string
	Date           time.Time
	Flags          []Flag
	Size           int64
	HasAttachments bool
	// Category es la pestana de la bandeja inteligente; vacia si no se leyeron sus cabeceras.
	Category Category
}

// Part describe una parte descargable: un adjunto o una imagen en linea (cid:).
type Part struct {
	ID          string
	ContentType string
	Filename    string
	Size        int64
	ContentID   string
	Inline      bool
}

// RawMessage es el mensaje tal como sale del almacen: HTML sin sanear.
type RawMessage struct {
	Envelope
	Bcc           []Address
	ReplyTo       []Address
	MessageID     string
	InReplyTo     []string
	References    []string
	Text          string
	TextTruncated bool
	HTML          string
	HTMLTruncated bool
	Parts         []Part
}

// RemoteImages informa de las imagenes remotas del HTML: si las habia y si se bloquearon.
type RemoteImages struct {
	Present bool
	Blocked bool
}

// Message es el mensaje listo para el cliente: HTML ya saneado.
type Message struct {
	Envelope
	Folder        string
	Bcc           []Address
	ReplyTo       []Address
	MessageID     string
	InReplyTo     []string
	References    []string
	Text          string
	TextTruncated bool
	HTML          string
	HTMLTruncated bool
	RemoteImages  RemoteImages
	Attachments   []Part
}

// ReadOptions gobierna la lectura de un mensaje.
type ReadOptions struct {
	// MarkSeen marca el mensaje como leido al abrirlo.
	MarkSeen bool
	// MaxBodyBytes acota lo que se lee de cada parte de texto o HTML.
	MaxBodyBytes int64
}

// ReplyReference es lo que hace falta del original para encadenar una respuesta.
type ReplyReference struct {
	MessageID  string
	References []string
}

// Quota es el uso de almacenamiento del buzon.
type Quota struct {
	UsedBytes  int64
	LimitBytes int64
}

// partIDPattern son numeros de seccion IMAP (RFC 3501 6.4.5): 1, 1.2, 2.1.3.
var partIDPattern = regexp.MustCompile(`^[1-9][0-9]{0,3}(\.[1-9][0-9]{0,3}){0,9}$`)

// ParsePartID convierte "1.2" en [1 2].
func ParsePartID(raw string) ([]int, error) {
	if !partIDPattern.MatchString(raw) {
		return nil, invalid("part", "identificador de parte inválido")
	}
	segments := strings.Split(raw, ".")
	out := make([]int, len(segments))
	for i, s := range segments {
		n, err := strconv.Atoi(s)
		if err != nil {
			return nil, invalid("part", "identificador de parte inválido")
		}
		out[i] = n
	}
	return out, nil
}

// FormatPartID es la inversa de ParsePartID.
func FormatPartID(path []int) string {
	parts := make([]string, len(path))
	for i, n := range path {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ".")
}

// ParseUID interpreta el UID de un mensaje (entero positivo de 32 bits).
func ParseUID(raw string) (uint32, error) {
	n, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || n == 0 {
		return 0, invalid("uid", "debe ser un entero positivo")
	}
	return uint32(n), nil
}

// Paginacion y busqueda del listado.
const (
	DefaultPerPage = 50
	MaxPerPage     = 100
	MaxSearchBytes = 256
	MaxPage        = 1_000_000
)

// ListQuery es la pagina pedida de una carpeta.
type ListQuery struct {
	Page    int
	PerPage int
	Search  string
	Filter  SearchFilter
}

// SearchFilter son los criterios de la busqueda avanzada; el valor cero de cada uno no filtra.
// Las fechas son de calendario y se comparan con la fecha del mensaje (cabecera Date): Since
// incluye ese dia y Before lo excluye, como SENTSINCE y SENTBEFORE de IMAP.
type SearchFilter struct {
	From           string
	To             string
	Subject        string
	Since          time.Time
	Before         time.Time
	Unread         bool
	Flagged        bool
	HasAttachments bool
	// Category filtra por la pestana de la bandeja inteligente (Classify).
	Category Category
}

// searchDateLayout es el formato de las fechas de la busqueda (YYYY-MM-DD).
const searchDateLayout = "2006-01-02"

// ParseSearchDate interpreta una fecha de la busqueda; vacia es sin fecha.
func ParseSearchDate(field, raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(searchDateLayout, raw)
	if err != nil {
		return time.Time{}, invalid(field, "debe ser una fecha AAAA-MM-DD")
	}
	return t, nil
}

// WithFilter anade los criterios de la busqueda avanzada. Cada texto se acota y se limpia igual
// que la busqueda libre; Before debe ser posterior a Since.
func (q ListQuery) WithFilter(f SearchFilter) (ListQuery, error) {
	for _, field := range []struct {
		name  string
		value *string
	}{{"from", &f.From}, {"to", &f.To}, {"subject", &f.Subject}} {
		v, err := searchText(field.name, *field.value)
		if err != nil {
			return ListQuery{}, err
		}
		*field.value = v
	}
	if !f.Since.IsZero() && !f.Before.IsZero() && !f.Before.After(f.Since) {
		return ListQuery{}, invalid("before", "debe ser posterior a since")
	}
	q.Filter = f
	return q, nil
}

// NewListQuery aplica los valores por defecto y los limites. La busqueda viaja como una
// cadena de IMAP SEARCH TEXT que la libreria cita; aqui solo se acota y se rechazan los
// caracteres de control.
func NewListQuery(page, perPage int, search string) (ListQuery, error) {
	if page < 1 {
		page = 1
	}
	// Ningun buzon llega a tantas paginas; el tope evita que (page-1)*perPage se desborde.
	if page > MaxPage {
		return ListQuery{}, invalid("page", "demasiado alta")
	}
	if perPage < 1 {
		perPage = DefaultPerPage
	}
	if perPage > MaxPerPage {
		perPage = MaxPerPage
	}
	search, err := searchText("search", search)
	if err != nil {
		return ListQuery{}, err
	}
	return ListQuery{Page: page, PerPage: perPage, Search: search}, nil
}

// searchText acota un texto de busqueda y rechaza lo que no puede viajar en IMAP SEARCH.
func searchText(field, v string) (string, error) {
	v = strings.TrimSpace(v)
	if len(v) > MaxSearchBytes {
		return "", invalid(field, "demasiado larga")
	}
	if !utf8.ValidString(v) {
		return "", invalid(field, "no es UTF-8 válido")
	}
	for _, r := range v {
		if isControl(r) {
			return "", invalid(field, "contiene caracteres de control")
		}
	}
	return v, nil
}

// Window devuelve el rango [start, end) de la pagina sobre un resultado de total filas.
func (q ListQuery) Window(total int) (int, int) {
	start := (q.Page - 1) * q.PerPage
	if start >= total {
		return total, total
	}
	return start, min(start+q.PerPage, total)
}

// MessagePage es una pagina del listado y el total de coincidencias. Capped indica que el
// total se calculo sobre los mensajes mas recientes y puede haber mas (filtro por adjuntos).
type MessagePage struct {
	Items  []Envelope
	Total  int
	Capped bool
}

// StoredMessage identifica un mensaje guardado en una carpeta: su UID solo vale con la
// UIDVALIDITY de la carpeta, y el Message-ID lo reconoce aunque cambie de UID.
type StoredMessage struct {
	UIDValidity uint32
	UID         uint32
	MessageID   string
	Size        int64
	// Subject y From (la primera direccion del remitente) son lo que se lista con un recordatorio.
	Subject string
	From    string
}

// AppendedMessage es la referencia IMAP de un mensaje recien guardado (APPENDUID).
type AppendedMessage struct {
	UID         uint32
	UIDValidity uint32
}

// inertTypes son los tipos que un navegador puede recibir sin ejecutar nada: con
// Content-Disposition: attachment y nosniff solo se descargan o se pintan en un <img>.
// SVG y HTML quedan fuera a proposito: son documentos con script.
var inertTypes = map[string]bool{
	"image/png":       true,
	"image/jpeg":      true,
	"image/gif":       true,
	"image/webp":      true,
	"application/pdf": true,
	"text/plain":      true,
}

// SafeDownloadType devuelve el Content-Type con el que se entrega una parte: el suyo si
// es inofensivo y application/octet-stream en cualquier otro caso.
func SafeDownloadType(contentType string) string {
	mt, _, err := mime.ParseMediaType(contentType)
	if err == nil && inertTypes[strings.ToLower(mt)] {
		return strings.ToLower(mt)
	}
	return "application/octet-stream"
}

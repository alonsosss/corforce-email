package domain

import (
	"errors"
	"mime"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Limites de redaccion que no son configuracion: acotan la forma del mensaje, no su
// volumen (el volumen lo fija Limits).
const (
	MaxAttachments       = 50
	MaxSubjectRunes      = 998
	maxDisplayNameRunes  = 256
	maxAddressFieldBytes = 64 << 10
	maxFilenameBytes     = 200
	maxExtensionBytes    = 16
	defaultFilename      = "adjunto"
)

// Limits son los topes de envio configurables.
type Limits struct {
	MaxRecipients   int
	MaxMessageBytes int64
}

// Validate rechaza topes que desactivarian el control.
func (l Limits) Validate() error {
	if l.MaxRecipients < 1 || l.MaxMessageBytes < 1 {
		return invalid("limits", "los topes de envio deben ser positivos")
	}
	return nil
}

// Attachment es un fichero adjuntado por el usuario.
type Attachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

// ReplyTarget identifica el mensaje al que se responde.
type ReplyTarget struct {
	Folder string
	UID    uint32
}

// PartSource son partes de un mensaje del propio buzon que se adjuntan tomadas del
// servidor (reenvio, borrador que se sigue redactando): no pasan por el navegador y se
// analizan con ClamAV igual que un adjunto subido.
type PartSource struct {
	Folder string
	UID    uint32
	Parts  []string
}

// NewPartSource valida la referencia: carpeta, UID y numeros de seccion sin repetir.
func NewPartSource(folder string, uid uint32, parts []string) (*PartSource, error) {
	if err := ValidateFolderName(folder); err != nil {
		var verr *ValidationError
		if errors.As(err, &verr) {
			return nil, invalid("source_folder", verr.Reason)
		}
		return nil, err
	}
	if uid == 0 {
		return nil, invalid("source_uid", "debe ser el UID del mensaje de origen")
	}
	if len(parts) == 0 {
		return nil, invalid("source_parts", "hace falta al menos una parte")
	}
	if len(parts) > MaxAttachments {
		return nil, invalid("source_parts", "demasiadas partes")
	}
	seen := make(map[string]bool, len(parts))
	for _, p := range parts {
		if _, err := ParsePartID(p); err != nil {
			return nil, invalid("source_parts", "identificador de parte invalido")
		}
		if seen[p] {
			return nil, invalid("source_parts", "parte repetida")
		}
		seen[p] = true
	}
	return &PartSource{Folder: folder, UID: uid, Parts: append([]string(nil), parts...)}, nil
}

// Draft es un mensaje redactado en el webmail, para enviar o para guardar como borrador.
type Draft struct {
	From        Address
	To          []Address
	Cc          []Address
	Bcc         []Address
	Subject     string
	Text        string
	HTML        string
	Attachments []Attachment
	InReplyTo   *ReplyTarget
	// Source son adjuntos que el servicio toma del buzon; al resolverse pasan a Attachments.
	Source *PartSource
}

// Outgoing es el borrador ya fechado e identificado, listo para componer.
type Outgoing struct {
	Draft
	MessageID  string
	Date       time.Time
	InReplyTo  string
	References []string
}

// SendResult es el desenlace de un envio. DraftRemoved solo es true si se pidio retirar un
// borrador y ya no esta en Borradores; Replayed indica que la peticion repetia un envio ya
// hecho con la misma clave y que no salio nada nuevo.
type SendResult struct {
	MessageID    string
	SavedToSent  bool
	DraftRemoved bool
	Replayed     bool
}

// ValidateForSend exige al menos un destinatario ademas de las reglas comunes.
func (d Draft) ValidateForSend(l Limits) error {
	if err := d.validate(l); err != nil {
		return err
	}
	if len(d.Recipients()) == 0 {
		return invalid("to", "hace falta al menos un destinatario")
	}
	return nil
}

// ValidateForSave admite un borrador sin destinatarios.
func (d Draft) ValidateForSave(l Limits) error {
	return d.validate(l)
}

func (d Draft) validate(l Limits) error {
	if d.From.Email == "" {
		return invalid("from", "es obligatorio")
	}
	if err := ValidateHeaderText("subject", d.Subject, 4*MaxSubjectRunes); err != nil {
		return err
	}
	if utf8.RuneCountInString(d.Subject) > MaxSubjectRunes {
		return invalid("subject", "demasiado largo")
	}
	if !utf8.ValidString(d.Text) {
		return invalid("text", "no es UTF-8 valido")
	}
	if !utf8.ValidString(d.HTML) {
		return invalid("html", "no es UTF-8 valido")
	}
	if len(d.Attachments)+d.pendingParts() > MaxAttachments {
		return invalid("attachments", "demasiados adjuntos")
	}
	if n := len(d.Recipients()); n > l.MaxRecipients {
		return ErrTooManyRecipients
	}
	if d.ContentBytes() > l.MaxMessageBytes {
		return ErrMessageTooLarge
	}
	return nil
}

func (d Draft) pendingParts() int {
	if d.Source == nil {
		return 0
	}
	return len(d.Source.Parts)
}

// Recipients son las direcciones del sobre SMTP: To, Cc y Bcc sin repetir.
func (d Draft) Recipients() []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]Address{d.To, d.Cc, d.Bcc} {
		for _, a := range list {
			key := strings.ToLower(a.Email)
			if !seen[key] {
				seen[key] = true
				out = append(out, a.Email)
			}
		}
	}
	return out
}

// ContentBytes es lo que el usuario aporta al mensaje. El tope real se vuelve a aplicar
// sobre el mensaje compuesto, que siempre pesa mas (codificacion base64 de adjuntos).
func (d Draft) ContentBytes() int64 {
	n := int64(len(d.Subject) + len(d.Text) + len(d.HTML))
	for _, a := range d.Attachments {
		n += int64(len(a.Data))
	}
	return n
}

// ValidateHeaderText rechaza un valor que no puede ir en una cabecera: UTF-8 invalido,
// CR, LF, NUL y cualquier otro caracter de control salvo el tabulador. Un CRLF dentro de
// un asunto o un nombre es inyeccion de cabeceras: permitiria anadir un Bcc invisible o
// partir el mensaje.
func ValidateHeaderText(field, value string, maxBytes int) error {
	if len(value) > maxBytes {
		return invalid(field, "demasiado largo")
	}
	if !utf8.ValidString(value) {
		return invalid(field, "no es UTF-8 valido")
	}
	for _, r := range value {
		if r == '\t' {
			continue
		}
		if isControl(r) {
			return invalid(field, "contiene saltos de linea o caracteres de control")
		}
	}
	return nil
}

var (
	localPartPattern = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+/=?^_`{|}~.-]{1,64}$")
	domainPattern    = regexp.MustCompile(`^[a-z0-9-]{1,63}(\.[a-z0-9-]{1,63})+$`)
)

// NewAddress valida una direccion para el sobre SMTP. Solo ASCII: Postfix descarta
// SMTPUTF8 (smtpd_discard_ehlo_keywords), asi que una direccion internacionalizada no
// llegaria a salir.
func NewAddress(field, name, email string) (Address, error) {
	email = strings.TrimSpace(email)
	at := strings.LastIndexByte(email, '@')
	if at <= 0 || at == len(email)-1 {
		return Address{}, invalid(field, "direccion invalida")
	}
	local, host := email[:at], strings.ToLower(email[at+1:])
	if !localPartPattern.MatchString(local) || strings.HasPrefix(local, ".") ||
		strings.HasSuffix(local, ".") || strings.Contains(local, "..") {
		return Address{}, invalid(field, "direccion invalida")
	}
	if len(host) > 253 || !domainPattern.MatchString(host) {
		return Address{}, invalid(field, "direccion invalida")
	}
	name = strings.TrimSpace(name)
	if err := ValidateHeaderText(field, name, 4*maxDisplayNameRunes); err != nil {
		return Address{}, err
	}
	if utf8.RuneCountInString(name) > maxDisplayNameRunes {
		return Address{}, invalid(field, "nombre demasiado largo")
	}
	return Address{Name: name, Email: local + "@" + host}, nil
}

// ParseAddressField interpreta los valores de un campo de direcciones: cada valor puede
// traer una lista separada por comas con nombres (RFC 5322).
func ParseAddressField(field string, values []string) ([]Address, error) {
	var out []Address
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if err := ValidateHeaderText(field, v, maxAddressFieldBytes); err != nil {
			return nil, err
		}
		list, err := mail.ParseAddressList(v)
		if err != nil {
			return nil, invalid(field, "lista de direcciones invalida")
		}
		for _, a := range list {
			addr, err := NewAddress(field, a.Name, a.Address)
			if err != nil {
				return nil, err
			}
			out = append(out, addr)
		}
	}
	return out, nil
}

// msgIDPattern es un identificador de mensaje sin corchetes (RFC 5322 3.6.4): ASCII
// visible sin espacios ni los delimitadores que lo rodean.
var msgIDPattern = regexp.MustCompile(`^[\x21-\x3b\x3d\x3f-\x7e]{1,250}$`)

// IsValidMessageID indica si un identificador puede ir en In-Reply-To o References. Se
// aplica a los que se copian de un mensaje recibido: un identificador hostil no debe
// poder romper ni ampliar las cabeceras de la respuesta.
func IsValidMessageID(id string) bool {
	return msgIDPattern.MatchString(id)
}

// NormalizeAttachmentType deja solo el tipo MIME de un adjunto, sin parametros: el tipo
// lo declara el navegador del usuario y un parametro es texto libre que acabaria en una
// cabecera del mensaje.
func NormalizeAttachmentType(contentType string) string {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.Contains(mt, "/") || strings.HasPrefix(strings.ToLower(mt), "multipart/") {
		return "application/octet-stream"
	}
	return strings.ToLower(mt)
}

// SanitizeFilename deja un nombre de fichero seguro para una cabecera y para el disco de
// quien lo descarga: sin rutas, sin caracteres de control, sin los marcadores de
// direccion bidireccional que disfrazan una extension (U+202E invierte "fdp.exe" para
// que se lea como ".pdf") y sin los caracteres que los sistemas de ficheros no admiten.
// Nunca devuelve vacio.
func SanitizeFilename(raw string) string {
	name := raw
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == utf8.RuneError, isControl(r), isBidiControl(r):
			continue
		case strings.ContainsRune(`"<>:|?*;`, r):
			b.WriteRune('_')
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	name = strings.Trim(b.String(), ". ")
	if len(name) > maxFilenameBytes {
		name = truncateKeepingExtension(name)
	}
	if name == "" {
		return defaultFilename
	}
	return name
}

func isBidiControl(r rune) bool {
	return (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) ||
		r == 0x200e || r == 0x200f || r == 0x061c
}

// truncateKeepingExtension recorta el nombre respetando los limites de UTF-8 y conserva
// la extension, que es lo que decide con que se abre el fichero.
func truncateKeepingExtension(name string) string {
	ext := ""
	if i := strings.LastIndexByte(name, '.'); i > 0 && len(name)-i <= maxExtensionBytes {
		ext = name[i:]
		name = name[:i]
	}
	limit := maxFilenameBytes - len(ext)
	for len(name) > limit {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return strings.TrimRight(name, ". ") + ext
}

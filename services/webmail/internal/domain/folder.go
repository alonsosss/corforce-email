package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// FolderRole es el papel de una carpeta especial (RFC 6154).
type FolderRole string

const (
	RoleNone    FolderRole = ""
	RoleInbox   FolderRole = "inbox"
	RoleSent    FolderRole = "sent"
	RoleDrafts  FolderRole = "drafts"
	RoleTrash   FolderRole = "trash"
	RoleJunk    FolderRole = "junk"
	RoleArchive FolderRole = "archive"
	// RoleScheduled es la carpeta de los envios programados. RFC 6154 no tiene atributo para
	// ella: se reconoce siempre por el nombre (ScheduledFolderName).
	RoleScheduled FolderRole = "scheduled"
	// RoleSnoozed es la carpeta de los mensajes pospuestos; como la de programados, por el nombre
	// (SnoozedFolderName).
	RoleSnoozed FolderRole = "snoozed"
)

// ScheduledFolderName es la carpeta donde espera el mensaje de un envio programado. La declara
// deploy/mail/dovecot/conf/dovecot.folders.conf (auto = no) y el webmail la crea al programar el
// primer envio del buzon.
const ScheduledFolderName = "Scheduled"

// SpecialRoles son los papeles que reconoce el webmail, en el orden en que se presentan.
var SpecialRoles = []FolderRole{RoleInbox, RoleDrafts, RoleScheduled, RoleSnoozed, RoleSent, RoleArchive, RoleJunk, RoleTrash}

// Folder es una carpeta del buzon. Total y Unread son cero cuando no se pidieron.
type Folder struct {
	Name       string
	Delimiter  string
	Role       FolderRole
	Selectable bool
	Total      uint32
	Unread     uint32
}

// MaxFolderNameBytes acota el nombre que se acepta de un cliente. Dovecot admite nombres
// mas largos, pero ninguno legitimo se acerca y el nombre viaja en cada comando IMAP.
const MaxFolderNameBytes = 512

// ValidateFolderName rechaza lo que no puede ser un nombre de carpeta seguro: vacio,
// demasiado largo, UTF-8 invalido, caracteres de control (un CR o LF partiria el comando
// IMAP aunque la libreria lo cite) y los comodines de LIST.
func ValidateFolderName(name string) error {
	if name == "" {
		return invalid("folder", "es obligatorio")
	}
	if len(name) > MaxFolderNameBytes {
		return invalid("folder", "demasiado largo")
	}
	if !utf8.ValidString(name) {
		return invalid("folder", "no es UTF-8 válido")
	}
	for _, r := range name {
		if isControl(r) {
			return invalid("folder", "contiene caracteres de control")
		}
		if r == '*' || r == '%' {
			return invalid("folder", "contiene comodines")
		}
	}
	return nil
}

// roleByName es el respaldo para servidores sin SPECIAL-USE. Dovecot si lo anuncia
// (deploy/mail/dovecot/conf/dovecot.folders.conf), asi que en la celda manda el atributo.
var roleByName = map[string]FolderRole{
	"sent":             RoleSent,
	"sent messages":    RoleSent,
	"sent items":       RoleSent,
	"drafts":           RoleDrafts,
	"trash":            RoleTrash,
	"deleted messages": RoleTrash,
	"deleted items":    RoleTrash,
	"junk":             RoleJunk,
	"spam":             RoleJunk,
	"archive":          RoleArchive,
	"scheduled":        RoleScheduled,
	"snoozed":          RoleSnoozed,
}

// RoleByName deduce el papel por el nombre. INBOX es siempre la bandeja de entrada y no
// distingue mayusculas (RFC 3501 5.1).
func RoleByName(name string) FolderRole {
	if strings.EqualFold(name, "INBOX") {
		return RoleInbox
	}
	return roleByName[strings.ToLower(name)]
}

// IsScheduledFolderName dice si name es la carpeta de envios programados. Es el unico papel que
// se reconoce por el nombre tambien cuando el servidor anuncia SPECIAL-USE.
func IsScheduledFolderName(name string) bool {
	return strings.EqualFold(name, ScheduledFolderName)
}

// FolderWithRole devuelve la primera carpeta seleccionable con ese papel.
func FolderWithRole(folders []Folder, role FolderRole) (Folder, bool) {
	for _, f := range folders {
		if f.Role == role && f.Selectable {
			return f, true
		}
	}
	return Folder{}, false
}

func isControl(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// FindFolder busca la carpeta por su nombre exacto. INBOX no distingue mayusculas (RFC 3501
// 5.1); el resto de nombres si, como en Dovecot.
func FindFolder(folders []Folder, name string) (Folder, bool) {
	for _, f := range folders {
		if f.Name == name || (strings.EqualFold(name, "INBOX") && strings.EqualFold(f.Name, "INBOX")) {
			return f, true
		}
	}
	return Folder{}, false
}

// FolderDelimiter es el separador de jerarquia que anuncia el servidor ("" si no hay ninguna
// carpeta con separador).
func FolderDelimiter(folders []Folder) string {
	for _, f := range folders {
		if f.Delimiter != "" {
			return f.Delimiter
		}
	}
	return ""
}

// ValidateNewFolderName aplica a un nombre que el usuario pone a una carpeta propia (al crearla o
// renombrarla) las reglas de ValidateFolderName mas las de jerarquia: sin separadores al principio
// o al final ni niveles vacios, "." o "..". INBOX y la carpeta de envios programados estan
// reservados: una carpeta propia con ese nombre tomaria su papel.
func ValidateNewFolderName(name, delimiter string) error {
	if err := ValidateFolderName(name); err != nil {
		return asField(err, "name")
	}
	if strings.EqualFold(name, "INBOX") || IsScheduledFolderName(name) || IsSnoozedFolderName(name) {
		return invalid("name", "es un nombre reservado")
	}
	if strings.TrimSpace(name) != name {
		return invalid("name", "no puede empezar ni terminar con espacios")
	}
	if delimiter == "" {
		return nil
	}
	for _, level := range strings.Split(name, delimiter) {
		if strings.TrimSpace(level) == "" || level == "." || level == ".." {
			return invalid("name", "tiene un nivel vacío o inválido")
		}
	}
	return nil
}

// ProtectedFolder dice si la carpeta no admite que el usuario la renombre ni la borre: INBOX y
// toda carpeta con papel (enviados, borradores, papelera, spam, archivo y programados).
func ProtectedFolder(f Folder) bool {
	return f.Role != RoleNone || strings.EqualFold(f.Name, "INBOX")
}

// CheckFolderChangeable exige que la carpeta exista y que ni ella ni ninguna de sus subcarpetas
// este protegida: renombrar un padre arrastra a sus hijas.
func CheckFolderChangeable(folders []Folder, name string) (Folder, error) {
	target, ok := FindFolder(folders, name)
	if !ok {
		return Folder{}, ErrFolderNotFound
	}
	if ProtectedFolder(target) {
		return Folder{}, ErrFolderProtected
	}
	for _, f := range Descendants(folders, target) {
		if ProtectedFolder(f) {
			return Folder{}, ErrFolderProtected
		}
	}
	return target, nil
}

// Descendants son las subcarpetas de parent, a cualquier profundidad.
func Descendants(folders []Folder, parent Folder) []Folder {
	if parent.Delimiter == "" {
		return nil
	}
	prefix := parent.Name + parent.Delimiter
	var out []Folder
	for _, f := range folders {
		if strings.HasPrefix(f.Name, prefix) {
			out = append(out, f)
		}
	}
	return out
}

// CheckRenameTarget valida el nombre nuevo de una carpeta: que no exista ya y que no quede dentro
// de si misma.
func CheckRenameTarget(folders []Folder, current Folder, newName string) error {
	if err := ValidateNewFolderName(newName, FolderDelimiter(folders)); err != nil {
		return err
	}
	if newName == current.Name {
		return invalid("name", "es el nombre actual")
	}
	if current.Delimiter != "" && strings.HasPrefix(newName, current.Name+current.Delimiter) {
		return invalid("name", "una carpeta no puede quedar dentro de si misma")
	}
	if _, exists := FindFolder(folders, newName); exists {
		return ErrFolderExists
	}
	return nil
}

// CheckEmptiable solo admite vaciar la papelera y el spam: vaciar otra carpeta de una vez borraria
// correo sin pasar por la papelera.
func CheckEmptiable(f Folder) error {
	if f.Role != RoleTrash && f.Role != RoleJunk {
		return ErrFolderNotEmptiable
	}
	return nil
}

// asField devuelve el ValidationError con otro nombre de campo; cualquier otro error, tal cual.
func asField(err error, field string) error {
	var verr *ValidationError
	if errors.As(err, &verr) {
		return invalid(field, verr.Reason)
	}
	return err
}

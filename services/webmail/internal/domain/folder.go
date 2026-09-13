package domain

import (
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
)

// SpecialRoles son los papeles que reconoce el webmail, en el orden en que se presentan.
var SpecialRoles = []FolderRole{RoleInbox, RoleDrafts, RoleSent, RoleArchive, RoleJunk, RoleTrash}

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
		return invalid("folder", "no es UTF-8 valido")
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
}

// RoleByName deduce el papel por el nombre. INBOX es siempre la bandeja de entrada y no
// distingue mayusculas (RFC 3501 5.1).
func RoleByName(name string) FolderRole {
	if strings.EqualFold(name, "INBOX") {
		return RoleInbox
	}
	return roleByName[strings.ToLower(name)]
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

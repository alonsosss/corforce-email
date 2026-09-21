package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Principal es el buzon que autentico mail-auth: la empresa elige la base y el buzon acota todo lo
// que se lee o se escribe. Ningun otro dato de la peticion (ruta, cabeceras) lo sustituye.
type Principal struct {
	TenantID  uuid.UUID
	MailboxID uuid.UUID
	Username  string
}

type Addressbook struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	MailboxID   uuid.UUID
	Slug        string
	DisplayName string
	Description string
	// SyncSeq crece en uno con cada cambio de la libreta: es su ctag y el numero de su token de
	// sincronizacion.
	SyncSeq int64
	// ChangesFloor es el ultimo cambio podado: un token anterior ya no se puede resolver como
	// diferencia y el cliente tiene que volver a listar.
	ChangesFloor int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Contact struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	MailboxID     uuid.UUID
	AddressbookID uuid.UUID
	ResourceName  string
	UID           string
	VCard         string
	ETag          string
	DisplayName   string
	Emails        []string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Change es el ultimo estado conocido de un recurso desde un token: Deleted lo da por borrado.
type Change struct {
	ResourceName string
	Deleted      bool
}

// Limits acota lo que un buzon puede guardar y lo que una peticion puede pedir.
type Limits struct {
	MaxVCardBytes             int
	MaxVCardProperties        int
	MaxContactsPerMailbox     int
	MaxAddressbooksPerMailbox int
	// MaxChangesRetained es cuantos cambios de una libreta se conservan para resolver un token.
	MaxChangesRetained int
}

func (l Limits) Validate() error {
	for name, v := range map[string]int{
		"MaxVCardBytes": l.MaxVCardBytes, "MaxVCardProperties": l.MaxVCardProperties,
		"MaxContactsPerMailbox": l.MaxContactsPerMailbox, "MaxAddressbooksPerMailbox": l.MaxAddressbooksPerMailbox,
		"MaxChangesRetained": l.MaxChangesRetained,
	} {
		if v < 1 {
			return fmt.Errorf("%s debe ser mayor que cero", name)
		}
	}
	return nil
}

const (
	MaxDisplayNameLength   = 200
	MaxDescriptionLength   = 1000
	maxUsernameLength      = 320
	MaxContactDisplayRunes = 300
)

var (
	slugRe         = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	resourceNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@=+~-]{0,195}\.vcf$`)
)

func ValidSlug(s string) bool { return slugRe.MatchString(s) }

// ValidResourceName admite solo lo que un cliente genera de forma normal (UUID.vcf y similares):
// nada de separadores, espacios ni caracteres de control, y siempre con la extension .vcf.
func ValidResourceName(s string) bool { return resourceNameRe.MatchString(s) }

// NormalizeUsername deja el nombre de buzon como lo guarda mail-auth: sin espacios y en minusculas.
func NormalizeUsername(raw string) (string, bool) {
	u := strings.ToLower(strings.TrimSpace(raw))
	local, host, ok := strings.Cut(u, "@")
	if !ok || local == "" || host == "" || len(u) > maxUsernameLength || strings.ContainsAny(u, " \t\r\n\x00/\\") {
		return "", false
	}
	return u, true
}

// ETagOf es el etag fuerte de un vCard: el SHA-256 de sus bytes exactos.
func ETagOf(vcard string) string {
	sum := sha256.Sum256([]byte(vcard))
	return hex.EncodeToString(sum[:])
}

const syncTokenPrefix = "urn:mail-dav:sync:"

// SyncToken liga la secuencia a la libreta: una libreta borrada y creada de nuevo con el mismo
// nombre empieza en cero, y un token de la anterior no puede resolverse como diferencia de la nueva.
func SyncToken(bookID uuid.UUID, seq int64) string {
	return syncTokenPrefix + bookID.String() + ":" + strconv.FormatInt(seq, 10)
}

// ParseSyncToken devuelve la libreta y la secuencia de un token; el vacio es la sincronizacion inicial
// (initial verdadero).
func ParseSyncToken(token string) (bookID uuid.UUID, seq int64, initial bool, err error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return uuid.Nil, 0, true, nil
	}
	rest, ok := strings.CutPrefix(token, syncTokenPrefix)
	if !ok {
		return uuid.Nil, 0, false, ErrInvalidSyncToken
	}
	id, num, ok := strings.Cut(rest, ":")
	if !ok {
		return uuid.Nil, 0, false, ErrInvalidSyncToken
	}
	bookID, err = uuid.Parse(id)
	if err != nil || bookID.String() != id {
		return uuid.Nil, 0, false, ErrInvalidSyncToken
	}
	seq, err = strconv.ParseInt(num, 10, 64)
	if err != nil || seq < 0 || strconv.FormatInt(seq, 10) != num {
		return uuid.Nil, 0, false, ErrInvalidSyncToken
	}
	return bookID, seq, false, nil
}

// Precondition son las cabeceras condicionales de un PUT o un DELETE, ya reducidas al valor de
// cada etag (sin comillas ni prefijo W/).
type Precondition struct {
	IfMatchAny     bool
	IfMatch        []string
	IfNoneMatchAny bool
	IfNoneMatch    []string
}

// Check compara con el etag actual del recurso (nil si no existe). If-Match exige que exista y
// coincida; If-None-Match exige lo contrario.
func (p Precondition) Check(current *string) error {
	if p.IfMatchAny || len(p.IfMatch) > 0 {
		if current == nil {
			return ErrPreconditionFailed
		}
		if !p.IfMatchAny && !contains(p.IfMatch, *current) {
			return ErrPreconditionFailed
		}
	}
	if p.IfNoneMatchAny && current != nil {
		return ErrPreconditionFailed
	}
	if current != nil && contains(p.IfNoneMatch, *current) {
		return ErrPreconditionFailed
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

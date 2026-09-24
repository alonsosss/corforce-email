package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// DownloadPath es la ruta publica del enlace tal como la declara el gateway (routes.json):
// /api/v1/public/files/{tenant}/{file}?x=<caducidad unix>&s=<firma>.
const DownloadPath = "/api/v1/public/files"

// linkPurpose abre el texto firmado: la firma de un enlace de fichero nunca vale como la de un enlace
// de baja, de ver en el navegador o de cuarentena, que usan la misma clave.
const linkPurpose = "mail-files"

// MinLinkKeyBytes es el minimo de MAIL_LINK_SIGNING_KEY, el mismo que exigen los demas servicios
// que firman enlaces con ella.
const MinLinkKeyBytes = 32

// LinkClaims es lo que protege la firma: la empresa (elige la base), el fichero y la caducidad.
type LinkClaims struct {
	TenantID  uuid.UUID
	FileID    uuid.UUID
	ExpiresAt time.Time
}

// LinkSigner firma y verifica los enlaces con HMAC-SHA256 y MAIL_LINK_SIGNING_KEY. La firma va
// completa en hexadecimal y se compara en tiempo constante.
type LinkSigner struct {
	key     []byte
	baseURL string
}

func NewLinkSigner(key, baseURL string) (*LinkSigner, error) {
	if len(key) < MinLinkKeyBytes {
		return nil, errors.New("MAIL_LINK_SIGNING_KEY debe tener al menos 32 caracteres")
	}
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" || base.User != nil || base.RawQuery != "" {
		return nil, errors.New("PUBLIC_BASE_URL debe ser una URL absoluta http(s) sin credenciales ni consulta")
	}
	return &LinkSigner{key: []byte(key), baseURL: base.String()}, nil
}

func (s *LinkSigner) canonical(c LinkClaims) []byte {
	return []byte(linkPurpose + "\n" + c.TenantID.String() + "\n" + c.FileID.String() + "\n" + strconv.FormatInt(c.ExpiresAt.Unix(), 10))
}

// Sign devuelve la firma hexadecimal de las claims.
func (s *LinkSigner) Sign(c LinkClaims) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write(s.canonical(c))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify comprueba la firma en tiempo constante. La caducidad la juzga quien llama, con su reloj.
func (s *LinkSigner) Verify(c LinkClaims, signature string) bool {
	expected := s.Sign(c)
	if len(signature) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) == 1
}

// URL construye el enlace completo que viaja en el correo.
func (s *LinkSigner) URL(c LinkClaims) string {
	q := url.Values{}
	q.Set("x", strconv.FormatInt(c.ExpiresAt.Unix(), 10))
	q.Set("s", s.Sign(c))
	return s.baseURL + DownloadPath + "/" + c.TenantID.String() + "/" + c.FileID.String() + "?" + q.Encode()
}

// ParseLink lee las claims de los segmentos y la consulta de la ruta publica. Cualquier dato
// ilegible es ErrLinkInvalid: el visitante no sabe que parte fallo.
func ParseLink(tenant, file, expires, signature string) (LinkClaims, string, error) {
	tenantID, err := uuid.Parse(tenant)
	if err != nil || tenantID == uuid.Nil {
		return LinkClaims{}, "", ErrLinkInvalid
	}
	fileID, err := uuid.Parse(file)
	if err != nil || fileID == uuid.Nil {
		return LinkClaims{}, "", ErrLinkInvalid
	}
	unix, err := strconv.ParseInt(expires, 10, 64)
	if err != nil || unix <= 0 {
		return LinkClaims{}, "", ErrLinkInvalid
	}
	if len(signature) != sha256.Size*2 {
		return LinkClaims{}, "", ErrLinkInvalid
	}
	return LinkClaims{TenantID: tenantID, FileID: fileID, ExpiresAt: time.Unix(unix, 0)}, signature, nil
}

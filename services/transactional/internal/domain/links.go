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

// Rutas publicas de los enlaces que viajan en el correo, tal como las declara el gateway.
const (
	UnsubscribePath   = "/api/v1/public/transactional/unsubscribe"
	ViewInBrowserPath = "/api/v1/public/transactional/view"
)

// DefaultViewInBrowserTTL es la vigencia del enlace de ver en el navegador cuando
// VIEW_IN_BROWSER_TTL no la fija.
const DefaultViewInBrowserTTL = 90 * 24 * time.Hour

// LinkSigner firma los enlaces de baja y de ver en el navegador con HMAC-SHA256 y
// MAIL_LINK_SIGNING_KEY. Cada enlace firma un texto que empieza por su proposito, asi que la
// firma de uno nunca vale para el otro. La firma va completa en hexadecimal (nunca truncada)
// y se compara en tiempo constante.
type LinkSigner struct {
	key     []byte
	baseURL string
}

func NewLinkSigner(key, baseURL string) (*LinkSigner, error) {
	if len(key) < 32 {
		return nil, errors.New("MAIL_LINK_SIGNING_KEY debe tener al menos 32 caracteres")
	}
	return &LinkSigner{key: []byte(key), baseURL: strings.TrimRight(baseURL, "/")}, nil
}

// UnsubscribeClaims es lo que protege la firma: empresa, mensaje y destinatario.
type UnsubscribeClaims struct {
	TenantID  uuid.UUID
	MessageID uuid.UUID
	Email     string
}

func (s *LinkSigner) canonical(c UnsubscribeClaims) []byte {
	return []byte("unsubscribe\n" + c.TenantID.String() + "\n" + c.MessageID.String() + "\n" + strings.ToLower(c.Email))
}

func (s *LinkSigner) mac(message []byte) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write(message)
	return hex.EncodeToString(mac.Sum(nil))
}

func sameSignature(expected, got string) bool {
	if len(got) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(got)) == 1
}

// Sign devuelve la firma hexadecimal de las claims.
func (s *LinkSigner) Sign(c UnsubscribeClaims) string {
	return s.mac(s.canonical(c))
}

// Verify comprueba la firma en tiempo constante.
func (s *LinkSigner) Verify(c UnsubscribeClaims, signature string) bool {
	return sameSignature(s.Sign(c), signature)
}

// ContainsUnsubscribeLink dice si el contenido renderizado lleva el enlace de baja de ese
// mensaje. Se busca por el prefijo de la ruta y por el identificador del mensaje, que
// ningun escapado de html/template altera (el resto de la URL puede llegar con & como
// &amp; o = como &#61;).
func (s *LinkSigner) ContainsUnsubscribeLink(content string, messageID uuid.UUID) bool {
	return strings.Contains(content, s.baseURL+UnsubscribePath) && strings.Contains(content, messageID.String())
}

// UnsubscribeURL construye el enlace completo que viaja en el correo.
func (s *LinkSigner) UnsubscribeURL(c UnsubscribeClaims) string {
	q := url.Values{}
	q.Set("t", c.TenantID.String())
	q.Set("m", c.MessageID.String())
	q.Set("e", c.Email)
	q.Set("sig", s.Sign(c))
	return s.baseURL + UnsubscribePath + "?" + q.Encode()
}

// ViewClaims protegen el enlace de ver en el navegador: empresa, mensaje y caducidad. No
// lleva la direccion del destinatario: el mensaje ya es de una sola persona y la URL no
// tiene por que exponerla en historiales ni registros.
type ViewClaims struct {
	TenantID  uuid.UUID
	MessageID uuid.UUID
	ExpiresAt time.Time
}

func (s *LinkSigner) viewCanonical(c ViewClaims) []byte {
	return []byte("view\n" + c.TenantID.String() + "\n" + c.MessageID.String() + "\n" + strconv.FormatInt(c.ExpiresAt.Unix(), 10))
}

// SignView devuelve la firma hexadecimal del enlace de ver en el navegador.
func (s *LinkSigner) SignView(c ViewClaims) string {
	return s.mac(s.viewCanonical(c))
}

// VerifyView comprueba la firma en tiempo constante. La caducidad la juzga quien llama, con
// su reloj.
func (s *LinkSigner) VerifyView(c ViewClaims, signature string) bool {
	return sameSignature(s.SignView(c), signature)
}

// ViewInBrowserURL construye el enlace completo; la caducidad viaja en segundos Unix.
func (s *LinkSigner) ViewInBrowserURL(c ViewClaims) string {
	q := url.Values{}
	q.Set("t", c.TenantID.String())
	q.Set("m", c.MessageID.String())
	q.Set("x", strconv.FormatInt(c.ExpiresAt.Unix(), 10))
	q.Set("sig", s.SignView(c))
	return s.baseURL + ViewInBrowserPath + "?" + q.Encode()
}

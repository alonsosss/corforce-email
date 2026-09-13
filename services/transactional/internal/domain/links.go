package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

// UnsubscribePath es la ruta publica del enlace de baja, tal como la declara el gateway.
const UnsubscribePath = "/api/v1/public/transactional/unsubscribe"

// LinkSigner firma los enlaces de baja con HMAC-SHA256 y MAIL_LINK_SIGNING_KEY. La firma
// va completa en hexadecimal (nunca truncada) y se compara en tiempo constante.
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

// Sign devuelve la firma hexadecimal de las claims.
func (s *LinkSigner) Sign(c UnsubscribeClaims) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write(s.canonical(c))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify comprueba la firma en tiempo constante.
func (s *LinkSigner) Verify(c UnsubscribeClaims, signature string) bool {
	expected := s.Sign(c)
	if len(signature) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) == 1
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

package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// formTokenKeyLabel separa la clave de los tokens de formulario de cualquier otro uso del
// secreto del que se deriva.
const formTokenKeyLabel = "core-force-mail/contacts/form-token/v1"

const (
	formTokenNonceBytes = 16
	maxFormTokenLength  = 128
	// minFormSecretLength es lo minimo que se acepta como secreto del que derivar la clave.
	minFormSecretLength = 32
)

// ErrFormTokenInvalid cubre el token alterado, de otro formulario, caducado, reutilizado o
// enviado antes del tiempo minimo de rellenado: quien automatiza envios no aprende cual fallo.
var ErrFormTokenInvalid = errors.New("el formulario caducó o se envió demasiado rápido: vuelve a cargarlo y envíalo de nuevo")

// FormTokenSigner emite y comprueba el token con que la plataforma sirve cada formulario. Lleva
// la hora de emision firmada: el envio exige un tiempo minimo de rellenado (un robot que envia
// al cargar no lo cumple) y un maximo de vigencia, y su nonce solo vale una vez.
type FormTokenSigner struct {
	key    []byte
	random io.Reader
}

// NewFormTokenSigner deriva la clave de firma de secret con HMAC-SHA256 y una etiqueta propia:
// la clave de los tokens no es el secreto ni sirve para nada mas.
func NewFormTokenSigner(secret string, random io.Reader) (*FormTokenSigner, error) {
	if len(secret) < minFormSecretLength {
		return nil, errors.New("CONTACTS_FORM_TOKEN_KEY falta o es demasiado corta (mínimo 32 caracteres: openssl rand -hex 32)")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(formTokenKeyLabel))
	return &FormTokenSigner{key: mac.Sum(nil), random: random}, nil
}

// Issue emite un token para el formulario con la hora dada.
func (s *FormTokenSigner) Issue(tenantID, formID uuid.UUID, now time.Time) (string, error) {
	nonce := make([]byte, formTokenNonceBytes)
	if _, err := io.ReadFull(s.random, nonce); err != nil {
		return "", err
	}
	issued := strconv.FormatInt(now.Unix(), 10)
	n := base64.RawURLEncoding.EncodeToString(nonce)
	return issued + "." + n + "." + s.sign(tenantID, formID, issued, n), nil
}

// Verify comprueba firma, formulario y ventana [minFill, maxAge] desde la emision, y devuelve
// el nonce para registrar su uso. Todo fallo es ErrFormTokenInvalid.
func (s *FormTokenSigner) Verify(token string, tenantID, formID uuid.UUID, now time.Time, minFill, maxAge time.Duration) (string, error) {
	if len(token) == 0 || len(token) > maxFormTokenLength {
		return "", ErrFormTokenInvalid
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", ErrFormTokenInvalid
	}
	issuedRaw, nonce, sig := parts[0], parts[1], parts[2]
	expected := s.sign(tenantID, formID, issuedRaw, nonce)
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return "", ErrFormTokenInvalid
	}
	issuedUnix, err := strconv.ParseInt(issuedRaw, 10, 64)
	if err != nil {
		return "", ErrFormTokenInvalid
	}
	elapsed := now.Sub(time.Unix(issuedUnix, 0))
	if elapsed < minFill || elapsed > maxAge {
		return "", ErrFormTokenInvalid
	}
	return nonce, nil
}

func (s *FormTokenSigner) sign(tenantID, formID uuid.UUID, issued, nonce string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(strings.Join([]string{"form", tenantID.String(), formID.String(), issued, nonce}, "\n")))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

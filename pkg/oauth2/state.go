// Package oauth2 reune la mecanica comun a cualquier integracion OAuth 2.0 del
// plataforma: el state firmado contra CSRF, el PKCE, el cifrado por empresa de los
// tokens y la validacion del redirect URI segun las reglas del proveedor.
//
// Antes vivia duplicada -dos implementaciones del mismo HMAC y dos constantes de
// TTL, una en meta-ads y otra en integration/google-. Cada proveedor nuevo la
// copiaba otra vez y el primero que olvidara un detalle abria un agujero.
package oauth2

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// StateTTL acota cuanto vive una autorizacion en vuelo. Diez minutos sobran para
// que una persona apruebe el consentimiento y no dejan la ventana abierta a un
// replay del codigo si el navegador queda expuesto.
const StateTTL = 10 * time.Minute

var (
	ErrInvalidState = errors.New("oauth2: state invalido")
	ErrStateExpired = errors.New("oauth2: state caducado")
)

// State es lo que viaja en el parametro homonimo y vuelve en el callback.
// Ademas de la defensa CSRF, el Nonce ata la respuesta a UNA solicitud concreta
// guardada en base: sin el, un state valido podria canjearse dos veces.
type State struct {
	TenantID uuid.UUID
	UserID   uuid.UUID
	Nonce    string
	IssuedAt time.Time
}

// StateSigner firma y verifica states con HMAC-SHA256. El purpose entra en la
// firma para que un state emitido por un proveedor no sea aceptado por otro
// aunque compartan la clave de plataforma.
type StateSigner struct {
	key     []byte
	purpose string
	ttl     time.Duration
}

func NewStateSigner(key []byte, purpose string) *StateSigner {
	return &StateSigner{key: key, purpose: purpose, ttl: StateTTL}
}

// WithTTL permite acortar la ventana en pruebas o en integraciones que exijan
// menos margen. Nunca se alarga por encima de StateTTL.
func (s *StateSigner) WithTTL(ttl time.Duration) *StateSigner {
	if ttl <= 0 || ttl > StateTTL {
		ttl = StateTTL
	}
	return &StateSigner{key: s.key, purpose: s.purpose, ttl: ttl}
}

func (s *StateSigner) configured() bool { return len(s.key) > 0 }

// Sign emite un state nuevo y devuelve tambien su contenido, porque el llamador
// necesita el Nonce para persistir la solicitud en vuelo.
func (s *StateSigner) Sign(tenantID, userID uuid.UUID) (string, State, error) {
	if !s.configured() {
		return "", State{}, errors.New("oauth2: falta la clave de firma del state")
	}
	nonceRaw := make([]byte, 16)
	if _, err := rand.Read(nonceRaw); err != nil {
		return "", State{}, err
	}
	st := State{
		TenantID: tenantID,
		UserID:   userID,
		Nonce:    base64.RawURLEncoding.EncodeToString(nonceRaw),
		IssuedAt: time.Now().UTC().Truncate(time.Second),
	}
	payload := s.payload(st)
	encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return encoded + "." + s.mac(encoded), st, nil
}

// Verify comprueba firma, caducidad y pertenencia a la empresa. El tenant se
// pasa aparte -no se lee del state- porque el state lo controla quien llama al
// callback: aceptar el tenant que trae el parametro dejaria a una empresa
// completar la autorizacion de otra.
func (s *StateSigner) Verify(raw string, tenantID uuid.UUID) (State, error) {
	if !s.configured() {
		return State{}, errors.New("oauth2: falta la clave de firma del state")
	}
	encoded, sig, found := strings.Cut(raw, ".")
	if !found || encoded == "" || sig == "" {
		return State{}, ErrInvalidState
	}
	if !hmac.Equal([]byte(sig), []byte(s.mac(encoded))) {
		return State{}, ErrInvalidState
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return State{}, ErrInvalidState
	}
	parts := strings.Split(string(decoded), "|")
	if len(parts) != 5 || parts[0] != s.purpose {
		return State{}, ErrInvalidState
	}
	st := State{Nonce: parts[3]}
	if st.TenantID, err = uuid.Parse(parts[1]); err != nil {
		return State{}, ErrInvalidState
	}
	if st.UserID, err = uuid.Parse(parts[2]); err != nil {
		return State{}, ErrInvalidState
	}
	seconds, err := strconv.ParseInt(parts[4], 10, 64)
	if err != nil {
		return State{}, ErrInvalidState
	}
	st.IssuedAt = time.Unix(seconds, 0).UTC()
	if st.TenantID != tenantID {
		return State{}, ErrInvalidState
	}
	if time.Since(st.IssuedAt) > s.ttl {
		return State{}, ErrStateExpired
	}
	return st, nil
}

func (s *StateSigner) payload(st State) string {
	return fmt.Sprintf("%s|%s|%s|%s|%d", s.purpose, st.TenantID, st.UserID, st.Nonce, st.IssuedAt.Unix())
}

func (s *StateSigner) mac(encoded string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(encoded))
	return hex.EncodeToString(mac.Sum(nil))
}

// FingerprintState reduce el state a un hash para poder guardarlo y compararlo
// sin conservar el valor original, que es material de sesion.
func FingerprintState(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

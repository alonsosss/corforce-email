package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Variables de entorno de las claves del token de acceso.
//
// JWT_SIGNING_KEY es secreto (almacen de secretos) y solo la recibe identity. El kid y las
// claves publicas son configuracion no sensible: el gateway verifica con las publicas y
// nunca ve la privada.
const (
	EnvSigningKey = "JWT_SIGNING_KEY"
	EnvSigningKID = "JWT_SIGNING_KID"
	EnvPublicKeys = "JWT_PUBLIC_KEYS"
)

var (
	// ErrSigningKeyRequired: identity sin clave de firma en un entorno que no admite el
	// par efimero.
	ErrSigningKeyRequired = errors.New(EnvSigningKey + " is required: only ENVIRONMENT development or test may use an ephemeral key")
	// ErrPublicKeysRequired: un verificador sin ninguna clave publica.
	ErrPublicKeysRequired = errors.New(EnvPublicKeys + " is required")
)

var kidPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func validateKID(kid string) error {
	if !kidPattern.MatchString(kid) {
		return fmt.Errorf("invalid kid %q: 1-64 characters from [A-Za-z0-9._-], starting with a letter or digit", kid)
	}
	return nil
}

// ParsePrivateKey lee una clave privada Ed25519 en PKCS#8 DER codificado en base64
// estandar (una sola linea: el almacen y el fichero de secretos no admiten saltos).
// Es lo que produce `openssl genpkey -algorithm ed25519 -outform DER | openssl base64 -A`.
// Los errores nunca incluyen el valor.
func ParsePrivateKey(value string) (ed25519.PrivateKey, error) {
	der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil, errors.New("private key is not standard base64")
	}
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, errors.New("private key is not PKCS#8 DER")
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is %T, not Ed25519", key)
	}
	return priv, nil
}

// ParsePublicKey lee una clave publica Ed25519 en SubjectPublicKeyInfo DER codificado en
// base64 estandar (`openssl pkey -pubout -outform DER`, o la GetPublicKey de KMS).
func ParsePublicKey(value string) (ed25519.PublicKey, error) {
	der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil, errors.New("public key is not standard base64")
	}
	key, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, errors.New("public key is not SubjectPublicKeyInfo DER")
	}
	pub, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is %T, not Ed25519", key)
	}
	return pub, nil
}

// EncodePublicKey es el inverso de ParsePublicKey.
func EncodePublicKey(pub ed25519.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(der), nil
}

// KeySet son las claves publicas aceptadas, por kid. Durante una rotacion convive la
// vigente con la anterior; un kid que no este aqui se rechaza.
type KeySet struct {
	keys map[string]ed25519.PublicKey
}

// ParseKeySet lee la forma de JWT_PUBLIC_KEYS: entradas `kid:clave` separadas por comas,
// con la clave como en ParsePublicKey. Un kid repetido es un error aunque la clave
// coincida: dos entradas para el mismo kid delatan una rotacion a medio escribir.
func ParseKeySet(spec string) (*KeySet, error) {
	ks := &KeySet{keys: map[string]ed25519.PublicKey{}}
	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		kid, value, ok := strings.Cut(entry, ":")
		if !ok {
			return nil, fmt.Errorf("%s: entry without kid (expected kid:base64)", EnvPublicKeys)
		}
		kid = strings.TrimSpace(kid)
		if err := validateKID(kid); err != nil {
			return nil, fmt.Errorf("%s: %w", EnvPublicKeys, err)
		}
		if _, dup := ks.keys[kid]; dup {
			return nil, fmt.Errorf("%s: kid %q appears twice", EnvPublicKeys, kid)
		}
		pub, err := ParsePublicKey(value)
		if err != nil {
			return nil, fmt.Errorf("%s: kid %q: %w", EnvPublicKeys, kid, err)
		}
		ks.keys[kid] = pub
	}
	if len(ks.keys) == 0 {
		return nil, ErrPublicKeysRequired
	}
	return ks, nil
}

// KeySetFromEnv lee JWT_PUBLIC_KEYS. Sin ella no hay verificador posible: el gateway no
// arranca.
func KeySetFromEnv() (*KeySet, error) {
	spec := strings.TrimSpace(os.Getenv(EnvPublicKeys))
	if spec == "" {
		return nil, ErrPublicKeysRequired
	}
	return ParseKeySet(spec)
}

// KIDs devuelve los kid aceptados, ordenados; es lo que se registra al arrancar.
func (ks *KeySet) KIDs() []string {
	out := make([]string, 0, len(ks.keys))
	for kid := range ks.keys {
		out = append(out, kid)
	}
	sort.Strings(out)
	return out
}

// Signer firma con la clave privada vigente y pone su kid en la cabecera.
type Signer struct {
	kid string
	key ed25519.PrivateKey
}

func NewSigner(kid string, key ed25519.PrivateKey) (*Signer, error) {
	if err := validateKID(kid); err != nil {
		return nil, err
	}
	if len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid Ed25519 private key size")
	}
	return &Signer{kid: kid, key: key}, nil
}

// NewEphemeralSigner genera un par que muere con el proceso. Solo para desarrollo y
// pruebas: ningun verificador lo conoce salvo que se le publique su clave publica.
func NewEphemeralSigner() (*Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ephemeral key: %w", err)
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return nil, fmt.Errorf("generate ephemeral kid: %w", err)
	}
	return NewSigner("ephemeral-"+hex.EncodeToString(suffix), priv)
}

// SignerFromEnv lee la clave de firma de identity (JWT_SIGNING_KEY y JWT_SIGNING_KID).
// Sin clave, solo allowEphemeral (ENVIRONMENT declarado development o test) genera un par
// efimero, y lo indica en ephemeral para que quien arranca lo avise; en cualquier otro
// entorno falla cerrado.
func SignerFromEnv(allowEphemeral bool) (signer *Signer, ephemeral bool, err error) {
	raw := strings.TrimSpace(os.Getenv(EnvSigningKey))
	kid := strings.TrimSpace(os.Getenv(EnvSigningKID))
	if raw == "" {
		if kid != "" {
			return nil, false, fmt.Errorf("%s is set without %s", EnvSigningKID, EnvSigningKey)
		}
		if !allowEphemeral {
			return nil, false, ErrSigningKeyRequired
		}
		signer, err := NewEphemeralSigner()
		return signer, err == nil, err
	}
	if kid == "" {
		return nil, false, fmt.Errorf("%s is required with %s", EnvSigningKID, EnvSigningKey)
	}
	key, err := ParsePrivateKey(raw)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", EnvSigningKey, err)
	}
	signer, err = NewSigner(kid, key)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", EnvSigningKID, err)
	}
	return signer, false, nil
}

func (s *Signer) KID() string { return s.kid }

func (s *Signer) PublicKey() ed25519.PublicKey {
	return s.key.Public().(ed25519.PublicKey)
}

// IssuerKeySet son las claves con las que identity verifica sus propios tokens de reto
// MFA y de step-up: las publicadas (JWT_PUBLIC_KEYS, que tambien lee el gateway) mas la
// suya. Si hay publicadas, tienen que incluir la clave de firma bajo su kid: si no, el
// gateway rechazaria todo lo que identity emite, y es mejor que identity no arranque. Esto
// fija el orden de una rotacion: primero se publica la clave publica nueva, despues se
// firma con ella.
func IssuerKeySet(signer *Signer, publishedSpec string) (*KeySet, error) {
	own := signer.PublicKey()
	if strings.TrimSpace(publishedSpec) == "" {
		return &KeySet{keys: map[string]ed25519.PublicKey{signer.kid: own}}, nil
	}
	ks, err := ParseKeySet(publishedSpec)
	if err != nil {
		return nil, err
	}
	published, ok := ks.keys[signer.kid]
	if !ok {
		return nil, fmt.Errorf("%s does not publish the signing kid %q: the gateway would reject every token", EnvPublicKeys, signer.kid)
	}
	if !published.Equal(own) {
		return nil, fmt.Errorf("%s publishes another key under the signing kid %q", EnvPublicKeys, signer.kid)
	}
	return ks, nil
}

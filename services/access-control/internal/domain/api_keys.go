package domain

import (
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Formato de una clave de API: cfm_<prefijo>_<secreto>. El prefijo identifica la clave en la
// base (es publico: la lista lo muestra y es el usuario SMTP) y el secreto es lo unico que la
// autentica. Los dos van en base32 en minusculas sin relleno, sin '_': el token se parte sin
// ambiguedad y cabe en una cabecera o en un usuario SMTP sin escaparlo.
const (
	APIKeyTokenPrefix = "cfm_"
	// APIKeyProvisioningTokenPrefix abre las credenciales de aprovisionamiento (docs/adr/0017), la
	// otra familia: crean empresas, dominios y claves de envio, y no mandan un solo correo. El
	// prefijo distinto las separa desde el primer byte, sin consultar la base.
	APIKeyProvisioningTokenPrefix = "cfp_"
	// apiKeyPrefixBytes da 12 caracteres (60 bits): unico entre todas las empresas con holgura.
	apiKeyPrefixBytes = 8
	APIKeyPrefixLen   = 12
	// apiKeySecretBytes: 256 bits. Una clave no se adivina, asi que el hash puede ser rapido.
	apiKeySecretBytes = 32
	APIKeySecretLen   = 52
	MaxAPIKeyNameLen  = 100
	// MaxAPIKeyLifetime acota la caducidad elegida: una clave sin caducidad es posible, pero una
	// fecha a siglos vista es casi siempre una errata.
	MaxAPIKeyLifetime = 5 * 365 * 24 * time.Hour
	// MinAPIKeyLifetime evita crear una clave que caduca antes de poder copiarla.
	MinAPIKeyLifetime = time.Hour
	// MaxAPIKeysPerTenant acota las claves vigentes de una empresa.
	MaxAPIKeysPerTenant = 100
)

var apiKeyEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

var (
	ErrAPIKeyNotFound = errors.New("api key not found")
	// ErrAPIKeyInvalid: el token no autentica ninguna clave usable. Se devuelve igual para una
	// clave inexistente, con otro secreto, revocada, caducada o de una cuenta cerrada: quien
	// prueba claves no debe distinguir los casos.
	ErrAPIKeyInvalid = errors.New("api key invalid")
	// ErrAPIKeyRevoked: la clave ya estaba revocada.
	ErrAPIKeyRevoked = errors.New("api key already revoked")
	// ErrAPIKeyScopeNotGrantable: el permiso no existe o no se puede dar a una clave.
	ErrAPIKeyScopeNotGrantable = errors.New("permission cannot be granted to an api key")
	// ErrAPIKeyLimit: la empresa ya tiene el maximo de claves vigentes.
	ErrAPIKeyLimit = errors.New("too many active api keys")
)

// Familias de credencial (docs/adr/0017). Sus poderes son disjuntos a proposito: una clave de envio
// manda correo y no gestiona credenciales; una de aprovisionamiento crea empresas, dominios y claves
// de envio, y no puede enviar ni leer un buzon. Quien las acepta decide por el tipo, no por el
// alcance: un alcance mal sembrado no convierte una credencial en la otra.
const (
	APIKeyKindSending      = "sending"
	APIKeyKindProvisioning = "provisioning"
)

// APIKeyKinds son las familias validas, en orden estable.
func APIKeyKinds() []string { return []string{APIKeyKindSending, APIKeyKindProvisioning} }

// APIKeyTokenPrefixFor es el prefijo del token de una familia; vacio si la familia no existe.
func APIKeyTokenPrefixFor(kind string) string {
	switch kind {
	case APIKeyKindSending:
		return APIKeyTokenPrefix
	case APIKeyKindProvisioning:
		return APIKeyProvisioningTokenPrefix
	}
	return ""
}

// APIKeyKindOf devuelve la familia que anuncia el token por su prefijo, vacia si no es ninguna.
func APIKeyKindOf(token string) string {
	switch {
	case strings.HasPrefix(token, APIKeyProvisioningTokenPrefix):
		return APIKeyKindProvisioning
	case strings.HasPrefix(token, APIKeyTokenPrefix):
		return APIKeyKindSending
	}
	return ""
}

// Estados visibles de una clave.
const (
	APIKeyActive  = "active"
	APIKeyRevoked = "revoked"
	APIKeyExpired = "expired"
)

// Motivos de rechazo al resolver una clave: solo para las metricas y el registro, nunca para
// quien la presenta.
const (
	APIKeyRejectMalformed = "malformed"
	APIKeyRejectUnknown   = "unknown"
	APIKeyRejectSecret    = "secret"
	APIKeyRejectRevoked   = "revoked"
	APIKeyRejectExpired   = "expired"
	APIKeyRejectOwner     = "owner"
	APIKeyRejectTenant    = "tenant"
	APIKeyRejectNoScope   = "no_scope"
)

// APIKey es una clave de API de una empresa. SecretHash y HashKeyID nunca salen del servicio.
type APIKey struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	Name       string
	Prefix     string
	Kind       string
	SecretHash []byte
	HashKeyID  string
	CreatedBy  uuid.UUID
	Scopes     []Permission
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	RevokedBy  *uuid.UUID
	LastUsedAt *time.Time
	LastUsedIP string
	CreatedAt  time.Time
}

// Status es el estado de la clave en el instante dado.
func (k APIKey) Status(now time.Time) string {
	switch {
	case k.RevokedAt != nil:
		return APIKeyRevoked
	case k.ExpiresAt != nil && !now.Before(*k.ExpiresAt):
		return APIKeyExpired
	}
	return APIKeyActive
}

// ResolvedAPIKey es lo que se sabe de una clave valida al presentarla: su empresa y su alcance
// efectivo en ese momento.
type ResolvedAPIKey struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	Kind      string
	Prefix    string
	Scopes    []Permission
	ExpiresAt *time.Time
}

// NewAPIKeyToken sortea prefijo y secreto con la fuente dada (crypto/rand en produccion).
func NewAPIKeyToken(random io.Reader) (prefix, secret string, err error) {
	var p [apiKeyPrefixBytes]byte
	var s [apiKeySecretBytes]byte
	if _, err := io.ReadFull(random, p[:]); err != nil {
		return "", "", fmt.Errorf("sortear el prefijo: %w", err)
	}
	if _, err := io.ReadFull(random, s[:]); err != nil {
		return "", "", fmt.Errorf("sortear el secreto: %w", err)
	}
	prefix = apiKeyEncoding.EncodeToString(p[:])[:APIKeyPrefixLen]
	return prefix, apiKeyEncoding.EncodeToString(s[:]), nil
}

// FormatAPIKeyToken compone el token que se entrega una sola vez, con el prefijo de su familia.
func FormatAPIKeyToken(kind, prefix, secret string) string {
	return APIKeyTokenPrefixFor(kind) + prefix + "_" + secret
}

// ParseAPIKeyToken separa familia, prefijo y secreto y comprueba su forma, sin tocar la base.
func ParseAPIKeyToken(token string) (kind, prefix, secret string, err error) {
	kind = APIKeyKindOf(token)
	if kind == "" {
		return "", "", "", ErrAPIKeyInvalid
	}
	rest, ok := strings.CutPrefix(token, APIKeyTokenPrefixFor(kind))
	if !ok {
		return "", "", "", ErrAPIKeyInvalid
	}
	prefix, secret, ok = strings.Cut(rest, "_")
	if !ok || !ValidAPIKeyPrefix(prefix) || len(secret) != APIKeySecretLen || !inAlphabet(secret) {
		return "", "", "", ErrAPIKeyInvalid
	}
	return kind, prefix, secret, nil
}

// ValidAPIKeyPrefix dice si s tiene la forma de un prefijo (tambien el usuario SMTP).
func ValidAPIKeyPrefix(s string) bool {
	return len(s) == APIKeyPrefixLen && inAlphabet(s)
}

func inAlphabet(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '2' && c <= '7') {
			return false
		}
	}
	return true
}

// APIKeyHashInput es lo que se firma: el prefijo va dentro para que el hash de una clave no
// sirva con el prefijo de otra.
func APIKeyHashInput(prefix, secret string) []byte {
	return []byte("core-force-mail/api-key/v1\n" + prefix + "\n" + secret)
}

// NormalizeAPIKeyName limpia y valida el nombre que la empresa da a la clave.
func NormalizeAPIKeyName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxAPIKeyNameLen {
		return "", fmt.Errorf("el nombre es obligatorio y admite hasta %d caracteres", MaxAPIKeyNameLen)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("el nombre no admite caracteres de control")
		}
	}
	return name, nil
}

// ValidateAPIKeyExpiry comprueba la caducidad elegida frente al instante de creacion.
func ValidateAPIKeyExpiry(expiresAt *time.Time, now time.Time) error {
	if expiresAt == nil {
		return nil
	}
	if expiresAt.Before(now.Add(MinAPIKeyLifetime)) {
		return errors.New("la caducidad debe ser al menos una hora posterior a la creación")
	}
	if expiresAt.After(now.Add(MaxAPIKeyLifetime)) {
		return errors.New("la caducidad no puede superar cinco anos")
	}
	return nil
}

// APIKeyEvent es el hecho auditable de una clave que se anuncia por la outbox.
type APIKeyEvent struct {
	Type      string
	TenantID  uuid.UUID
	ActorID   uuid.UUID
	KeyID     uuid.UUID
	Name      string
	Prefix    string
	Scopes    []Permission
	ExpiresAt *time.Time
	At        time.Time
}

// Tipos de APIKeyEvent.
const (
	APIKeyEventCreated = "created"
	APIKeyEventRevoked = "revoked"
)

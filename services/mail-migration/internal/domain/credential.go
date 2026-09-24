package domain

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"strings"

	"github.com/google/uuid"
)

// DestinationTokenPrefix abre toda credencial de destino de un trabajo. Dovecot lo usa para decidir, sin
// llamar a nadie, si una contrasena puede ser de un trabajo de migracion.
const DestinationTokenPrefix = "cfmj1"

const destinationSecretBytes = 32

// ErrCredentialInvalid es el unico motivo que sale de una verificacion fallida: un formato malo, un
// trabajo que no existe, uno cerrado, cancelado o con el lease vencido, un secreto que no coincide o un
// buzon distinto son lo mismo para quien pregunta.
var ErrCredentialInvalid = errors.New("credencial de destino no válida")

// DestinationCredential es la credencial que abre el buzon destino de UN trabajo mientras dura. Token
// viaja una sola vez, al ejecutor que reclama el trabajo; el servicio solo guarda Hash, el SHA-256 del
// secreto (256 bits aleatorios: no necesita un hash lento). El token lleva la empresa y el trabajo en
// claro porque el servicio abre una base por empresa y necesita saber en cual buscar; solo el secreto
// autentica.
type DestinationCredential struct {
	secret []byte
	Hash   []byte
}

// NewDestinationCredential genera el secreto con rnd (crypto/rand en produccion).
func NewDestinationCredential(rnd io.Reader) (DestinationCredential, error) {
	secret := make([]byte, destinationSecretBytes)
	if _, err := io.ReadFull(rnd, secret); err != nil {
		return DestinationCredential{}, err
	}
	return DestinationCredential{secret: secret, Hash: hashSecret(secret)}, nil
}

// Token compone la contrasena del trabajo: cfmj1.<empresa>.<trabajo>.<secreto>.
func (c DestinationCredential) Token(tenantID, jobID uuid.UUID) string {
	return strings.Join([]string{
		DestinationTokenPrefix, tenantID.String(), jobID.String(), base64.RawURLEncoding.EncodeToString(c.secret),
	}, ".")
}

// ParsedDestinationToken es un token con formato valido; todavia no se sabe si abre algo.
type ParsedDestinationToken struct {
	TenantID uuid.UUID
	JobID    uuid.UUID
	Hash     []byte
}

// ParseDestinationToken acepta solo la forma exacta que produce Token: nada de mayusculas en los
// identificadores ni de rellenos, para que un mismo secreto no tenga dos escrituras.
func ParseDestinationToken(token string) (ParsedDestinationToken, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != DestinationTokenPrefix {
		return ParsedDestinationToken{}, ErrCredentialInvalid
	}
	tenantID, err := uuid.Parse(parts[1])
	if err != nil || tenantID.String() != parts[1] || tenantID == uuid.Nil {
		return ParsedDestinationToken{}, ErrCredentialInvalid
	}
	jobID, err := uuid.Parse(parts[2])
	if err != nil || jobID.String() != parts[2] || jobID == uuid.Nil {
		return ParsedDestinationToken{}, ErrCredentialInvalid
	}
	secret, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(secret) != destinationSecretBytes || base64.RawURLEncoding.EncodeToString(secret) != parts[3] {
		return ParsedDestinationToken{}, ErrCredentialInvalid
	}
	return ParsedDestinationToken{TenantID: tenantID, JobID: jobID, Hash: hashSecret(secret)}, nil
}

// Matches compara en tiempo constante el hash del token con el guardado.
func (p ParsedDestinationToken) Matches(stored []byte) bool {
	return len(stored) == sha256.Size && subtle.ConstantTimeCompare(p.Hash, stored) == 1
}

func hashSecret(secret []byte) []byte {
	sum := sha256.Sum256(secret)
	return sum[:]
}

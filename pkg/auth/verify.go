package auth

import (
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
)

// Algorithm es el unico algoritmo que acepta el verificador. Fijarlo aqui, y no leerlo
// del token, es lo que cierra `none`, HS256 firmado con una clave publica como secreto y
// cualquier otro cambio de algoritmo.
const Algorithm = "EdDSA"

var (
	ErrAlgorithm   = errors.New("token algorithm is not " + Algorithm)
	ErrMissingKID  = errors.New("token without kid")
	ErrUnknownKID  = errors.New("token kid is not an accepted key")
	ErrTokenType   = errors.New("token type does not match")
	ErrTokenClaims = errors.New("token claims are incomplete")
	errNilVerifier = errors.New("no verifier configured")
)

// Verifier comprueba firma, kid, tipo, emisor y caducidad de los tokens de identity con
// las claves publicas aceptadas. Es inmutable tras crearse y seguro entre goroutines.
type Verifier struct {
	keys   map[string]ed25519.PublicKey
	parser *jwt.Parser
}

func NewVerifier(ks *KeySet) (*Verifier, error) {
	if ks == nil || len(ks.keys) == 0 {
		return nil, ErrPublicKeysRequired
	}
	keys := make(map[string]ed25519.PublicKey, len(ks.keys))
	for kid, pub := range ks.keys {
		keys[kid] = pub
	}
	return &Verifier{
		keys: keys,
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{Algorithm}),
			jwt.WithIssuer(Issuer),
			jwt.WithExpirationRequired(),
		),
	}, nil
}

func (v *Verifier) parse(tokenStr, typ string, claims jwt.Claims) error {
	if v == nil {
		return errNilVerifier
	}
	_, err := v.parser.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		// WithValidMethods ya lo rechaza antes de llegar aqui; se repite para que la
		// clave nunca se entregue a otro metodo aunque cambie la configuracion del parser.
		if t.Method.Alg() != Algorithm {
			return nil, ErrAlgorithm
		}
		if got, _ := t.Header["typ"].(string); got != typ {
			return nil, ErrTokenType
		}
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, ErrMissingKID
		}
		key, ok := v.keys[kid]
		if !ok {
			return nil, ErrUnknownKID
		}
		return key, nil
	})
	return err
}

// ParseAccess valida un token de acceso. Es lo que usa el gateway en cada peticion.
func (v *Verifier) ParseAccess(tokenStr string) (*Claims, error) {
	claims := &Claims{}
	if err := v.parse(tokenStr, typAccess, claims); err != nil {
		return nil, fmt.Errorf("parse access token: %w", err)
	}
	// Sin usuario el RBAC del gateway no tiene a quien aplicar la politica.
	if claims.UserID == "" || claims.TenantID == "" {
		return nil, ErrTokenClaims
	}
	return claims, nil
}

// ParseMFAChallenge valida el token de reto MFA y devuelve usuario y empresa.
func (v *Verifier) ParseMFAChallenge(tokenStr string) (userID, tenantID string, err error) {
	claims := &MFAChallengeClaims{}
	if err := v.parse(tokenStr, typMFAChallenge, claims); err != nil {
		return "", "", fmt.Errorf("parse mfa challenge: %w", err)
	}
	if claims.MFA != "challenge" || claims.UserID == "" {
		return "", "", fmt.Errorf("invalid mfa challenge token: %w", ErrTokenClaims)
	}
	return claims.UserID, claims.TenantID, nil
}

// ParseStepUp valida un token de step-up y devuelve usuario y empresa.
func (v *Verifier) ParseStepUp(tokenStr string) (userID, tenantID string, err error) {
	claims := &StepUpClaims{}
	if err := v.parse(tokenStr, typStepUp, claims); err != nil {
		return "", "", fmt.Errorf("parse step-up: %w", err)
	}
	if claims.Purpose != stepUpPurpose || claims.UserID == "" {
		return "", "", fmt.Errorf("invalid step-up token: %w", ErrTokenClaims)
	}
	return claims.UserID, claims.TenantID, nil
}

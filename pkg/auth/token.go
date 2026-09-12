package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Issuer es el emisor de todos los tokens del personal (identity y realtime).
// Se valida al parsear: un token que no lo lleve no es del personal. Como el
// secreto de firma se comparte con otros emisores (p. ej. el token de cliente del
// storefront), este claim es lo que impide que un token de otro dominio se use
// como token del personal.
const Issuer = "core-force-mail"

type TokenService struct {
	secret     []byte
	issuer     string
	accessTTL  time.Duration
	refreshTTL time.Duration
}

type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

type Claims struct {
	UserID      string   `json:"uid"`
	TenantID    string   `json:"tid"`
	Roles       []string `json:"roles"`
	jwt.RegisteredClaims
}

func NewTokenService(secret string, accessTTL, refreshTTL time.Duration) *TokenService {
	return &TokenService{
		secret:     []byte(secret),
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
		issuer:     Issuer,
	}
}

func (ts *TokenService) GeneratePair(userID, tenantID string, roles []string) (*TokenPair, error) {
	now := time.Now().UTC()

	accessClaims := &Claims{
		UserID:       userID,
		TenantID:     tenantID,
		Roles:        roles,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    ts.issuer,
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ts.accessTTL)),
		},
	}

	accessToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims).SignedString(ts.secret)
	if err != nil {
		return nil, fmt.Errorf("sign access token: %w", err)
	}

	refreshBytes := make([]byte, 32)
	if _, err := rand.Read(refreshBytes); err != nil {
		return nil, fmt.Errorf("generate refresh token: %w", err)
	}
	refreshToken := hex.EncodeToString(refreshBytes)

	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int64(ts.accessTTL.Seconds()),
		TokenType:    "Bearer",
	}, nil
}

func (ts *TokenService) ValidateAccess(tokenStr string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return ts.secret, nil
	}, jwt.WithIssuer(Issuer), jwt.WithExpirationRequired())
	if err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}
	if !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	return claims, nil
}

// MFAChallengeClaims are the claims embedded in a short-lived MFA challenge token.
type MFAChallengeClaims struct {
	UserID   string `json:"uid"`
	TenantID string `json:"tid"`
	MFA      string `json:"mfa"` // always "challenge"
	jwt.RegisteredClaims
}

// GenerateMFAChallenge returns a signed JWT valid for 5 minutes that represents
// a pending MFA verification step. It carries no role information.
func (ts *TokenService) GenerateMFAChallenge(userID, tenantID string) (string, error) {
	now := time.Now().UTC()
	claims := &MFAChallengeClaims{
		UserID:   userID,
		TenantID: tenantID,
		MFA:      "challenge",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    ts.issuer,
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(ts.secret)
	if err != nil {
		return "", fmt.Errorf("sign mfa challenge: %w", err)
	}
	return token, nil
}

// ValidateMFAChallenge parses and validates an MFA challenge token, returning
// the embedded userID and tenantID on success.
func (ts *TokenService) ValidateMFAChallenge(tokenStr string) (userID, tenantID string, err error) {
	claims := &MFAChallengeClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return ts.secret, nil
	}, jwt.WithIssuer(Issuer), jwt.WithExpirationRequired())
	if err != nil {
		return "", "", fmt.Errorf("parse mfa challenge: %w", err)
	}
	if !token.Valid || claims.MFA != "challenge" {
		return "", "", fmt.Errorf("invalid mfa challenge token")
	}
	return claims.UserID, claims.TenantID, nil
}

// Step-up (re-autenticacion para acciones criticas).
//
// Un token de step-up prueba que el usuario acaba de re-verificar su identidad
// (contrasena + MFA) hace poco. Las operaciones peligrosas lo exigen ademas de la
// sesion: asi, una sesion robada no basta para gestionar usuarios, cambiar cuentas
// bancarias, hacer reembolsos o exportar datos. Es corto (5 min) y stateless: se
// valida por firma, sin consultar ningun store, asi que no anade latencia ni cuello
// de botella. Lleva un "purpose" propio para no confundirse con el token de acceso.
const stepUpPurpose = "step-up"

// StepUpTTL es la vida del token de step-up: corto para que re-probar identidad
// cubra solo una ventana breve de acciones criticas.
const StepUpTTL = 5 * time.Minute

type StepUpClaims struct {
	UserID   string `json:"uid"`
	TenantID string `json:"tid"`
	Purpose  string `json:"pur"` // always "step-up"
	jwt.RegisteredClaims
}

// GenerateStepUp emite el token tras una re-autenticacion exitosa.
func (ts *TokenService) GenerateStepUp(userID, tenantID string) (string, error) {
	now := time.Now().UTC()
	claims := &StepUpClaims{
		UserID:   userID,
		TenantID: tenantID,
		Purpose:  stepUpPurpose,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    ts.issuer,
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(StepUpTTL)),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(ts.secret)
	if err != nil {
		return "", fmt.Errorf("sign step-up: %w", err)
	}
	return token, nil
}

// ValidateStepUp verifica un token de step-up con el secreto dado y devuelve el
// userID. Es standalone (no requiere TokenService) para que cualquier servicio lo
// valide en un middleware. Comprueba firma HS256, issuer, expiracion y purpose.
func ValidateStepUp(secret, tokenStr string) (userID, tenantID string, err error) {
	claims := &StepUpClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return []byte(secret), nil
	}, jwt.WithIssuer(Issuer), jwt.WithExpirationRequired())
	if err != nil {
		return "", "", fmt.Errorf("parse step-up: %w", err)
	}
	if !token.Valid || claims.Purpose != stepUpPurpose {
		return "", "", fmt.Errorf("invalid step-up token")
	}
	return claims.UserID, claims.TenantID, nil
}

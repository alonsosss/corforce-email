package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Issuer es el emisor de todos los tokens del personal (identity). Se valida al parsear.
const Issuer = "core-force-mail"

// Tipos de token, en la cabecera typ (RFC 8725, 3.11). Los tres salen de la misma clave:
// sin tipo explicito, un token de reto MFA (emitido tras la contrasena, antes del segundo
// factor) pasaria por un token de acceso. El verificador exige el tipo que espera.
const (
	typAccess       = "at+jwt" // RFC 9068
	typMFAChallenge = "mfa-challenge+jwt"
	typStepUp       = "step-up+jwt"
)

type TokenService struct {
	signer     *Signer
	verifier   *Verifier
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
	UserID   string   `json:"uid"`
	TenantID string   `json:"tid"`
	Roles    []string `json:"roles"`
	jwt.RegisteredClaims
}

// NewTokenService firma con signer y valida sus propios tokens de reto MFA y de step-up
// con verifier (IssuerKeySet: la clave vigente y, durante una rotacion, la anterior).
func NewTokenService(signer *Signer, verifier *Verifier, accessTTL, refreshTTL time.Duration) *TokenService {
	return &TokenService{
		signer:     signer,
		verifier:   verifier,
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
		issuer:     Issuer,
	}
}

// sign firma con EdDSA y lleva el kid de la clave y el tipo del token en la cabecera.
func (s *Signer) sign(claims jwt.Claims, typ string) (string, error) {
	t := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	t.Header["kid"] = s.kid
	t.Header["typ"] = typ
	return t.SignedString(s.key)
}

func (ts *TokenService) GeneratePair(userID, tenantID string, roles []string) (*TokenPair, error) {
	now := time.Now().UTC()

	accessClaims := &Claims{
		UserID:   userID,
		TenantID: tenantID,
		Roles:    roles,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    ts.issuer,
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ts.accessTTL)),
		},
	}

	accessToken, err := ts.signer.sign(accessClaims, typAccess)
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
	token, err := ts.signer.sign(claims, typMFAChallenge)
	if err != nil {
		return "", fmt.Errorf("sign mfa challenge: %w", err)
	}
	return token, nil
}

// ValidateMFAChallenge parses and validates an MFA challenge token, returning
// the embedded userID and tenantID on success.
func (ts *TokenService) ValidateMFAChallenge(tokenStr string) (userID, tenantID string, err error) {
	return ts.verifier.ParseMFAChallenge(tokenStr)
}

// Step-up (re-autenticacion para acciones criticas).
//
// Un token de step-up prueba que el usuario acaba de re-verificar su identidad
// (contrasena + MFA) hace poco. Las operaciones peligrosas lo exigen ademas de la
// sesion: asi, una sesion robada no basta para gestionar usuarios o cambiar la politica
// de sesion. Es corto (5 min) y stateless: se valida por firma, sin consultar ningun
// store. Lleva un tipo y un "purpose" propios para no confundirse con el token de acceso.
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
	token, err := ts.signer.sign(claims, typStepUp)
	if err != nil {
		return "", fmt.Errorf("sign step-up: %w", err)
	}
	return token, nil
}

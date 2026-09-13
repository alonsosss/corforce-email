package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/auth"
	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
)

type keyPair struct {
	private, public string
}

func newKeyPair(t *testing.T) keyPair {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	public, err := auth.EncodePublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return keyPair{private: base64.StdEncoding.EncodeToString(der), public: public}
}

func tokenEnv(t *testing.T, key, kid, published string) {
	t.Helper()
	t.Setenv(auth.EnvSigningKey, key)
	t.Setenv(auth.EnvSigningKID, kid)
	t.Setenv(auth.EnvPublicKeys, published)
}

func testConfig(allowEphemeral bool) *config.Config {
	return &config.Config{JWT: config.JWTConfig{AccessTTL: 5 * time.Minute, RefreshTTL: time.Hour, AllowEphemeralSigningKey: allowEphemeral}}
}

// identity firma con la clave del almacen y el kid de la configuracion, y el gateway,
// con solo las claves publicas, acepta lo que emite.
func TestIdentityEmiteConElKidConfigurado(t *testing.T) {
	vigente, anterior := newKeyPair(t), newKeyPair(t)
	tokenEnv(t, vigente.private, "20260913-a1b2c3d4", "20260101-00000001:"+anterior.public+",20260913-a1b2c3d4:"+vigente.public)

	tokens, verifier, err := newTokenService(testConfig(false), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tokens.GeneratePair("user-1", "tenant-1", []string{"tenant_admin"})
	if err != nil {
		t.Fatal(err)
	}
	parsed, _, err := jwt.NewParser().ParseUnverified(pair.AccessToken, &auth.Claims{})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Header["kid"] != "20260913-a1b2c3d4" || parsed.Header["alg"] != "EdDSA" {
		t.Fatalf("cabecera del token de acceso: %v", parsed.Header)
	}

	gatewayKeys, err := auth.KeySetFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := auth.NewVerifier(gatewayKeys)
	if err != nil {
		t.Fatal(err)
	}
	if claims, err := gateway.ParseAccess(pair.AccessToken); err != nil || claims.UserID != "user-1" {
		t.Fatalf("el gateway debe aceptar el token de identity: %v", err)
	}

	stepUp, err := tokens.GenerateStepUp("user-1", "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	if uid, _, err := verifier.ParseStepUp(stepUp); err != nil || uid != "user-1" {
		t.Fatalf("identity valida su propio step-up: %v", err)
	}
}

func TestIdentitySinClaveDeFirmaValida(t *testing.T) {
	tokenEnv(t, "", "", "")
	if _, _, err := newTokenService(testConfig(false), zap.NewNop()); !errors.Is(err, auth.ErrSigningKeyRequired) {
		t.Fatalf("fuera de desarrollo sin clave: %v, se esperaba ErrSigningKeyRequired", err)
	}

	tokens, _, err := newTokenService(testConfig(true), zap.NewNop())
	if err != nil {
		t.Fatalf("en desarrollo sin clave arranca con un par efimero: %v", err)
	}
	pair, _ := tokens.GeneratePair("u", "t", nil)
	parsed, _, _ := jwt.NewParser().ParseUnverified(pair.AccessToken, &auth.Claims{})
	if kid, _ := parsed.Header["kid"].(string); !strings.HasPrefix(kid, "ephemeral-") {
		t.Fatalf("kid del par efimero: %v", parsed.Header["kid"])
	}

	// Con claves publicadas, un par efimero nunca estaria entre ellas: el gateway
	// rechazaria todo, asi que identity no arranca.
	publicada := newKeyPair(t)
	tokenEnv(t, "", "", "20260913-a1b2c3d4:"+publicada.public)
	if _, _, err := newTokenService(testConfig(true), zap.NewNop()); err == nil {
		t.Fatal("par efimero con JWT_PUBLIC_KEYS fijada: identity no debe arrancar")
	}

	// El kid de firma publicado con otra clave (almacen y configuracion desincronizados).
	tokenEnv(t, newKeyPair(t).private, "20260913-a1b2c3d4", "20260913-a1b2c3d4:"+publicada.public)
	if _, _, err := newTokenService(testConfig(false), zap.NewNop()); err == nil {
		t.Fatal("clave de firma distinta de la publicada bajo su kid: identity no debe arrancar")
	}
}

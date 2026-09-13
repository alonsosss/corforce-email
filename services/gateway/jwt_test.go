package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/auth"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

func testSigner(t *testing.T, kid string) *auth.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := auth.NewSigner(kid, priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// identityTokens emite como identity: firma con signer y conoce solo su propia clave.
func identityTokens(t *testing.T, signer *auth.Signer) *auth.TokenService {
	t.Helper()
	ks, err := auth.IssuerKeySet(signer, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := auth.NewVerifier(ks)
	if err != nil {
		t.Fatal(err)
	}
	return auth.NewTokenService(signer, v, 5*time.Minute, time.Hour)
}

// testJWTAuth es la autenticacion del gateway con una clave efimera, para las pruebas que
// solo necesitan que una ruta exija sesion.
func testJWTAuth(t *testing.T) *middleware.JWTAuth {
	t.Helper()
	signer := testSigner(t, "prueba")
	public, err := auth.EncodePublicKey(signer.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(auth.EnvPublicKeys, "prueba:"+public)
	jwtAuth, _, err := jwtAuthFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	return jwtAuth
}

func TestGatewayVerificaConLasClavesPublicasDeIdentity(t *testing.T) {
	vigente := testSigner(t, "20260913-a1b2c3d4")
	public, err := auth.EncodePublicKey(vigente.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(auth.EnvPublicKeys, "20260913-a1b2c3d4:"+public)
	jwtAuth, kids, err := jwtAuthFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if len(kids) != 1 || kids[0] != "20260913-a1b2c3d4" {
		t.Fatalf("kids registrados: %v", kids)
	}

	var gotUser string
	r := chi.NewRouter()
	r.Use(jwtAuth.Authenticate)
	r.Get("/api/v1/users", func(w http.ResponseWriter, r *http.Request) {
		gotUser = middleware.GetUserID(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	status := func(token string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}

	tokens := identityTokens(t, vigente)
	pair, err := tokens.GeneratePair("user-1", "tenant-1", []string{"tenant_admin"})
	if err != nil {
		t.Fatal(err)
	}
	if code := status(pair.AccessToken); code != http.StatusOK || gotUser != "user-1" {
		t.Fatalf("token de identity: %d (usuario %q), se esperaba 200", code, gotUser)
	}

	// Un servicio comprometido con su propia clave, aunque use el kid vigente.
	forjado, _ := identityTokens(t, testSigner(t, "20260913-a1b2c3d4")).GeneratePair("user-1", "tenant-1", []string{"superadmin"})
	if code := status(forjado.AccessToken); code != http.StatusUnauthorized {
		t.Fatalf("token firmado con otra clave: %d, se esperaba 401", code)
	}

	// Un token HS256 como los de antes, con el secreto que tenian todos los servicios. No
	// hay transicion: se rechaza siempre.
	for _, kid := range []string{"", "20260913-a1b2c3d4"} {
		legacy := jwt.NewWithClaims(jwt.SigningMethodHS256, &auth.Claims{
			UserID: "user-1", TenantID: "tenant-1", Roles: []string{"superadmin"},
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer: auth.Issuer, IssuedAt: jwt.NewNumericDate(time.Now()),
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
			},
		})
		if kid != "" {
			legacy.Header["kid"] = kid
		}
		signed, err := legacy.SignedString([]byte("secreto-compartido-de-la-plataforma-0123456789"))
		if err != nil {
			t.Fatal(err)
		}
		if code := status(signed); code != http.StatusUnauthorized {
			t.Fatalf("HS256 (kid %q): %d, se esperaba 401", kid, code)
		}
	}

	// El reto MFA sale de identity con la misma clave, pero no abre la API.
	challenge, err := tokens.GenerateMFAChallenge("user-1", "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	if code := status(challenge); code != http.StatusUnauthorized {
		t.Fatalf("reto MFA como sesion: %d, se esperaba 401", code)
	}
}

func TestGatewaySinClavesPublicasNoArranca(t *testing.T) {
	t.Setenv(auth.EnvPublicKeys, "")
	if _, _, err := jwtAuthFromEnv(); err == nil {
		t.Fatal("sin JWT_PUBLIC_KEYS el gateway no debe arrancar")
	}
	t.Setenv(auth.EnvPublicKeys, "vigente:no-es-una-clave")
	if _, _, err := jwtAuthFromEnv(); err == nil {
		t.Fatal("con una clave ilegible el gateway no debe arrancar")
	}
}

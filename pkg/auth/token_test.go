package auth

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func newTestSigner(t *testing.T, kid string) *Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSigner(kid, priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func verifierOf(t *testing.T, signers ...*Signer) *Verifier {
	t.Helper()
	ks := &KeySet{keys: map[string]ed25519.PublicKey{}}
	for _, s := range signers {
		ks.keys[s.kid] = s.PublicKey()
	}
	v, err := NewVerifier(ks)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func serviceOf(t *testing.T, s *Signer, accessTTL time.Duration) *TokenService {
	t.Helper()
	return NewTokenService(s, verifierOf(t, s), accessTTL, time.Hour)
}

func accessClaims(uid, tid string, exp time.Time) *Claims {
	return &Claims{UserID: uid, TenantID: tid, RegisteredClaims: jwt.RegisteredClaims{
		Issuer: Issuer, Subject: uid, IssuedAt: jwt.NewNumericDate(time.Now()), ExpiresAt: jwt.NewNumericDate(exp),
	}}
}

func header(t *testing.T, token string) map[string]any {
	t.Helper()
	parsed, _, err := jwt.NewParser().ParseUnverified(token, &Claims{})
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Header
}

func TestFirmaYVerificacion(t *testing.T) {
	s := newTestSigner(t, "20260913-a1b2c3d4")
	pair, err := serviceOf(t, s, 5*time.Minute).GeneratePair("u-1", "t-1", []string{"tenant_admin"})
	if err != nil {
		t.Fatal(err)
	}
	h := header(t, pair.AccessToken)
	if h["alg"] != "EdDSA" || h["kid"] != "20260913-a1b2c3d4" || h["typ"] != "at+jwt" {
		t.Fatalf("cabecera inesperada: %v", h)
	}
	claims, err := verifierOf(t, s).ParseAccess(pair.AccessToken)
	if err != nil {
		t.Fatalf("un token de identity debe verificar: %v", err)
	}
	if claims.UserID != "u-1" || claims.TenantID != "t-1" || len(claims.Roles) != 1 || claims.IssuedAt == nil {
		t.Fatalf("claims inesperados: %+v", claims)
	}
}

func TestKidDesconocidoOAusente(t *testing.T) {
	accepted := newTestSigner(t, "vigente")
	stranger := newTestSigner(t, "otra")
	pair, _ := serviceOf(t, stranger, time.Minute).GeneratePair("u", "t", nil)
	if _, err := verifierOf(t, accepted).ParseAccess(pair.AccessToken); !errors.Is(err, ErrUnknownKID) {
		t.Fatalf("kid desconocido: %v, se esperaba ErrUnknownKID", err)
	}

	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, accessClaims("u", "t", time.Now().Add(time.Minute)))
	tok.Header["typ"] = typAccess
	signed, err := tok.SignedString(accepted.key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifierOf(t, accepted).ParseAccess(signed); !errors.Is(err, ErrMissingKID) {
		t.Fatalf("sin kid: %v, se esperaba ErrMissingKID", err)
	}
}

// Quien conozca un kid aceptado no puede firmar en su nombre con otra clave.
func TestMismoKidConOtraClave(t *testing.T) {
	accepted := newTestSigner(t, "vigente")
	impostor := newTestSigner(t, "vigente")
	pair, _ := serviceOf(t, impostor, time.Minute).GeneratePair("u", "t", nil)
	if _, err := verifierOf(t, accepted).ParseAccess(pair.AccessToken); !errors.Is(err, jwt.ErrTokenSignatureInvalid) {
		t.Fatalf("firma con otra clave: %v, se esperaba firma invalida", err)
	}
}

func TestAlgoritmoDistintoRechazado(t *testing.T) {
	s := newTestSigner(t, "vigente")
	v := verifierOf(t, s)
	claims := accessClaims("u", "t", time.Now().Add(time.Minute))

	sign := func(method jwt.SigningMethod, key any) string {
		t.Helper()
		tok := jwt.NewWithClaims(method, claims)
		tok.Header["kid"] = "vigente"
		tok.Header["typ"] = typAccess
		out, err := tok.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(s.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		// El HS256 de antes, con el secreto compartido que tenian todos los servicios.
		"HS256 con secreto compartido": sign(jwt.SigningMethodHS256, []byte("secreto-compartido-de-la-plataforma-0123456789")),
		// La confusion clasica: la clave publica usada como secreto HMAC.
		"HS256 con la clave publica": sign(jwt.SigningMethodHS256, pubDER),
		"ES256":                      sign(jwt.SigningMethodES256, ecKey),
		"none":                       sign(jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType),
	}
	for name, token := range cases {
		if _, err := v.ParseAccess(token); err == nil {
			t.Errorf("%s: aceptado, se esperaba rechazo", name)
		} else if !errors.Is(err, jwt.ErrTokenSignatureInvalid) {
			t.Errorf("%s: %v, se esperaba rechazo por algoritmo", name, err)
		}
	}
}

func TestRotacionConDosClavesPublicas(t *testing.T) {
	anterior := newTestSigner(t, "20260101-00000001")
	vigente := newTestSigner(t, "20260913-00000002")
	pairAnterior, _ := serviceOf(t, anterior, time.Minute).GeneratePair("u", "t", nil)
	pairVigente, _ := serviceOf(t, vigente, time.Minute).GeneratePair("u", "t", nil)

	durante := verifierOf(t, anterior, vigente)
	for name, token := range map[string]string{"anterior": pairAnterior.AccessToken, "vigente": pairVigente.AccessToken} {
		if _, err := durante.ParseAccess(token); err != nil {
			t.Errorf("durante la rotacion el token %s debe verificar: %v", name, err)
		}
	}
	despues := verifierOf(t, vigente)
	if _, err := despues.ParseAccess(pairAnterior.AccessToken); !errors.Is(err, ErrUnknownKID) {
		t.Fatalf("retirada la anterior, su token: %v, se esperaba ErrUnknownKID", err)
	}
	if _, err := despues.ParseAccess(pairVigente.AccessToken); err != nil {
		t.Fatalf("la vigente sigue verificando: %v", err)
	}
}

func TestTokenCaducado(t *testing.T) {
	s := newTestSigner(t, "vigente")
	pair, err := serviceOf(t, s, -time.Minute).GeneratePair("u", "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifierOf(t, s).ParseAccess(pair.AccessToken); !errors.Is(err, jwt.ErrTokenExpired) {
		t.Fatalf("caducado: %v, se esperaba ErrTokenExpired", err)
	}
}

func TestEmisorYClaimsObligatorios(t *testing.T) {
	s := newTestSigner(t, "vigente")
	v := verifierOf(t, s)

	ajeno := accessClaims("u", "t", time.Now().Add(time.Minute))
	ajeno.Issuer = "otro-emisor"
	token, _ := s.sign(ajeno, typAccess)
	if _, err := v.ParseAccess(token); !errors.Is(err, jwt.ErrTokenInvalidIssuer) {
		t.Fatalf("otro emisor: %v", err)
	}

	sinExp := accessClaims("u", "t", time.Now())
	sinExp.ExpiresAt = nil
	token, _ = s.sign(sinExp, typAccess)
	if _, err := v.ParseAccess(token); !errors.Is(err, jwt.ErrTokenRequiredClaimMissing) {
		t.Fatalf("sin exp: %v", err)
	}

	token, _ = s.sign(accessClaims("", "t", time.Now().Add(time.Minute)), typAccess)
	if _, err := v.ParseAccess(token); !errors.Is(err, ErrTokenClaims) {
		t.Fatalf("sin usuario: %v", err)
	}
}

// Los tres tokens salen de la misma clave: cada uno solo sirve para lo suyo. En particular
// el reto MFA, emitido antes del segundo factor, no abre la API.
func TestTiposDeTokenNoIntercambiables(t *testing.T) {
	s := newTestSigner(t, "vigente")
	ts := serviceOf(t, s, time.Minute)
	v := verifierOf(t, s)

	pair, _ := ts.GeneratePair("u", "t", nil)
	challenge, _ := ts.GenerateMFAChallenge("u", "t")
	stepUp, _ := ts.GenerateStepUp("u", "t")

	if uid, tid, err := ts.ValidateMFAChallenge(challenge); err != nil || uid != "u" || tid != "t" {
		t.Fatalf("reto MFA propio: %q %q %v", uid, tid, err)
	}
	if uid, _, err := v.ParseStepUp(stepUp); err != nil || uid != "u" {
		t.Fatalf("step-up propio: %q %v", uid, err)
	}
	for name, token := range map[string]string{"reto MFA": challenge, "step-up": stepUp} {
		if _, err := v.ParseAccess(token); !errors.Is(err, ErrTokenType) {
			t.Errorf("%s como token de acceso: %v, se esperaba ErrTokenType", name, err)
		}
	}
	if _, _, err := v.ParseStepUp(pair.AccessToken); !errors.Is(err, ErrTokenType) {
		t.Errorf("acceso como step-up: %v", err)
	}
	if _, _, err := v.ParseMFAChallenge(stepUp); !errors.Is(err, ErrTokenType) {
		t.Errorf("step-up como reto MFA: %v", err)
	}
}

func TestVerificadorSinClaves(t *testing.T) {
	if _, err := NewVerifier(nil); !errors.Is(err, ErrPublicKeysRequired) {
		t.Fatalf("sin claves: %v", err)
	}
	var v *Verifier
	if _, err := v.ParseAccess("x.y.z"); err == nil {
		t.Fatal("un verificador nulo no puede aceptar nada")
	}
}

func encodedPrivate(t *testing.T, key any) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(der)
}

func TestFormatoDeClaves(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	got, err := ParsePrivateKey(" " + encodedPrivate(t, priv) + "\n")
	if err != nil || !got.Equal(priv) {
		t.Fatalf("privada PKCS#8 en base64: %v", err)
	}
	encoded, err := EncodePublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	if back, err := ParsePublicKey(encoded); err != nil || !back.Equal(pub) {
		t.Fatalf("publica SPKI en base64: %v", err)
	}

	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if _, err := ParsePrivateKey(encodedPrivate(t, ec)); err == nil || !strings.Contains(err.Error(), "not Ed25519") {
		t.Fatalf("una privada ECDSA debe rechazarse: %v", err)
	}
	ecPub, _ := x509.MarshalPKIXPublicKey(&ec.PublicKey)
	if _, err := ParsePublicKey(base64.StdEncoding.EncodeToString(ecPub)); err == nil {
		t.Fatal("una publica ECDSA debe rechazarse")
	}
	corrupta := "MC4CAQAwBQYDK2VwBCIEI-no-es-base64"
	if _, err := ParsePrivateKey(corrupta); err == nil || strings.Contains(err.Error(), corrupta) {
		t.Fatalf("el error no puede repetir el valor: %v", err)
	}
}

func TestParseKeySet(t *testing.T) {
	a, b := newTestSigner(t, "b-2"), newTestSigner(t, "a-1")
	ea, _ := EncodePublicKey(a.PublicKey())
	eb, _ := EncodePublicKey(b.PublicKey())

	ks, err := ParseKeySet(" b-2:" + ea + " ,\n a-1:" + eb + ",")
	if err != nil {
		t.Fatal(err)
	}
	if kids := ks.KIDs(); len(kids) != 2 || kids[0] != "a-1" || kids[1] != "b-2" {
		t.Fatalf("kids: %v", kids)
	}
	for name, spec := range map[string]string{
		"vacio":          " , ",
		"sin kid":        ea,
		"kid repetido":   "a-1:" + ea + ",a-1:" + ea,
		"kid invalido":   "a 1:" + ea,
		"clave ilegible": "a-1:no-es-una-clave",
	} {
		if _, err := ParseKeySet(spec); err == nil {
			t.Errorf("%s: aceptado", name)
		}
	}
}

func TestSignerFromEnv(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	set := func(key, kid string) {
		t.Setenv(EnvSigningKey, key)
		t.Setenv(EnvSigningKID, kid)
	}

	set("", "")
	if _, _, err := SignerFromEnv(false); !errors.Is(err, ErrSigningKeyRequired) {
		t.Fatalf("sin clave fuera de desarrollo: %v", err)
	}
	s, ephemeral, err := SignerFromEnv(true)
	if err != nil || !ephemeral || !strings.HasPrefix(s.KID(), "ephemeral-") {
		t.Fatalf("par efimero en desarrollo: %v %v", ephemeral, err)
	}

	set("", "20260913-x")
	if _, _, err := SignerFromEnv(true); err == nil {
		t.Fatal("un kid sin clave es un error")
	}
	set(encodedPrivate(t, priv), "")
	if _, _, err := SignerFromEnv(false); err == nil {
		t.Fatal("una clave sin kid es un error")
	}
	set(encodedPrivate(t, priv), "20260913-x")
	s, ephemeral, err = SignerFromEnv(false)
	if err != nil || ephemeral || s.KID() != "20260913-x" || !s.PublicKey().Equal(priv.Public()) {
		t.Fatalf("clave del almacen: %v", err)
	}

	t.Setenv(EnvPublicKeys, "")
	if _, err := KeySetFromEnv(); !errors.Is(err, ErrPublicKeysRequired) {
		t.Fatalf("sin claves publicas: %v", err)
	}
}

func TestIssuerKeySet(t *testing.T) {
	vigente := newTestSigner(t, "vigente")
	anterior := newTestSigner(t, "anterior")
	ev, _ := EncodePublicKey(vigente.PublicKey())
	ea, _ := EncodePublicKey(anterior.PublicKey())

	if ks, err := IssuerKeySet(vigente, ""); err != nil || len(ks.KIDs()) != 1 {
		t.Fatalf("sin publicadas: solo la propia: %v", err)
	}
	if _, err := IssuerKeySet(vigente, "anterior:"+ea); err == nil {
		t.Fatal("publicadas sin la clave de firma: identity no debe arrancar")
	}
	if _, err := IssuerKeySet(vigente, "vigente:"+ea); err == nil {
		t.Fatal("otra clave bajo el kid de firma: identity no debe arrancar")
	}
	ks, err := IssuerKeySet(vigente, "anterior:"+ea+",vigente:"+ev)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := NewVerifier(ks)
	stepUp, _ := serviceOf(t, anterior, time.Minute).GenerateStepUp("u", "t")
	if _, _, err := v.ParseStepUp(stepUp); err != nil {
		t.Fatalf("un step-up firmado con la anterior sigue valiendo durante la rotacion: %v", err)
	}
}

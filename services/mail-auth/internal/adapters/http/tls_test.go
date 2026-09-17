package http

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

// writePair escribe un par nuevo como la renovacion: ficheros temporales y renombre.
func writePair(t *testing.T, dir string, serial int64) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: tlsTestHost},
		DNSNames:     []string{tlsTestHost},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	replaceFile(t, filepath.Join(dir, "server.key"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	replaceFile(t, filepath.Join(dir, "server.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func replaceFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path+".nuevo", data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".nuevo", path); err != nil {
		t.Fatal(err)
	}
}

const tlsTestHost = "mail-auth"

func servedSerial(t *testing.T, r *certReloader) int64 {
	t.Helper()
	cert, err := r.GetCertificate(&tls.ClientHelloInfo{ServerName: tlsTestHost})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf.SerialNumber.Int64()
}

func TestCertReloaderTomaElParRenovadoSinReiniciar(t *testing.T) {
	dir := t.TempDir()
	writePair(t, dir, 1)
	r, err := newCertReloader(filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key"), 0, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if got := servedSerial(t, r); got != 1 {
		t.Fatalf("serie servida %d, se esperaba 1", got)
	}
	writePair(t, dir, 2)
	if got := servedSerial(t, r); got != 2 {
		t.Fatalf("tras renovar se sirve la serie %d, se esperaba 2", got)
	}
}

func TestCertReloaderConservaElAnteriorSiElParNoCasa(t *testing.T) {
	dir := t.TempDir()
	writePair(t, dir, 1)
	r, err := newCertReloader(filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key"), 0, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	// Solo la clave nueva, como entre los dos renombres de la renovacion.
	other := t.TempDir()
	writePair(t, other, 9)
	key, err := os.ReadFile(filepath.Join(other, "server.key"))
	if err != nil {
		t.Fatal(err)
	}
	replaceFile(t, filepath.Join(dir, "server.key"), key)
	if got := servedSerial(t, r); got != 1 {
		t.Fatalf("con el par a medias se sirve la serie %d, se esperaba la anterior", got)
	}
	crt, err := os.ReadFile(filepath.Join(other, "server.crt"))
	if err != nil {
		t.Fatal(err)
	}
	replaceFile(t, filepath.Join(dir, "server.crt"), crt)
	if got := servedSerial(t, r); got != 9 {
		t.Fatalf("con el par completo se sirve la serie %d, se esperaba 9", got)
	}
}

func TestCertReloaderRespetaElIntervalo(t *testing.T) {
	dir := t.TempDir()
	writePair(t, dir, 1)
	r, err := newCertReloader(filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key"), time.Hour, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	writePair(t, dir, 2)
	if got := servedSerial(t, r); got != 1 {
		t.Fatalf("dentro del intervalo se relee el disco (serie %d)", got)
	}
}

func TestTLSConfigExigeElParCompleto(t *testing.T) {
	if _, _, err := TLSConfig("/no/existe.crt", "", tlsTestHost, zap.NewNop()); err == nil {
		t.Fatal("se acepta el certificado sin su clave")
	}
	if _, _, err := TLSConfig("/no/existe.crt", "/no/existe.key", tlsTestHost, zap.NewNop()); err == nil {
		t.Fatal("se arranca con ficheros que no existen")
	}
	cfg, selfSigned, err := TLSConfig("", "", tlsTestHost, zap.NewNop())
	if err != nil || !selfSigned || len(cfg.Certificates) != 1 {
		t.Fatalf("sin ficheros: selfSigned=%v err=%v", selfSigned, err)
	}
}

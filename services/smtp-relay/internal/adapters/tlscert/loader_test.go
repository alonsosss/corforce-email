package tlscert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
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

func writePair(t *testing.T, dir string, notAfter time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "smtp.relay.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "key.pem"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRecargaAlCambiarLosFicheros(t *testing.T) {
	dir := t.TempDir()
	first := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	writePair(t, dir, first)
	l, err := New(filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if !l.NotAfter().Equal(first) || l.Config().MinVersion == 0 {
		t.Fatalf("caducidad: %v", l.NotAfter())
	}
	now := time.Now()
	l.now = func() time.Time { return now }

	second := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	writePair(t, dir, second)
	later := now.Add(2 * time.Minute)
	for _, f := range []string{"cert.pem", "key.pem"} {
		if err := os.Chtimes(filepath.Join(dir, f), later, later); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.GetCertificate(nil); err != nil || !l.NotAfter().Equal(first) {
		t.Fatal("antes del intervalo se sirve el que habia")
	}
	l.now = func() time.Time { return now.Add(2 * checkInterval) }
	if _, err := l.GetCertificate(nil); err != nil || !l.NotAfter().Equal(second) {
		t.Fatalf("recargado: %v %v", l.NotAfter(), err)
	}

	// Una renovacion a medias (clave que no casa) conserva el certificado anterior.
	if err := os.WriteFile(filepath.Join(dir, "key.pem"), []byte("roto"), 0o600); err != nil {
		t.Fatal(err)
	}
	even := later.Add(time.Minute)
	_ = os.Chtimes(filepath.Join(dir, "key.pem"), even, even)
	l.now = func() time.Time { return now.Add(4 * checkInterval) }
	if c, err := l.GetCertificate(nil); err != nil || c == nil || !l.NotAfter().Equal(second) {
		t.Fatalf("fallo de recarga: %v", err)
	}
}

func TestSinCertificadoNoArranca(t *testing.T) {
	dir := t.TempDir()
	if _, err := New(filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"), zap.NewNop()); err == nil {
		t.Fatal("sin ficheros no hay relay")
	}
}

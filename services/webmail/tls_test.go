package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T, name string) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (ca testCA) leaf(t *testing.T, host string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// WEBMAIL_TLS_CA_FILE es la CA interna que firma mail-auth, pero el mismo pool verifica a
// Dovecot y Postfix con un certificado publico: anadir la CA no puede sustituir las raices del
// sistema. La raiz del sistema se simula con SSL_CERT_FILE en un proceso hijo, porque
// x509.SystemCertPool se carga una vez por proceso.
func TestTLSConfigAnadeLaCASinPerderLasRaicesDelSistema(t *testing.T) {
	const childEnv = "WEBMAIL_TLS_TEST_CHILD"
	if os.Getenv(childEnv) == "1" {
		verifyWithSystemAndInternalCA(t)
		return
	}
	dir := t.TempDir()
	public := newTestCA(t, "raiz publica simulada")
	internal := newTestCA(t, "CA interna")
	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	encode := func(c *x509.Certificate) []byte {
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestTLSConfigAnadeLaCASinPerderLasRaicesDelSistema$", "-test.v")
	cmd.Env = append(os.Environ(),
		childEnv+"=1",
		"SSL_CERT_DIR="+dir+"/sin-directorio",
		"SSL_CERT_FILE="+write("sistema.pem", public.pem),
		"TEST_INTERNAL_CA="+write("interna.pem", internal.pem),
		"TEST_PUBLIC_LEAF="+write("publico.pem", encode(public.leaf(t, "mail.cfm.test"))),
		"TEST_INTERNAL_LEAF="+write("mail-auth.pem", encode(internal.leaf(t, "mail-auth"))),
		"TEST_FOREIGN_LEAF="+write("ajeno.pem", encode(newTestCA(t, "ajena").leaf(t, "mail-auth"))),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("proceso hijo: %v\n%s", err, out)
	}
}

func verifyWithSystemAndInternalCA(t *testing.T) {
	read := func(env string) *x509.Certificate {
		data, err := os.ReadFile(os.Getenv(env))
		if err != nil {
			t.Fatal(err)
		}
		block, _ := pem.Decode(data)
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		return cert
	}
	verify := func(env, host string) error {
		cfg, err := tlsConfig(host, os.Getenv("TEST_INTERNAL_CA"), false)
		if err != nil {
			t.Fatal(err)
		}
		_, err = read(env).Verify(x509.VerifyOptions{DNSName: host, Roots: cfg.RootCAs})
		return err
	}
	if err := verify("TEST_PUBLIC_LEAF", "mail.cfm.test"); err != nil {
		t.Errorf("con la CA interna anadida no se verifica el certificado publico de los motores: %v", err)
	}
	if err := verify("TEST_INTERNAL_LEAF", "mail-auth"); err != nil {
		t.Errorf("no se verifica mail-auth con la CA interna: %v", err)
	}
	if err := verify("TEST_FOREIGN_LEAF", "mail-auth"); err == nil {
		t.Error("se acepta un mail-auth firmado por una CA ajena")
	}
}

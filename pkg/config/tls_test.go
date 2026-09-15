package config

import (
	"crypto/tls"
	"strings"
	"testing"
)

// Sin CA propia valen las raices del sistema; una CA que no se lee o que no es PEM impide
// arrancar en vez de degradar a otra verificacion.
func TestClientTLS(t *testing.T) {
	cfg, err := ClientTLS("mail.acme.test", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerName != "mail.acme.test" || cfg.MinVersion != tls.VersionTLS12 || cfg.RootCAs != nil || cfg.InsecureSkipVerify {
		t.Fatalf("configuracion %+v", cfg)
	}
	if _, err := ClientTLS("mail.acme.test", "/no/existe.pem"); err == nil || !strings.Contains(err.Error(), "CA file") {
		t.Fatalf("CA inexistente: %v", err)
	}
	if _, err := ClientTLS("mail.acme.test", writeTempFile(t, "vacia.pem", []byte("no es un certificado"))); err == nil || !strings.Contains(err.Error(), "no PEM") {
		t.Fatalf("CA sin PEM: %v", err)
	}
	ca := newTestCA(t, "cfm test CA")
	cfg, err = ClientTLS("", writeTempFile(t, "ca.pem", ca.pem))
	if err != nil || cfg.RootCAs == nil {
		t.Fatalf("CA propia: %v %+v", err, cfg)
	}
}

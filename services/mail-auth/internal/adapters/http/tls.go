package http

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"time"
)

// selfSignedValidity acota el certificado generado en memoria. Dovecot no lo verifica
// (insecure = true en passwd-verify.lua), asi que su caducidad no corta el servicio;
// se limita igualmente para que un despliegue sin certificado no arrastre una clave
// eterna.
const selfSignedValidity = 365 * 24 * time.Hour

// TLSConfig carga el par de certificado y clave del listener de Dovecot. Si no se
// configuro ninguno, genera un certificado autofirmado en memoria y lo indica con
// selfSigned=true para que main lo avise en el log: sirve para desarrollo y para
// arrancar antes de que operacion entregue el certificado interno; en produccion se
// esperan ficheros.
func TLSConfig(certFile, keyFile, hostname string) (cfg *tls.Config, selfSigned bool, err error) {
	var cert tls.Certificate
	switch {
	case certFile != "" && keyFile != "":
		cert, err = tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, false, fmt.Errorf("cargar certificado TLS: %w", err)
		}
	case certFile == "" && keyFile == "":
		cert, err = selfSignedCertificate(hostname)
		if err != nil {
			return nil, false, err
		}
		selfSigned = true
	default:
		return nil, false, fmt.Errorf("MAIL_AUTH_TLS_CERT y MAIL_AUTH_TLS_KEY deben configurarse juntas")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, selfSigned, nil
}

func selfSignedCertificate(hostname string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generar clave TLS: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generar numero de serie: %w", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: hostname},
		DNSNames:              []string{hostname},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(selfSignedValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("firmar certificado: %w", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

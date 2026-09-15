package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// ClientTLS es la configuracion TLS de un cliente que siempre verifica el certificado: contra
// serverName si no esta vacio (si lo esta, contra el host al que conecta) y con las raices del
// sistema mas, si caFile no esta vacio, las CA de ese fichero PEM. Minimo TLS 1.2.
func ClientTLS(serverName, caFile string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}
	if caFile == "" {
		return cfg, nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("CA file: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("CA file %s holds no PEM certificate", caFile)
	}
	cfg.RootCAs = pool
	return cfg, nil
}

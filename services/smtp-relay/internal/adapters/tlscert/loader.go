// Package tlscert sirve el certificado publico del relay desde ficheros y lo recarga cuando
// cambian: acme lo renueva cada pocas semanas y el relay no debe reiniciarse para tomarlo.
package tlscert

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"go.uber.org/zap"
)

// checkInterval es cada cuanto se mira si los ficheros cambiaron.
const checkInterval = time.Minute

// Loader implementa tls.Config.GetCertificate con recarga.
type Loader struct {
	certFile, keyFile string
	logger            *zap.Logger
	now               func() time.Time

	mu        sync.RWMutex
	cert      *tls.Certificate
	modCert   time.Time
	modKey    time.Time
	checkedAt time.Time
}

// New carga el certificado. Falla si no se puede leer o no es valido: sin certificado el relay no
// puede ofrecer TLS y no debe arrancar.
func New(certFile, keyFile string, logger *zap.Logger) (*Loader, error) {
	l := &Loader{certFile: certFile, keyFile: keyFile, logger: logger, now: time.Now}
	if err := l.reload(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Loader) reload() error {
	cert, err := tls.LoadX509KeyPair(l.certFile, l.keyFile)
	if err != nil {
		return fmt.Errorf("certificado TLS del relay: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return fmt.Errorf("certificado TLS del relay: %w", err)
	}
	cert.Leaf = leaf
	modCert, modKey, err := l.modTimes()
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.cert, l.modCert, l.modKey, l.checkedAt = &cert, modCert, modKey, l.now()
	l.mu.Unlock()
	return nil
}

func (l *Loader) modTimes() (time.Time, time.Time, error) {
	c, err := os.Stat(l.certFile)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	k, err := os.Stat(l.keyFile)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return c.ModTime(), k.ModTime(), nil
}

// GetCertificate devuelve el certificado vigente y, como mucho una vez por minuto, comprueba si
// los ficheros cambiaron. Una recarga fallida (acme escribiendo a medias) conserva el anterior.
func (l *Loader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	l.mu.RLock()
	cert, checked, modCert, modKey := l.cert, l.checkedAt, l.modCert, l.modKey
	l.mu.RUnlock()
	if cert == nil {
		return nil, errors.New("sin certificado")
	}
	if l.now().Sub(checked) < checkInterval {
		return cert, nil
	}
	c, k, err := l.modTimes()
	if err == nil && (!c.Equal(modCert) || !k.Equal(modKey)) {
		if err := l.reload(); err != nil {
			l.logger.Warn("smtp-relay: no se pudo recargar el certificado; se sigue con el anterior", zap.Error(err))
		} else {
			l.logger.Info("smtp-relay: certificado recargado")
		}
	}
	l.mu.Lock()
	l.checkedAt = l.now()
	current := l.cert
	l.mu.Unlock()
	return current, nil
}

// NotAfter es la caducidad del certificado vigente, para la metrica y la alerta.
func (l *Loader) NotAfter() time.Time {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.cert == nil || l.cert.Leaf == nil {
		return time.Time{}
	}
	return l.cert.Leaf.NotAfter
}

// Config es la configuracion TLS del relay: TLS 1.2 como minimo, como el resto de la plataforma.
func (l *Loader) Config() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: l.GetCertificate}
}

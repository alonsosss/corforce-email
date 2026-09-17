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
	"os"
	"sync"
	"time"

	"go.uber.org/zap"
)

// selfSignedValidity acota el certificado generado en memoria. Dovecot no lo verifica
// (insecure = true en passwd-verify.lua), asi que su caducidad no corta el servicio;
// se limita igualmente para que un despliegue sin certificado no arrastre una clave
// eterna.
const selfSignedValidity = 365 * 24 * time.Hour

// certReloadInterval es cada cuanto, como mucho, se miran los ficheros del certificado. La
// renovacion del TLS interno los reemplaza con margen de dias, asi que un retraso de segundos
// no importa, y acotarlo evita un stat por saludo y un aviso por saludo si el par queda roto.
const certReloadInterval = 15 * time.Second

// TLSConfig prepara el listener de Dovecot. Con MAIL_AUTH_TLS_CERT y MAIL_AUTH_TLS_KEY sirve
// ese par y lo relee cuando cambia en disco, sin reiniciar: un reinicio corta la verificacion
// de Dovecot, que ante un error de mail-auth vacia la cache del usuario y responde fallo. Sin
// ninguno de los dos genera un certificado autofirmado en memoria y lo indica con
// selfSigned=true para que main lo avise: sirve para desarrollo; en produccion se esperan
// ficheros.
func TLSConfig(certFile, keyFile, hostname string, logger *zap.Logger) (cfg *tls.Config, selfSigned bool, err error) {
	cfg = &tls.Config{MinVersion: tls.VersionTLS12}
	switch {
	case certFile != "" && keyFile != "":
		reloader, err := newCertReloader(certFile, keyFile, certReloadInterval, logger)
		if err != nil {
			return nil, false, err
		}
		cfg.GetCertificate = reloader.GetCertificate
	case certFile == "" && keyFile == "":
		cert, err := selfSignedCertificate(hostname)
		if err != nil {
			return nil, false, err
		}
		cfg.Certificates = []tls.Certificate{cert}
		selfSigned = true
	default:
		return nil, false, fmt.Errorf("MAIL_AUTH_TLS_CERT y MAIL_AUTH_TLS_KEY deben configurarse juntas")
	}
	return cfg, selfSigned, nil
}

// certReloader sirve el ultimo par valido. La renovacion reemplaza clave y certificado con dos
// renombres: entre ambos el par no casa, la carga falla y se sigue sirviendo el anterior hasta
// la siguiente comprobacion.
type certReloader struct {
	certFile, keyFile string
	interval          time.Duration
	logger            *zap.Logger

	mu        sync.Mutex
	cert      *tls.Certificate
	certInfo  os.FileInfo
	keyInfo   os.FileInfo
	checkedAt time.Time
}

func newCertReloader(certFile, keyFile string, interval time.Duration, logger *zap.Logger) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile, interval: interval, logger: logger}
	certInfo, keyInfo, err := r.stat()
	if err != nil {
		return nil, fmt.Errorf("cargar certificado TLS: %w", err)
	}
	if err := r.load(certInfo, keyInfo); err != nil {
		return nil, fmt.Errorf("cargar certificado TLS: %w", err)
	}
	r.checkedAt = time.Now()
	return r, nil
}

func (r *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.checkedAt) >= r.interval {
		r.checkedAt = time.Now()
		r.refresh()
	}
	return r.cert, nil
}

func (r *certReloader) refresh() {
	certInfo, keyInfo, err := r.stat()
	if err != nil {
		r.logger.Warn("mail-auth: no se pueden mirar los ficheros del certificado TLS; se sigue sirviendo el cargado", zap.Error(err))
		return
	}
	if sameFile(r.certInfo, certInfo) && sameFile(r.keyInfo, keyInfo) {
		return
	}
	if err := r.load(certInfo, keyInfo); err != nil {
		r.logger.Warn("mail-auth: el certificado TLS cambio en disco pero no se puede cargar; se sigue sirviendo el anterior", zap.Error(err))
		return
	}
	fields := []zap.Field{}
	if r.cert.Leaf != nil {
		fields = append(fields, zap.Time("not_after", r.cert.Leaf.NotAfter))
	}
	r.logger.Info("mail-auth: certificado TLS recargado", fields...)
}

func (r *certReloader) stat() (os.FileInfo, os.FileInfo, error) {
	certInfo, err := os.Stat(r.certFile)
	if err != nil {
		return nil, nil, err
	}
	keyInfo, err := os.Stat(r.keyFile)
	if err != nil {
		return nil, nil, err
	}
	return certInfo, keyInfo, nil
}

func (r *certReloader) load(certInfo, keyInfo os.FileInfo) error {
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return err
	}
	r.cert, r.certInfo, r.keyInfo = &cert, certInfo, keyInfo
	return nil
}

// sameFile compara identidad (inodo), tamano y fecha: un renombre cambia la primera y una
// reescritura en sitio, las otras.
func sameFile(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
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

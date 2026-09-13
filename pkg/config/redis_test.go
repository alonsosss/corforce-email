package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testCA es una CA desechable generada en la prueba: su clave nunca sale de memoria.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func randomSerial(t *testing.T) *big.Int {
	t.Helper()
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func newTestCA(t *testing.T, name string) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          randomSerial(t),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// issue firma un certificado de servidor para dnsName (y las IP dadas) y lo devuelve con
// su clave, los dos en PEM.
func (ca testCA) issue(t *testing.T, dnsName string, ips ...net.IP) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: randomSerial(t),
		Subject:      pkix.Name{CommonName: dnsName},
		DNSNames:     []string{dnsName},
		IPAddresses:  ips,
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func writeTempFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertVerifies(t *testing.T, cfg *tls.Config) {
	t.Helper()
	if cfg.InsecureSkipVerify || cfg.VerifyPeerCertificate != nil || cfg.VerifyConnection != nil {
		t.Fatal("la configuracion TLS de Redis no puede saltarse ni sustituir la verificacion")
	}
	if cfg.MinVersion < tls.VersionTLS12 {
		t.Fatalf("version minima %x, se exige TLS 1.2", cfg.MinVersion)
	}
}

func TestRedisTLSFromEnv(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		want    RedisTLS
		wantErr string
	}{
		{name: "sin variables va en claro", env: map[string]string{}, want: RedisTLS{}},
		{name: "true", env: map[string]string{"REDIS_TLS": "true"}, want: RedisTLS{Enabled: true}},
		{name: "1 con CA y nombre", env: map[string]string{"REDIS_TLS": " 1 ", "REDIS_TLS_CA_FILE": " /etc/cfm/redis-ca.pem ", "REDIS_TLS_SERVER_NAME": "redis.internal"},
			want: RedisTLS{Enabled: true, CAFile: "/etc/cfm/redis-ca.pem", ServerName: "redis.internal"}},
		{name: "false explicito", env: map[string]string{"REDIS_TLS": "false"}, want: RedisTLS{}},
		{name: "valor que no es booleano", env: map[string]string{"REDIS_TLS": "yes"}, wantErr: "REDIS_TLS"},
		{name: "CA con TLS apagado", env: map[string]string{"REDIS_TLS_CA_FILE": "/etc/cfm/redis-ca.pem"}, wantErr: "REDIS_TLS is off"},
		{name: "nombre con TLS apagado", env: map[string]string{"REDIS_TLS": "false", "REDIS_TLS_SERVER_NAME": "redis.internal"}, wantErr: "REDIS_TLS is off"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, key := range []string{"REDIS_TLS", "REDIS_TLS_CA_FILE", "REDIS_TLS_SERVER_NAME"} {
				t.Setenv(key, c.env[key])
			}
			got, err := RedisTLSFromEnv(PlatformRedisEnvPrefix)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("error %v, se esperaba uno con %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("%+v, se esperaba %+v", got, c.want)
			}
		})
	}
}

// El prefijo separa los dos Redis: la configuracion de uno no enciende ni apaga el otro.
func TestRedisTLSFromEnvPorPrefijo(t *testing.T) {
	t.Setenv("REDIS_TLS", "true")
	t.Setenv("MAIL_REDIS_TLS", "")
	t.Setenv("MAIL_REDIS_TLS_CA_FILE", "")
	t.Setenv("MAIL_REDIS_TLS_SERVER_NAME", "")
	engine, err := RedisTLSFromEnv(EngineRedisEnvPrefix)
	if err != nil || engine.Enabled {
		t.Fatalf("MAIL_REDIS_TLS sin definir: %+v, %v", engine, err)
	}
	t.Setenv("MAIL_REDIS_TLS", "on")
	if _, err := RedisTLSFromEnv(EngineRedisEnvPrefix); err == nil || !strings.Contains(err.Error(), "MAIL_REDIS_TLS") {
		t.Fatalf("el error debe nombrar la variable del prefijo: %v", err)
	}
}

// Fuera de un ENVIRONMENT declarado de desarrollo o de prueba el Redis de la plataforma no
// se abre en claro; la exigencia no impide cargar la configuracion a quien no usa Redis.
func TestLoadRedisExigeTLSFueraDeDesarrollo(t *testing.T) {
	cases := map[string]bool{"development": true, "Test": true, "production": false, "staging": false, "": false}
	for environment, plaintextAllowed := range cases {
		setEnv(t, map[string]string{"ENVIRONMENT": environment, "POSTGRES_PASSWORD": "platform-pass"})
		cfg, err := Load()
		if err != nil {
			t.Fatalf("ENVIRONMENT=%q: Load: %v", environment, err)
		}
		tlsCfg, err := cfg.Redis.TLSConfig()
		if plaintextAllowed {
			if err != nil || tlsCfg != nil {
				t.Errorf("ENVIRONMENT=%q: en claro deberia admitirse: %v, %v", environment, tlsCfg, err)
			}
			continue
		}
		if !errors.Is(err, ErrRedisTLSRequired) {
			t.Errorf("ENVIRONMENT=%q sin REDIS_TLS: %v, se esperaba ErrRedisTLSRequired", environment, err)
		}

		setEnv(t, map[string]string{"ENVIRONMENT": environment, "POSTGRES_PASSWORD": "platform-pass", "REDIS_TLS": "true"})
		rc, err := LoadRedis()
		if err != nil {
			t.Fatal(err)
		}
		tlsCfg, err = rc.TLSConfig()
		if err != nil || tlsCfg == nil {
			t.Fatalf("ENVIRONMENT=%q con REDIS_TLS=true: %v, %v", environment, tlsCfg, err)
		}
		assertVerifies(t, tlsCfg)
	}
}

// Un RedisConfig construido a mano sin decidir nada falla cerrado.
func TestRedisConfigSinDecidirExigeTLS(t *testing.T) {
	if _, err := (RedisConfig{Host: "redis", Port: 6379}).TLSConfig(); !errors.Is(err, ErrRedisTLSRequired) {
		t.Fatalf("%v, se esperaba ErrRedisTLSRequired", err)
	}
}

func TestLoadRechazaREDIS_TLSInvalido(t *testing.T) {
	setEnv(t, map[string]string{"ENVIRONMENT": "development", "POSTGRES_PASSWORD": "platform-pass", "REDIS_TLS": "si"})
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "REDIS_TLS") {
		t.Fatalf("un REDIS_TLS ilegible no puede leerse como apagado: %v", err)
	}
}

func TestRedisAddrConHostIPv6(t *testing.T) {
	if got := (RedisConfig{Host: "::1", Port: 6379}).Addr(); got != "[::1]:6379" {
		t.Fatalf("Addr = %q", got)
	}
	if got := (RedisConfig{Host: "redis", Port: 6380}).Addr(); got != "redis:6380" {
		t.Fatalf("Addr = %q", got)
	}
}

func TestClientConfigConCA(t *testing.T) {
	ca := newTestCA(t, "cfm test CA")
	certPEM, _ := ca.issue(t, "redis.internal")
	cfg, err := RedisTLS{Enabled: true, CAFile: writeTempFile(t, "ca.pem", ca.pem), ServerName: "redis.internal"}.ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	assertVerifies(t, cfg)
	if cfg.ServerName != "redis.internal" || cfg.RootCAs == nil {
		t.Fatalf("ServerName %q, RootCAs %v", cfg.ServerName, cfg.RootCAs)
	}
	block, _ := pem.Decode(certPEM)
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: cfg.RootCAs, DNSName: "redis.internal"}); err != nil {
		t.Fatalf("la CA del fichero no quedo entre las raices: %v", err)
	}

	sinCA, err := RedisTLS{Enabled: true}.ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	assertVerifies(t, sinCA)
	if sinCA.RootCAs != nil {
		t.Fatal("sin REDIS_TLS_CA_FILE deben valer las raices del sistema")
	}
}

func TestClientConfigErroresDeCA(t *testing.T) {
	if _, err := (RedisTLS{Enabled: true, CAFile: filepath.Join(t.TempDir(), "no-existe.pem")}).ClientConfig(); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("CA inexistente: %v", err)
	}
	notPEM := writeTempFile(t, "ca.pem", []byte("esto no es un certificado"))
	if _, err := (RedisTLS{Enabled: true, CAFile: notPEM}).ClientConfig(); err == nil || !strings.Contains(err.Error(), "no PEM certificate") {
		t.Fatalf("CA sin PEM: %v", err)
	}
	cfg, err := RedisTLS{}.ClientConfig()
	if err != nil || cfg != nil {
		t.Fatalf("TLS apagado: %v, %v", cfg, err)
	}
}

// Negociacion real contra un servidor TLS en proceso: solo pasa con la CA y el nombre que
// firman el certificado.
func TestClientConfigVerificaElCertificado(t *testing.T) {
	ca := newTestCA(t, "cfm test CA")
	certPEM, keyPEM := ca.issue(t, "redis.internal")
	serverCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{serverCert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.(*tls.Conn).Handshake()
			_ = conn.Close()
		}
	}()

	caFile := writeTempFile(t, "ca.pem", ca.pem)
	otherCA := writeTempFile(t, "other.pem", newTestCA(t, "otra CA").pem)
	dial := func(rt RedisTLS) error {
		cfg, err := rt.ClientConfig()
		if err != nil {
			t.Fatal(err)
		}
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", ln.Addr().String(), cfg)
		if err == nil {
			_ = conn.Close()
		}
		return err
	}

	if err := dial(RedisTLS{Enabled: true, CAFile: caFile, ServerName: "redis.internal"}); err != nil {
		t.Fatalf("con la CA y el nombre correctos: %v", err)
	}
	var unknown x509.UnknownAuthorityError
	if err := dial(RedisTLS{Enabled: true, ServerName: "redis.internal"}); !errors.As(err, &unknown) {
		t.Fatalf("solo con las raices del sistema: %v, se esperaba autoridad desconocida", err)
	}
	if err := dial(RedisTLS{Enabled: true, CAFile: otherCA, ServerName: "redis.internal"}); !errors.As(err, &unknown) {
		t.Fatalf("con otra CA: %v, se esperaba autoridad desconocida", err)
	}
	var hostname x509.HostnameError
	if err := dial(RedisTLS{Enabled: true, CAFile: caFile, ServerName: "otro.internal"}); !errors.As(err, &hostname) {
		t.Fatalf("con otro nombre: %v, se esperaba nombre que no coincide", err)
	}
}

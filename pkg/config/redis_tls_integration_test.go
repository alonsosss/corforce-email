//go:build integration

// Prueba del TLS hacia Redis contra un Redis 7 real que solo escucha en tls-port. La CA y el
// certificado del servidor se generan al correr la prueba: sus claves viven en memoria y
// dentro del contenedor, que la prueba crea y borra. REDIS_TLS_TEST_CONTAINER es el nombre
// del contenedor y REDIS_TLS_TEST_PORT el puerto del anfitrion (make test-integration los
// define):
//
//	REDIS_TLS_TEST_CONTAINER=cfm-it-tls-redis REDIS_TLS_TEST_PORT=27890 \
//	  go test -tags integration -race -count=1 -run TLS ./pkg/config/
package config

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	redisTLSTestImage      = "redis:7.4.10-alpine"
	redisTLSTestServerName = "redis-tls.test"
	redisTLSContainerPort  = "6380"
)

// integrationEnv devuelve la variable de entorno que apunta a la infraestructura de la
// prueba. Sin ella la prueba se salta, salvo con INTEGRATION_REQUIRED=1 (make
// test-integration y CI): ahi es un fallo, porque un salto esconderia que no llego.
func integrationEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		if os.Getenv("INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("%s no definida con INTEGRATION_REQUIRED=1", name)
		}
		t.Skipf("%s no definida", name)
	}
	return v
}

func dockerRun(t *testing.T, stdin []byte, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command("docker", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker %s: %v: %s", args[0], err, out)
	}
}

// startTLSRedis crea el Redis con tls-port y sin puerto en claro, le copia los ficheros por
// la entrada estandar (sin montar rutas del anfitrion) y lo arranca. La contrasena llega por
// el entorno del proceso docker, no por sus argumentos.
func startTLSRedis(t *testing.T, name, hostPort, password string, files map[string][]byte) {
	t.Helper()
	_ = exec.Command("docker", "rm", "-f", name).Run()
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	dockerRun(t, nil, []string{"REDIS_TLS_IT_PASSWORD=" + password},
		"create", "--name", name, "-e", "REDIS_TLS_IT_PASSWORD",
		"-p", "127.0.0.1:"+hostPort+":"+redisTLSContainerPort, redisTLSTestImage,
		"sh", "-c", `exec redis-server --port 0 --tls-port `+redisTLSContainerPort+
			` --tls-cert-file /tls/server.crt --tls-key-file /tls/server.key --tls-ca-cert-file /tls/ca.crt`+
			` --tls-auth-clients no --save "" --appendonly no --requirepass "$REDIS_TLS_IT_PASSWORD"`)

	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if err := tw.WriteHeader(&tar.Header{Name: "tls/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	for path, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: path, Mode: 0o600, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	dockerRun(t, archive.Bytes(), nil, "cp", "-", name+":/")
	dockerRun(t, nil, nil, "start", name)
}

func newTestRedisClient(t *testing.T, rc RedisConfig) *redis.Client {
	t.Helper()
	tlsCfg, err := rc.TLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(&redis.Options{
		Addr: rc.Addr(), Password: rc.Password, TLSConfig: tlsCfg,
		DialTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second,
		MaxRetries: -1,
	})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func pingErr(t *testing.T, rc RedisConfig) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return newTestRedisClient(t, rc).Ping(ctx).Err()
}

func TestRedisTLSContraRedisReal(t *testing.T) {
	container := integrationEnv(t, "REDIS_TLS_TEST_CONTAINER")
	port := integrationEnv(t, "REDIS_TLS_TEST_PORT")
	if _, err := strconv.Atoi(port); err != nil {
		t.Fatalf("REDIS_TLS_TEST_PORT=%q no es un puerto", port)
	}

	ca := newTestCA(t, "cfm-it redis CA")
	certPEM, keyPEM := ca.issue(t, redisTLSTestServerName, net.ParseIP("127.0.0.1"))
	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	password := hex.EncodeToString(secret)
	startTLSRedis(t, container, port, password, map[string][]byte{
		"tls/ca.crt": ca.pem, "tls/server.crt": certPEM, "tls/server.key": keyPEM,
	})

	// El contrato completo, como lo recibe un servicio en produccion.
	caFile := writeTempFile(t, "redis-ca.pem", ca.pem)
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("REDIS_HOST", "127.0.0.1")
	t.Setenv("REDIS_PORT", port)
	t.Setenv("REDIS_PASSWORD", password)
	t.Setenv("REDIS_TLS", "true")
	t.Setenv("REDIS_TLS_CA_FILE", caFile)
	t.Setenv("REDIS_TLS_SERVER_NAME", redisTLSTestServerName)
	rc, err := LoadRedis()
	if err != nil {
		t.Fatal(err)
	}

	var lastErr error
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		if lastErr = pingErr(t, rc); lastErr == nil {
			break
		}
	}
	if lastErr != nil {
		t.Fatalf("conexion verificada contra el Redis TLS: %v", lastErr)
	}

	t.Run("verificada lee y escribe", func(t *testing.T) {
		rdb := newTestRedisClient(t, rc)
		ctx := context.Background()
		if err := rdb.Set(ctx, "cfm:it:tls", "ok", time.Minute).Err(); err != nil {
			t.Fatal(err)
		}
		if got, err := rdb.Get(ctx, "cfm:it:tls").Result(); err != nil || got != "ok" {
			t.Fatalf("GET = %q, %v", got, err)
		}
	})

	t.Run("sin la CA propia no verifica", func(t *testing.T) {
		sinCA := rc
		sinCA.TLS.CAFile = ""
		var unknown x509.UnknownAuthorityError
		if err := pingErr(t, sinCA); !errors.As(err, &unknown) {
			t.Fatalf("%v, se esperaba autoridad desconocida", err)
		}
	})

	t.Run("con otra CA no verifica", func(t *testing.T) {
		otraCA := rc
		otraCA.TLS.CAFile = writeTempFile(t, "otra-ca.pem", newTestCA(t, "otra CA").pem)
		var unknown x509.UnknownAuthorityError
		if err := pingErr(t, otraCA); !errors.As(err, &unknown) {
			t.Fatalf("%v, se esperaba autoridad desconocida", err)
		}
	})

	t.Run("con otro nombre de servidor no verifica", func(t *testing.T) {
		otroNombre := rc
		otroNombre.TLS.ServerName = "otro.test"
		var hostname x509.HostnameError
		if err := pingErr(t, otroNombre); !errors.As(err, &hostname) {
			t.Fatalf("%v, se esperaba nombre que no coincide", err)
		}
	})

	t.Run("en claro no conecta", func(t *testing.T) {
		enClaro := rc
		enClaro.TLS = RedisTLS{}
		enClaro.AllowPlaintext = true
		if err := pingErr(t, enClaro); err == nil {
			t.Fatal("un cliente en claro no deberia hablar con el puerto TLS")
		}
	})

	t.Run("contrasena equivocada no entra", func(t *testing.T) {
		otraClave := rc
		otraClave.Password = password + "x"
		if err := pingErr(t, otraClave); err == nil {
			t.Fatal("con TLS verificado la contrasena se sigue exigiendo")
		}
	})
}

//go:build integration

// Pruebas del contador de extraccion masiva contra un Redis real (make test-integration da
// REDIS_TEST_ADDR y REDIS_TEST_PASSWORD).
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

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

func redisDePrueba(t *testing.T) *redis.Client {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{
		Addr:     integrationEnv(t, "REDIS_TEST_ADDR"),
		Password: os.Getenv("REDIS_TEST_PASSWORD"),
	})
	t.Cleanup(func() { _ = rdb.Close() })
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("redis de prueba: %v", err)
	}
	return rdb
}

func usuarioUnico(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "usuario-" + hex.EncodeToString(b)
}

// replicaDePrueba es un gateway aparte: su propio contador (y su memoria) sobre el Redis
// comun.
func replicaDePrueba(store middleware.RateLimitStore, threshold int, window time.Duration) *auditTrail {
	return &auditTrail{
		reads:       newExfilCounter(store, threshold, window, zap.NewNop()),
		exfilMax:    int64(threshold),
		exfilWindow: window,
	}
}

func degradadas(t *testing.T) float64 {
	t.Helper()
	familias, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range familias {
		if f.GetName() != "rate_limit_degraded_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "limiter" && l.GetValue() == exfilLimiterName {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// Repartir las lecturas entre dos replicas no esquiva el umbral: el total es uno solo y
// la alerta sale una vez, en la lectura que lo alcanza, sea cual sea la replica que la
// atienda.
func TestExfilDosReplicasCompartenElConteo(t *testing.T) {
	rdb := redisDePrueba(t)
	store := middleware.NewRedisRateLimitStore(rdb)
	a := replicaDePrueba(store, 6, time.Minute)
	b := replicaDePrueba(store, 6, time.Minute)
	user := usuarioUnico(t)
	ctx := context.Background()

	var alertas []int64
	for i := 0; i < 10; i++ {
		replica := a
		if i%2 == 1 {
			replica = b
		}
		n, alert := replica.trackRead(ctx, user)
		if n != int64(i+1) {
			t.Fatalf("lectura %d: total %d; las dos replicas deben ver un conteo comun", i+1, n)
		}
		if alert {
			alertas = append(alertas, n)
		}
	}
	if len(alertas) != 1 || alertas[0] != 6 {
		t.Fatalf("alertas: %v; se esperaba una sola, en la lectura 6", alertas)
	}
}

// La ventana la cierra Redis: pasada, el conteo vuelve a empezar y una nueva extraccion
// vuelve a alertar.
func TestExfilLaVentanaExpira(t *testing.T) {
	rdb := redisDePrueba(t)
	store := middleware.NewRedisRateLimitStore(rdb)
	a := replicaDePrueba(store, 3, time.Second)
	b := replicaDePrueba(store, 3, time.Second)
	user := usuarioUnico(t)
	ctx := context.Background()

	if _, alert := a.trackRead(ctx, user); alert {
		t.Fatal("alerta antes del umbral")
	}
	b.trackRead(ctx, user)
	if n, alert := a.trackRead(ctx, user); !alert || n != 3 {
		t.Fatalf("tercera lectura: total %d, alerta %v", n, alert)
	}

	time.Sleep(1300 * time.Millisecond)

	if n, alert := b.trackRead(ctx, user); n != 1 || alert {
		t.Fatalf("tras la ventana el conteo debe reiniciarse: total %d, alerta %v", n, alert)
	}
	a.trackRead(ctx, user)
	if n, alert := b.trackRead(ctx, user); !alert || n != 3 {
		t.Fatalf("la segunda ventana debe volver a alertar: total %d, alerta %v", n, alert)
	}
}

// Con Redis inalcanzable cada replica cuenta en su memoria: la lectura no espera al
// almacen, nunca se corta y se avisa en rate_limit_degraded_total.
func TestExfilRedisInalcanzableDegradaAMemoria(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 200 * time.Millisecond})
	t.Cleanup(func() { _ = rdb.Close() })
	store := middleware.NewRedisRateLimitStore(rdb)
	a := replicaDePrueba(store, 3, time.Minute)
	b := replicaDePrueba(store, 3, time.Minute)
	user := usuarioUnico(t)
	ctx := context.Background()
	antes := degradadas(t)

	inicio := time.Now()
	for i, replica := range []*auditTrail{a, a, b, b, a, b} {
		n, alert := replica.trackRead(ctx, user)
		propio := localEsperado(i)
		if n != propio {
			t.Fatalf("lectura %d: total %d, se esperaba el propio de la replica (%d)", i+1, n, propio)
		}
		if alert != (n == 3) {
			t.Fatalf("lectura %d: alerta %v con total %d", i+1, alert, n)
		}
	}
	if elapsed := time.Since(inicio); elapsed > sharedTimeoutParaPrueba+time.Second {
		t.Fatalf("seis lecturas con Redis caido tardaron %v", elapsed)
	}
	if got := degradadas(t) - antes; got != 6 {
		t.Fatalf("rate_limit_degraded_total subio %v; se esperaba 6", got)
	}
}

// sharedTimeoutParaPrueba es el plazo por peticion del almacen (pkg/middleware.sharedTimeout).
const sharedTimeoutParaPrueba = 250 * time.Millisecond

// localEsperado da el total propio de la replica en la secuencia a, a, b, b, a, b.
func localEsperado(i int) int64 {
	return [...]int64{1, 2, 1, 2, 3, 3}[i]
}

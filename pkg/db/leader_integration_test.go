//go:build integration

package db

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Prueba contra un Postgres real (TEST_DATABASE_URL). Lo que importa comprobar no cabe en
// un mock: que el segundo intento NO obtiene el cerrojo mientras el primero lo tiene, y
// que al soltarlo vuelve a estar disponible. Con el helper de sesion anterior la primera
// afirmacion se cumplia por casualidad en local (sin pgbouncer) y fallaba en produccion.
func TestTryLeaderLockExcluyeYLibera(t *testing.T) {
	url := integrationEnv(t, "TEST_DATABASE_URL")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	const key int64 = 987654321

	release1, ok := TryLeaderLock(ctx, pool, key)
	if !ok {
		t.Fatal("el primer intento debe obtener el cerrojo")
	}
	if _, ok := TryLeaderLock(ctx, pool, key); ok {
		t.Fatal("el segundo intento NO debe obtener el cerrojo mientras el primero lo tiene")
	}
	release1()

	release3, ok := TryLeaderLock(ctx, pool, key)
	if !ok {
		t.Fatal("tras soltarlo, el cerrojo debe volver a estar disponible")
	}
	release3()
}

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

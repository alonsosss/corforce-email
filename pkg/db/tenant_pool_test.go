package db

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// poolSinConexion crea un pool que no abre ninguna conexion: pgxpool conecta bajo demanda.
func poolSinConexion(t *testing.T) *pgxpool.Pool {
	t.Helper()
	p, err := pgxpool.New(context.Background(), "postgres://prueba@127.0.0.1:1/prueba")
	if err != nil {
		t.Fatalf("pool de prueba: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

func gestorDePrueba(abrir func(ctx context.Context, key, dsn string) (*pgxpool.Pool, error)) *TenantPoolManager {
	m := NewTenantPoolManager(func(db string) string { return "postgres://prueba@127.0.0.1:1/" + db }, zap.NewNop())
	m.abrir = abrir
	return m
}

func nombreUnico(t *testing.T, base string) string {
	return fmt.Sprintf("%s_%s_%d", base, t.Name(), time.Now().UnixNano())
}

// Una base inalcanzable no puede frenar a las demas empresas del servicio: antes GetPool
// retenia el cerrojo global mientras abria, y cada peticion de cualquier empresa esperaba.
func TestGetPoolUnaBaseLentaNoFrenaALasDemas(t *testing.T) {
	lenta, rapida := nombreUnico(t, "lenta"), nombreUnico(t, "rapida")
	entro, soltar := make(chan struct{}), make(chan struct{})
	pool := poolSinConexion(t)
	m := gestorDePrueba(func(ctx context.Context, key, dsn string) (*pgxpool.Pool, error) {
		if key == lenta {
			close(entro)
			<-soltar
		}
		return pool, nil
	})
	defer close(soltar)

	go func() { _, _ = m.GetPoolByDBName(context.Background(), lenta) }()
	<-entro

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	inicio := time.Now()
	if _, err := m.GetPoolByDBName(ctx, rapida); err != nil {
		t.Fatalf("la empresa rapida no obtuvo su pool mientras otra abria: %v", err)
	}
	if espera := time.Since(inicio); espera > 500*time.Millisecond {
		t.Fatalf("la empresa rapida espero %s detras de la lenta", espera)
	}
}

func TestGetPoolAbreUnaSolaVezPorDestino(t *testing.T) {
	base := nombreUnico(t, "compartida")
	var aperturas atomic.Int32
	soltar := make(chan struct{})
	pool := poolSinConexion(t)
	m := gestorDePrueba(func(ctx context.Context, key, dsn string) (*pgxpool.Pool, error) {
		aperturas.Add(1)
		<-soltar
		return pool, nil
	})

	const llamantes = 20
	var wg sync.WaitGroup
	obtenidos := make(chan *pgxpool.Pool, llamantes)
	for range llamantes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := m.GetPoolByDBName(context.Background(), base)
			if err != nil {
				t.Errorf("GetPool: %v", err)
				return
			}
			obtenidos <- p
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(soltar)
	wg.Wait()
	close(obtenidos)

	if n := aperturas.Load(); n != 1 {
		t.Fatalf("se abrio %d veces el mismo destino; se esperaba 1", n)
	}
	for p := range obtenidos {
		if p != pool {
			t.Fatal("un llamante recibio un pool distinto")
		}
	}
	if _, err := m.GetPoolByDBName(context.Background(), base); err != nil || aperturas.Load() != 1 {
		t.Fatalf("el pool abierto no quedo en cache (aperturas=%d, err=%v)", aperturas.Load(), err)
	}
}

// Cada llamante espera como mucho lo que permite su contexto, y su abandono no cancela la
// apertura que otros pueden estar esperando.
func TestGetPoolRespetaElContextoDeCadaLlamante(t *testing.T) {
	base := nombreUnico(t, "contexto")
	entro, soltar := make(chan struct{}), make(chan struct{})
	var ctxDeApertura context.Context
	pool := poolSinConexion(t)
	m := gestorDePrueba(func(ctx context.Context, key, dsn string) (*pgxpool.Pool, error) {
		ctxDeApertura = ctx
		close(entro)
		<-soltar
		return pool, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	go func() { <-entro }()
	inicio := time.Now()
	_, err := m.GetPoolByDBName(ctx, base)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("se esperaba el plazo del llamante, hubo: %v", err)
	}
	if espera := time.Since(inicio); espera > time.Second {
		t.Fatalf("el llamante espero %s pese a su plazo de 100ms", espera)
	}
	<-entro
	if ctxDeApertura.Err() != nil {
		t.Fatal("el abandono del llamante cancelo la apertura compartida")
	}
	close(soltar)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if p, ok := m.poolEnCache(m.keyDe(base)); ok && p == pool {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("la apertura no termino tras el abandono del llamante")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestGetPoolNoCacheaUnError(t *testing.T) {
	base := nombreUnico(t, "error")
	var intentos atomic.Int32
	pool := poolSinConexion(t)
	m := gestorDePrueba(func(ctx context.Context, key, dsn string) (*pgxpool.Pool, error) {
		if intentos.Add(1) == 1 {
			return nil, errors.New("base inalcanzable")
		}
		return pool, nil
	})
	if _, err := m.GetPoolByDBName(context.Background(), base); err == nil {
		t.Fatal("se esperaba el error de la primera apertura")
	}
	if p, err := m.GetPoolByDBName(context.Background(), base); err != nil || p != pool {
		t.Fatalf("tras recuperarse la base, GetPool siguio fallando: %v", err)
	}
}

func TestGetPoolTrasCloseAllNoGuardaUnPoolAbiertoDuranteElCierre(t *testing.T) {
	base := nombreUnico(t, "cierre")
	entro, soltar := make(chan struct{}), make(chan struct{})
	m := gestorDePrueba(func(ctx context.Context, key, dsn string) (*pgxpool.Pool, error) {
		close(entro)
		<-soltar
		return poolSinConexion(t), nil
	})
	errc := make(chan error, 1)
	go func() { _, err := m.GetPoolByDBName(context.Background(), base); errc <- err }()
	<-entro
	m.CloseAll()
	close(soltar)
	if err := <-errc; err == nil {
		t.Fatal("una apertura que cruzo un CloseAll devolvio un pool")
	}
	if _, ok := m.poolEnCache(m.keyDe(base)); ok {
		t.Fatal("una apertura que cruzo un CloseAll quedo en cache")
	}
}

func (m *TenantPoolManager) keyDe(dbName string) string { return DBTarget{DBName: dbName}.Key() }

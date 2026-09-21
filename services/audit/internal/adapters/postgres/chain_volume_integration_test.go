//go:build integration

// Verificacion de la cadena con volumen: memoria acotada, cancelable y sin frenar a los escritores.
// El volumen sale de AUDIT_VOLUME_ROWS (por defecto 6000, suficiente para pasar por varios lotes
// del verificador); para medir de verdad, por ejemplo con 100000:
//
//	AUDIT_VOLUME_ROWS=100000 go test -tags integration -run TestChainVolume -v ./services/audit/internal/adapters/postgres
package postgres

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/google/uuid"
)

func volumeRows(t *testing.T) int {
	t.Helper()
	raw := os.Getenv("AUDIT_VOLUME_ROWS")
	if raw == "" {
		return 6000
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		t.Fatalf("AUDIT_VOLUME_ROWS=%q no es un entero positivo", raw)
	}
	return n
}

func (e *env) seedVolume(t *testing.T, n int) {
	t.Helper()
	const batch = 500
	start := time.Now()
	for done := 0; done < n; done += batch {
		size := min(batch, n-done)
		logs := make([]*domain.AuditLog, size)
		for i := range logs {
			logs[i] = e.fullLog("volume." + strconv.Itoa(done+i))
			logs[i].CreatedAt = time.Now().UTC()
		}
		if err := e.logs.BulkCreate(e.ctx, logs); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("sembradas %d filas de audit_logs en %s (%.0f filas/s)", n, time.Since(start).Round(time.Millisecond), float64(n)/time.Since(start).Seconds())
	if _, err := e.admin.Exec(context.Background(), `ANALYZE audit.audit_logs`); err != nil {
		t.Fatal(err)
	}
}

// heapWatcher muestrea el heap mientras corre una operacion y devuelve su maximo.
func heapWatcher() (stop func() uint64) {
	var peak atomic.Uint64
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > peak.Load() {
				peak.Store(m.HeapAlloc)
			}
			select {
			case <-done:
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	return func() uint64 {
		close(done)
		wg.Wait()
		return peak.Load()
	}
}

func TestChainVolume_VerifyBoundedCancelableAndNonBlocking(t *testing.T) {
	e := setup(t)
	e.useKeys(testRing(t, testKeyA, ""))
	n := volumeRows(t)
	e.seedVolume(t, n)

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	stopWatch := heapWatcher()
	start := time.Now()
	res := e.verify(t)
	elapsed := time.Since(start)
	peak := stopWatch()
	if !res.OK || res.Checked != n {
		t.Fatalf("la cadena de %d filas debia estar intacta: %+v", n, res)
	}
	growth := int64(peak) - int64(before.HeapAlloc)
	t.Logf("verificacion de %d filas: %s (%.0f filas/s), pico de heap +%.1f MiB", n, elapsed.Round(time.Millisecond), float64(n)/elapsed.Seconds(), float64(growth)/(1<<20))
	if growth > 96<<20 {
		t.Fatalf("la verificacion crecio el heap %d MiB: la memoria debe ser independiente del tamano de la cadena", growth>>20)
	}

	t.Run("cancelable", func(t *testing.T) {
		ctx, cancel := context.WithCancel(e.ctx)
		go func() {
			time.Sleep(elapsed / 10)
			cancel()
		}()
		start := time.Now()
		_, err := e.logs.VerifyChain(ctx, e.tenant, domain.VerifyOptions{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("se esperaba context.Canceled, llego %v", err)
		}
		t.Logf("cancelada a %s del inicio (recorrido completo: %s)", time.Since(start).Round(time.Millisecond), elapsed.Round(time.Millisecond))
		if time.Since(start) > elapsed/2+2*time.Second {
			t.Fatalf("la cancelacion tardo %s con un recorrido completo de %s", time.Since(start), elapsed)
		}
	})

	t.Run("no bloquea las escrituras", func(t *testing.T) {
		base := writeLatencies(t, e, 30)
		var wg sync.WaitGroup
		wg.Add(1)
		var verified *domain.ChainIntegrity
		go func() {
			defer wg.Done()
			var err error
			if verified, err = e.logs.VerifyChain(e.ctx, e.tenant, domain.VerifyOptions{}); err != nil {
				t.Error(err)
			}
		}()
		during := writeLatencies(t, e, 30)
		wg.Wait()
		if verified == nil || !verified.OK {
			t.Fatalf("la cadena debia seguir intacta con escrituras concurrentes: %+v", verified)
		}
		t.Logf("escritura de una fila: sin verificacion p50=%s max=%s; con verificacion en curso p50=%s max=%s",
			pct(base, 50), pct(base, 100), pct(during, 50), pct(during, 100))
		if pct(during, 100) > 5*time.Second {
			t.Fatalf("una escritura tardo %s con una verificacion en curso", pct(during, 100))
		}
		e.assertIntact(t, n+60)
	})
}

func writeLatencies(t *testing.T, e *env, count int) []time.Duration {
	t.Helper()
	out := make([]time.Duration, 0, count)
	for i := 0; i < count; i++ {
		l := e.fullLog("volume.write." + uuid.NewString())
		start := time.Now()
		if err := e.logs.Create(e.ctx, l); err != nil {
			t.Fatal(err)
		}
		out = append(out, time.Since(start))
	}
	return out
}

func pct(d []time.Duration, p int) time.Duration {
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := (len(s)*p + 99) / 100
	if idx < 1 {
		idx = 1
	}
	return s[idx-1].Round(10 * time.Microsecond)
}

// El listado y el recuento de la API sobre una cadena grande: el plan tiene que usar el indice
// (tenant_id, created_at) y no recorrer la tabla.
func TestChainVolume_ListAndCountUseIndexes(t *testing.T) {
	e := setup(t)
	e.useKeys(testRing(t, testKeyA, ""))
	n := volumeRows(t)
	e.seedVolume(t, n)

	plan := func(sql string, args ...any) string {
		rows, err := e.admin.Query(context.Background(), "EXPLAIN (ANALYZE, BUFFERS, COSTS OFF) "+sql, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			out += line + "\n"
		}
		return out
	}
	list := plan(`SELECT id FROM audit.audit_logs WHERE tenant_id=$1 ORDER BY created_at DESC, id DESC LIMIT 50 OFFSET 0`, e.tenant)
	t.Logf("listado por empresa:\n%s", list)
	count := plan(`SELECT COUNT(*) FROM audit.audit_logs WHERE tenant_id=$1`, e.tenant)
	t.Logf("recuento por empresa:\n%s", count)
	chain := plan(`SELECT id FROM audit.audit_logs WHERE seq > $1 ORDER BY seq ASC LIMIT $2`, int64(n/2), verifyBatchSize)
	t.Logf("lote del verificador:\n%s", chain)
	head := plan(`SELECT seq, entry_hash FROM audit.audit_logs WHERE seq IS NOT NULL AND entry_hash IS NOT NULL ORDER BY seq DESC LIMIT 1`)
	t.Logf("cabeza de la cadena:\n%s", head)
	for name, p := range map[string]string{"lote del verificador": chain, "cabeza": head} {
		if !strings.Contains(p, "Index") {
			t.Fatalf("%s no usa un indice:\n%s", name, p)
		}
	}
}

//go:build integration

package postgres

// Rutas calientes de mail-migration con volumen: una empresa con muchos trabajos terminados (lo que deja
// un historial de migraciones) y unos pocos activos. Mide el reclamo del ejecutor, el listado paginado, el
// conteo de activos y la expiracion de trabajos perdidos, y deja los planes. El volumen sale de
// MAIL_MIGRATION_VOLUME_JOBS (por defecto 5000); para medir de verdad:
//
//	MAIL_MIGRATION_VOLUME_JOBS=100000 go test -tags integration -run TestVolumen -v ./services/mail-migration/internal/adapters/postgres

import (
	"context"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/ports"
	"github.com/google/uuid"
)

func TestVolumen_HistorialDeTrabajos(t *testing.T) {
	ctx, repo, pool := setup(t)
	n := 5000
	if raw := os.Getenv("MAIL_MIGRATION_VOLUME_JOBS"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 100 {
			t.Fatalf("MAIL_MIGRATION_VOLUME_JOBS=%q no es un entero valido (minimo 100)", raw)
		}
		n = v
	}
	tenant := uuid.New()
	other := uuid.New()
	admin := context.Background()
	for _, tn := range []uuid.UUID{tenant, other} {
		if _, err := pool.Exec(admin, `DELETE FROM mail_migration.jobs WHERE tenant_id = $1`, tn); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	// Terminados: la credencial ya no existe (jobs_credential_check) y finished_at es obligatorio.
	if _, err := pool.Exec(admin, `
INSERT INTO mail_migration.jobs (tenant_id, mailbox_id, mailbox_username, source_host, source_port, source_tls, source_username,
    status, requested_by, finished_at, created_at, updated_at)
SELECT $1, gen_random_uuid(), 'buzon' || g || '@acme.test', 'imap.origen.example', 993, 'ssl', 'u' || g,
       (ARRAY['succeeded','failed','cancelled'])[1 + g % 3], gen_random_uuid(),
       now() - (g || ' minutes')::interval + interval '5 minutes', now() - (g || ' minutes')::interval, now() - (g || ' minutes')::interval
  FROM generate_series(1, $2) g`, tenant, n); err != nil {
		t.Fatal(err)
	}
	// Otra empresa en la misma base de prueba: no debe estorbar.
	if _, err := pool.Exec(admin, `
INSERT INTO mail_migration.jobs (tenant_id, mailbox_id, mailbox_username, source_host, source_port, source_tls, source_username,
    status, requested_by, finished_at)
SELECT $1, gen_random_uuid(), 'x' || g, 'imap.origen.example', 993, 'ssl', 'u', 'succeeded', gen_random_uuid(), now()
  FROM generate_series(1, $2) g`, other, n); err != nil {
		t.Fatal(err)
	}
	// Activos: 20 pendientes y 5 en curso con el lease vencido.
	if _, err := pool.Exec(admin, `
INSERT INTO mail_migration.jobs (tenant_id, mailbox_id, mailbox_username, source_host, source_port, source_tls, source_username,
    source_password_enc, status, requested_by, created_at)
SELECT $1, gen_random_uuid(), 'pend' || g, 'imap.origen.example', 993, 'ssl', 'u', '\x00'::bytea, 'pending', gen_random_uuid(), now() + (g || ' seconds')::interval
  FROM generate_series(1, 20) g`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(admin, `
INSERT INTO mail_migration.jobs (tenant_id, mailbox_id, mailbox_username, source_host, source_port, source_tls, source_username,
    source_password_enc, status, requested_by, lease_id, lease_expires_at, heartbeat_at, started_at, attempt)
SELECT $1, gen_random_uuid(), 'run' || g, 'imap.origen.example', 993, 'ssl', 'u', '\x00'::bytea, 'running', gen_random_uuid(),
       gen_random_uuid(), now() - interval '1 hour', now() - interval '1 hour', now() - interval '2 hours', 1
  FROM generate_series(1, 5) g`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(admin, `ANALYZE mail_migration.jobs`); err != nil {
		t.Fatal(err)
	}
	t.Logf("sembrados %d trabajos terminados de la empresa, %d de otra y 25 activos en %s", n, n, time.Since(start).Round(time.Millisecond))

	timed := func(label string, fn func()) {
		s := time.Now()
		fn()
		t.Logf("%-62s %s", label, time.Since(s).Round(10*time.Microsecond))
	}
	timed("listado sin filtro, pagina 1 de 50 (mas el recuento)", func() {
		jobs, total, err := repo.List(ctx, tenant, ports.ListFilter{}, ports.Page{Limit: 50, Offset: 0})
		if err != nil || len(jobs) != 50 || total != int64(n+25) {
			t.Fatalf("listado: %v %d %d", err, len(jobs), total)
		}
	})
	timed("listado sin filtro, pagina profunda (offset n/2)", func() {
		if _, _, err := repo.List(ctx, tenant, ports.ListFilter{}, ports.Page{Limit: 50, Offset: n / 2}); err != nil {
			t.Fatal(err)
		}
	})
	failed := domain.StatusFailed
	timed("listado por estado failed", func() {
		if _, _, err := repo.List(ctx, tenant, ports.ListFilter{Status: &failed}, ports.Page{Limit: 50}); err != nil {
			t.Fatal(err)
		}
	})
	timed("conteo de activos (limite por empresa)", func() {
		if c, err := repo.CountActive(ctx, tenant); err != nil || c != 25 {
			t.Fatalf("activos: %v %d", err, c)
		}
	})
	timed("ExpireLost con 5 en curso vencidos (attempt < max: se reclaman)", func() {
		if lost, err := repo.ExpireLost(ctx, tenant, time.Now(), 3); err != nil || len(lost) != 0 {
			t.Fatalf("expirar: %v %d", err, len(lost))
		}
	})
	var claimed *domain.Job
	timed("Claim (el mas antiguo de 20 pendientes y 5 vencidos entre miles)", func() {
		p := ports.ClaimParams{RunnerID: "r1", LeaseID: uuid.New(), Now: time.Now(), LeaseUntil: time.Now().Add(time.Minute), MaxAttempts: 3}
		j, err := repo.Claim(ctx, tenant, p)
		if err != nil || j == nil {
			t.Fatalf("reclamo: %v %v", err, j)
		}
		claimed = j
	})
	_ = claimed

	// Reclamo concurrente: 8 ejecutores a la vez sobre los 24 restantes; nadie recibe dos veces el mismo.
	var mu sync.Mutex
	seen := map[uuid.UUID]int{}
	var wg sync.WaitGroup
	start = time.Now()
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for {
				p := ports.ClaimParams{RunnerID: "runner-" + strconv.Itoa(w), LeaseID: uuid.New(), Now: time.Now(), LeaseUntil: time.Now().Add(time.Minute), MaxAttempts: 3}
				j, err := repo.Claim(ctx, tenant, p)
				if err != nil {
					t.Error(err)
					return
				}
				if j == nil {
					return
				}
				mu.Lock()
				seen[j.ID]++
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	t.Logf("reclamo concurrente de 8 ejecutores: %d trabajos en %s", len(seen), time.Since(start).Round(time.Millisecond))
	for id, c := range seen {
		if c != 1 {
			t.Fatalf("el trabajo %s se entrego %d veces", id, c)
		}
	}

	explain := func(label, sql string, args ...any) {
		rows, err := pool.Query(admin, "EXPLAIN (ANALYZE, BUFFERS, COSTS OFF) "+sql, args...)
		if err != nil {
			t.Fatal(err)
		}
		plan := ""
		for rows.Next() {
			var l string
			_ = rows.Scan(&l)
			plan += "    " + l + "\n"
		}
		rows.Close()
		t.Logf("plan de %s:\n%s", label, plan)
	}
	explain("listado sin filtro", `SELECT id FROM mail_migration.jobs WHERE tenant_id = $1 AND ($2::uuid IS NULL OR mailbox_id = $2) AND ($3::text IS NULL OR status = $3) ORDER BY created_at DESC, id LIMIT 50 OFFSET 0`, tenant, nil, nil)
	explain("recuento del listado", `SELECT count(*) FROM mail_migration.jobs WHERE tenant_id = $1 AND ($2::uuid IS NULL OR mailbox_id = $2) AND ($3::text IS NULL OR status = $3)`, tenant, nil, nil)
	explain("reclamo", `SELECT id FROM mail_migration.jobs WHERE tenant_id = $1 AND cancel_requested_at IS NULL AND (status = 'pending' OR (status = 'running' AND lease_expires_at < now() AND attempt < 3)) ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1`, tenant)
}

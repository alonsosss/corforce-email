//go:build integration

package postgres

// Rutas calientes de CalDAV y CardDAV con volumen, con el rol de servicio sujeto a las politicas de fila: el
// listado inicial y el sync-collection de un calendario lleno, el calendar-query con ventana de tiempo y el
// sync incremental con el registro de cambios lleno. El volumen sale de MAIL_DAV_VOLUME_EVENTS (por defecto
// 3000, para que la suite corra rapido); para medir de verdad, por ejemplo con el tope por buzon de 20000 y
// 100 buzones mas de relleno:
//
//	MAIL_DAV_VOLUME_EVENTS=20000 MAIL_DAV_VOLUME_OTHER_MAILBOXES=100 go test -tags integration -run TestVolumen -v ./services/mail-dav/internal/adapters/postgres

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
)

func volumeEnvInt(t *testing.T, name string, def int) int {
	t.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		t.Fatalf("%s=%q no es un entero valido", name, raw)
	}
	return n
}

func peakHeap(fn func()) (peakBytes uint64) {
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
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	fn()
	close(done)
	wg.Wait()
	return peak.Load()
}

func timed(t *testing.T, label string, fn func()) time.Duration {
	t.Helper()
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	peak := peakHeap(fn)
	elapsed := time.Since(start)
	t.Logf("%-58s %10s  pico de heap +%6.1f MiB", label, elapsed.Round(100*time.Microsecond), float64(int64(peak)-int64(before.HeapAlloc))/(1<<20))
	return elapsed
}

// seedEvents inserta n eventos de un calendario por SQL directo (con el dueno de las tablas) y registra
// sus cambios. Cada iCalendar es un VEVENT valido de unos 350 bytes.
func (e *env) seedEvents(t *testing.T, p domain.Principal, calendarID uuid.UUID, n int) {
	t.Helper()
	_, err := e.owner.Exec(context.Background(), `
INSERT INTO mail_dav.events (tenant_id, mailbox_id, calendar_id, resource_name, uid, ical, etag, summary, first_start, last_end)
SELECT $1, $2, $3, 'e' || g || '.ics', 'uid-' || g,
       'BEGIN:VCALENDAR' || E'\r\n' || 'VERSION:2.0' || E'\r\n' || 'BEGIN:VEVENT' || E'\r\n' || 'UID:uid-' || g || E'\r\n' ||
       'DTSTAMP:20260101T000000Z' || E'\r\n' || 'DTSTART:20260101T000000Z' || E'\r\n' || 'SUMMARY:Evento ' || g || E'\r\n' ||
       'DESCRIPTION:' || repeat('x', 150) || E'\r\n' || 'END:VEVENT' || E'\r\n' || 'END:VCALENDAR' || E'\r\n',
       md5(g::text) || md5(g::text), 'Evento ' || g,
       timestamptz '2026-01-01' + (g || ' hours')::interval, timestamptz '2026-01-01' + (g || ' hours')::interval + interval '1 hour'
  FROM generate_series(1, $4) g`, p.TenantID, p.MailboxID, calendarID, n)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.owner.Exec(context.Background(), `
INSERT INTO mail_dav.calendar_changes (calendar_id, seq, tenant_id, mailbox_id, resource_name, deleted)
SELECT $1, g, $2, $3, 'e' || g || '.ics', false FROM generate_series(1, $4) g`, calendarID, p.TenantID, p.MailboxID, n); err != nil {
		t.Fatal(err)
	}
	if _, err := e.owner.Exec(context.Background(),
		`UPDATE mail_dav.calendars SET sync_seq = $2, changes_floor = 0 WHERE id = $1`, calendarID, n); err != nil {
		t.Fatal(err)
	}
}

func TestVolumen_CalDAVRutasCalientes(t *testing.T) {
	e := setup(t)
	n := volumeEnvInt(t, "MAIL_DAV_VOLUME_EVENTS", 3000)
	others := volumeEnvInt(t, "MAIL_DAV_VOLUME_OTHER_MAILBOXES", 20)
	ana := newPrincipal("ana")
	e.cleanup(t, ana)
	cal := e.calendar(t, ana, "calendar")

	start := time.Now()
	e.seedEvents(t, ana, cal.ID, n)
	// Buzones de relleno de la misma empresa: el indice y la politica de fila tienen que ignorarlos.
	filler := 0
	for i := 0; i < others; i++ {
		q := domain.Principal{TenantID: ana.TenantID, MailboxID: uuid.New(), Username: fmt.Sprintf("otro%d@it.test", i)}
		c := e.calendar(t, q, "calendar")
		e.seedEvents(t, q, c.ID, 1000)
		filler += 1000
		t.Cleanup(func() { e.wipe(t, q) })
	}
	if _, err := e.owner.Exec(context.Background(), `ANALYZE mail_dav.events; ANALYZE mail_dav.calendar_changes`); err != nil {
		t.Fatal(err)
	}
	t.Logf("sembrados %d eventos del buzon y %d de relleno (%d filas en total) en %s", n, filler, n+filler, time.Since(start).Round(time.Millisecond))

	timed(t, fmt.Sprintf("listado inicial / sync-collection inicial (%d eventos)", n), func() {
		_, events, err := e.repo.ListEvents(e.ctx, ana, "calendar", domain.EventWindow{})
		if err != nil || len(events) != n {
			t.Fatalf("listado: %v %d", err, len(events))
		}
	})
	week := domain.EventWindow{Start: at("20260301T000000Z"), End: at("20260308T000000Z")}
	timed(t, "calendar-query con ventana de una semana (indice de rango)", func() {
		_, events, err := e.repo.ListEvents(e.ctx, ana, "calendar", week)
		if err != nil || len(events) == 0 || len(events) > 200 {
			t.Fatalf("ventana: %v %d", err, len(events))
		}
	})
	timed(t, "sync-collection incremental: 100 cambios pendientes", func() {
		_, changed, removed, err := e.repo.EventChangesSince(e.ctx, ana, "calendar", int64(n-100))
		if err != nil || len(changed) != 100 || len(removed) != 0 {
			t.Fatalf("incremental: %v %d %d", err, len(changed), len(removed))
		}
	})
	timed(t, fmt.Sprintf("sync-collection incremental: %d cambios pendientes (todo el registro)", n), func() {
		_, changed, _, err := e.repo.EventChangesSince(e.ctx, ana, "calendar", 0)
		if err != nil || len(changed) != n {
			t.Fatalf("registro completo: %v %d", err, len(changed))
		}
	})
	timed(t, "GetEvent por nombre de recurso", func() {
		if _, err := e.repo.GetEvent(e.ctx, ana, "calendar", fmt.Sprintf("e%d.ics", n/2)); err != nil {
			t.Fatal(err)
		}
	})

	for name, sql := range map[string]string{
		"listado":     `SELECT id FROM mail_dav.events WHERE tenant_id = $1 AND mailbox_id = $2 AND calendar_id = $3 ORDER BY resource_name`,
		"ventana":     `SELECT id FROM mail_dav.events WHERE tenant_id = $1 AND mailbox_id = $2 AND calendar_id = $3 AND first_start < '2026-03-08' AND (last_end IS NULL OR last_end >= '2026-03-01') ORDER BY resource_name`,
		"cambios":     `SELECT DISTINCT ON (resource_name) resource_name, deleted FROM mail_dav.calendar_changes WHERE calendar_id = $3 AND tenant_id = $1 AND mailbox_id = $2 AND seq > 0 AND seq <= 999999 ORDER BY resource_name, seq DESC`,
		"limite (ct)": `SELECT count(*) FROM mail_dav.events WHERE tenant_id = $1 AND mailbox_id = $2 AND $3::uuid IS NOT NULL`,
	} {
		rows, err := e.owner.Query(context.Background(), "EXPLAIN (ANALYZE, BUFFERS, COSTS OFF) "+sql, ana.TenantID, ana.MailboxID, cal.ID)
		if err != nil {
			t.Fatal(err)
		}
		plan := ""
		for rows.Next() {
			var line string
			_ = rows.Scan(&line)
			plan += "    " + line + "\n"
		}
		rows.Close()
		t.Logf("plan de %s:\n%s", name, plan)
	}
}

func (e *env) wipe(t *testing.T, p domain.Principal) {
	t.Helper()
	for _, q := range []string{
		`DELETE FROM mail_dav.calendars WHERE tenant_id = $1 AND mailbox_id = $2`,
		`DELETE FROM mail_dav.addressbooks WHERE tenant_id = $1 AND mailbox_id = $2`,
	} {
		if _, err := e.owner.Exec(context.Background(), q, p.TenantID, p.MailboxID); err != nil {
			t.Errorf("limpiar: %v", err)
		}
	}
}

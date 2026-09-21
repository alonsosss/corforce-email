//go:build integration

package postgres

// Endurecimiento contra Postgres real: el espacio por buzon, las lecturas acotadas, el orden de los cerrojos
// entre una escritura y la baja de los datos de un buzon, y el costo de las consultas reales con un volumen
// de decenas de miles de contactos y de eventos en un buzon (con el plan de ejecucion a la vista).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
)

func storage(maxItems int, maxBytes int64) domain.WriteLimits {
	return domain.WriteLimits{MaxItems: maxItems, MaxBytes: maxBytes, MaxChanges: 1000}
}

func sizedContact(t *testing.T, res, uid string, size int) domain.Contact {
	t.Helper()
	raw := fmt.Sprintf("BEGIN:VCARD\r\nVERSION:3.0\r\nUID:%s\r\nFN:%s\r\nNOTE:%s\r\nEND:VCARD\r\n", uid, uid, strings.Repeat("x", size))
	c, err := domain.NewContact(res, raw, domain.Limits{MaxVCardBytes: 1 << 20, MaxVCardProperties: 100})
	if err != nil {
		t.Fatal(err)
	}
	c.ID = uuid.New()
	return c
}

func TestElEspacioDelBuzonSeCuentaEnLaBase(t *testing.T) {
	e := setup(t)
	ana, bea := newPrincipal("ana"), newPrincipal("bea")
	e.cleanup(t, ana, bea)
	e.book(t, ana, "contacts")
	e.book(t, bea, "contacts")
	put := func(p domain.Principal, c domain.Contact) error {
		c.TenantID, c.MailboxID = p.TenantID, p.MailboxID
		_, err := e.repo.PutContact(e.ctx, p, "contacts", c, domain.Precondition{}, storage(100, 5000))
		return err
	}
	if err := put(ana, sizedContact(t, "a.vcf", "a", 2000)); err != nil {
		t.Fatal(err)
	}
	if err := put(ana, sizedContact(t, "b.vcf", "b", 2000)); err != nil {
		t.Fatal(err)
	}
	if err := put(ana, sizedContact(t, "c.vcf", "c", 2000)); !errors.Is(err, domain.ErrStorageLimit) {
		t.Fatalf("pasar el espacio del buzon: %v", err)
	}
	if err := put(ana, sizedContact(t, "a.vcf", "a", 4000)); !errors.Is(err, domain.ErrStorageLimit) {
		t.Fatalf("crecer un contacto pasando el espacio: %v", err)
	}
	if err := put(ana, sizedContact(t, "a.vcf", "a", 2500)); err != nil {
		t.Fatalf("crecer sin pasar el espacio: %v", err)
	}
	if err := put(bea, sizedContact(t, "a.vcf", "a", 4500)); err != nil {
		t.Fatalf("el espacio es de cada buzon: %v", err)
	}
	// Un buzon que ya estaba por encima (se bajo el tope, o lo dejo una version anterior) puede reducir lo suyo.
	if _, err := e.owner.Exec(context.Background(), `UPDATE mail_dav.contacts SET vcard = vcard || repeat('y', 6000) WHERE mailbox_id = $1 AND resource_name = 'b.vcf'`, ana.MailboxID); err != nil {
		t.Fatal(err)
	}
	if err := put(ana, sizedContact(t, "b.vcf", "b", 3000)); err != nil {
		t.Fatalf("reducir un contacto estando por encima: %v", err)
	}
	if err := put(ana, sizedContact(t, "b.vcf", "b", 3500)); !errors.Is(err, domain.ErrStorageLimit) {
		t.Fatalf("crecer estando por encima: %v", err)
	}

	e.calendar(t, ana, "calendar")
	big := evt(t, "g.ics", "g", strings.Repeat("s", 250), "20260101T100000Z", "DESCRIPTION:"+strings.Repeat("d", 3000))
	ev := big
	ev.TenantID, ev.MailboxID = ana.TenantID, ana.MailboxID
	if _, err := e.repo.PutEvent(e.ctx, ana, "calendar", ev, domain.Precondition{}, storage(100, int64(len(big.ICal))+100)); err != nil {
		t.Fatal(err)
	}
	other := evt(t, "h.ics", "h", "otro", "20260102T100000Z", "DESCRIPTION:"+strings.Repeat("d", 3000))
	other.TenantID, other.MailboxID = ana.TenantID, ana.MailboxID
	if _, err := e.repo.PutEvent(e.ctx, ana, "calendar", other, domain.Precondition{}, storage(100, int64(len(big.ICal))+100)); !errors.Is(err, domain.ErrStorageLimit) {
		t.Fatalf("el espacio de eventos es aparte del de contactos y tambien se acota: %v", err)
	}
}

func TestUnaLecturaSinDatosNoTraeElObjetoYConDatosSeAcota(t *testing.T) {
	e := setup(t)
	ana := newPrincipal("ana")
	e.cleanup(t, ana)
	e.book(t, ana, "contacts")
	for i := 0; i < 3; i++ {
		c := sizedContact(t, fmt.Sprintf("c%d.vcf", i), fmt.Sprintf("c%d", i), 40000)
		c.TenantID, c.MailboxID = ana.TenantID, ana.MailboxID
		if _, err := e.repo.PutContact(e.ctx, ana, "contacts", c, domain.Precondition{}, storage(100, 1<<30)); err != nil {
			t.Fatal(err)
		}
	}
	_, light, err := e.repo.ListContacts(e.ctx, ana, "contacts", domain.ReadOptions{})
	if err != nil || len(light) != 3 {
		t.Fatalf("sin datos: %v %d", err, len(light))
	}
	for _, c := range light {
		if c.VCard != "" || c.Size < 40000 || c.ETag == "" || c.DisplayName == "" {
			t.Fatalf("una lectura sin datos trae solo metadatos y tamano: vcard %d bytes, size %d", len(c.VCard), c.Size)
		}
	}
	_, full, err := e.repo.ListContacts(e.ctx, ana, "contacts", domain.ReadOptions{WithData: true, MaxBytes: 1 << 20})
	if err != nil || len(full) != 3 || len(full[0].VCard) != full[0].Size {
		t.Fatalf("con datos: %v %d", err, len(full))
	}
	if _, _, err := e.repo.ListContacts(e.ctx, ana, "contacts", domain.ReadOptions{WithData: true, MaxBytes: 100000}); !errors.Is(err, domain.ErrResultTooLarge) {
		t.Fatalf("pasado el tope de bytes: %v", err)
	}
	if _, err := e.repo.GetContacts(e.ctx, ana, "contacts", []string{"c0.vcf", "c1.vcf", "c2.vcf"}, domain.ReadOptions{WithData: true, MaxBytes: 100000}); !errors.Is(err, domain.ErrResultTooLarge) {
		t.Fatalf("multiget pasado el tope de bytes: %v", err)
	}
	if got, err := e.repo.GetContacts(e.ctx, ana, "contacts", []string{"c0.vcf", "c1.vcf", "c2.vcf"}, domain.ReadOptions{MaxBytes: 100000}); err != nil || len(got) != 3 {
		t.Fatalf("sin datos no hay tope que pasar: %v %d", err, len(got))
	}

	seen := 0
	if _, err := e.repo.EachContact(e.ctx, ana, "contacts", func(c domain.Contact) (bool, error) {
		seen++
		return seen < 2, nil
	}); err != nil || seen != 2 {
		t.Fatalf("EachContact debe parar cuando se le pide: %v %d", err, seen)
	}
	boom := errors.New("fallo del que recorre")
	if _, err := e.repo.EachContact(e.ctx, ana, "contacts", func(domain.Contact) (bool, error) { return true, boom }); !errors.Is(err, boom) {
		t.Fatalf("el error de quien recorre se devuelve: %v", err)
	}
}

// Una escritura y la baja de los datos del buzon toman los cerrojos en el mismo orden: si una escritura
// tomara el de la coleccion y luego el del buzon, mientras la baja toma el del buzon y luego borra la
// coleccion, se esperarian una a otra y Postgres abortaria una con un fallo de interbloqueo.
func TestUnaEscrituraYLaBajaDelBuzonNoSeInterbloquean(t *testing.T) {
	e := setup(t)
	for round := 0; round < 25; round++ {
		ana := newPrincipal("ana")
		e.cleanup(t, ana)
		e.book(t, ana, "contacts")
		var wg sync.WaitGroup
		errs := make(chan error, 64)
		for w := 0; w < 4; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := 0; i < 6; i++ {
					c := sizedContact(t, fmt.Sprintf("w%dc%d.vcf", w, i), fmt.Sprintf("w%dc%d", w, i), 50)
					c.TenantID, c.MailboxID = ana.TenantID, ana.MailboxID
					if _, err := e.repo.PutContact(e.ctx, ana, "contacts", c, domain.Precondition{}, storage(1000, 1<<30)); err != nil && !errors.Is(err, domain.ErrNotFound) {
						errs <- err
					}
				}
			}(w)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(time.Millisecond)
			if _, err := e.repo.DeleteMailboxData(e.ctx, ana); err != nil {
				errs <- err
			}
		}()
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("ronda %d: %v", round, err)
		}
	}
}

// El volumen de un buzon con cincuenta mil contactos y cincuenta mil eventos, con otros cien mil objetos de
// otros buzones alrededor: las consultas reales usan sus indices y responden en milisegundos, y el plan queda
// en el registro de la prueba.
func TestLasConsultasRealesConUnVolumenAlto(t *testing.T) {
	e := setup(t)
	ana, otra := newPrincipal("ana"), newPrincipal("otra")
	e.cleanup(t, ana, otra)
	book, cal := e.book(t, ana, "contacts"), e.calendar(t, ana, "calendar")
	otherBook, otherCal := e.book(t, otra, "contacts"), e.calendar(t, otra, "calendar")
	const n = 50000
	seed := func(p domain.Principal, book domain.Addressbook, cal domain.Calendar) {
		if _, err := e.owner.Exec(context.Background(), `
			INSERT INTO mail_dav.contacts (id, tenant_id, mailbox_id, addressbook_id, resource_name, uid, vcard, etag, display_name, emails)
			SELECT gen_random_uuid(), $1, $2, $3, 'c' || g || '.vcf', 'uid' || g,
			       'BEGIN:VCARD' || E'\r\n' || 'VERSION:3.0' || E'\r\n' || 'UID:uid' || g || E'\r\n' || 'FN:Contacto ' || g || E'\r\n' || 'END:VCARD' || E'\r\n',
			       md5(g::text) || md5(g::text), 'Contacto ' || g, ARRAY['c' || g || '@it.test']
			  FROM generate_series(1, $4) g`, p.TenantID, p.MailboxID, book.ID, n); err != nil {
			t.Fatal(err)
		}
		if _, err := e.owner.Exec(context.Background(), `
			INSERT INTO mail_dav.events (id, tenant_id, mailbox_id, calendar_id, resource_name, uid, ical, etag, summary, first_start, last_end)
			SELECT gen_random_uuid(), $1, $2, $3, 'e' || g || '.ics', 'uid' || g,
			       'BEGIN:VCALENDAR' || E'\r\n' || 'VERSION:2.0' || E'\r\n' || 'BEGIN:VEVENT' || E'\r\n' || 'UID:uid' || g || E'\r\n' || 'END:VEVENT' || E'\r\n' || 'END:VCALENDAR' || E'\r\n',
			       md5(g::text) || md5(g::text), 'Evento ' || g,
			       timestamptz '2024-01-01' + (g || ' hours')::interval,
			       CASE WHEN g % 100 = 0 THEN NULL ELSE timestamptz '2024-01-01' + (g || ' hours')::interval + interval '1 hour' END
			  FROM generate_series(1, $4) g`, p.TenantID, p.MailboxID, cal.ID, n); err != nil {
			t.Fatal(err)
		}
	}
	seed(ana, book, cal)
	seed(otra, otherBook, otherCal)
	if _, err := e.owner.Exec(context.Background(), `ANALYZE mail_dav.contacts; ANALYZE mail_dav.events; ANALYZE mail_dav.collection_changes; ANALYZE mail_dav.calendar_changes`); err != nil {
		t.Fatal(err)
	}

	timed := func(name string, budget time.Duration, f func() error) {
		t.Helper()
		start := time.Now()
		if err := f(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		d := time.Since(start)
		t.Logf("%-46s %v", name, d.Round(time.Millisecond))
		if d > budget {
			t.Errorf("%s tardo %v, presupuesto %v", name, d, budget)
		}
	}
	timed("PROPFIND profundidad 1 sin datos (50000)", 3*time.Second, func() error {
		_, got, err := e.repo.ListContacts(e.ctx, ana, "contacts", domain.ReadOptions{})
		if err == nil && len(got) != n {
			err = fmt.Errorf("%d contactos", len(got))
		}
		return err
	})
	timed("PROPFIND profundidad 1 sin datos, eventos", 3*time.Second, func() error {
		_, got, err := e.repo.ListEvents(e.ctx, ana, "calendar", domain.ReadOptions{})
		if err == nil && len(got) != n {
			err = fmt.Errorf("%d eventos", len(got))
		}
		return err
	})
	timed("multiget de 50 contactos con datos", 500*time.Millisecond, func() error {
		names := make([]string, 50)
		for i := range names {
			names[i] = fmt.Sprintf("c%d.vcf", 1000+i)
		}
		got, err := e.repo.GetContacts(e.ctx, ana, "contacts", names, withData)
		if err == nil && len(got) != 50 {
			err = fmt.Errorf("%d contactos", len(got))
		}
		return err
	})
	timed("calendar-query por un dia (indice de tiempo)", 500*time.Millisecond, func() error {
		_, got, err := e.windowEvents(e.ctx, ana, "calendar", domain.EventWindow{Start: at("20240301T000000Z"), End: at("20240302T000000Z")})
		if err == nil && (len(got) < 20 || len(got) > 700) {
			err = fmt.Errorf("%d candidatos", len(got))
		}
		return err
	})
	timed("alta de un contacto (cuota de espacio y conteo)", 500*time.Millisecond, func() error {
		c := sizedContact(t, "nuevo.vcf", "nuevo", 100)
		c.TenantID, c.MailboxID = ana.TenantID, ana.MailboxID
		_, err := e.repo.PutContact(e.ctx, ana, "contacts", c, domain.Precondition{}, storage(n+10, 1<<30))
		return err
	})
	timed("alta de un evento (cuota de espacio y conteo)", 500*time.Millisecond, func() error {
		ev := evt(t, "nuevo.ics", "nuevo", "Nuevo", "20260101T100000Z")
		ev.TenantID, ev.MailboxID = ana.TenantID, ana.MailboxID
		_, err := e.repo.PutEvent(e.ctx, ana, "calendar", ev, domain.Precondition{}, storage(n+10, 1<<30))
		return err
	})
	timed("sync-collection con 1000 cambios", 1*time.Second, func() error {
		cur, err := e.repo.GetAddressbook(e.ctx, ana, "contacts")
		if err != nil {
			return err
		}
		if _, err := e.owner.Exec(context.Background(), `
			INSERT INTO mail_dav.collection_changes (addressbook_id, seq, tenant_id, mailbox_id, resource_name, deleted)
			SELECT $1, g, $2, $3, 'c' || g || '.vcf', false FROM generate_series(1, 1000) g
			ON CONFLICT DO NOTHING`, cur.ID, ana.TenantID, ana.MailboxID); err != nil {
			return err
		}
		if _, err := e.owner.Exec(context.Background(), `UPDATE mail_dav.addressbooks SET sync_seq = 1000 WHERE id = $1 AND sync_seq < 1000`, cur.ID); err != nil {
			return err
		}
		_, changed, _, err := e.repo.ChangesSince(e.ctx, ana, "contacts", 0, domain.ReadOptions{})
		if err == nil && len(changed) != 1000 {
			err = fmt.Errorf("%d cambios", len(changed))
		}
		return err
	})

	for name, q := range map[string]string{
		"contactos de una libreta en orden": `SELECT id FROM mail_dav.contacts WHERE tenant_id = $1 AND mailbox_id = $2 AND addressbook_id = $3 ORDER BY resource_name`,
		"eventos por ventana":               `SELECT id FROM mail_dav.events WHERE tenant_id = $1 AND mailbox_id = $2 AND calendar_id = $3 AND first_start < '2024-03-02' AND (last_end IS NULL OR last_end >= '2024-03-01') ORDER BY resource_name`,
		"espacio de contactos":              `SELECT COALESCE(sum(octet_length(vcard)), 0) FROM mail_dav.contacts WHERE tenant_id = $1 AND mailbox_id = $2 AND $3::uuid IS NOT NULL`,
	} {
		rows, err := e.owner.Query(context.Background(), `EXPLAIN (ANALYZE, BUFFERS, COSTS OFF) `+q, ana.TenantID, ana.MailboxID, book.ID)
		if name == "eventos por ventana" {
			rows.Close()
			rows, err = e.owner.Query(context.Background(), `EXPLAIN (ANALYZE, BUFFERS, COSTS OFF) `+q, ana.TenantID, ana.MailboxID, cal.ID)
		}
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, line)
		}
		rows.Close()
		t.Logf("plan de %s:\n  %s", name, strings.Join(plan, "\n  "))
		if name == "eventos por ventana" && !strings.Contains(strings.Join(plan, "\n"), "idx_mail_dav_events_range") {
			t.Errorf("la consulta por ventana no usa el indice de tiempo:\n%s", strings.Join(plan, "\n"))
		}
	}
}

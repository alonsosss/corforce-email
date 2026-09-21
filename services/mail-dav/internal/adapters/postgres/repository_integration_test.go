//go:build integration

package postgres

// Prueba de integracion contra un Postgres real. Se ejecuta con:
//
//	MAIL_DAV_TEST_DSN=postgres://user:pass@localhost:5432/db?sslmode=disable \
//	  go test -tags integration ./services/mail-dav/internal/adapters/postgres/
//
// Aplica las migraciones canonicas del servicio dos veces (idempotencia) y ejercita el repositorio con la
// credencial que tendra en produccion: un rol de login miembro de mail_dav_service, que no es dueno de las
// tablas y por eso queda sujeto a las politicas de fila. Sobre eso prueba el aislamiento entre buzones y entre
// empresas, con una mutacion que demuestra que la prueba detectaria una politica ausente, las restricciones
// de la base, los limites y la concurrencia.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
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

const (
	maxChanges  = 3
	maxContacts = 5
	maxBooks    = 3
)

type env struct {
	ctx     context.Context
	repo    *Repository
	owner   *pgxpool.Pool
	service *pgxpool.Pool
}

// setup migra la base y abre un segundo pool como el rol de servicio, con un login propio de esta prueba.
func setup(t *testing.T) *env {
	t.Helper()
	dsn := integrationEnv(t, "MAIL_DAV_TEST_DSN")
	ctx := context.Background()
	owner, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("conectar: %v", err)
	}
	t.Cleanup(owner.Close)

	files, err := filepath.Glob(filepath.Join("..", "..", "..", "..", "..", "migrations", "tenant", "canonical", "mail-dav", "*.sql"))
	if err != nil || len(files) < 2 {
		t.Fatalf("migraciones del servicio: %v %v", files, err)
	}
	sort.Strings(files)
	for pass := 1; pass <= 2; pass++ {
		for _, f := range files {
			sqlBytes, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := owner.Exec(ctx, string(sqlBytes)); err != nil {
				t.Fatalf("aplicar %s (pasada %d): %v", filepath.Base(f), pass, err)
			}
		}
	}

	login := "it_dav_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	password := strings.ReplaceAll(uuid.NewString(), "-", "")
	for _, q := range []string{
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s'`, login, password),
		fmt.Sprintf(`GRANT mail_dav_service TO %s`, login),
	} {
		if _, err := owner.Exec(ctx, q); err != nil {
			t.Fatalf("crear el rol de login: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = owner.Exec(ctx, fmt.Sprintf(`REVOKE mail_dav_service FROM %s`, login))
		_, _ = owner.Exec(ctx, fmt.Sprintf(`DROP ROLE IF EXISTS %s`, login))
	})

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.User, cfg.ConnConfig.Password = login, password
	service, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("conectar como el rol de servicio: %v", err)
	}
	t.Cleanup(service.Close)
	if err := service.Ping(ctx); err != nil {
		t.Fatalf("el rol de servicio no puede conectar: %v", err)
	}
	return &env{ctx: db.WithPool(ctx, service), repo: NewRepository(&db.ContextPool{}), owner: owner, service: service}
}

func newPrincipal(name string) domain.Principal {
	return domain.Principal{TenantID: uuid.New(), MailboxID: uuid.New(), Username: name + "@it.test"}
}

func sameTenant(p domain.Principal, name string) domain.Principal {
	return domain.Principal{TenantID: p.TenantID, MailboxID: uuid.New(), Username: name + "@it.test"}
}

func (e *env) cleanup(t *testing.T, ps ...domain.Principal) {
	t.Helper()
	t.Cleanup(func() {
		for _, p := range ps {
			_, _ = e.owner.Exec(context.Background(), `DELETE FROM mail_dav.addressbooks WHERE mailbox_id = $1`, p.MailboxID)
			_, _ = e.owner.Exec(context.Background(), `DELETE FROM mail_dav.calendars WHERE mailbox_id = $1`, p.MailboxID)
		}
	})
}

func (e *env) book(t *testing.T, p domain.Principal, slug string) domain.Addressbook {
	t.Helper()
	b, err := e.repo.CreateAddressbook(e.ctx, p, domain.Addressbook{ID: uuid.New(), Slug: slug, DisplayName: slug}, maxBooks)
	if err != nil {
		t.Fatalf("crear libreta %s: %v", slug, err)
	}
	return b
}

func contact(t *testing.T, res, uid, name string) domain.Contact {
	t.Helper()
	raw := fmt.Sprintf("BEGIN:VCARD\r\nVERSION:3.0\r\nUID:%s\r\nFN:%s\r\nEMAIL:%s@it.test\r\nEND:VCARD\r\n", uid, name, uid)
	c, err := domain.NewContact(res, raw, domain.Limits{MaxVCardBytes: 1 << 20, MaxVCardProperties: 100, MaxContactsPerMailbox: 1, MaxAddressbooksPerMailbox: 1, MaxChangesRetained: 1})
	if err != nil {
		t.Fatal(err)
	}
	c.ID = uuid.New()
	return c
}

func (e *env) put(t *testing.T, p domain.Principal, slug string, c domain.Contact, cond domain.Precondition) (bool, error) {
	t.Helper()
	c.TenantID, c.MailboxID = p.TenantID, p.MailboxID
	return e.repo.PutContact(e.ctx, p, slug, c, cond, maxContacts, maxChanges)
}

func TestCicloDeVidaDeLibretasYContactos(t *testing.T) {
	e := setup(t)
	ana := newPrincipal("ana")
	e.cleanup(t, ana)

	if books, err := e.repo.ListAddressbooks(e.ctx, ana); err != nil || len(books) != 0 {
		t.Fatalf("sin libretas: %v %+v", err, books)
	}
	b := e.book(t, ana, "contacts")
	if b.SyncSeq != 0 || b.ChangesFloor != 0 || b.MailboxID != ana.MailboxID || b.TenantID != ana.TenantID {
		t.Fatalf("libreta nueva: %+v", b)
	}
	if _, err := e.repo.CreateAddressbook(e.ctx, ana, domain.Addressbook{ID: uuid.New(), Slug: "contacts", DisplayName: "otra"}, maxBooks); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("libreta repetida: %v", err)
	}

	c := contact(t, "a.vcf", "a", "Ana")
	if created, err := e.put(t, ana, "contacts", c, domain.Precondition{IfNoneMatchAny: true}); err != nil || !created {
		t.Fatalf("alta: %v %v", created, err)
	}
	got, err := e.repo.GetContact(e.ctx, ana, "contacts", "a.vcf")
	if err != nil || got.VCard != c.VCard || got.ETag != c.ETag || got.UID != "a" || got.DisplayName != "Ana" || len(got.Emails) != 1 || got.Emails[0] != "a@it.test" {
		t.Fatalf("lectura: %v %+v", err, got)
	}
	if _, err := e.put(t, ana, "contacts", contact(t, "a.vcf", "a", "Ana"), domain.Precondition{IfNoneMatchAny: true}); !errors.Is(err, domain.ErrPreconditionFailed) {
		t.Fatalf("If-None-Match * sobre uno existente: %v", err)
	}
	// Reenviar el mismo contenido no es un cambio: no avanza el ctag ni deja rastro.
	before, _ := e.repo.GetAddressbook(e.ctx, ana, "contacts")
	if created, err := e.put(t, ana, "contacts", c, domain.Precondition{}); err != nil || created {
		t.Fatalf("mismo contenido: %v %v", created, err)
	}
	if same, _ := e.repo.GetAddressbook(e.ctx, ana, "contacts"); same.SyncSeq != before.SyncSeq {
		t.Fatalf("el ctag avanzo sin cambio: %d -> %d", before.SyncSeq, same.SyncSeq)
	}
	edited := contact(t, "a.vcf", "a", "Ana editada")
	if created, err := e.put(t, ana, "contacts", edited, domain.Precondition{IfMatch: []string{c.ETag}}); err != nil || created {
		t.Fatalf("edicion con If-Match: %v %v", created, err)
	}
	if _, err := e.put(t, ana, "contacts", contact(t, "a.vcf", "a", "Tarde"), domain.Precondition{IfMatch: []string{c.ETag}}); !errors.Is(err, domain.ErrPreconditionFailed) {
		t.Fatalf("If-Match viejo: %v", err)
	}
	if _, err := e.put(t, ana, "contacts", contact(t, "b.vcf", "a", "Mismo UID"), domain.Precondition{}); err == nil {
		t.Fatal("el UID no se repite")
	} else if conflict := (*domain.UIDConflictError)(nil); !errors.As(err, &conflict) || conflict.Resource != "a.vcf" {
		t.Fatalf("conflicto de UID: %v", err)
	}
	if _, err := e.put(t, ana, "noexiste", contact(t, "z.vcf", "z", "Z"), domain.Precondition{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("libreta inexistente: %v", err)
	}

	book, contacts, err := e.repo.ListContacts(e.ctx, ana, "contacts")
	if err != nil || len(contacts) != 1 || book.SyncSeq != 2 || contacts[0].DisplayName != "Ana editada" {
		t.Fatalf("listado: %v %+v %+v", err, book, contacts)
	}
	if many, err := e.repo.GetContacts(e.ctx, ana, "contacts", []string{"a.vcf", "nada.vcf"}); err != nil || len(many) != 1 {
		t.Fatalf("multiget: %v %+v", err, many)
	}

	if err := e.repo.DeleteContact(e.ctx, ana, "contacts", "a.vcf", domain.Precondition{IfMatch: []string{c.ETag}}, maxChanges); !errors.Is(err, domain.ErrPreconditionFailed) {
		t.Fatalf("borrar con etag viejo: %v", err)
	}
	if err := e.repo.DeleteContact(e.ctx, ana, "contacts", "a.vcf", domain.Precondition{IfMatch: []string{edited.ETag}}, maxChanges); err != nil {
		t.Fatal(err)
	}
	if err := e.repo.DeleteContact(e.ctx, ana, "contacts", "a.vcf", domain.Precondition{}, maxChanges); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("borrar dos veces: %v", err)
	}
	if _, err := e.repo.GetContact(e.ctx, ana, "contacts", "a.vcf"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("borrado: %v", err)
	}

	e.book(t, ana, "familia")
	if _, err := e.put(t, ana, "familia", contact(t, "f.vcf", "f", "F"), domain.Precondition{}); err != nil {
		t.Fatal(err)
	}
	if err := e.repo.DeleteAddressbook(e.ctx, ana, "familia"); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := e.owner.QueryRow(e.ctx, `SELECT (SELECT count(*) FROM mail_dav.contacts WHERE mailbox_id = $1 AND uid = 'f') + (SELECT count(*) FROM mail_dav.collection_changes WHERE mailbox_id = $1 AND resource_name = 'f.vcf')`, ana.MailboxID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("borrar la libreta borra sus contactos y su registro de cambios: %d %v", left, err)
	}
	if err := e.repo.DeleteAddressbook(e.ctx, ana, "familia"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("borrar dos veces: %v", err)
	}
}

func TestSincronizacionPorCambios(t *testing.T) {
	e := setup(t)
	ana := newPrincipal("ana")
	e.cleanup(t, ana)
	b := e.book(t, ana, "contacts")

	for i := 1; i <= 2; i++ {
		e.put(t, ana, "contacts", contact(t, fmt.Sprintf("c%d.vcf", i), fmt.Sprintf("c%d", i), "C"), domain.Precondition{})
	}
	afterTwo, changed, removed, err := e.repo.ChangesSince(e.ctx, ana, "contacts", 0)
	if err != nil || afterTwo.SyncSeq != 2 || len(changed) != 2 || len(removed) != 0 {
		t.Fatalf("desde el principio: %v %+v %d %v", err, afterTwo, len(changed), removed)
	}
	e.put(t, ana, "contacts", contact(t, "c1.vcf", "c1", "C1 editado"), domain.Precondition{})
	if err := e.repo.DeleteContact(e.ctx, ana, "contacts", "c2.vcf", domain.Precondition{}, maxChanges); err != nil {
		t.Fatal(err)
	}
	book, changed, removed, err := e.repo.ChangesSince(e.ctx, ana, "contacts", afterTwo.SyncSeq)
	if err != nil || book.SyncSeq != 4 || len(changed) != 1 || changed[0].ResourceName != "c1.vcf" || len(removed) != 1 || removed[0] != "c2.vcf" {
		t.Fatalf("diferencia: %v %+v %+v %v", err, book, changed, removed)
	}
	// Un recurso creado y borrado dentro del intervalo solo consta como borrado.
	e.put(t, ana, "contacts", contact(t, "c3.vcf", "c3", "C3"), domain.Precondition{})
	_ = e.repo.DeleteContact(e.ctx, ana, "contacts", "c3.vcf", domain.Precondition{}, maxChanges)
	_, changed, removed, err = e.repo.ChangesSince(e.ctx, ana, "contacts", book.SyncSeq)
	if err != nil || len(changed) != 0 || len(removed) != 1 || removed[0] != "c3.vcf" {
		t.Fatalf("creado y borrado: %v %+v %v", err, changed, removed)
	}

	// Con la poda a maxChanges cambios, un token anterior al limite ya no se resuelve.
	current, _ := e.repo.GetAddressbook(e.ctx, ana, "contacts")
	if current.ChangesFloor != current.SyncSeq-maxChanges {
		t.Fatalf("suelo de cambios %d con secuencia %d", current.ChangesFloor, current.SyncSeq)
	}
	if _, _, _, err := e.repo.ChangesSince(e.ctx, ana, "contacts", current.ChangesFloor-1); !errors.Is(err, domain.ErrInvalidSyncToken) {
		t.Fatalf("token podado: %v", err)
	}
	if _, _, _, err := e.repo.ChangesSince(e.ctx, ana, "contacts", current.ChangesFloor); err != nil {
		t.Fatalf("el limite exacto aun se resuelve: %v", err)
	}
	if _, _, _, err := e.repo.ChangesSince(e.ctx, ana, "contacts", current.SyncSeq+1); !errors.Is(err, domain.ErrInvalidSyncToken) {
		t.Fatalf("token del futuro: %v", err)
	}
	var kept int
	if err := e.owner.QueryRow(e.ctx, `SELECT count(*) FROM mail_dav.collection_changes WHERE addressbook_id = $1`, b.ID).Scan(&kept); err != nil || kept != maxChanges {
		t.Fatalf("cambios conservados: %d %v", kept, err)
	}
}

func TestLimitesYConcurrencia(t *testing.T) {
	e := setup(t)
	ana := newPrincipal("ana")
	e.cleanup(t, ana)
	e.book(t, ana, "uno")
	e.book(t, ana, "dos")
	e.book(t, ana, "tres")
	if _, err := e.repo.CreateAddressbook(e.ctx, ana, domain.Addressbook{ID: uuid.New(), Slug: "cuatro", DisplayName: "x"}, maxBooks); !errors.Is(err, domain.ErrAddressbookLimit) {
		t.Fatalf("limite de libretas: %v", err)
	}

	// 20 altas simultaneas repartidas en dos libretas: el limite es del buzon y no se supera.
	var wg sync.WaitGroup
	results := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			slug := []string{"uno", "dos"}[i%2]
			c := contact(t, fmt.Sprintf("p%d.vcf", i), fmt.Sprintf("p%d", i), "P")
			c.TenantID, c.MailboxID = ana.TenantID, ana.MailboxID
			_, err := e.repo.PutContact(e.ctx, ana, slug, c, domain.Precondition{}, maxContacts, 1000)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	var ok, full int
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, domain.ErrContactLimit):
			full++
		default:
			t.Fatalf("error inesperado: %v", err)
		}
	}
	if ok != maxContacts || full != 20-maxContacts {
		t.Fatalf("altas concurrentes: %d admitidas y %d rechazadas, el limite es %d", ok, full, maxContacts)
	}

	// Cinco PUT con If-None-Match * sobre el mismo recurso: solo uno lo crea. El buzon ya esta en su limite:
	// se libera un lugar para que el resultado dependa de la precondicion y no del limite.
	var slug, resource string
	if err := e.owner.QueryRow(e.ctx, `SELECT b.slug, c.resource_name FROM mail_dav.contacts c JOIN mail_dav.addressbooks b ON b.id = c.addressbook_id WHERE c.mailbox_id = $1 ORDER BY c.resource_name LIMIT 1`, ana.MailboxID).Scan(&slug, &resource); err != nil {
		t.Fatal(err)
	}
	if err := e.repo.DeleteContact(e.ctx, ana, slug, resource, domain.Precondition{}, 1000); err != nil {
		t.Fatal(err)
	}
	created := make(chan error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := contact(t, "unico.vcf", "unico", "U")
			c.TenantID, c.MailboxID = ana.TenantID, ana.MailboxID
			_, err := e.repo.PutContact(e.ctx, ana, "tres", c, domain.Precondition{IfNoneMatchAny: true}, maxContacts, 1000)
			created <- err
		}()
	}
	wg.Wait()
	close(created)
	var won, lost int
	for err := range created {
		switch {
		case err == nil:
			won++
		case errors.Is(err, domain.ErrPreconditionFailed):
			lost++
		default:
			t.Fatalf("error inesperado: %v", err)
		}
	}
	// El mismo contenido reenviado a un recurso que ya existe cuenta como "sin cambios" (nil), asi que el
	// unico invariante es que hay exactamente uno guardado y ninguno perdio por otra causa.
	if won < 1 || won+lost != 5 {
		t.Fatalf("ganadores %d, perdedores %d", won, lost)
	}
	var stored int
	if err := e.owner.QueryRow(e.ctx, `SELECT count(*) FROM mail_dav.contacts WHERE mailbox_id = $1 AND resource_name = 'unico.vcf'`, ana.MailboxID).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("guardados: %d %v", stored, err)
	}

	// Las secuencias de una libreta no se repiten ni dejan huecos aunque los cambios sean simultaneos.
	var seqs []int64
	rows, err := e.owner.Query(e.ctx, `SELECT seq FROM mail_dav.collection_changes ch JOIN mail_dav.addressbooks b ON b.id = ch.addressbook_id WHERE b.mailbox_id = $1 AND b.slug = 'uno' ORDER BY seq`, ana.MailboxID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s int64
		_ = rows.Scan(&s)
		seqs = append(seqs, s)
	}
	rows.Close()
	for i := 1; i < len(seqs); i++ {
		if seqs[i] != seqs[i-1]+1 {
			t.Fatalf("secuencias con huecos o repetidas: %v", seqs)
		}
	}
}

// asServiceIn ejecuta fn en una transaccion del rol de servicio con la sesion de p, como la abre el repositorio.
func (e *env) asService(t *testing.T, p *domain.Principal, fn func(tx pgx.Tx)) {
	t.Helper()
	tx, err := e.service.Begin(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(e.ctx)
	if p != nil {
		if _, err := tx.Exec(e.ctx, `SELECT set_config('app.current_tenant_id', $1, true), set_config('app.current_user_id', $2, true)`, p.TenantID.String(), p.MailboxID.String()); err != nil {
			t.Fatal(err)
		}
	}
	fn(tx)
}

func count(t *testing.T, tx pgx.Tx, q string, args ...any) int {
	t.Helper()
	var n int
	if err := tx.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestAislamientoEntreBuzonesYEmpresas(t *testing.T) {
	e := setup(t)
	ana := newPrincipal("ana")
	cris := sameTenant(ana, "cris")
	bea := newPrincipal("bea")
	e.cleanup(t, ana, cris, bea)
	for _, p := range []domain.Principal{ana, cris, bea} {
		e.book(t, p, "contacts")
	}
	e.put(t, ana, "contacts", contact(t, "secreto.vcf", "secreto", "Secreto de Ana"), domain.Precondition{})

	// Por el repositorio: ni el buzon de la misma empresa ni el de otra ven ni tocan lo de Ana.
	for name, other := range map[string]domain.Principal{"misma empresa": cris, "otra empresa": bea} {
		if _, err := e.repo.GetContact(e.ctx, other, "contacts", "secreto.vcf"); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s lee: %v", name, err)
		}
		if _, contacts, err := e.repo.ListContacts(e.ctx, other, "contacts"); err != nil || len(contacts) != 0 {
			t.Errorf("%s lista: %v %+v", name, err, contacts)
		}
		if got, err := e.repo.GetContacts(e.ctx, other, "contacts", []string{"secreto.vcf"}); err != nil || len(got) != 0 {
			t.Errorf("%s pide por nombre: %v %+v", name, err, got)
		}
		if err := e.repo.DeleteContact(e.ctx, other, "contacts", "secreto.vcf", domain.Precondition{}, maxChanges); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s borra: %v", name, err)
		}
		if _, _, _, err := e.repo.ChangesSince(e.ctx, other, "contacts", 0); err != nil {
			t.Errorf("%s pide cambios de su propia libreta: %v", name, err)
		}
		// La misma libreta no se puede alcanzar con el id de otro: sus lecturas son por slug DENTRO de su buzon.
		if books, err := e.repo.ListAddressbooks(e.ctx, other); err != nil || len(books) != 1 || books[0].MailboxID != other.MailboxID {
			t.Errorf("%s ve libretas ajenas: %v %+v", name, err, books)
		}
	}
	if err := e.repo.DeleteAddressbook(e.ctx, cris, "contacts"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.repo.GetContact(e.ctx, ana, "contacts", "secreto.vcf"); err != nil {
		t.Fatalf("borrar la libreta de otro buzon con el mismo nombre toco la de Ana: %v", err)
	}

	// Por SQL directo con el rol de servicio: la politica de fila aisla aunque la consulta no filtre nada.
	e.book(t, cris, "contacts")
	e.put(t, cris, "contacts", contact(t, "mio.vcf", "mio", "De Cris"), domain.Precondition{})
	e.asService(t, &cris, func(tx pgx.Tx) {
		for table, own := range map[string]int{"contacts": 1, "addressbooks": 1, "collection_changes": 1} {
			if n := count(t, tx, `SELECT count(*) FROM mail_dav.`+table); n != own {
				t.Errorf("cris ve %d filas de %s sin filtro, tiene %d", n, table, own)
			}
			if n := count(t, tx, `SELECT count(*) FROM mail_dav.`+table+` WHERE mailbox_id = $1`, ana.MailboxID); n != 0 {
				t.Errorf("cris ve %d filas de Ana en %s", n, table)
			}
		}
	})
	e.asService(t, &bea, func(tx pgx.Tx) {
		if n := count(t, tx, `SELECT count(*) FROM mail_dav.contacts WHERE tenant_id = $1`, ana.TenantID); n != 0 {
			t.Errorf("otra empresa ve %d contactos de Ana", n)
		}
	})
	e.asService(t, &ana, func(tx pgx.Tx) {
		if n := count(t, tx, `SELECT count(*) FROM mail_dav.contacts`); n != 1 {
			t.Errorf("Ana ve %d contactos, tiene 1", n)
		}
	})
	e.asService(t, nil, func(tx pgx.Tx) {
		if n := count(t, tx, `SELECT count(*) FROM mail_dav.contacts`); n != 0 {
			t.Errorf("sin sesion se ven %d contactos: la politica no es fail-closed", n)
		}
	})

	// Escribir como otro buzon, o con la empresa de otro, no pasa la politica.
	e.asService(t, &cris, func(tx pgx.Tx) {
		_, err := tx.Exec(e.ctx, `INSERT INTO mail_dav.addressbooks (id, tenant_id, mailbox_id, slug, display_name) VALUES ($1, $2, $3, 'robado', 'x')`, uuid.New(), ana.TenantID, ana.MailboxID)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("insertar como Ana siendo Cris: %v", err)
		}
	})
	e.asService(t, &cris, func(tx pgx.Tx) {
		tag, err := tx.Exec(e.ctx, `UPDATE mail_dav.contacts SET display_name = 'hackeado' WHERE mailbox_id = $1`, ana.MailboxID)
		if err != nil || tag.RowsAffected() != 0 {
			t.Errorf("Cris modifico contactos de Ana: %v %d", err, tag.RowsAffected())
		}
		tag, err = tx.Exec(e.ctx, `DELETE FROM mail_dav.contacts WHERE mailbox_id = $1`, ana.MailboxID)
		if err != nil || tag.RowsAffected() != 0 {
			t.Errorf("Cris borro contactos de Ana: %v %d", err, tag.RowsAffected())
		}
	})

	// Mutacion: sin la politica, la misma consulta veria los datos de Ana. Demuestra que la prueba de arriba
	// detectaria una politica ausente, y no pasa por casualidad. La transaccion se revierte.
	tx, err := e.owner.Begin(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(e.ctx)
	if _, err := tx.Exec(e.ctx, `ALTER TABLE mail_dav.contacts DISABLE ROW LEVEL SECURITY`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(e.ctx, `SET LOCAL ROLE mail_dav_service`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(e.ctx, `SELECT set_config('app.current_tenant_id', $1, true), set_config('app.current_user_id', $2, true)`, bea.TenantID.String(), bea.MailboxID.String()); err != nil {
		t.Fatal(err)
	}
	if n := count(t, tx, `SELECT count(*) FROM mail_dav.contacts WHERE mailbox_id = $1`, ana.MailboxID); n != 1 {
		t.Fatalf("con la politica desactivada Bea deberia ver el contacto de Ana (%d): la prueba no detectaria una politica ausente", n)
	}
}

func TestLaBaseRechazaLoQueElServicioNoDeberiaEscribir(t *testing.T) {
	e := setup(t)
	ana := newPrincipal("ana")
	cris := sameTenant(ana, "cris")
	e.cleanup(t, ana, cris)
	bookAna := e.book(t, ana, "contacts")
	e.book(t, cris, "contacts")

	insert := func(q string, args ...any) error {
		_, err := e.owner.Exec(e.ctx, q, args...)
		return err
	}
	const cols = `INSERT INTO mail_dav.contacts (id, tenant_id, mailbox_id, addressbook_id, resource_name, uid, vcard, etag) VALUES ($1, $2, $3, $4, $5, $6, 'x', $7)`
	etag := strings.Repeat("a", 64)
	for name, err := range map[string]error{
		"contacto en la libreta de otro buzon":        insert(cols, uuid.New(), ana.TenantID, cris.MailboxID, bookAna.ID, "a.vcf", "u1", etag),
		"contacto en la libreta de otra empresa":      insert(cols, uuid.New(), uuid.New(), ana.MailboxID, bookAna.ID, "a.vcf", "u2", etag),
		"nombre de recurso con ruta":                  insert(cols, uuid.New(), ana.TenantID, ana.MailboxID, bookAna.ID, "../a.vcf", "u3", etag),
		"recurso sin .vcf":                            insert(cols, uuid.New(), ana.TenantID, ana.MailboxID, bookAna.ID, "a.txt", "u4", etag),
		"etag que no es sha256":                       insert(cols, uuid.New(), ana.TenantID, ana.MailboxID, bookAna.ID, "b.vcf", "u5", "no-es-hex"),
		"libreta con nombre invalido":                 insert(`INSERT INTO mail_dav.addressbooks (id, tenant_id, mailbox_id, slug, display_name) VALUES ($1, $2, $3, 'Mayus/x', 'x')`, uuid.New(), ana.TenantID, ana.MailboxID),
		"vCard de mas de 4 MiB":                       insert(`INSERT INTO mail_dav.contacts (id, tenant_id, mailbox_id, addressbook_id, resource_name, uid, vcard, etag) VALUES ($1, $2, $3, $4, 'g.vcf', 'g', repeat('a', 4194305), $5)`, uuid.New(), ana.TenantID, ana.MailboxID, bookAna.ID, etag),
		"suelo de cambios por encima de la secuencia": insert(`UPDATE mail_dav.addressbooks SET changes_floor = sync_seq + 1 WHERE id = $1`, bookAna.ID),
	} {
		var pgErr *pgconn.PgError
		if err == nil || !errors.As(err, &pgErr) || (pgErr.Code != "23514" && pgErr.Code != "23503") {
			t.Errorf("%s: debia rechazarlo una restriccion, fue %v", name, err)
		}
	}

	// El rol de servicio solo tiene DML: nada de DDL, TRUNCATE ni otros esquemas.
	for name, q := range map[string]string{
		"crear una tabla":    `CREATE TABLE mail_dav.intrusa (id int)`,
		"borrar una tabla":   `DROP TABLE mail_dav.contacts`,
		"truncar":            `TRUNCATE mail_dav.contacts`,
		"quitar la politica": `DROP POLICY mailbox_isolation ON mail_dav.contacts`,
		"apagar RLS":         `ALTER TABLE mail_dav.contacts DISABLE ROW LEVEL SECURITY`,
	} {
		_, err := e.service.Exec(e.ctx, q)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || (pgErr.Code != "42501" && pgErr.Code != "42P01") {
			t.Errorf("%s: el rol de servicio no debia poder, fue %v", name, err)
		}
	}
	// Ni por el sombrero de otro rol: no es miembro de nada mas.
	if _, err := e.service.Exec(e.ctx, `SET ROLE postgres`); err == nil {
		t.Error("el rol de servicio pudo cambiar de rol")
	}
}

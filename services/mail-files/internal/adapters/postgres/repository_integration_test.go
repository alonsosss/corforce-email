//go:build integration

package postgres

// Prueba de integracion contra un Postgres real. Se ejecuta con:
//
//	MAIL_FILES_TEST_DSN=postgres://user:pass@localhost:5432/db?sslmode=disable \
//	  go test -tags integration ./services/mail-files/internal/adapters/postgres/
//
// Aplica las migraciones canonicas del servicio dos veces (idempotencia) y ejercita el repositorio con la
// credencial que tendra en produccion: un rol de login miembro de mail_files_service, que no es dueno de la
// tabla y por eso queda sujeto a la politica de fila por empresa.

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
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/mail-files/internal/domain"
	"github.com/google/uuid"
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

type env struct {
	repo    *Repository
	owner   *pgxpool.Pool
	service *pgxpool.Pool
}

func setup(t *testing.T) *env {
	t.Helper()
	dsn := integrationEnv(t, "MAIL_FILES_TEST_DSN")
	ctx := context.Background()
	owner, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("conectar: %v", err)
	}
	t.Cleanup(owner.Close)

	files, err := filepath.Glob(filepath.Join("..", "..", "..", "..", "..", "migrations", "tenant", "canonical", "mail-files", "*.sql"))
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

	login := "it_files_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	password := strings.ReplaceAll(uuid.NewString(), "-", "")
	for _, q := range []string{
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s'`, login, password),
		fmt.Sprintf(`GRANT mail_files_service TO %s`, login),
	} {
		if _, err := owner.Exec(ctx, q); err != nil {
			t.Fatalf("crear el rol de login: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = owner.Exec(ctx, fmt.Sprintf(`REVOKE mail_files_service FROM %s`, login))
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
	return &env{repo: NewRepository(&db.ContextPool{}), owner: owner, service: service}
}

// as es el contexto de una operacion de la empresa, como lo deja tenantdb.Binder.
func (e *env) as(tenant uuid.UUID) context.Context {
	return db.WithPool(middleware.WithTenantID(context.Background(), tenant.String()), e.service)
}

func (e *env) cleanup(t *testing.T, tenants ...uuid.UUID) {
	t.Cleanup(func() {
		for _, id := range tenants {
			_, _ = e.owner.Exec(context.Background(), `DELETE FROM mail_files.shared_files WHERE tenant_id = $1`, id)
		}
	})
}

var itPolicy = domain.Policy{MaxFileBytes: 100, DefaultExpiryDays: 7, MaxExpiryDays: 30, DefaultMaxDownloads: 2, MaxDownloads: 5,
	MailboxQuotaBytes: 300, TenantQuotaBytes: 400, MaxActivePerMailbox: 10}

func newFile(o domain.Owner, size int64, now time.Time) domain.File {
	id := uuid.New()
	return domain.File{
		ID: id, TenantID: o.TenantID, MailboxID: o.MailboxID, Name: "planos.dwg", SizeBytes: size,
		SHA256: strings.Repeat("a", 64), ObjectKey: domain.ObjectKey(o.TenantID, id),
		ExpiresAt: now.Add(24 * time.Hour).Truncate(time.Second), MaxDownloads: 2,
	}
}

func TestCicloDeVidaDeUnFichero(t *testing.T) {
	e := setup(t)
	o := domain.Owner{TenantID: uuid.New(), MailboxID: uuid.New()}
	e.cleanup(t, o.TenantID)
	ctx, now := e.as(o.TenantID), time.Now()

	f := newFile(o, 50, now)
	if err := e.repo.CreatePending(ctx, f, itPolicy, now); err != nil {
		t.Fatal(err)
	}
	if u, err := e.repo.Usage(ctx, o, now); err != nil || u.MailboxBytes != 50 || u.MailboxActive != 1 || u.TenantBytes != 50 {
		t.Fatalf("uso con pendiente: %v %+v", err, u)
	}
	ready, err := e.repo.MarkReady(ctx, o.TenantID, f.ID)
	if err != nil || ready.Status != domain.StatusReady || !ready.ExpiresAt.Equal(f.ExpiresAt) || ready.Name != f.Name {
		t.Fatalf("listo: %v %+v", err, ready)
	}
	if _, err := e.repo.MarkReady(ctx, o.TenantID, f.ID); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("listo dos veces: %v", err)
	}
	for i := 1; i <= 2; i++ {
		got, err := e.repo.ClaimDownload(ctx, o.TenantID, f.ID, now)
		if err != nil || got.Downloads != i || got.LastDownloadAt == nil {
			t.Fatalf("descarga %d: %v %+v", i, err, got)
		}
	}
	if _, err := e.repo.ClaimDownload(ctx, o.TenantID, f.ID, now); !errors.Is(err, domain.ErrLinkInvalid) {
		t.Fatalf("pasado el tope: %v", err)
	}
	if u, _ := e.repo.Usage(ctx, o, now); u.MailboxActive != 0 {
		t.Fatalf("un agotado ocupa cuota: %+v", u)
	}
	list, err := e.repo.ListByMailbox(ctx, o, 10)
	if err != nil || len(list) != 1 || list[0].State(now) != domain.StateExhausted {
		t.Fatalf("listado: %v %+v", err, list)
	}
	if _, err := e.repo.Revoke(ctx, domain.Owner{TenantID: o.TenantID, MailboxID: uuid.New()}, f.ID, now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("revocar el de otro buzon: %v", err)
	}
	revoked, err := e.repo.Revoke(ctx, o, f.ID, now)
	if err != nil || revoked.Status != domain.StatusRevoked || revoked.RevokedAt == nil {
		t.Fatalf("revocar: %v %+v", err, revoked)
	}
	if _, err := e.repo.Revoke(ctx, o, f.ID, now); !errors.Is(err, domain.ErrNotRevocable) {
		t.Fatalf("revocar dos veces: %v", err)
	}
	due, err := e.repo.DueForDeletion(ctx, o.TenantID, now, time.Hour, 10)
	if err != nil || len(due) != 1 || due[0].ID != f.ID {
		t.Fatalf("a borrar: %v %+v", err, due)
	}
	if err := e.repo.MarkObjectDeleted(ctx, o.TenantID, f.ID); err != nil {
		t.Fatal(err)
	}
	if due, _ := e.repo.DueForDeletion(ctx, o.TenantID, now, time.Hour, 10); len(due) != 0 {
		t.Fatalf("se repite el borrado: %+v", due)
	}
	if n, err := e.repo.PurgeHistory(ctx, o.TenantID, now.Add(-time.Hour)); err != nil || n != 0 {
		t.Fatalf("poda prematura: %d %v", n, err)
	}
	if n, err := e.repo.PurgeHistory(ctx, o.TenantID, time.Now().Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("poda: %d %v", n, err)
	}
}

func TestCuotasBajoConcurrencia(t *testing.T) {
	e := setup(t)
	o := domain.Owner{TenantID: uuid.New(), MailboxID: uuid.New()}
	e.cleanup(t, o.TenantID)
	ctx, now := e.as(o.TenantID), time.Now()

	// Diez subidas de 100 a la vez contra una cuota de buzon de 300: pasan exactamente tres.
	var wg sync.WaitGroup
	results := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- e.repo.CreatePending(ctx, newFile(o, 100, now), itPolicy, now)
		}()
	}
	wg.Wait()
	close(results)
	ok, quota := 0, 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, domain.ErrMailboxQuota):
			quota++
		default:
			t.Fatalf("error inesperado: %v", err)
		}
	}
	if ok != 3 || quota != 7 {
		t.Fatalf("pasaron %d y se rechazaron %d", ok, quota)
	}
	colleague := domain.Owner{TenantID: o.TenantID, MailboxID: uuid.New()}
	if err := e.repo.CreatePending(ctx, newFile(colleague, 100, now), itPolicy, now); err != nil {
		t.Fatal(err)
	}
	if err := e.repo.CreatePending(ctx, newFile(colleague, 1, now), itPolicy, now); !errors.Is(err, domain.ErrTenantQuota) {
		t.Fatalf("empresa llena: %v", err)
	}
}

func TestBarridoCaducaYCierraPendientes(t *testing.T) {
	e := setup(t)
	o := domain.Owner{TenantID: uuid.New(), MailboxID: uuid.New()}
	e.cleanup(t, o.TenantID)
	ctx, now := e.as(o.TenantID), time.Now()

	expiring := newFile(o, 10, now)
	if err := e.repo.CreatePending(ctx, expiring, itPolicy, now); err != nil {
		t.Fatal(err)
	}
	if _, err := e.repo.MarkReady(ctx, o.TenantID, expiring.ID); err != nil {
		t.Fatal(err)
	}
	stale := newFile(o, 10, now)
	if err := e.repo.CreatePending(ctx, stale, itPolicy, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	later := expiring.ExpiresAt.Add(time.Minute)
	if n, err := e.repo.ExpireDue(ctx, o.TenantID, later); err != nil || n != 1 {
		t.Fatalf("caducar: %d %v", n, err)
	}
	if n, err := e.repo.FailStalePending(ctx, o.TenantID, now.Add(-time.Hour)); err != nil || n != 1 {
		t.Fatalf("pendientes: %d %v", n, err)
	}
	due, err := e.repo.DueForDeletion(ctx, o.TenantID, later, time.Hour, 10)
	if err != nil || len(due) != 1 || due[0].ID != stale.ID {
		t.Fatalf("dentro de la gracia solo el fallido: %v %+v", err, due)
	}
	if due, _ := e.repo.DueForDeletion(ctx, o.TenantID, later.Add(2*time.Hour), time.Hour, 10); len(due) != 2 {
		t.Fatalf("pasada la gracia: %+v", due)
	}
}

func TestLaPoliticaDeFilaAislaEmpresas(t *testing.T) {
	e := setup(t)
	a := domain.Owner{TenantID: uuid.New(), MailboxID: uuid.New()}
	b := domain.Owner{TenantID: uuid.New(), MailboxID: uuid.New()}
	e.cleanup(t, a.TenantID, b.TenantID)
	now := time.Now()
	f := newFile(a, 10, now)
	if err := e.repo.CreatePending(e.as(a.TenantID), f, itPolicy, now); err != nil {
		t.Fatal(err)
	}
	// Con la sesion de otra empresa la fila no existe, aunque la consulta nombre la empresa dueña.
	if _, err := e.repo.Get(e.as(b.TenantID), a.TenantID, f.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("lectura cruzada: %v", err)
	}
	if n, err := e.repo.FailStalePending(e.as(b.TenantID), a.TenantID, now.Add(time.Hour)); err != nil || n != 0 {
		t.Fatalf("escritura cruzada: %d %v", n, err)
	}
	// Tampoco se puede escribir una fila de otra empresa.
	if err := e.repo.CreatePending(e.as(b.TenantID), newFile(a, 10, now), itPolicy, now); err == nil {
		t.Fatal("alta en otra empresa aceptada")
	}
	// Sin empresa en la sesion no se ve nada: la politica no deja pasar una consulta sin identidad.
	bare := db.WithPool(context.Background(), e.service)
	if _, err := e.repo.Get(bare, a.TenantID, f.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("sin empresa: %v", err)
	}
	if got, err := e.repo.Get(e.as(a.TenantID), a.TenantID, f.ID); err != nil || got.ID != f.ID {
		t.Fatalf("su propia empresa: %v", err)
	}
}

func TestRestriccionesDeLaTabla(t *testing.T) {
	e := setup(t)
	o := domain.Owner{TenantID: uuid.New(), MailboxID: uuid.New()}
	e.cleanup(t, o.TenantID)
	now := time.Now()
	for name, mutate := range map[string]func(*domain.File){
		"tamano cero":  func(f *domain.File) { f.SizeBytes = 0 },
		"huella":       func(f *domain.File) { f.SHA256 = "no-es-hex" },
		"descargas":    func(f *domain.File) { f.MaxDownloads = 0 },
		"nombre vacio": func(f *domain.File) { f.Name = "" },
		"nombre largo": func(f *domain.File) { f.Name = strings.Repeat("x", 256) },
	} {
		f := newFile(o, 10, now)
		mutate(&f)
		if err := e.repo.CreatePending(e.as(o.TenantID), f, itPolicy, now); err == nil {
			t.Errorf("%s aceptado", name)
		}
	}
	dup := newFile(o, 10, now)
	if err := e.repo.CreatePending(e.as(o.TenantID), dup, itPolicy, now); err != nil {
		t.Fatal(err)
	}
	again := newFile(o, 10, now)
	again.ObjectKey = dup.ObjectKey
	if err := e.repo.CreatePending(e.as(o.TenantID), again, itPolicy, now); err == nil {
		t.Fatal("clave de objeto repetida aceptada")
	}
}

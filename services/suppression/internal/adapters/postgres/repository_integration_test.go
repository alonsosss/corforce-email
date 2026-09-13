//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/suppression/internal/domain"
	"github.com/alonsosss/corforce-email/services/suppression/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// repoRoot es la raiz del repositorio, para leer las migraciones canonicas.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "..")
}

// suppressionMigrations devuelve las migraciones del servicio en orden de aplicacion.
func suppressionMigrations(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(repoRoot(t), "migrations", "tenant", "canonical", "suppression", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("migraciones de suppression: %v %v", files, err)
	}
	sort.Strings(files)
	return files
}

func applyFiles(t *testing.T, ctx context.Context, pool *pgxpool.Pool, files ...string) {
	t.Helper()
	for _, f := range files {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("aplicar %s: %v", filepath.Base(f), err)
		}
	}
}

// SUPPRESSION_TEST_DSN apunta a una base de pruebas (vacia o ya migrada). La prueba aplica
// platform/00_outbox.sql y todas las migraciones de suppression DOS veces, que es lo que
// garantiza que toleran re-ejecutarse. Cada prueba usa una empresa nueva.
func testPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	dsn := os.Getenv("SUPPRESSION_TEST_DSN")
	if dsn == "" {
		t.Skip("SUPPRESSION_TEST_DSN no definido")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	files := append([]string{filepath.Join(repoRoot(t), "migrations", "tenant", "canonical", "platform", "00_outbox.sql")}, suppressionMigrations(t)...)
	applyFiles(t, ctx, pool, append(files, files...)...)
	return pool, db.WithPool(ctx, pool)
}

func TestRepositorioCausas(t *testing.T) {
	_, ctx := testPool(t)
	repo := NewRepository(&db.ContextPool{})
	tenant := uuid.New()
	now := time.Now()

	e := &domain.Entry{TenantID: tenant, Email: "ana@example.com", Reason: domain.ReasonUnsubscribe, Source: "transactional", Detail: "enlace"}
	if err := repo.Insert(ctx, e); err != nil {
		t.Fatal(err)
	}
	if e.ID == uuid.Nil || e.CreatedAt.IsZero() {
		t.Fatalf("insert no devolvio id/created_at: %+v", e)
	}
	// La misma direccion admite otra causa; la misma causa, no.
	manual := &domain.Entry{TenantID: tenant, Email: "ana@example.com", Reason: domain.ReasonManual, Source: "api"}
	if err := repo.Insert(ctx, manual); err != nil {
		t.Fatalf("una segunda causa de la misma direccion: %v", err)
	}
	if err := repo.Insert(ctx, &domain.Entry{TenantID: tenant, Email: "ana@example.com", Reason: domain.ReasonUnsubscribe}); !errors.Is(err, domain.ErrEntryAlreadyExists) {
		t.Fatalf("la misma causa: se esperaba ErrEntryAlreadyExists, hubo %v", err)
	}
	// La misma direccion y causa en otra empresa es otra fila.
	if err := repo.Insert(ctx, &domain.Entry{TenantID: uuid.New(), Email: "ana@example.com", Reason: domain.ReasonUnsubscribe}); err != nil {
		t.Fatalf("otra empresa: %v", err)
	}
	// El CHECK de minusculas protege aunque el codigo no normalice.
	if err := repo.Insert(ctx, &domain.Entry{TenantID: tenant, Email: "MAYUS@example.com", Reason: domain.ReasonManual}); err == nil {
		t.Fatal("el CHECK de minusculas debe rechazar la fila")
	}
	// expires_at solo en manual.
	exp := now.Add(time.Hour)
	if err := repo.Insert(ctx, &domain.Entry{TenantID: tenant, Email: "rebote@example.com", Reason: domain.ReasonHardBounce, ExpiresAt: &exp}); err == nil {
		t.Fatal("expires_at con hard_bounce debe violar el CHECK")
	}

	got, err := repo.GetCauseForUpdate(ctx, tenant, "ana@example.com", domain.ReasonManual)
	if err != nil || got.ID != manual.ID {
		t.Fatalf("GetCauseForUpdate: %+v err=%v", got, err)
	}
	if _, err := repo.GetCauseForUpdate(ctx, tenant, "ana@example.com", domain.ReasonComplaint); !errors.Is(err, domain.ErrEntryNotFound) {
		t.Fatalf("causa que no tiene: %v", err)
	}

	msg := uuid.New()
	manual.Source, manual.MessageID, manual.ExpiresAt = "campaign", &msg, &exp
	if err := repo.Update(ctx, manual); err != nil {
		t.Fatal(err)
	}
	got, err = repo.GetByID(ctx, tenant, manual.ID)
	if err != nil || got.Source != "campaign" || got.MessageID == nil || *got.MessageID != msg || got.ExpiresAt == nil {
		t.Fatalf("update no persistio: %+v err=%v", got, err)
	}
	if !got.UpdatedAt.After(got.CreatedAt) {
		t.Fatalf("el trigger de updated_at no corrio: created=%s updated=%s", got.CreatedAt, got.UpdatedAt)
	}
	// Convertir una causa en otra que la direccion ya tiene choca con la unicidad.
	manual.Reason, manual.ExpiresAt = domain.ReasonUnsubscribe, nil
	if err := repo.Update(ctx, manual); !errors.Is(err, domain.ErrEntryAlreadyExists) {
		t.Fatalf("update a una causa repetida: %v", err)
	}
	manual.Reason, manual.ExpiresAt = domain.ReasonManual, &exp
	if _, err := repo.GetByID(ctx, uuid.New(), e.ID); !errors.Is(err, domain.ErrEntryNotFound) {
		t.Fatalf("otra empresa no debe ver la fila: %v", err)
	}

	found, err := repo.FindByEmails(ctx, tenant, []string{"ana@example.com", "nadie@example.com"})
	if err != nil || len(found) != 2 {
		t.Fatalf("FindByEmails devuelve todas las causas: %d err=%v", len(found), err)
	}

	if err := repo.Delete(ctx, tenant, manual.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, tenant, manual.ID); !errors.Is(err, domain.ErrEntryNotFound) {
		t.Fatalf("segundo delete: %v", err)
	}
	if left, _ := repo.FindByEmails(ctx, tenant, []string{"ana@example.com"}); len(left) != 1 || left[0].Reason != domain.ReasonUnsubscribe {
		t.Fatalf("borrar una causa deja las demas: %+v", left)
	}
}

func TestRepositorioInsertMissingReactivaLaCaducada(t *testing.T) {
	_, ctx := testPool(t)
	repo := NewRepository(&db.ContextPool{})
	tenant := uuid.New()
	now := time.Now()
	past, future := now.Add(-time.Hour), now.Add(time.Hour)

	for _, e := range []*domain.Entry{
		{TenantID: tenant, Email: "caducada@example.com", Reason: domain.ReasonManual, Source: "api", ExpiresAt: &past},
		{TenantID: tenant, Email: "vigente@example.com", Reason: domain.ReasonManual, Source: "api", ExpiresAt: &future},
		{TenantID: tenant, Email: "baja@example.com", Reason: domain.ReasonUnsubscribe, Source: "transactional"},
	} {
		if err := repo.Insert(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	added, err := repo.InsertMissing(ctx, tenant,
		[]string{"caducada@example.com", "vigente@example.com", "baja@example.com", "nueva@example.com", "nueva@example.com"},
		domain.ReasonManual, "import", "lote", now)
	if err != nil {
		t.Fatal(err)
	}
	byEmail := map[string]domain.Entry{}
	for _, e := range added {
		byEmail[e.Email] = e
	}
	if len(added) != 3 || byEmail["caducada@example.com"].ExpiresAt != nil || byEmail["caducada@example.com"].Source != "import" {
		t.Fatalf("entran la nueva, la baja (causa aparte) y la caducada reactivada: %+v", added)
	}
	if _, ok := byEmail["vigente@example.com"]; ok {
		t.Fatal("una manual vigente no se toca")
	}
	if left, _ := repo.FindByEmails(ctx, tenant, []string{"baja@example.com"}); len(left) != 2 {
		t.Fatalf("la baja conserva su fila y suma la manual: %+v", left)
	}
}

func TestRepositorioListaYCuentaPorCausaPrincipal(t *testing.T) {
	_, ctx := testPool(t)
	repo := NewRepository(&db.ContextPool{})
	tenant := uuid.New()
	now := time.Now()
	past := now.Add(-time.Hour)

	seed := []*domain.Entry{
		{TenantID: tenant, Email: "ana@example.com", Reason: domain.ReasonManual},
		{TenantID: tenant, Email: "ana@example.com", Reason: domain.ReasonUnsubscribe},
		{TenantID: tenant, Email: "eva@example.com", Reason: domain.ReasonManual},
		{TenantID: tenant, Email: "luis@example.com", Reason: domain.ReasonManual, ExpiresAt: &past},
		{TenantID: tenant, Email: "luis@example.com", Reason: domain.ReasonInvalid},
		{TenantID: tenant, Email: "caducada@example.com", Reason: domain.ReasonManual, ExpiresAt: &past},
		{TenantID: tenant, Email: "otra_cosa@example.com", Reason: domain.ReasonComplaint},
	}
	for _, e := range seed {
		if err := repo.Insert(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	emails, total, err := repo.ListAddresses(ctx, tenant, ports.ListFilter{Page: 1, PerPage: 10}, now)
	if err != nil || total != 5 || len(emails) != 5 {
		t.Fatalf("una fila por direccion: total=%d emails=%v err=%v", total, emails, err)
	}
	// La principal de ana es la baja; la de luis, invalid (vigente) antes que la manual
	// caducada; la direccion solo caducada sigue listada con su manual.
	for reason, want := range map[domain.Reason][]string{
		domain.ReasonUnsubscribe: {"ana@example.com"},
		domain.ReasonManual:      {"caducada@example.com", "eva@example.com"},
		domain.ReasonInvalid:     {"luis@example.com"},
	} {
		got, n, err := repo.ListAddresses(ctx, tenant, ports.ListFilter{Reason: reason, Page: 1, PerPage: 10}, now)
		sort.Strings(got)
		if err != nil || n != int64(len(want)) || len(got) != len(want) || (len(got) > 0 && got[0] != want[0]) {
			t.Errorf("%s: %v (total %d) err=%v, se esperaba %v", reason, got, n, err, want)
		}
	}
	page, total, err := repo.ListAddresses(ctx, tenant, ports.ListFilter{Search: "EXAMPLE", Page: 2, PerPage: 2}, now)
	if err != nil || total != 5 || len(page) != 2 {
		t.Fatalf("paginado: total=%d page=%v err=%v", total, page, err)
	}
	if got, n, _ := repo.ListAddresses(ctx, tenant, ports.ListFilter{Search: "%", Page: 1, PerPage: 10}, now); n != 0 || len(got) != 0 {
		t.Fatalf("el comodin de LIKE debe escaparse: %v", got)
	}
	if got, n, _ := repo.ListAddresses(ctx, tenant, ports.ListFilter{Search: "_cosa", Page: 1, PerPage: 10}, now); n != 1 || got[0] != "otra_cosa@example.com" {
		t.Fatalf("el guion bajo se busca literal: %v", got)
	}

	counts, err := repo.CountByReason(ctx, tenant, now)
	if err != nil {
		t.Fatal(err)
	}
	byReason := map[domain.Reason]int64{}
	for _, c := range counts {
		byReason[c.Reason] = c.Count
	}
	want := map[domain.Reason]int64{domain.ReasonUnsubscribe: 1, domain.ReasonManual: 1, domain.ReasonInvalid: 1, domain.ReasonComplaint: 1}
	if len(byReason) != len(want) {
		t.Fatalf("CountByReason: %+v", counts)
	}
	for r, n := range want {
		if byReason[r] != n {
			t.Fatalf("CountByReason: %+v", counts)
		}
	}
}

func TestRepositorioBloqueaLaDireccionEnTransaccion(t *testing.T) {
	pool, ctx := testPool(t)
	ctxPool := &db.ContextPool{}
	repo := NewRepository(ctxPool)
	tenant := uuid.New()
	if err := repo.Insert(ctx, &domain.Entry{TenantID: tenant, Email: "lock@example.com", Reason: domain.ReasonManual}); err != nil {
		t.Fatal(err)
	}

	locked := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- ctxPool.Transact(ctx, func(txCtx context.Context) error {
			if err := repo.LockAddress(txCtx, tenant, "lock@example.com"); err != nil {
				return err
			}
			close(locked)
			<-release
			return repo.Insert(txCtx, &domain.Entry{TenantID: tenant, Email: "lock@example.com", Reason: domain.ReasonComplaint})
		})
	}()
	<-locked

	// Mientras la primera transaccion tiene la direccion, otra no puede tomarla.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`,
		addressLockPrefix+tenant.String()+":lock@example.com").Scan(&got); err != nil || got {
		t.Fatalf("la direccion debe estar bloqueada: got=%v err=%v", got, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if left, _ := repo.FindByEmails(ctx, tenant, []string{"lock@example.com"}); len(left) != 2 {
		t.Fatalf("la transaccion confirmo: %+v", left)
	}
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`,
		addressLockPrefix+tenant.String()+":lock@example.com").Scan(&got); err != nil || !got {
		t.Fatalf("el bloqueo se libera con la transaccion: got=%v err=%v", got, err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock_all()`); err != nil {
		t.Fatal(err)
	}
}

func TestRepositorioCargas(t *testing.T) {
	_, ctx := testPool(t)
	repo := NewImportRepository(&db.ContextPool{})
	tenant := uuid.New()
	imp := &domain.Import{TenantID: tenant, Total: 5, Added: 3, Skipped: 2, CreatedBy: uuid.New()}
	if err := repo.Create(ctx, imp); err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(ctx, &domain.Import{TenantID: tenant, Total: 5, Added: 3, Skipped: 1, CreatedBy: uuid.New()}); err == nil {
		t.Fatal("el CHECK de conteos debe rechazar added+skipped != total")
	}
	list, total, err := repo.List(ctx, tenant, 1, 10)
	if err != nil || total != 1 || len(list) != 1 || list[0].ID != imp.ID {
		t.Fatalf("List: total=%d n=%d err=%v", total, len(list), err)
	}
}

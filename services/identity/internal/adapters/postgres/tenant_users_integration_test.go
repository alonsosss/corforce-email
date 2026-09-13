//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/google/uuid"
)

// Las cuentas de una empresa entera contra Postgres real, con las migraciones del registro
// aplicadas dos veces (registryDB): dos altas simultaneas del primer usuario de una empresa
// dejan una sola cuenta, retirar las cuentas de una empresa se lleva sus sesiones sin tocar
// las de otra, y un apunte sin IP (una operacion interna) se guarda.
func TestCuentasDeUnaEmpresaEntera(t *testing.T) {
	ctx := context.Background()
	pool := registryDB(t)
	repo := NewUserRepo(pool)
	tenantA, tenantB := uuid.New(), uuid.New()
	t.Cleanup(func() {
		cctx := context.Background()
		tenants := []uuid.UUID{tenantA, tenantB}
		_, _ = pool.Exec(cctx, `DELETE FROM identity.users WHERE tenant_id = ANY($1)`, tenants)
		_, _ = pool.Exec(cctx, `DELETE FROM identity.audit_log WHERE tenant_id = ANY($1)`, tenants)
	})
	user := func(tenant uuid.UUID, email string) *domain.User {
		return &domain.User{ID: uuid.New(), TenantID: tenant, Email: email, PasswordHash: "hash",
			FirstName: "Ana", LastName: "Perez", Status: domain.UserStatusActive}
	}

	candidates := []*domain.User{user(tenantA, "uno@a.test"), user(tenantA, "dos@a.test")}
	errs := make([]error, len(candidates))
	var wg sync.WaitGroup
	for i, u := range candidates {
		wg.Add(1)
		go func(i int, u *domain.User) {
			defer wg.Done()
			errs[i] = repo.CreateFirst(ctx, u)
		}(i, u)
	}
	wg.Wait()
	var winner *domain.User
	for i, err := range errs {
		switch {
		case err == nil:
			winner = candidates[i]
		case !errors.Is(err, domain.ErrFirstUserConflict):
			t.Fatalf("alta %d: %v", i, err)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM identity.users WHERE tenant_id = $1`, tenantA).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if winner == nil || count != 1 {
		t.Fatalf("cuentas de la empresa = %d; dos altas simultaneas del primer usuario dejan una", count)
	}
	if err := repo.CreateFirst(ctx, user(tenantA, "tres@a.test")); !errors.Is(err, domain.ErrFirstUserConflict) {
		t.Fatalf("otro primer usuario = %v; want ErrFirstUserConflict", err)
	}
	if err := repo.CreateFirst(ctx, user(tenantB, "admin@b.test")); err != nil {
		t.Fatalf("primer usuario de otra empresa: %v", err)
	}

	loginAt := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	session := &domain.Session{ID: uuid.New(), UserID: winner.ID, RefreshTokenHash: strings.ReplaceAll(uuid.NewString(), "-", ""),
		IPAddress: "127.0.0.1", UserAgent: "it", ExpiresAt: loginAt.AddDate(100, 0, 0), CreatedAt: loginAt, LoginAt: loginAt}
	if err := NewSessionRepo(pool).Create(ctx, session); err != nil {
		t.Fatal(err)
	}

	removed, err := repo.DeleteByTenant(ctx, tenantA)
	if err != nil || removed != 1 {
		t.Fatalf("retirar = %d, %v; want 1", removed, err)
	}
	var sessions, others int
	if err := pool.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM identity.sessions WHERE user_id = $1),
		        (SELECT count(*) FROM identity.users WHERE tenant_id = $2)`, winner.ID, tenantB).Scan(&sessions, &others); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 || others != 1 {
		t.Fatalf("sesiones %d, cuentas de la otra empresa %d; want 0 y 1", sessions, others)
	}
	if again, err := repo.DeleteByTenant(ctx, tenantA); err != nil || again != 0 {
		t.Fatalf("repetir = %d, %v; want 0", again, err)
	}

	entry := &domain.AuditEntry{ID: uuid.New(), TenantID: tenantA, Action: "remove_tenant_users", Resource: "tenant",
		ResourceID: tenantA.String(), Details: map[string]interface{}{"users": removed}, CreatedAt: loginAt}
	if err := NewAuditRepo(pool).Log(ctx, entry); err != nil {
		t.Fatalf("apunte sin IP: %v", err)
	}
	var ipIsNull bool
	if err := pool.QueryRow(ctx, `SELECT ip_address IS NULL FROM identity.audit_log WHERE id = $1`, entry.ID).Scan(&ipIsNull); err != nil || !ipIsNull {
		t.Fatalf("ip_address nula = %v, %v; want true", ipIsNull, err)
	}
}

// identity decide si puede retirar las cuentas de una empresa por la vista publicada de
// organization, nunca por su tabla.
func TestEstadoDeLaEmpresaParaIdentity(t *testing.T) {
	ctx := context.Background()
	pool := registryDB(t)
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	active, suspended := uuid.New(), uuid.New()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM organization.tenants WHERE id = ANY($1)`, []uuid.UUID{active, suspended})
	})
	if _, err := pool.Exec(ctx,
		`INSERT INTO organization.tenants (id, slug, name, db_name, status, cell_id)
		 VALUES ($1, $3, 'Activa', $4, 'active', $7), ($2, $5, 'Suspendida', $6, 'suspended', $7)`,
		active, suspended, "it-a-"+suffix, "mail_tenant_it_a_"+suffix, "it-s-"+suffix, "mail_tenant_it_s_"+suffix, uuid.New()); err != nil {
		t.Fatal(err)
	}
	tenants := NewTenantRepo(pool)
	for id, want := range map[uuid.UUID]bool{active: true, suspended: false, uuid.New(): false} {
		if got, err := tenants.IsActive(ctx, id); err != nil || got != want {
			t.Errorf("IsActive(%s) = %v, %v; want %v", id, got, err, want)
		}
	}
}

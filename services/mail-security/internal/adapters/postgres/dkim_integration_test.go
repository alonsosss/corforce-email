//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

func esperar(t *testing.T, ch <-chan struct{}, que string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(15 * time.Second):
		t.Fatalf("no ocurrio: %s", que)
	}
}

// Las claves DKIM contra el directorio real de la celda: el cerrojo por dominio entre
// transacciones, la regla al escribir y el repaso sobre las vistas publicadas.
func TestIntegracionDKIM(t *testing.T) {
	dsn := os.Getenv("MAIL_SECURITY_TEST_DSN")
	if dsn == "" {
		t.Skip("MAIL_SECURITY_TEST_DSN no definido")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	applyMigrations(t, ctx, pool, repoRoot(t))
	seedDirectory(t, ctx, pool)

	ctxPool := &db.ContextPool{}
	engineCtx := db.WithPool(ctx, pool)
	directory := NewDirectoryRepository(ctxPool)
	lock := NewDKIMLock(ctxPool)

	t.Run("cerrojo por dominio entre transacciones", func(t *testing.T) {
		held, release, entered := make(chan struct{}), make(chan struct{}), make(chan struct{})
		first, second := make(chan error, 1), make(chan error, 1)
		go func() {
			first <- lock.WithDomainLock(engineCtx, "acme.com", func(context.Context) error {
				close(held)
				<-release
				return nil
			})
		}()
		esperar(t, held, "el primero toma el cerrojo de acme.com")
		go func() {
			second <- lock.WithDomainLock(engineCtx, "acme.com", func(context.Context) error {
				close(entered)
				return nil
			})
		}()
		otro, cancel := context.WithTimeout(engineCtx, 15*time.Second)
		defer cancel()
		if err := lock.WithDomainLock(otro, "otra.com", func(context.Context) error { return nil }); err != nil {
			t.Fatalf("otro dominio no espera: %v", err)
		}
		select {
		case <-entered:
			t.Fatal("el mismo dominio entro con el cerrojo tomado")
		case <-time.After(300 * time.Millisecond):
		}
		close(release)
		esperar(t, entered, "el segundo entra cuando el primero termina su transaccion")
		if err := errors.Join(<-first, <-second); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("regla y repaso sobre el directorio real", func(t *testing.T) {
		store := apptest.NewStore()
		sync := app.NewRedisSync(store, directory, NewPolicyReader(ctxPool), zap.NewNop())
		tenants := &apptest.Tenants{Gone: map[uuid.UUID]bool{}}
		uc := app.NewDKIMUseCase(app.DKIMDeps{Directory: directory, Lock: lock, Sync: sync, Tenants: tenants, Logger: zap.NewNop()})
		keys := []domain.DKIMKey{{Selector: "s1", PrivateKeyPEM: "-----BEGIN RSA PRIVATE KEY-----\neA==\n-----END RSA PRIVATE KEY-----"}}

		if err := uc.PutKeys(engineCtx, tenantA, "acme.com", keys); err != nil {
			t.Fatalf("dominio activo de la empresa: %v", err)
		}
		if err := uc.PutKeys(engineCtx, tenantB, "otra.com", keys); err != nil {
			t.Fatalf("dominio activo de la otra empresa: %v", err)
		}
		for _, c := range []struct {
			tenant uuid.UUID
			name   string
			want   error
		}{
			{tenantB, "baja.com", domain.ErrDKIMDomainNotActive},
			{tenantA, "acme-alias.com", domain.ErrDKIMDomainNotActive},
			{tenantA, "nadie.com", domain.ErrDKIMDomainNotActive},
			{tenantA, "otra.com", domain.ErrObjectNotOwned},
			{tenantA, "baja.com", domain.ErrObjectNotOwned},
		} {
			if err := uc.PutKeys(engineCtx, c.tenant, c.name, keys); !errors.Is(err, c.want) {
				t.Errorf("%s por %s: %v, se esperaba %v", c.name, c.tenant, err, c.want)
			}
		}

		for _, field := range []string{"s1.baja.com", "s1.fantasma.com"} {
			if err := store.HSet(ctx, domain.RedisDKIMPrivKeys, field, "x"); err != nil {
				t.Fatal(err)
			}
		}
		tenants.Gone[tenantB] = true
		rep, err := uc.Reconcile(engineCtx)
		if err != nil || rep != (app.DKIMReconcileReport{Domains: 4, NotServed: 2, TenantGone: 1}) {
			t.Fatalf("repaso: %+v %v", rep, err)
		}
		if names, _ := sync.DKIMDomains(ctx); len(names) != 1 || names[0] != "acme.com" {
			t.Fatalf("solo quedan las de acme.com: %v", names)
		}

		if _, err := pool.Exec(ctx, `UPDATE mail.domains SET active = false WHERE domain = 'acme.com'`); err != nil {
			t.Fatal(err)
		}
		if err := uc.ForgetIfNotServed(engineCtx, "acme.com"); err != nil {
			t.Fatal(err)
		}
		if names, _ := sync.DKIMDomains(ctx); len(names) != 0 {
			t.Fatalf("desactivado en el directorio, sin claves: %v", names)
		}
	})
}

//go:build integration

package postgres

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/db"
	outboxadapter "github.com/alonsosss/corforce-email/services/mail-directory/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/adapters/secrets"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	migrationsOnce sync.Once
	migrationsErr  error
)

// integrationEnv devuelve la variable de entorno que apunta a la infraestructura de la
// prueba. Sin ella la prueba se salta, salvo con INTEGRATION_REQUIRED=1 (make
// test-integration y CI): ahi es un fallo, porque un salto esconderia que no llego.
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

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", ".."))
}

// applyCellMigrations aplica sobre la base DESECHABLE de la prueba las migraciones de
// celda del servicio y de sus vecinos (platform, mail-directory, mail-security) DOS veces:
// tienen que tolerar re-ejecutarse, que es como las aplica el runner.
func applyCellMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	migrationsOnce.Do(func() {
		var files []string
		for _, dir := range []string{"platform", "mail-directory", "mail-security"} {
			found, err := filepath.Glob(filepath.Join(repoRoot(), "migrations/cell/canonical", dir, "*.sql"))
			if err != nil || len(found) == 0 {
				migrationsErr = fmt.Errorf("migraciones de %s: %v", dir, err)
				return
			}
			sort.Strings(found)
			files = append(files, found...)
		}
		for pass := 1; pass <= 2; pass++ {
			for _, f := range files {
				if err := execFile(ctx, pool, f); err != nil {
					migrationsErr = fmt.Errorf("pasada %d: %w", pass, err)
					return
				}
			}
		}
	})
	if migrationsErr != nil {
		t.Fatal(migrationsErr)
	}
}

func execFile(ctx context.Context, pool *pgxpool.Pool, path string) error {
	sql, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, string(sql)); err != nil {
		return fmt.Errorf("aplicar %s: %w", filepath.Base(path), err)
	}
	return nil
}

// newUseCase cablea el caso de uso con los repositorios reales y la outbox, como main.go.
func newUseCase(ctxPool *db.ContextPool) *app.UseCase {
	return app.New(app.Deps{
		Tx: NewTransactor(ctxPool), Domains: NewDomainRepo(ctxPool), AliasDomains: NewAliasDomainRepo(ctxPool),
		Mailboxes: NewMailboxRepo(ctxPool), AppPasswords: NewAppPasswordRepo(ctxPool), Sieve: NewSieveRepo(ctxPool),
		Aliases: NewAliasRepo(ctxPool), SpamAliases: NewSpamAliasRepo(ctxPool), SenderACL: NewSenderACLRepo(ctxPool),
		Relayhosts: NewRelayhostRepo(ctxPool), Transports: NewTransportRepo(ctxPool), TLSPolicies: NewTLSPolicyRepo(ctxPool),
		RecipientMap: NewRecipientMapRepo(ctxPool), BCCMaps: NewBCCMapRepo(ctxPool), Retirements: NewRetirementRepo(ctxPool),
		Secrets: secrets.New(), Events: outboxadapter.NewPublisher(ctxPool),
	})
}

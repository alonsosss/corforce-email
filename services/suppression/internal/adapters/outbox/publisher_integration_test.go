//go:build integration

package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/suppression/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
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

// applyMigrationsTwice aplica en orden, dos veces, las migraciones de cada directorio
// (relativo a la raiz del repositorio): la segunda pasada demuestra que toleran
// re-ejecutarse.
func applyMigrationsTwice(ctx context.Context, pool *pgxpool.Pool, dirs ...string) error {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "..")
	var files []string
	for _, dir := range dirs {
		found, err := filepath.Glob(filepath.Join(root, dir, "*.sql"))
		if err != nil || len(found) == 0 {
			return fmt.Errorf("migraciones de %s: %v", dir, err)
		}
		sort.Strings(found)
		files = append(files, found...)
	}
	for pass := 1; pass <= 2; pass++ {
		for _, f := range files {
			sql, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			if _, err := pool.Exec(ctx, string(sql)); err != nil {
				return fmt.Errorf("pasada %d, %s: %w", pass, filepath.Base(f), err)
			}
		}
	}
	return nil
}

// SUPPRESSION_TEST_DSN apunta a una base desechable, a la que la prueba aplica dos veces la
// outbox de plataforma. Comprueba que el evento queda en la outbox de la misma transaccion
// y que se pierde si esta se revierte: es la garantia por la que existe este adaptador.
func TestPublicadorEncolaEnLaTransaccion(t *testing.T) {
	dsn := integrationEnv(t, "SUPPRESSION_TEST_DSN")
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := applyMigrationsTwice(ctx, pool, "migrations/tenant/canonical/platform"); err != nil {
		t.Fatalf("outbox de plataforma: %v", err)
	}

	tenant, user := uuid.New(), uuid.New()
	ctx = middleware.WithIdentity(db.WithPool(ctx, pool), user.String(), tenant.String())
	ctxPool := &db.ContextPool{}
	pub := NewPublisher(ctxPool)
	e := &domain.Entry{TenantID: tenant, Email: "ana@example.com", Reason: domain.ReasonComplaint, Source: "ses"}

	remaining := []domain.Reason{domain.ReasonHardBounce, domain.ReasonManual}
	if err := ctxPool.Transact(ctx, func(ctx context.Context) error { return pub.EntryRemoved(ctx, e, remaining) }); err != nil {
		t.Fatal(err)
	}
	var subject string
	var payload []byte
	if err := pool.QueryRow(ctx, `SELECT subject, payload FROM platform.event_outbox WHERE tenant_id = $1`, tenant).Scan(&subject, &payload); err != nil {
		t.Fatal(err)
	}
	if subject != SubjectEntryRemoved {
		t.Fatalf("subject %q", subject)
	}
	var evt struct {
		Type     string         `json:"type"`
		Source   string         `json:"source"`
		TenantID string         `json:"tenant_id"`
		UserID   string         `json:"user_id"`
		Data     map[string]any `json:"data"`
	}
	if err := json.Unmarshal(payload, &evt); err != nil {
		t.Fatal(err)
	}
	if evt.Type != SubjectEntryRemoved || evt.Source != source || evt.TenantID != tenant.String() || evt.UserID != user.String() {
		t.Fatalf("envelope: %+v", evt)
	}
	if evt.Data["email"] != "ana@example.com" || evt.Data["reason"] != "complaint" || evt.Data["source"] != "ses" || evt.Data["tenant_id"] != tenant.String() {
		t.Fatalf("payload: %+v", evt.Data)
	}
	if got, _ := json.Marshal(evt.Data["reasons"]); string(got) != `["hard_bounce","manual"]` {
		t.Fatalf("reasons lleva las causas que quedan: %s", got)
	}

	// Una transaccion revertida no deja evento; una direccion que queda libre publica
	// reasons vacio, no null.
	otro := uuid.New()
	e.TenantID = otro
	_ = ctxPool.Transact(ctx, func(ctx context.Context) error {
		if err := pub.EntryAdded(ctx, e, nil); err != nil {
			return err
		}
		return context.Canceled
	})
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.event_outbox WHERE tenant_id = $1`, otro).Scan(&n); err != nil || n != 0 {
		t.Fatalf("la outbox no debe conservar un evento de una transaccion revertida: n=%d err=%v", n, err)
	}
}

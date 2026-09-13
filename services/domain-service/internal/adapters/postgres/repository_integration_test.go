//go:build integration

package postgres

// Prueba de integracion contra un Postgres real. Se ejecuta con:
//
//	DOMAIN_SERVICE_TEST_DSN=postgres://user:pass@localhost:5432/db?sslmode=disable \
//	  go test -tags integration ./services/domain-service/internal/adapters/postgres/
//
// Aplica la migracion canonica dos veces (idempotencia) y ejercita todas las consultas
// del repositorio sobre una base desechable.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
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

func setup(t *testing.T) (context.Context, *Repository) {
	t.Helper()
	dsn := integrationEnv(t, "DOMAIN_SERVICE_TEST_DSN")
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("conectar: %v", err)
	}
	t.Cleanup(pool.Close)

	sqlBytes, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "migrations", "tenant", "canonical", "domain-service", "01_domains.sql"))
	if err != nil {
		t.Fatalf("leer migracion: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := pool.Exec(ctx, string(sqlBytes)); err != nil {
			t.Fatalf("aplicar migracion (pasada %d): %v", i+1, err)
		}
	}
	return db.WithPool(ctx, pool), NewRepository(&db.ContextPool{})
}

func sample(tenantID uuid.UUID, name string) *domain.Domain {
	return &domain.Domain{
		ID: uuid.New(), TenantID: tenantID, Domain: name,
		Purpose: domain.PurposeCorporate, Status: domain.StatusPending,
		VerificationToken: "0123456789abcdef0123456789abcdef",
		DKIMSelector:      "cfm202609", DKIMPrivateKeyEnc: []byte{1, 2, 3}, DKIMPublicKey: "PUB",
		DKIMKeyBits: 2048, DMARCPolicy: domain.DMARCQuarantine,
	}
}

func TestRepositoryRoundTrip(t *testing.T) {
	ctx, repo := setup(t)
	tenantID := uuid.New()
	d := sample(tenantID, "acme-"+uuid.NewString()[:8]+".test")

	if err := repo.Create(ctx, d); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if d.CreatedAt.IsZero() {
		t.Error("Create debe devolver los sellos de tiempo")
	}
	if err := repo.Create(ctx, sample(tenantID, d.Domain)); !errors.Is(err, domain.ErrDomainAlreadyExists) {
		t.Errorf("duplicado: %v", err)
	}
	if err := repo.Create(ctx, sample(uuid.New(), d.Domain)); err != nil {
		t.Errorf("el mismo nombre en otra empresa es valido: %v", err)
	}

	got, err := repo.GetByName(ctx, tenantID, d.Domain)
	if err != nil || got.ID != d.ID {
		t.Fatalf("GetByName: %v", err)
	}
	if _, err := repo.GetByID(ctx, uuid.New(), d.ID); !errors.Is(err, domain.ErrDomainNotFound) {
		t.Errorf("otra empresa no ve la fila: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	got.Status = domain.StatusVerified
	got.VerifiedAt, got.LastCheckedAt = &now, &now
	got.DKIMPreviousSelector, got.DKIMPreviousPrivateKeyEnc, got.DKIMPreviousPublicKey, got.DKIMRotatedAt = "cfm202608", []byte{9}, "OLD", &now
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	again, _ := repo.GetByID(ctx, tenantID, d.ID)
	if again.Status != domain.StatusVerified || !again.HasPreviousDKIM() || again.DKIMPreviousPublicKey != "OLD" || !again.UpdatedAt.After(again.CreatedAt) {
		t.Errorf("Update no persistio: %+v", again)
	}

	checks := []domain.DNSCheck{
		{TenantID: tenantID, DomainID: d.ID, CheckedAt: now.Add(-time.Hour), Record: domain.RecordSPF, Expected: "a", Observed: "", OK: false, Detail: "viejo"},
		{TenantID: tenantID, DomainID: d.ID, CheckedAt: now, Record: domain.RecordSPF, Expected: "a", Observed: "a", OK: true},
		{TenantID: tenantID, DomainID: d.ID, CheckedAt: now, Record: domain.RecordDKIMPrevious, Expected: "b", OK: true},
	}
	if err := repo.SaveChecks(ctx, checks); err != nil {
		t.Fatalf("SaveChecks: %v", err)
	}
	latest, err := repo.LatestChecks(ctx, tenantID, d.ID)
	if err != nil || len(latest) != 2 {
		t.Fatalf("LatestChecks = %d, %v", len(latest), err)
	}
	for _, c := range latest {
		if c.Record == domain.RecordSPF && !c.OK {
			t.Error("LatestChecks debe devolver la comprobacion mas reciente de cada registro")
		}
	}

	recheck, err := repo.ListForRecheck(ctx, tenantID, now.Add(-7*24*time.Hour))
	if err != nil || len(recheck) != 1 {
		t.Errorf("ListForRecheck = %d, %v", len(recheck), err)
	}
	expired, err := repo.ListWithExpiredPreviousDKIM(ctx, tenantID, now.Add(time.Minute))
	if err != nil || len(expired) != 1 {
		t.Errorf("ListWithExpiredPreviousDKIM = %d, %v", len(expired), err)
	}
	if expired, _ := repo.ListWithExpiredPreviousDKIM(ctx, tenantID, now.Add(-time.Minute)); len(expired) != 0 {
		t.Error("una rotacion reciente no esta vencida")
	}

	list, total, err := repo.List(ctx, tenantID, 0, 10)
	if err != nil || total != 1 || len(list) != 1 {
		t.Errorf("List = %d/%d, %v", len(list), total, err)
	}

	pruned, err := repo.PruneChecks(ctx, tenantID, now.Add(-time.Minute))
	if err != nil || pruned != 1 {
		t.Errorf("PruneChecks = %d, %v", pruned, err)
	}

	if err := repo.Delete(ctx, tenantID, d.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := repo.Delete(ctx, tenantID, d.ID); !errors.Is(err, domain.ErrDomainNotFound) {
		t.Errorf("segundo Delete: %v", err)
	}
	if left, _ := repo.LatestChecks(ctx, tenantID, d.ID); len(left) != 0 {
		t.Error("las comprobaciones caen con el dominio (ON DELETE CASCADE)")
	}
}

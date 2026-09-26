//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/google/uuid"
)

// La exigencia del segundo factor se guarda y se lee con el resto de la politica, y una empresa
// que nunca la configuro no la exige.
func TestLaPoliticaGuardaLaExigenciaDelSegundoFactor(t *testing.T) {
	pool := registryDB(t)
	ctx := context.Background()
	tenant := uuid.New()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM identity.session_policies WHERE tenant_id = $1`, tenant)
	})
	repo := NewSessionPolicyRepo(pool)

	if p, err := repo.Get(ctx, tenant); err != nil || p.RequireMFA {
		t.Fatalf("sin configurar: %+v %v", p, err)
	}
	policy := domain.DefaultSessionPolicy(tenant)
	policy.RequireMFA = true
	if err := repo.Upsert(ctx, policy); err != nil {
		t.Fatal(err)
	}
	if p, err := repo.Get(ctx, tenant); err != nil || !p.RequireMFA || p.RefreshTTLHours != policy.RefreshTTLHours {
		t.Fatalf("guardada: %+v %v", p, err)
	}
	policy.RequireMFA = false
	if err := repo.Upsert(ctx, policy); err != nil {
		t.Fatal(err)
	}
	if p, err := repo.Get(ctx, tenant); err != nil || p.RequireMFA {
		t.Fatalf("desactivada: %+v %v", p, err)
	}
}

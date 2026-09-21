//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/google/uuid"
)

// RequestedSince mira los enlaces de reinicio de UN usuario (usados o no) creados desde un instante:
// es lo que frena que una solicitud publica llene de enlaces el buzon de una cuenta.
func TestUnEnlaceDeReinicioRecienteSeReconoce(t *testing.T) {
	ctx := context.Background()
	pool := registryDB(t)
	users, resets := NewUserRepo(pool), NewPasswordResetRepo(pool)
	tenant := uuid.New()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM identity.users WHERE tenant_id = $1`, tenant)
	})
	user := func(email string) *domain.User {
		return &domain.User{ID: uuid.New(), TenantID: tenant, Email: email, PasswordHash: "hash",
			FirstName: "Ana", LastName: "Perez", Status: domain.UserStatusActive}
	}
	ana, eva := user("ana@reset.test"), user("eva@reset.test")
	for _, u := range []*domain.User{ana, eva} {
		if err := users.Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	add := func(u *domain.User, createdAt time.Time) uuid.UUID {
		tok := &domain.PasswordResetToken{ID: uuid.New(), UserID: u.ID, TenantID: tenant, TokenHash: uuid.NewString() + uuid.NewString()[:28],
			ExpiresAt: createdAt.Add(30 * time.Minute), CreatedAt: createdAt}
		if err := resets.Create(ctx, tok); err != nil {
			t.Fatal(err)
		}
		return tok.ID
	}
	asked := func(u *domain.User, since time.Time) bool {
		got, err := resets.RequestedSince(ctx, u.ID, since)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	if asked(ana, now.Add(-time.Hour)) {
		t.Fatal("un usuario sin enlaces no tiene ninguno reciente")
	}
	old := add(ana, now.Add(-10*time.Minute))
	if asked(ana, now.Add(-2*time.Minute)) {
		t.Fatal("un enlace de hace diez minutos no es reciente para dos")
	}
	if !asked(ana, now.Add(-time.Hour)) {
		t.Fatal("el enlace de hace diez minutos si entra en la ultima hora")
	}
	if !asked(ana, now.Add(-10*time.Minute)) {
		t.Fatal("el limite es inclusivo")
	}
	fresh := add(ana, now.Add(-30*time.Second))
	if !asked(ana, now.Add(-2*time.Minute)) {
		t.Fatal("un enlace de hace treinta segundos es reciente")
	}
	if asked(eva, now.Add(-time.Hour)) {
		t.Fatal("los enlaces de otro usuario no cuentan")
	}

	// Usarlo o anularlo no borra la solicitud: el freno cuenta las peticiones, no los enlaces vigentes.
	if err := resets.InvalidateForUser(ctx, ana.ID); err != nil {
		t.Fatal(err)
	}
	if err := resets.MarkUsed(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	_ = old
	if !asked(ana, now.Add(-2*time.Minute)) {
		t.Fatal("un enlace ya usado sigue contando como solicitud reciente")
	}
}

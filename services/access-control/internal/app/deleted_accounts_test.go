package app

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
)

type fakeDeletedRoles struct {
	remaining map[uuid.UUID]int64
	err       error
	calls     [][2]uuid.UUID
}

func (f *fakeDeletedRoles) RemoveRolesOfDeletedUser(_ context.Context, tenantID, userID uuid.UUID) (int64, error) {
	f.calls = append(f.calls, [2]uuid.UUID{tenantID, userID})
	if f.err != nil {
		return 0, f.err
	}
	n := f.remaining[userID]
	delete(f.remaining, userID)
	return n, nil
}

type fakePolicyCache struct{ invalidated []uuid.UUID }

func (f *fakePolicyCache) InvalidateUsers(_ context.Context, ids []uuid.UUID) {
	f.invalidated = append(f.invalidated, ids...)
}

func TestLaBajaDeUnaCuentaRetiraSusRolesYSuPolitica(t *testing.T) {
	tenant, user := uuid.New(), uuid.New()
	roles := &fakeDeletedRoles{remaining: map[uuid.UUID]int64{user: 2}}
	cache := &fakePolicyCache{}
	uc := NewDeletedAccountsUseCase(roles, cache)

	removed, err := uc.ForgetDeletedUser(context.Background(), tenant, user)
	if err != nil || removed != 2 {
		t.Fatalf("primera entrega: %d, %v; se esperaban 2 asignaciones", removed, err)
	}
	if len(roles.calls) != 1 || roles.calls[0] != [2]uuid.UUID{tenant, user} {
		t.Fatalf("llamadas %v", roles.calls)
	}
	// La reentrega, o la entrega despues de que la baja de la empresa retirara los roles, no
	// borra nada y vuelve a descartar la politica.
	removed, err = uc.ForgetDeletedUser(context.Background(), tenant, user)
	if err != nil || removed != 0 {
		t.Fatalf("reentrega: %d, %v; se esperaba 0", removed, err)
	}
	if !slices.Equal(cache.invalidated, []uuid.UUID{user, user}) {
		t.Fatalf("politicas descartadas %v", cache.invalidated)
	}
}

func TestSinRedisLaBajaSeAplicaIgual(t *testing.T) {
	user := uuid.New()
	uc := NewDeletedAccountsUseCase(&fakeDeletedRoles{remaining: map[uuid.UUID]int64{user: 1}}, nil)
	if removed, err := uc.ForgetDeletedUser(context.Background(), uuid.New(), user); err != nil || removed != 1 {
		t.Fatalf("%d, %v", removed, err)
	}
}

// Un fallo de la base no descarta la cache: el evento se reentrega y lo hara entonces.
func TestUnFalloDeLaBaseSeDevuelve(t *testing.T) {
	roles := &fakeDeletedRoles{err: errors.New("registro no disponible")}
	cache := &fakePolicyCache{}
	_, err := NewDeletedAccountsUseCase(roles, cache).ForgetDeletedUser(context.Background(), uuid.New(), uuid.New())
	if !errors.Is(err, roles.err) {
		t.Fatalf("err = %v", err)
	}
	if len(cache.invalidated) != 0 {
		t.Fatalf("politicas descartadas %v", cache.invalidated)
	}
}

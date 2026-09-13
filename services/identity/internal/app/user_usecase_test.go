package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/alonsosss/corforce-email/services/identity/internal/ports"
	"github.com/google/uuid"
)

// txMarker marca el contexto que abre markingTx: quien escribe con el, escribe en la transaccion.
type txMarker struct{}

type markingTx struct{ calls int }

func (m *markingTx) Transact(ctx context.Context, fn func(context.Context) error) error {
	m.calls++
	return fn(context.WithValue(ctx, txMarker{}, true))
}

type deletableUsers struct {
	ports.UserRepository
	users       map[uuid.UUID]*domain.User
	deleteErr   error
	deletedInTx []bool
}

func (s *deletableUsers) GetByID(_ context.Context, id uuid.UUID) (*domain.User, error) {
	if u, ok := s.users[id]; ok {
		cp := *u
		return &cp, nil
	}
	return nil, domain.ErrUserNotFound
}

func (s *deletableUsers) Delete(ctx context.Context, id uuid.UUID) error {
	s.deletedInTx = append(s.deletedInTx, ctx.Value(txMarker{}) != nil)
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.users, id)
	return nil
}

type recordedDeletions struct {
	got  []domain.UserDeletion
	inTx []bool
	err  error
}

func (r *recordedDeletions) UserDeleted(ctx context.Context, d domain.UserDeletion) error {
	r.inTx = append(r.inTx, ctx.Value(txMarker{}) != nil)
	if r.err != nil {
		return r.err
	}
	r.got = append(r.got, d)
	return nil
}

type deleteFixture struct {
	uc     *UserUseCase
	users  *deletableUsers
	tx     *markingTx
	events *recordedDeletions
	user   *domain.User
}

func newDeleteFixture(now time.Time) *deleteFixture {
	user := &domain.User{ID: uuid.New(), TenantID: uuid.New(), Email: "ana@example.test", Status: domain.UserStatusActive}
	f := &deleteFixture{
		users:  &deletableUsers{users: map[uuid.UUID]*domain.User{user.ID: user}},
		tx:     &markingTx{},
		events: &recordedDeletions{},
		user:   user,
	}
	f.uc = NewUserUseCase(UserDeps{Users: f.users, Tx: f.tx, AccountEvents: f.events, Now: func() time.Time { return now }})
	return f
}

func TestBorrarUnaCuentaAnunciaLaBajaEnLaMismaTransaccion(t *testing.T) {
	now := time.Date(2026, 3, 1, 5, 0, 0, 0, time.FixedZone("PET", -5*3600))
	f := newDeleteFixture(now)
	actor := uuid.New()
	if err := f.uc.Delete(context.Background(), f.user.ID, actor); err != nil {
		t.Fatal(err)
	}
	want := domain.UserDeletion{UserID: f.user.ID, TenantID: f.user.TenantID, ActorID: actor, DeletedAt: now.UTC()}
	if len(f.events.got) != 1 || f.events.got[0] != want {
		t.Fatalf("baja anunciada %+v, se esperaba %+v", f.events.got, want)
	}
	if f.events.got[0].DeletedAt.Location() != time.UTC {
		t.Fatalf("deleted_at en %v, se esperaba UTC", f.events.got[0].DeletedAt.Location())
	}
	if f.tx.calls != 1 || len(f.users.deletedInTx) != 1 || !f.users.deletedInTx[0] || !f.events.inTx[0] {
		t.Fatalf("borrado en tx=%v, evento en tx=%v, transacciones=%d; los dos van en la misma", f.users.deletedInTx, f.events.inTx, f.tx.calls)
	}
}

// Si el evento no se puede encolar, la transaccion devuelve el error y el Transactor la
// revierte: no queda una cuenta borrada sin su anuncio.
func TestSinEventoNoHayBaja(t *testing.T) {
	f := newDeleteFixture(time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC))
	f.events.err = errors.New("outbox no disponible")
	err := f.uc.Delete(context.Background(), f.user.ID, uuid.New())
	if !errors.Is(err, f.events.err) {
		t.Fatalf("err = %v, se esperaba el fallo de la outbox", err)
	}
	if f.tx.calls != 1 {
		t.Fatalf("transacciones = %d, se esperaba una que revertir", f.tx.calls)
	}
}

func TestBorrarUnaCuentaInexistenteNoAnunciaNada(t *testing.T) {
	f := newDeleteFixture(time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC))
	if err := f.uc.Delete(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, domain.ErrUserNotFound) {
		t.Fatalf("err = %v, se esperaba ErrUserNotFound", err)
	}
	if f.tx.calls != 0 || len(f.events.inTx) != 0 {
		t.Fatalf("transacciones=%d eventos=%d, se esperaba ninguno", f.tx.calls, len(f.events.inTx))
	}
}

// Dos bajas simultaneas de la misma cuenta: la que no borra ninguna fila no la anuncia.
func TestUnaBajaSimultaneaNoSeAnunciaDosVeces(t *testing.T) {
	f := newDeleteFixture(time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC))
	f.users.deleteErr = domain.ErrUserNotFound
	if err := f.uc.Delete(context.Background(), f.user.ID, uuid.New()); !errors.Is(err, domain.ErrUserNotFound) {
		t.Fatalf("err = %v, se esperaba ErrUserNotFound", err)
	}
	if len(f.events.inTx) != 0 {
		t.Fatalf("se anuncio una baja que no borro nada: %+v", f.events.got)
	}
}

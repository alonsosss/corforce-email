package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/google/uuid"
)

type breachStub struct {
	found bool
	err   error
}

func (b breachStub) IsBreached(context.Context, string) (bool, error) { return b.found, b.err }
func (b breachStub) Enabled() bool                                    { return true }

func TestLaFormaSeComprebaAntesQueLasFiltraciones(t *testing.T) {
	policy := domain.DefaultPasswordPolicy(uuid.New())
	// Corta y ademas filtrada: debe salir el error de forma, que es lo que el usuario
	// puede arreglar sin consultar nada.
	err := checkNewPassword(context.Background(), "abc", policy, breachStub{found: true}, nil)
	if !errors.Is(err, domain.ErrPasswordPolicyFail) {
		t.Fatalf("se esperaba ErrPasswordPolicyFail, llego %v", err)
	}
}

func TestFiltradaSeRechazaAunqueCumplaLaForma(t *testing.T) {
	policy := domain.DefaultPasswordPolicy(uuid.New())
	err := checkNewPassword(context.Background(), "Password1!", policy, breachStub{found: true}, nil)
	if !errors.Is(err, domain.ErrPasswordBreached) {
		t.Fatalf("se esperaba ErrPasswordBreached, llego %v", err)
	}
}

func TestServicioDeFiltracionesCaidoNoBloquea(t *testing.T) {
	policy := domain.DefaultPasswordPolicy(uuid.New())
	err := checkNewPassword(context.Background(), "Zx9!qWe4rTy7", policy, breachStub{err: errors.New("timeout")}, nil)
	if err != nil {
		t.Fatalf("con el servicio caido se admite la contrasena; llego %v", err)
	}
}

func TestSinComprobadorSoloRigeLaForma(t *testing.T) {
	policy := domain.DefaultPasswordPolicy(uuid.New())
	if err := checkNewPassword(context.Background(), "Zx9!qWe4rTy7", policy, nil, nil); err != nil {
		t.Fatalf("sin comprobador y con forma correcta no hay error; llego %v", err)
	}
}

package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/totp"
	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/google/uuid"
)

// policyStore es la politica de sesion en memoria; err simula la base caida.
type policyStore struct {
	policy *domain.SessionPolicy
	err    error
}

func (p *policyStore) Get(_ context.Context, tenantID uuid.UUID) (*domain.SessionPolicy, error) {
	if p.err != nil {
		return nil, p.err
	}
	if p.policy == nil {
		return domain.DefaultSessionPolicy(tenantID), nil
	}
	return p.policy, nil
}

func (p *policyStore) Upsert(_ context.Context, policy *domain.SessionPolicy) error {
	p.policy = policy
	return nil
}

func withRequiredMFA(f *authFixture) *policyStore {
	store := &policyStore{policy: domain.DefaultSessionPolicy(f.tenant)}
	store.policy.RequireMFA = true
	f.uc.sessionPolicies = store
	return store
}

// Con la politica activa, la contrasena correcta de quien no tiene segundo factor no abre
// sesion: entrega un token de alta, que solo sirve para configurarlo, y la sesion se abre al
// activarlo con el primer codigo. Despues el token ya no sirve.
func TestLaEmpresaExigeSegundoFactorYLaCuentaLoDaDeAlta(t *testing.T) {
	f := newAuthFixture(t)
	withRequiredMFA(f)
	u := f.account(domain.UserStatusActive, nil)

	res, err := f.login(u, authPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !res.MFAEnrollmentRequired || res.MFAToken == "" || res.AccessToken != "" || f.sessions.created != 0 {
		t.Fatalf("login sin segundo factor: %+v sesiones=%d", res, f.sessions.created)
	}
	if _, err := f.uc.VerifyMFAChallenge(context.Background(), res.MFAToken, "123456", "203.0.113.7", "prueba"); !errors.Is(err, domain.ErrInvalidMFACode) {
		t.Fatalf("el token de alta sirvio como reto: %v", err)
	}

	secret, uri, err := f.uc.SetupMFAEnrollment(context.Background(), res.MFAToken, "Core Force Mail")
	if err != nil || secret == "" || uri == "" {
		t.Fatalf("alta: %q %q %v", secret, uri, err)
	}
	if _, err := f.uc.CompleteMFAEnrollment(context.Background(), res.MFAToken, secret, "000000", "203.0.113.7", "prueba"); !errors.Is(err, domain.ErrInvalidMFACode) {
		t.Fatalf("codigo malo: %v", err)
	}
	code, err := totp.Generate(secret, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	session, err := f.uc.CompleteMFAEnrollment(context.Background(), res.MFAToken, secret, code, "203.0.113.7", "prueba")
	if err != nil || session.AccessToken == "" || session.RefreshToken == "" || !u.MFAEnabled || f.sessions.created != 1 {
		t.Fatalf("activar: %+v %v enabled=%v sesiones=%d", session, err, u.MFAEnabled, f.sessions.created)
	}
	if _, _, err := f.uc.SetupMFAEnrollment(context.Background(), res.MFAToken, "Core Force Mail"); !errors.Is(err, domain.ErrMFAAlreadyEnabled) {
		t.Fatalf("el token de alta volvio a servir: %v", err)
	}

	// Con el segundo factor ya activo el login vuelve al reto de siempre.
	res, err = f.login(u, authPassword)
	if err != nil || !res.MFARequired || res.MFAEnrollmentRequired {
		t.Fatalf("login tras el alta: %+v %v", res, err)
	}
}

// Con la politica activa el segundo factor no se desactiva, ni con contrasena y codigo.
func TestConLaPoliticaActivaNoSeDesactivaElSegundoFactor(t *testing.T) {
	f := newAuthFixture(t)
	u := f.account(domain.UserStatusActive, nil)
	secret, _ := totp.GenerateSecret()
	if err := f.uc.ActivateMFA(context.Background(), u.ID, secret, codeAt(t, secret, f.clock)); err != nil {
		t.Fatal(err)
	}
	withRequiredMFA(f)
	f.clock = f.clock.Add(30 * time.Second)
	if err := f.uc.DisableMFA(context.Background(), u.ID, authPassword, codeAt(t, secret, f.clock)); !errors.Is(err, domain.ErrMFARequiredByPolicy) || !u.MFAEnabled {
		t.Fatalf("desactivar con la politica activa: %v enabled=%v", err, u.MFAEnabled)
	}
}

// Si no se puede leer la politica el login no abre sesion: entrar sin segundo factor es
// justo lo que la politica impide.
func TestSinPoliticaLegibleNoSeEntra(t *testing.T) {
	f := newAuthFixture(t)
	f.uc.sessionPolicies = &policyStore{err: errors.New("base caida")}
	u := f.account(domain.UserStatusActive, nil)
	if res, err := f.login(u, authPassword); err == nil || f.sessions.created != 0 {
		t.Fatalf("login con la politica ilegible: %+v %v sesiones=%d", res, err, f.sessions.created)
	}
}

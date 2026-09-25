package app

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/crypto"
	"github.com/alonsosss/corforce-email/pkg/totp"
	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/alonsosss/corforce-email/services/identity/internal/ports"
	"github.com/google/uuid"
)

// testSealer cifra con una llave de la prueba, como MAIL_ENCRYPTION_KEY en produccion.
func testSealer(t *testing.T) *crypto.KeyRing {
	t.Helper()
	t.Setenv("IDENTITY_TEST_KEY", strings.Repeat("3c", 32))
	kr, err := crypto.LoadKeyRing("IDENTITY_TEST_KEY", "IDENTITY_TEST_KEYS_OLD")
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

// Las sentencias del segundo factor sobre la cuenta en memoria, con la misma condicion que las
// reales: las pruebas de integracion comprueban que el SQL la sigue.
func (s *authUsers) EnableMFA(_ context.Context, id uuid.UUID, sealed []byte, step int64) error {
	u := s.users[id]
	u.MFAEnabled, u.MFASecretSealed, u.MFASecretLegacy, u.MFALastStep = true, sealed, "", step
	return nil
}

func (s *authUsers) DisableMFA(_ context.Context, id uuid.UUID) error {
	u := s.users[id]
	u.MFAEnabled, u.MFASecretSealed, u.MFASecretLegacy = false, nil, ""
	return nil
}

func (s *authUsers) AdvanceMFAStep(_ context.Context, id uuid.UUID, step int64) (bool, error) {
	u := s.users[id]
	if u.MFALastStep >= step {
		return false, nil
	}
	u.MFALastStep = step
	return true, nil
}

func (s *authUsers) ListPlainMFASecrets(_ context.Context, after uuid.UUID, limit int) ([]ports.PlainMFASecret, error) {
	var out []ports.PlainMFASecret
	for _, u := range s.users {
		if u.MFASecretLegacy != "" && strings.Compare(u.ID.String(), after.String()) > 0 {
			out = append(out, ports.PlainMFASecret{UserID: u.ID, Secret: u.MFASecretLegacy})
		}
	}
	slices.SortFunc(out, func(a, b ports.PlainMFASecret) int { return strings.Compare(a.UserID.String(), b.UserID.String()) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *authUsers) SealPlainMFASecret(_ context.Context, id uuid.UUID, plain string, sealed []byte) (bool, error) {
	u := s.users[id]
	if u == nil || u.MFASecretLegacy != plain {
		return false, nil
	}
	u.MFASecretSealed, u.MFASecretLegacy = sealed, ""
	return true, nil
}

func (s *authUsers) DropDisabledMFASecrets(context.Context) (int64, error) {
	var n int64
	for _, u := range s.users {
		if !u.MFAEnabled && (u.MFASecretLegacy != "" || u.MFASecretSealed != nil) {
			u.MFASecretLegacy, u.MFASecretSealed = "", nil
			n++
		}
	}
	return n, nil
}

func codeAt(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	code, err := totp.Generate(secret, at)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// Activar guarda solo el cifrado, atado a la cuenta, y el paso del codigo de activacion: ese
// codigo no vale despues como segundo factor.
func TestActivarGuardaElSecretoCifradoYConsumeElCodigo(t *testing.T) {
	f := newAuthFixture(t)
	u := f.account(domain.UserStatusActive, nil)
	secret, _ := totp.GenerateSecret()
	code := codeAt(t, secret, f.clock)

	if err := f.uc.ActivateMFA(context.Background(), u.ID, secret, "000000"); !errors.Is(err, domain.ErrInvalidMFACode) {
		t.Fatalf("codigo malo: %v", err)
	}
	if err := f.uc.ActivateMFA(context.Background(), u.ID, secret, code); err != nil {
		t.Fatal(err)
	}
	if !u.MFAEnabled || u.MFASecretLegacy != "" || len(u.MFASecretSealed) == 0 || bytes.Contains(u.MFASecretSealed, []byte(secret)) {
		t.Fatalf("fila: enabled=%v legado=%q cifrado=%d bytes", u.MFAEnabled, u.MFASecretLegacy, len(u.MFASecretSealed))
	}
	if plain, err := testSealer(t).DecryptWithAAD(u.MFASecretSealed, domain.MFASecretAAD(u.ID)); err != nil || string(plain) != secret {
		t.Fatalf("abrir con su cuenta: %v", err)
	}
	if _, err := testSealer(t).DecryptWithAAD(u.MFASecretSealed, domain.MFASecretAAD(uuid.New())); err == nil {
		t.Fatal("el secreto de una cuenta se abre como el de otra")
	}
	if u.MFALastStep != f.clock.Unix()/30 {
		t.Fatalf("paso apuntado %d", u.MFALastStep)
	}
	if _, err := f.uc.StepUp(context.Background(), u.ID, authPassword, code); !errors.Is(err, domain.ErrInvalidMFACode) {
		t.Fatalf("el codigo de la activacion volvio a valer: %v", err)
	}
	f.clock = f.clock.Add(30 * time.Second)
	if _, err := f.uc.StepUp(context.Background(), u.ID, authPassword, codeAt(t, secret, f.clock)); err != nil {
		t.Fatalf("codigo del paso siguiente: %v", err)
	}
}

// Un codigo aceptado no se acepta otra vez, en ninguno de los tres caminos; uno de un paso
// anterior tampoco.
func TestUnCodigoDelSegundoFactorNoSeRepite(t *testing.T) {
	f := newAuthFixture(t)
	u := f.account(domain.UserStatusActive, nil)
	secret, _ := totp.GenerateSecret()
	if err := f.uc.ActivateMFA(context.Background(), u.ID, secret, codeAt(t, secret, f.clock.Add(-60*time.Second))); err == nil {
		t.Fatal("un codigo de hace dos ventanas activo el segundo factor")
	}
	if err := f.uc.ActivateMFA(context.Background(), u.ID, secret, codeAt(t, secret, f.clock.Add(-30*time.Second))); err != nil {
		t.Fatal(err)
	}

	code := codeAt(t, secret, f.clock)
	challenge, err := f.tokens.GenerateMFAChallenge(u.ID.String(), f.tenant.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.VerifyMFAChallenge(context.Background(), challenge, code, "203.0.113.7", "prueba"); err != nil {
		t.Fatalf("primer uso: %v", err)
	}
	if _, err := f.uc.VerifyMFAChallenge(context.Background(), challenge, code, "203.0.113.7", "prueba"); !errors.Is(err, domain.ErrInvalidMFACode) {
		t.Fatalf("repetido en el reto: %v", err)
	}
	if _, err := f.uc.StepUp(context.Background(), u.ID, authPassword, code); !errors.Is(err, domain.ErrInvalidMFACode) {
		t.Fatalf("repetido en el step-up: %v", err)
	}
	if err := f.uc.DisableMFA(context.Background(), u.ID, authPassword, code); !errors.Is(err, domain.ErrInvalidMFACode) || !u.MFAEnabled {
		t.Fatalf("repetido al desactivar: %v enabled=%v", err, u.MFAEnabled)
	}
	// El codigo de la ventana anterior sigue dentro del margen pero ya es un paso pasado.
	if _, err := f.uc.StepUp(context.Background(), u.ID, authPassword, codeAt(t, secret, f.clock.Add(-30*time.Second))); !errors.Is(err, domain.ErrInvalidMFACode) {
		t.Fatalf("paso anterior: %v", err)
	}
	f.clock = f.clock.Add(30 * time.Second)
	if err := f.uc.DisableMFA(context.Background(), u.ID, authPassword, codeAt(t, secret, f.clock)); err != nil {
		t.Fatalf("desactivar: %v", err)
	}
	if u.MFAEnabled || u.MFASecretSealed != nil {
		t.Fatalf("desactivado con secreto: enabled=%v", u.MFAEnabled)
	}
}

// Una fila de antes del cifrado sigue entrando con su secreto en claro, y el barrido la cifra
// sin cambiar el codigo que la abre. Es idempotente, y borra el secreto de quien ya no tiene
// segundo factor.
func TestElSecretoEnClaroSeRespetaYElBarridoLoCifra(t *testing.T) {
	f := newAuthFixture(t)
	legado := f.account(domain.UserStatusActive, nil)
	secret, _ := totp.GenerateSecret()
	legado.MFAEnabled, legado.MFASecretLegacy = true, secret
	apagada := f.account(domain.UserStatusActive, nil)
	apagada.MFASecretLegacy = "JBSWY3DPEHPK3PXP"

	if _, err := f.uc.StepUp(context.Background(), legado.ID, authPassword, codeAt(t, secret, f.clock)); err != nil {
		t.Fatalf("secreto en claro: %v", err)
	}

	sweep, err := f.uc.SealLegacyMFASecrets(context.Background(), 1)
	if err != nil || sweep != (MFASecretSweep{Dropped: 1, Sealed: 1}) {
		t.Fatalf("barrido: %+v %v", sweep, err)
	}
	if legado.MFASecretLegacy != "" || len(legado.MFASecretSealed) == 0 || apagada.MFASecretLegacy != "" {
		t.Fatalf("tras el barrido: legado=%q cifrado=%d apagada=%q", legado.MFASecretLegacy, len(legado.MFASecretSealed), apagada.MFASecretLegacy)
	}
	f.clock = f.clock.Add(30 * time.Second)
	if _, err := f.uc.StepUp(context.Background(), legado.ID, authPassword, codeAt(t, secret, f.clock)); err != nil {
		t.Fatalf("tras cifrarlo: %v", err)
	}
	if sweep, err := f.uc.SealLegacyMFASecrets(context.Background(), 1); err != nil || sweep != (MFASecretSweep{}) {
		t.Fatalf("segunda pasada: %+v %v", sweep, err)
	}
	if _, err := f.uc.SealLegacyMFASecrets(context.Background(), 0); err == nil {
		t.Fatal("lote cero aceptado")
	}
}

// Un secreto que ninguna llave abre (una llave retirada antes de tiempo) es un fallo del
// servicio: no se cuenta como intento fallido ni se confunde con un codigo malo.
func TestUnSecretoIlegibleEsUnFalloDelServicio(t *testing.T) {
	f := newAuthFixture(t)
	u := f.account(domain.UserStatusActive, nil)
	u.MFAEnabled, u.MFASecretSealed = true, []byte("no es un cifrado de este anillo")
	challenge, err := f.tokens.GenerateMFAChallenge(u.ID.String(), f.tenant.String())
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.uc.VerifyMFAChallenge(context.Background(), challenge, "123456", "203.0.113.7", "prueba")
	if err == nil || errors.Is(err, domain.ErrInvalidMFACode) {
		t.Fatalf("err=%v, se esperaba un error interno", err)
	}
	if f.users.increments != 0 || f.sessions.created != 0 {
		t.Fatalf("%d intentos y %d sesiones", f.users.increments, f.sessions.created)
	}
}

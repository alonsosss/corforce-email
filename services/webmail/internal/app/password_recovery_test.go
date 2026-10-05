package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

func TestRecuperarLaContrasenaRevocaLasSesiones(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	withMFA(h)
	h.security.validCode, h.security.recoveryCode = "123456", "AAAAA-BBBBB"
	h.clock.Advance(time.Second)

	res, err := h.svc.RecoverPassword(context.Background(), " "+strings.ToUpper(testUser)+" ", " 123456 ", "AAAAA-BBBBB", "nueva-larga-segura", testIP)
	if err != nil {
		t.Fatal(err)
	}
	if res.RecoveryRemaining != 9 || h.security.recovered != "nueva-larga-segura" {
		t.Fatalf("recuperacion: %+v %q", res, h.security.recovered)
	}
	if !sess.RevokedBy(h.store.revoked[testUser]) {
		t.Fatal("las sesiones abiertas antes de recuperar quedan revocadas")
	}
	if len(h.store.sessions) != 0 {
		t.Fatal("recuperar cierra las sesiones del buzon y no abre ninguna")
	}
}

func TestLaRecuperacionRechazadaNoDiceElMotivo(t *testing.T) {
	h := newHarness(t)
	withMFA(h)
	h.security.validCode, h.security.recoveryCode = "123456", "AAAAA-BBBBB"
	ctx := context.Background()

	for _, c := range []struct{ username, totp, recovery string }{
		{"no-es-un-buzon", "123456", "AAAAA-BBBBB"},
		{testUser, "", "AAAAA-BBBBB"},
		{testUser, "123456", ""},
		{testUser, "123456", strings.Repeat("A", domain.MaxMFACodeLen+1)},
		{testUser, "654321", "AAAAA-BBBBB"},
		{testUser, "123456", "ZZZZZ-ZZZZZ"},
	} {
		if _, err := h.svc.RecoverPassword(ctx, c.username, c.totp, c.recovery, "nueva-larga-segura", testIP); !errors.Is(err, domain.ErrPasswordRecoveryRejected) {
			t.Errorf("%+v: %v", c, err)
		}
	}
	if h.security.recovered != "" {
		t.Fatal("un rechazo no cambia la contrasena")
	}
	if got := strings.Count(strings.Join(h.security.calls, ","), "recover"); got != 2 {
		t.Fatalf("lo que no puede ser un buzon o un codigo no llega al directorio: %d llamadas", got)
	}
	if revoked := h.store.revoked[testUser]; !revoked.IsZero() {
		t.Fatal("un rechazo no revoca nada")
	}
}

func TestLaRecuperacionSenalaLaContrasenaNueva(t *testing.T) {
	h := newHarness(t)
	withMFA(h)
	h.security.validCode, h.security.recoveryCode = "123456", "AAAAA-BBBBB"
	ctx := context.Background()

	var verr *domain.ValidationError
	for _, next := range []string{"", strings.Repeat("x", maxPasswordBytes+1), "corta"} {
		if _, err := h.svc.RecoverPassword(ctx, testUser, "123456", "AAAAA-BBBBB", next, testIP); !errors.As(err, &verr) || verr.Field != "new_password" {
			t.Errorf("nueva de %d bytes: %v", len(next), err)
		}
	}
}

func TestLaRecuperacionConElDirectorioCaido(t *testing.T) {
	h := newHarness(t)
	withMFA(h)
	h.security.err = fmt.Errorf("%w: mail-directory caido", domain.ErrUnavailable)
	if _, err := h.svc.RecoverPassword(context.Background(), testUser, "123456", "AAAAA-BBBBB", "nueva-larga-segura", testIP); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("%v", err)
	}
}

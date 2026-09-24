//go:build integration

package postgres

import (
	"testing"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/google/uuid"
)

func TestAsistenteContraPostgres(t *testing.T) {
	f := newWebmailFixture(t)
	ctxPool := &db.ContextPool{}
	uc := app.New(app.Deps{
		Tx: NewTransactor(ctxPool), Retirements: NewRetirementRepo(ctxPool), Locator: NewMailboxLocator(ctxPool),
		Assistant: NewAssistantSettingsRepo(ctxPool),
	})
	t.Cleanup(func() {
		if _, err := f.pool.Exec(f.ctx, `DELETE FROM mail.assistant_settings WHERE tenant_id = ANY($1)`, []uuid.UUID{f.tenantA, f.tenantB}); err != nil {
			t.Errorf("limpiar: %v", err)
		}
	})
	ctxA := middleware.WithIdentity(db.WithPool(f.ctx, f.pool), uuid.New().String(), f.tenantA.String())
	admin := uuid.New()

	s, err := uc.AssistantSettings(ctxA, f.tenantA)
	if err != nil || s.Enabled {
		t.Fatalf("sin fila, apagado: %+v %v", s, err)
	}
	if s, err = uc.SetAssistantSettings(ctxA, f.tenantA, admin, true); err != nil || !s.Enabled || s.EnabledAt == nil || s.UpdatedAt.IsZero() {
		t.Fatalf("activar: %+v %v", s, err)
	}
	accepted := *s.EnabledAt
	if s, err = uc.SetAssistantSettings(ctxA, f.tenantA, admin, true); err != nil || !s.EnabledAt.Equal(accepted) {
		t.Fatalf("repetir no mueve la aceptacion: %+v %v", s, err)
	}
	if got, err := uc.AssistantSettingsByUsername(f.internal, f.ana.Username); err != nil || !got.Enabled || *got.UpdatedBy != admin {
		t.Fatalf("el webmail de A lo ve activado: %+v %v", got, err)
	}
	if got, err := uc.AssistantSettingsByUsername(f.internal, f.luis.Username); err != nil || got.Enabled {
		t.Fatalf("la activacion de A no alcanza a B: %+v %v", got, err)
	}

	// RLS y minimo privilegio: B no ve la fila de A y los motores no leen la tabla.
	if n, err := f.asRole(t, "mail_app", f.tenantB, `SELECT count(*) FROM mail.assistant_settings WHERE tenant_id = $1`, f.tenantA); err != nil || n != 0 {
		t.Fatalf("mail_app de B ve el ajuste de A: %d %v", n, err)
	}
	if n, err := f.asRole(t, "mail_app", f.tenantA, `SELECT count(*) FROM mail.assistant_settings WHERE tenant_id = $1`, f.tenantA); err != nil || n != 1 {
		t.Fatalf("mail_app de A no ve el suyo: %d %v", n, err)
	}
	_, err = f.asRole(t, "mail_engine", uuid.Nil, `SELECT count(*) FROM mail.assistant_settings`)
	expectSQLState(t, err, "42501", "mail_engine lee el ajuste del asistente")

	_, err = f.pool.Exec(f.ctx, `INSERT INTO mail.assistant_settings (tenant_id, enabled) VALUES ($1, true)`, uuid.New())
	expectSQLState(t, err, "23514", "activado sin instante de aceptacion")

	if s, err = uc.SetAssistantSettings(ctxA, f.tenantA, admin, false); err != nil || s.Enabled {
		t.Fatalf("apagar: %+v %v", s, err)
	}
	if got, err := uc.AssistantSettingsByUsername(f.internal, f.ana.Username); err != nil || got.Enabled {
		t.Fatalf("apagado para el webmail: %+v %v", got, err)
	}
}

//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/crypto"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/totp"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func (f *webmailFixture) outboxCount(t *testing.T, tenant uuid.UUID, subject string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM platform.event_outbox WHERE tenant_id = $1 AND subject = $2`, tenant, subject).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestVerificacionEnDosPasosContraPostgres(t *testing.T) {
	f := newWebmailFixture(t)
	secret, _ := totp.GenerateSecret()
	code, _ := totp.Generate(secret, time.Now())

	codes, err := f.uc.ActivateMFAByUsername(f.internal, f.ana.Username, secret, code)
	if err != nil || len(codes) != domain.RecoveryCodeCount {
		t.Fatalf("activar: %v %v", codes, err)
	}
	var enabled bool
	var sealed []byte
	var hashes []string
	if err := f.pool.QueryRow(f.ctx,
		`SELECT m.mfa_enabled, x.secret_enc, x.recovery_hashes FROM mail.mailboxes m JOIN mail.mailbox_mfa x ON x.mailbox_id = m.id WHERE m.id = $1`,
		f.ana.ID).Scan(&enabled, &sealed, &hashes); err != nil {
		t.Fatal(err)
	}
	if !enabled || bytes.Contains(sealed, []byte(secret)) || len(hashes) != domain.RecoveryCodeCount {
		t.Fatalf("fila: enabled=%v hashes=%d", enabled, len(hashes))
	}
	if got, err := testSealer().Open(sealed, f.ana.ID[:]); err != nil || string(got) != secret {
		t.Fatalf("el secreto se abre solo con su buzon: %v", err)
	}
	if _, err := testSealer().Open(sealed, f.luis.ID[:]); err == nil {
		t.Fatal("el secreto de un buzon no se abre como el de otro")
	}
	if _, err := f.uc.ActivateMFAByUsername(f.internal, f.ana.Username, secret, code); !errors.Is(err, domain.ErrMFAAlreadyEnabled) {
		t.Fatalf("activar dos veces: %v", err)
	}
	// El codigo de la activacion no vale otra vez.
	if _, err := f.uc.VerifyMFAByUsername(f.internal, f.ana.Username, code); !errors.Is(err, domain.ErrInvalidMFACode) {
		t.Fatalf("repetido: %v", err)
	}

	// Anti-repeticion y consumo de codigos, atomicos en SQL.
	repo := NewMFARepo(&db.ContextPool{})
	row, err := repo.Get(f.internal, f.tenantA, f.ana.ID)
	if err != nil {
		t.Fatal(err)
	}
	next := row.LastStep + 1
	if ok, err := repo.AdvanceStep(f.internal, f.tenantA, f.ana.ID, next); err != nil || !ok {
		t.Fatalf("paso siguiente: %v %v", ok, err)
	}
	if ok, err := repo.AdvanceStep(f.internal, f.tenantA, f.ana.ID, next); err != nil || ok {
		t.Fatalf("el mismo paso dos veces: %v %v", ok, err)
	}
	if ok, _ := repo.AdvanceStep(f.internal, f.tenantB, f.ana.ID, row.LastStep+10); ok {
		t.Fatal("otra empresa no mueve el paso")
	}
	v, err := f.uc.VerifyMFAByUsername(f.internal, f.ana.Username, codes[0])
	if err != nil || v.Method != domain.MFAMethodRecovery || v.RecoveryRemaining != domain.RecoveryCodeCount-1 {
		t.Fatalf("recuperacion: %+v %v", v, err)
	}
	if _, err := f.uc.VerifyMFAByUsername(f.internal, f.ana.Username, codes[0]); !errors.Is(err, domain.ErrInvalidMFACode) {
		t.Fatalf("gastado: %v", err)
	}
	fresh, err := f.uc.RegenerateRecoveryCodesByUsername(f.internal, f.ana.Username, codes[1])
	if err != nil || len(fresh) != domain.RecoveryCodeCount {
		t.Fatalf("regenerar: %v", err)
	}
	st, err := f.uc.MFAStatusByUsername(f.internal, f.ana.Username)
	if err != nil || !st.Enabled || st.RecoveryRemaining != domain.RecoveryCodeCount {
		t.Fatalf("estado: %+v %v", st, err)
	}

	// Minimo privilegio: los motores leen el booleano pero no la tabla; otra empresa no ve la fila.
	if n, err := f.asRole(t, "mail_engine", uuid.Nil, `SELECT count(*) FROM mail.mailboxes WHERE id = $1 AND mfa_enabled`, f.ana.ID); err != nil || n != 1 {
		t.Fatalf("mail_engine lee mfa_enabled: %d %v", n, err)
	}
	_, err = f.asRole(t, "mail_engine", uuid.Nil, `SELECT count(*) FROM mail.mailbox_mfa`)
	expectSQLState(t, err, "42501", "mail_engine lee los secretos TOTP")
	if n, err := f.asRole(t, "mail_app", f.tenantB, `SELECT count(*) FROM mail.mailbox_mfa WHERE mailbox_id = $1`, f.ana.ID); err != nil || n != 0 {
		t.Fatalf("mail_app de B ve la verificacion de A: %d %v", n, err)
	}
	_, err = f.pool.Exec(f.ctx, `UPDATE mail.mailbox_mfa SET recovery_hashes = array_fill('x'::text, ARRAY[11]) WHERE mailbox_id = $1`, f.ana.ID)
	expectSQLState(t, err, "23514", "mas de diez codigos de recuperacion")

	// El administrador la restablece: fila fuera, booleano apagado y los tres avisos en la outbox.
	actor := uuid.New()
	if err := f.uc.ResetMailboxMFA(f.ctxA, f.tenantA, f.ana.ID, actor); err != nil {
		t.Fatalf("restablecer: %v", err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT mfa_enabled FROM mail.mailboxes WHERE id = $1`, f.ana.ID).Scan(&enabled); err != nil || enabled {
		t.Fatalf("apagado: %v %v", enabled, err)
	}
	if _, err := repo.Get(f.internal, f.tenantA, f.ana.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("fila borrada: %v", err)
	}
	for subject, want := range map[string]int{
		"mail.mailbox.mfa_enabled": 1, "mail.mailbox.mfa_disabled": 1, "mail.mailbox.credentials_changed": 1,
	} {
		if got := f.outboxCount(t, f.tenantA, subject); got != want {
			t.Errorf("%s: %d", subject, got)
		}
	}
	if err := f.uc.ResetMailboxMFA(f.ctxA, f.tenantA, f.ana.ID, actor); !errors.Is(err, domain.ErrMFANotEnabled) {
		t.Fatalf("restablecer sin verificacion: %v", err)
	}

	// Borrar el buzon borra su verificacion.
	code2, _ := totp.Generate(secret, time.Now())
	if _, err := f.uc.ActivateMFAByUsername(f.internal, f.luis.Username, secret, code2); err != nil {
		t.Fatal(err)
	}
	if err := f.uc.DeleteMailbox(f.ctxB, f.tenantB, f.luis.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(f.internal, f.tenantB, f.luis.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("la verificacion del buzon borrado queda: %v", err)
	}
}

func TestPoliticaDeReenvioExternoContraPostgres(t *testing.T) {
	f := newWebmailFixture(t)
	alias := "alias-" + f.suffix + ".example"
	if _, err := f.uc.CreateAliasDomain(f.ctxA, f.tenantA, app.CreateAliasDomainRequest{AliasDomain: alias, TargetDomain: f.domainA}); err != nil {
		t.Fatalf("dominio alias: %v", err)
	}
	owned, err := NewDomainRepo(&db.ContextPool{}).OwnedNames(f.ctxA, f.tenantA, []string{f.domainA, alias, "gmail.example"})
	if err != nil || len(owned) != 2 {
		t.Fatalf("dominios propios y alias: %v %v", owned, err)
	}

	internal := app.PutFiltersRequest{Forwarding: domain.Forwarding{Enabled: true, Addresses: []string{"eva@" + alias}}}
	if _, err := f.uc.PutFiltersByUsername(f.internal, f.ana.Username, internal); err != nil {
		t.Fatalf("un alias de la empresa no es externo: %v", err)
	}
	external := app.PutFiltersRequest{
		Rules: []domain.FilterRule{{Name: "fuera", Enabled: true, Conditions: []domain.FilterCondition{{Field: "from", Op: "contains", Value: "x"}},
			Actions: []domain.FilterAction{{Type: "forward", Address: "fuera@gmail.example"}}}},
		Forwarding: internal.Forwarding,
	}
	_, err = f.uc.PutFiltersByUsername(f.internal, f.ana.Username, external)
	var fe *domain.ForwardingError
	if !errors.As(err, &fe) || !errors.Is(err, domain.ErrReauthRequired) {
		t.Fatalf("sin reautenticar: %v", err)
	}
	external.Reauthenticated = true
	if _, err := f.uc.PutFiltersByUsername(f.internal, f.ana.Username, external); err != nil {
		t.Fatal(err)
	}
	if got := f.outboxCount(t, f.tenantA, "mail.mailbox.forwarding_changed"); got != 2 {
		t.Fatalf("avisos de reenvio: %d", got)
	}

	p, removed, err := f.uc.SetMailPolicy(f.ctxA, f.tenantA, uuid.New(), false)
	if err != nil || p.ExternalForwardingAllowed || removed != 1 {
		t.Fatalf("apagar: %+v %d %v", p, removed, err)
	}
	got, _ := f.uc.FiltersByUsername(f.internal, f.ana.Username)
	if len(got.Rules) != 0 || !got.Forwarding.Enabled || bytes.Contains([]byte(got.ScriptData), []byte("gmail")) {
		t.Fatalf("filtros tras apagar: %+v", got)
	}
	var script string
	if err := f.pool.QueryRow(f.ctx, `SELECT script_data FROM mail.mailbox_filters WHERE username = $1`, f.ana.Username).Scan(&script); err != nil ||
		bytes.Contains([]byte(script), []byte("gmail")) {
		t.Fatalf("el script guardado ya no reenvia fuera: %q %v", script, err)
	}
	if _, err := f.uc.PutFiltersByUsername(f.internal, f.ana.Username, external); !errors.Is(err, domain.ErrExternalForwardingDisabled) {
		t.Fatalf("prohibido: %v", err)
	}
	if n := f.outboxCount(t, f.tenantA, "mail.policy.updated"); n != 1 {
		t.Fatalf("aviso de politica: %d", n)
	}
	if def, err := f.uc.MailPolicy(f.ctxB, f.tenantB); err != nil || !def.ExternalForwardingAllowed {
		t.Fatalf("la politica de A no alcanza a B: %+v %v", def, err)
	}
	if n, err := f.asRole(t, "mail_app", f.tenantB, `SELECT count(*) FROM mail.mail_policy WHERE tenant_id = $1`, f.tenantA); err != nil || n != 0 {
		t.Fatalf("mail_app de B ve la politica de A: %d %v", n, err)
	}
	_, err = f.asRole(t, "mail_engine", uuid.Nil, `SELECT count(*) FROM mail.mail_policy`)
	expectSQLState(t, err, "42501", "mail_engine lee la politica")
}

// servicePool abre la base de la prueba con el rol del servicio (mail_service), el que tiene el
// barrido de mantenimiento de la celda en produccion: ve las filas de todas las empresas.
func servicePool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("MAIL_DIRECTORY_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET ROLE mail_service")
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// La rotacion de MAIL_ENCRYPTION_KEY re-cifra bajo la llave activa los secretos de todas las
// empresas de la celda, atados a su buzon; una segunda pasada no toca nada y la sustitucion
// condicional no pisa lo que otro escribio.
func TestRotacionDelSecretoDeLaVerificacionContraPostgres(t *testing.T) {
	f := newWebmailFixture(t)
	secret, _ := totp.GenerateSecret()
	for _, m := range []*domain.Mailbox{f.ana, f.luis} {
		if _, err := f.uc.ActivateMFAByUsername(f.internal, m.Username, secret, mustCode(t, secret)); err != nil {
			t.Fatalf("activar %s: %v", m.Username, err)
		}
	}
	sealedOf := func(id uuid.UUID) []byte {
		var sealed []byte
		if err := f.pool.QueryRow(f.ctx, `SELECT secret_enc FROM mail.mailbox_mfa WHERE mailbox_id = $1`, id).Scan(&sealed); err != nil {
			t.Fatal(err)
		}
		return sealed
	}
	before := sealedOf(f.ana.ID)

	newKey := strings.Repeat("6b", 32)
	t.Setenv("MAIL_DIRECTORY_IT_ROTATE_KEY", newKey)
	t.Setenv("MAIL_DIRECTORY_IT_ROTATE_KEYS_OLD", strings.Repeat("5a", 32))
	rotating, err := crypto.LoadKeyRing("MAIL_DIRECTORY_IT_ROTATE_KEY", "MAIL_DIRECTORY_IT_ROTATE_KEYS_OLD")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAIL_DIRECTORY_IT_ROTATE_KEYS_OLD", "")
	retired, err := crypto.LoadKeyRing("MAIL_DIRECTORY_IT_ROTATE_KEY", "MAIL_DIRECTORY_IT_ROTATE_KEYS_OLD")
	if err != nil {
		t.Fatal(err)
	}

	store := NewMFASecretStore(servicePool(t, f.ctx))
	rep, err := crypto.RotateStore(f.ctx, rotating, store, uuid.Nil, 1, domain.MFASecretAAD)
	if err != nil || rep.Rotated < 2 || rep.Pending != 0 {
		t.Fatalf("rotacion: %+v %v", rep, err)
	}
	for _, m := range []*domain.Mailbox{f.ana, f.luis} {
		if plain, err := retired.DecryptWithAAD(sealedOf(m.ID), domain.MFASecretAAD(m.ID)); err != nil || string(plain) != secret {
			t.Fatalf("%s sin la llave vieja: %v", m.Username, err)
		}
	}
	if _, err := retired.DecryptWithAAD(sealedOf(f.ana.ID), domain.MFASecretAAD(f.luis.ID)); err == nil {
		t.Fatal("el secreto re-cifrado de un buzon se abre como el de otro")
	}
	again, err := crypto.RotateStore(f.ctx, rotating, store, uuid.Nil, 1, domain.MFASecretAAD)
	if err != nil || again.Rotated != 0 || again.Pending != 0 {
		t.Fatalf("segunda pasada: %+v %v", again, err)
	}
	rotated := sealedOf(f.ana.ID)
	if ok, err := store.ReplaceSealed(f.ctx, f.ana.ID, before, []byte("pisado")); err != nil || ok {
		t.Fatalf("sustituir un cifrado que ya cambio: %v %v", ok, err)
	}
	if !bytes.Equal(sealedOf(f.ana.ID), rotated) {
		t.Fatal("la sustitucion condicional piso la fila")
	}
}

func mustCode(t *testing.T, secret string) string {
	t.Helper()
	code, err := totp.Generate(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return code
}

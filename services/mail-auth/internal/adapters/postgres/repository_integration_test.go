//go:build integration

package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-auth/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// Prueba las consultas contra un Postgres real con el esquema mail de mail-directory ya
// migrado (MAIL_AUTH_TEST_DSN). Lo que importa comprobar no cabe en un mock: que los
// nombres de columna existen, que inet acepta lo que se le pasa y que RecentLogins ve
// solo lo de su empresa.
func TestRepositorioContraEsquemaReal(t *testing.T) {
	dsn := os.Getenv("MAIL_AUTH_TEST_DSN")
	if dsn == "" {
		t.Skip("MAIL_AUTH_TEST_DSN no definida")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	// Los Cleanup corren en orden inverso: el pool se registra primero para que siga
	// abierto cuando se borren las filas sembradas (un defer se ejecutaria antes).
	t.Cleanup(pool.Close)
	ctx = db.WithPool(ctx, pool)

	tenantID := uuid.New()
	otherTenant := uuid.New()
	username := "it-" + uuid.NewString()[:8] + "@pruebas.local"
	hash, err := bcrypt.GenerateFromPassword([]byte("secreta"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}

	var mailboxID uuid.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO mail.mailboxes (tenant_id, username, local_part, domain, password_hash, pop3_access)
		VALUES ($1, $2, split_part($2, '@', 1), split_part($2, '@', 2), $3, false)
		RETURNING id`, tenantID, username, string(hash)).Scan(&mailboxID)
	if err != nil {
		t.Fatalf("sembrar buzon: %v", err)
	}
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = pool.Exec(cctx, `DELETE FROM mail.sasl_logins WHERE username = $1`, username)
		_, _ = pool.Exec(cctx, `DELETE FROM mail.app_passwords WHERE mailbox_id = $1`, mailboxID)
		_, _ = pool.Exec(cctx, `DELETE FROM mail.mailboxes WHERE id = $1`, mailboxID)
	})

	var appID uuid.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO mail.app_passwords (tenant_id, mailbox_id, name, password_hash, imap_access, smtp_access)
		VALUES ($1, $2, 'movil', $3, false, true)
		RETURNING id`, tenantID, mailboxID, string(hash)).Scan(&appID)
	if err != nil {
		t.Fatalf("sembrar contrasena de aplicacion: %v", err)
	}

	repo := NewRepository(&db.ContextPool{})

	mb, err := repo.FindByUsername(ctx, username)
	if err != nil {
		t.Fatalf("FindByUsername: %v", err)
	}
	if mb.ID != mailboxID || mb.TenantID != tenantID || mb.Active != domain.MailboxActive {
		t.Fatalf("buzon inesperado: %+v", mb)
	}
	if !mb.Access.IMAP || mb.Access.POP3 {
		t.Fatalf("flags inesperados: %+v", mb.Access)
	}
	if bcrypt.CompareHashAndPassword([]byte(mb.PasswordHash), []byte("secreta")) != nil {
		t.Fatal("el hash leido no verifica la contrasena sembrada")
	}
	if _, err := repo.FindByUsername(ctx, "nadie@pruebas.local"); err != domain.ErrNotFound {
		t.Fatalf("buzon inexistente: err=%v, se esperaba ErrNotFound", err)
	}

	smtp, err := repo.ListAppPasswords(ctx, mailboxID, domain.ProtocolSMTP)
	if err != nil {
		t.Fatalf("ListAppPasswords smtp: %v", err)
	}
	if len(smtp) != 1 || smtp[0].ID != appID || smtp[0].Name != "movil" {
		t.Fatalf("smtp: %+v", smtp)
	}
	imap, err := repo.ListAppPasswords(ctx, mailboxID, domain.ProtocolIMAP)
	if err != nil {
		t.Fatalf("ListAppPasswords imap: %v", err)
	}
	if len(imap) != 0 {
		t.Fatalf("imap debe filtrar por flag: %+v", imap)
	}

	if err := repo.TouchAppPassword(ctx, appID); err != nil {
		t.Fatalf("TouchAppPassword: %v", err)
	}
	var lastUsed *time.Time
	if err := pool.QueryRow(ctx, `SELECT last_used_at FROM mail.app_passwords WHERE id = $1`, appID).Scan(&lastUsed); err != nil {
		t.Fatalf("leer last_used_at: %v", err)
	}
	if lastUsed == nil {
		t.Fatal("last_used_at sigue en NULL")
	}

	id := appID
	for _, l := range []domain.Login{
		{TenantID: tenantID, Username: username, Service: "smtp", AppPasswordID: &id, RemoteIP: "203.0.113.7"},
		{TenantID: tenantID, Username: username, Service: "imap", RemoteIP: "2001:db8::1"},
		{TenantID: tenantID, Username: username, Service: "imap", RemoteIP: "no-es-una-ip"},
		{TenantID: otherTenant, Username: username, Service: "imap", RemoteIP: "198.51.100.2"},
	} {
		if err := repo.RecordLogin(ctx, l); err != nil {
			t.Fatalf("RecordLogin %+v: %v", l, err)
		}
	}

	logins, err := repo.RecentLogins(ctx, tenantID, username, 10)
	if err != nil {
		t.Fatalf("RecentLogins: %v", err)
	}
	if len(logins) != 3 {
		t.Fatalf("RecentLogins debe acotar por empresa: %d filas", len(logins))
	}
	var withApp, withV6, withNull int
	for _, l := range logins {
		if l.AppPasswordID != nil && *l.AppPasswordID == appID {
			withApp++
		}
		if l.RemoteIP == "2001:db8::1" {
			withV6++
		}
		if l.RemoteIP == "" {
			withNull++
		}
	}
	if withApp != 1 || withV6 != 1 || withNull != 1 {
		t.Fatalf("filas inesperadas: %+v", logins)
	}
	limited, err := repo.RecentLogins(ctx, tenantID, username, 1)
	if err != nil || len(limited) != 1 {
		t.Fatalf("limit: %d filas, err=%v", len(limited), err)
	}
}

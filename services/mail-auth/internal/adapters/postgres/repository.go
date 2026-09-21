package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-auth/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Repository lee el directorio de correo de la celda como DUENO del pool, sin
// TransactRLS.
//
// Es la excepcion consciente de la plataforma: Dovecot no sabe a que empresa pertenece
// el usuario que intenta entrar, solo tiene su username, y username es clave unica en
// toda la celda. Con el rol mail_app y sin app.current_tenant_id la politica RLS no
// devolveria ninguna fila y nadie podria autenticar. Por eso este servicio es el unico
// que resuelve una identidad de correo sin empresa previa, y a cambio se limita a lo
// minimo: lee mailboxes y app_passwords, escribe sasl_logins y app_passwords.last_used_at.
// La unica lectura de vuelta (RecentLogins) si va acotada por tenant, en SQL y bajo RLS.
type Repository struct {
	pool *db.ContextPool
}

func NewRepository(pool *db.ContextPool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) FindByUsername(ctx context.Context, username string) (*domain.Mailbox, error) {
	var m domain.Mailbox
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, username, display_name, password_hash, active, force_pw_update,
		       imap_access, pop3_access, smtp_access, sieve_access, dav_access
		  FROM mail.mailboxes
		 WHERE username = $1`, username,
	).Scan(&m.ID, &m.TenantID, &m.Username, &m.DisplayName, &m.PasswordHash, &m.Active, &m.ForcePasswordUpdate,
		&m.Access.IMAP, &m.Access.POP3, &m.Access.SMTP, &m.Access.Sieve, &m.Access.DAV)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("buscar buzon: %w", err)
	}
	return &m, nil
}

// accessColumns fija la columna de mail.app_passwords por protocolo. La consulta se
// compone con este mapa y nunca con la cadena que llega de Dovecot.
var accessColumns = map[domain.Protocol]string{
	domain.ProtocolIMAP:  "imap_access",
	domain.ProtocolPOP3:  "pop3_access",
	domain.ProtocolSMTP:  "smtp_access",
	domain.ProtocolSieve: "sieve_access",
	domain.ProtocolDAV:   "dav_access",
}

// maxAppPasswordCandidates acota las contrasenas de aplicacion que se comparan en cada intento fallido
// (una comparacion de bcrypt cada una). mail-directory no deja crear mas de 25 por buzon; el margen cubre
// las que una carrera entre altas dejara pasar.
const maxAppPasswordCandidates = 50

func (r *Repository) ListAppPasswords(ctx context.Context, mailboxID uuid.UUID, p domain.Protocol) ([]domain.AppPassword, error) {
	column, ok := accessColumns[p]
	if !ok {
		return nil, fmt.Errorf("protocolo sin columna de acceso: %q", p)
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, password_hash
		  FROM mail.app_passwords
		 WHERE mailbox_id = $1 AND active AND `+column+`
		 ORDER BY created_at
		 LIMIT $2`, mailboxID, maxAppPasswordCandidates)
	if err != nil {
		return nil, fmt.Errorf("listar contrasenas de aplicacion: %w", err)
	}
	defer rows.Close()

	var out []domain.AppPassword
	for rows.Next() {
		var ap domain.AppPassword
		if err := rows.Scan(&ap.ID, &ap.Name, &ap.PasswordHash); err != nil {
			return nil, fmt.Errorf("leer contrasena de aplicacion: %w", err)
		}
		out = append(out, ap)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recorrer contrasenas de aplicacion: %w", err)
	}
	return out, nil
}

func (r *Repository) TouchAppPassword(ctx context.Context, id uuid.UUID) error {
	if _, err := r.pool.Exec(ctx, `UPDATE mail.app_passwords SET last_used_at = now() WHERE id = $1`, id); err != nil {
		return fmt.Errorf("anotar uso de contrasena de aplicacion: %w", err)
	}
	return nil
}

func (r *Repository) RecordLogin(ctx context.Context, login domain.Login) error {
	// remote_ip es inet: una IP que no se pueda interpretar se guarda como NULL en vez
	// de hacer fallar el registro del inicio.
	var remoteIP any
	if addr, err := netip.ParseAddr(login.RemoteIP); err == nil {
		remoteIP = addr.String()
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO mail.sasl_logins (tenant_id, username, service, app_password_id, remote_ip)
		VALUES ($1, $2, $3, $4, $5::inet)`,
		login.TenantID, login.Username, login.Service, login.AppPasswordID, remoteIP)
	if err != nil {
		return fmt.Errorf("registrar inicio de sesion: %w", err)
	}
	return nil
}

func (r *Repository) RecentLogins(ctx context.Context, tenantID uuid.UUID, username string, limit int) ([]domain.Login, error) {
	var out []domain.Login
	err := r.pool.TransactRLS(ctx, func(ctx context.Context) error {
		rows, err := r.pool.Query(ctx, `
			SELECT id, tenant_id, username, service, app_password_id, host(remote_ip), logged_at
			  FROM mail.sasl_logins
			 WHERE tenant_id = $1 AND username = $2
			 ORDER BY logged_at DESC
			 LIMIT $3`, tenantID, username, limit)
		if err != nil {
			return fmt.Errorf("listar inicios de sesion: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var l domain.Login
			var ip *string
			if err := rows.Scan(&l.ID, &l.TenantID, &l.Username, &l.Service, &l.AppPasswordID, &ip, &l.LoggedAt); err != nil {
				return fmt.Errorf("leer inicio de sesion: %w", err)
			}
			if ip != nil {
				l.RemoteIP = *ip
			}
			out = append(out, l)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

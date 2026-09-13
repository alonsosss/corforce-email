package config

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestDSNsConservanElFormato(t *testing.T) {
	p := PostgresConfig{
		Host: "pgbouncer", Port: 5432, User: "mail_admin", Password: "s3cr3t-Pass+1=",
		DBName: "mail_registry", DirectHost: "db.internal", DirectPort: 5433,
	}
	cases := map[string]struct{ got, want string }{
		"registro":         {p.DSN(), "postgres://mail_admin:s3cr3t-Pass+1=@pgbouncer:5432/mail_registry?sslmode=disable"},
		"empresa":          {p.TenantDSN("mail_tenant_acme"), "postgres://mail_admin:s3cr3t-Pass+1=@pgbouncer:5432/mail_tenant_acme?sslmode=disable"},
		"empresa en celda": {p.TenantDSNAt("cell2.db", 6432, "mail_tenant_acme"), "postgres://mail_admin:s3cr3t-Pass+1=@cell2.db:6432/mail_tenant_acme?sslmode=prefer"},
		"directa":          {p.TenantDirectDSN("mail_tenant_acme"), "postgres://mail_admin:s3cr3t-Pass+1=@db.internal:5433/mail_tenant_acme?sslmode=prefer"},
	}
	for name, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: %q, se esperaba %q", name, c.got, c.want)
		}
	}
}

func TestDSNEscapaCredencialesYHostIPv6(t *testing.T) {
	password := "a@b/c?d:e%f"
	p := PostgresConfig{Host: "::1", Port: 5432, User: "mail_cell_pe_01_svc", CellPassword: password, CellDBName: "mail_cell_pe_01"}
	dsn, err := p.CellDSN()
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("DSN ilegible %q: %v", dsn, err)
	}
	if got, _ := u.User.Password(); got != password {
		t.Errorf("contrasena %q, se esperaba %q", got, password)
	}
	if u.Host != "[::1]:5432" || u.Path != "/mail_cell_pe_01" {
		t.Errorf("host %q y ruta %q inesperados", u.Host, u.Path)
	}
}

func TestCellConnection(t *testing.T) {
	platform := PostgresConfig{Host: "pgbouncer", Port: 5432, User: "mail_admin", Password: "platform-pass", CellDBName: "mail_cell_pe_01"}

	withCell := platform
	withCell.CellPassword = "cell-pass-0123456789"
	withExplicitUser := withCell
	withExplicitUser.CellUser = "cell_login"
	userWithoutPassword := platform
	userWithoutPassword.CellUser = "cell_login"
	userWithoutPassword.AllowPlatformCellCredential = true
	development := platform
	development.AllowPlatformCellCredential = true
	developmentWithCell := withCell
	developmentWithCell.AllowPlatformCellCredential = true
	noCell := withCell
	noCell.CellDBName = ""
	developmentWithoutAnyPassword := development
	developmentWithoutAnyPassword.Password = ""

	cases := []struct {
		name         string
		cfg          PostgresConfig
		wantUser     string
		wantPlatform bool
		wantErr      error
		errContains  string
	}{
		{name: "credencial de celda con el rol por convencion", cfg: withCell, wantUser: "mail_cell_pe_01_svc"},
		{name: "credencial de celda con rol explicito", cfg: withExplicitUser, wantUser: "cell_login"},
		{name: "fuera de desarrollo sin credencial de celda falla cerrado", cfg: platform, wantErr: ErrCellCredentialRequired},
		{name: "en desarrollo sin credencial de celda usa la de plataforma", cfg: development, wantUser: "mail_admin", wantPlatform: true},
		{name: "en desarrollo con credencial de celda usa la de la celda", cfg: developmentWithCell, wantUser: "mail_cell_pe_01_svc"},
		{name: "rol sin contrasena es un error aunque haya respaldo", cfg: userWithoutPassword, errContains: "CELL_DB_USER"},
		{name: "sin base de celda", cfg: noCell, errContains: "CELL_DB_NAME"},
		{name: "respaldo sin contrasena de plataforma", cfg: developmentWithoutAnyPassword, errContains: "CELL_DB_PASSWORD"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			conn, err := c.cfg.CellConnection()
			switch {
			case c.wantErr != nil:
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("error %v, se esperaba %v", err, c.wantErr)
				}
				return
			case c.errContains != "":
				if err == nil || !strings.Contains(err.Error(), c.errContains) {
					t.Fatalf("error %v, se esperaba uno que nombre %s", err, c.errContains)
				}
				return
			case err != nil:
				t.Fatalf("error inesperado: %v", err)
			}
			if conn.User != c.wantUser || conn.PlatformCredential != c.wantPlatform {
				t.Fatalf("usuario %q (plataforma %v), se esperaba %q (plataforma %v)", conn.User, conn.PlatformCredential, c.wantUser, c.wantPlatform)
			}
			u, err := url.Parse(conn.DSN)
			if err != nil {
				t.Fatalf("DSN ilegible: %v", err)
			}
			if u.User.Username() != c.wantUser || u.Path != "/mail_cell_pe_01" || u.Host != "pgbouncer:5432" {
				t.Fatalf("DSN inesperado: %s", conn.DSN)
			}
			if dsn, err := c.cfg.CellDSN(); err != nil || dsn != conn.DSN {
				t.Fatalf("CellDSN no coincide con CellConnection: %q, %v", dsn, err)
			}
		})
	}
}

// setEnv fija las variables que lee Load; una variable ausente del mapa queda vacia, que
// para Load es lo mismo que no definida.
func setEnv(t *testing.T, vars map[string]string) {
	t.Helper()
	for _, key := range []string{"ENVIRONMENT", "JWT_SECRET", "POSTGRES_PASSWORD", "POSTGRES_USER", "CELL_DB_NAME", "CELL_DB_USER", "CELL_DB_PASSWORD"} {
		t.Setenv(key, vars[key])
	}
}

func TestLoadCredencialDeCelda(t *testing.T) {
	base := func(extra map[string]string) map[string]string {
		vars := map[string]string{"JWT_SECRET": "jwt-secret-for-tests", "CELL_DB_NAME": "mail_cell_pe_01"}
		for k, v := range extra {
			vars[k] = v
		}
		return vars
	}
	cases := []struct {
		name         string
		env          map[string]string
		wantLoadErr  bool
		wantErr      error
		wantUser     string
		wantPlatform bool
	}{
		{name: "produccion sin credencial de celda: carga pero la celda no abre",
			env: base(map[string]string{"ENVIRONMENT": "production", "POSTGRES_PASSWORD": "platform-pass"}), wantErr: ErrCellCredentialRequired},
		{name: "staging tampoco admite el respaldo",
			env: base(map[string]string{"ENVIRONMENT": "staging", "POSTGRES_PASSWORD": "platform-pass"}), wantErr: ErrCellCredentialRequired},
		{name: "sin ENVIRONMENT declarado no hay respaldo",
			env: base(map[string]string{"POSTGRES_PASSWORD": "platform-pass"}), wantErr: ErrCellCredentialRequired},
		{name: "desarrollo declarado usa la de plataforma",
			env: base(map[string]string{"ENVIRONMENT": "Development", "POSTGRES_PASSWORD": "platform-pass"}), wantUser: "mail_admin", wantPlatform: true},
		{name: "produccion con credencial de celda y sin la de plataforma",
			env: base(map[string]string{"ENVIRONMENT": "production", "CELL_DB_PASSWORD": "cell-pass-0123456789"}), wantUser: "mail_cell_pe_01_svc"},
		{name: "sin ninguna credencial no carga",
			env: base(map[string]string{"ENVIRONMENT": "production"}), wantLoadErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setEnv(t, c.env)
			cfg, err := Load()
			if c.wantLoadErr {
				if err == nil {
					t.Fatal("Load deberia fallar sin ninguna credencial de base")
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			conn, err := cfg.Postgres.CellConnection()
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("error %v, se esperaba %v", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("CellConnection: %v", err)
			}
			if conn.User != c.wantUser || conn.PlatformCredential != c.wantPlatform {
				t.Fatalf("usuario %q (plataforma %v), se esperaba %q (plataforma %v)", conn.User, conn.PlatformCredential, c.wantUser, c.wantPlatform)
			}
		})
	}
}

func TestLoadServicioDeEmpresaSigueExigiendoLaDePlataforma(t *testing.T) {
	setEnv(t, map[string]string{"ENVIRONMENT": "production", "JWT_SECRET": "jwt-secret-for-tests", "CELL_DB_PASSWORD": "cell-pass-0123456789"})
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "POSTGRES_PASSWORD") {
		t.Fatalf("sin CELL_DB_NAME la credencial de celda no sustituye a la de plataforma: %v", err)
	}
}

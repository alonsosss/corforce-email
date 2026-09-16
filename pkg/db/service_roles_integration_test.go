//go:build integration

package db

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Credenciales por servicio contra un Postgres DESECHABLE. Lo que se comprueba no es que el
// rol pueda trabajar -eso lo dice cualquier prueba-, sino lo contrario: que lo que NO le
// toca le responde con un error de Postgres. Un privilegio negado solo esta probado cuando
// una conexion real lo intenta y se lo niegan.
//
// Cubre las tres credenciales que introduce el reparto:
//
//   - enrutado (mail_router): lee organization.v_tenant_routing y nada mas del registro.
//
//   - servicio de empresa (mail_svc_contacts): DML en su esquema, nada del esquema vecino,
//     nada del registro, ningun DDL.
//
//   - motores de una celda (<base>_engine): lo que Postfix y Dovecot leen de SU celda, sin
//     credenciales de buzon y sin entrar en otra celda.
//
//     DB_ROLES_TEST_DSN='postgres://mail_admin:...@127.0.0.1:27000/postgres?sslmode=disable' \
//     DB_ROLES_TEST_CONTAINER=cfm-it-pg \
//     go test -tags integration -run TestRoles ./pkg/db/
func TestRolesPorServicioAislados(t *testing.T) {
	dsn := integrationEnv(t, "DB_ROLES_TEST_DSN")
	shim := rolesPsqlShim(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	adminCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	admin, err := pgxpool.NewWithConfig(ctx, adminCfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(admin.Close)

	sufijo := strings.Split(uuid.NewString(), "-")[0]
	registro := "mail_registry_rol_" + sufijo
	empresaA := "mail_tenant_rol_a_" + sufijo
	empresaB := "mail_tenant_rol_b_" + sufijo
	t.Cleanup(func() {
		bg := context.Background()
		for _, n := range []string{registro, empresaA, empresaB} {
			_, _ = admin.Exec(bg, "DROP DATABASE IF EXISTS "+n+" WITH (FORCE)")
		}
		// Los roles son del cluster: se retiran o la siguiente ejecucion los encuentra con
		// otra contrasena.
		for _, r := range []string{"mail_router", "mail_svc_contacts"} {
			_, _ = admin.Exec(bg, "DROP ROLE IF EXISTS "+pgx.Identifier{r}.Sanitize())
		}
	})

	for _, n := range []string{registro, empresaA, empresaB} {
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+n); err != nil {
			t.Fatalf("crear %s: %v", n, err)
		}
		// Igual que organization al crear la base de una empresa: Postgres concede CONNECT y
		// TEMPORARY a PUBLIC en toda base nueva, y sin retirarselo cualquier rol de login
		// entraria por ahi. Sin esto la prueba mediria un cluster que no se parece al de
		// produccion y daria por aislado lo que no lo esta.
		if _, err := admin.Exec(ctx, "REVOKE CONNECT, TEMPORARY ON DATABASE "+n+" FROM PUBLIC"); err != nil {
			t.Fatalf("cerrar %s a PUBLIC: %v", n, err)
		}
	}
	// El registro entero: la vista del enrutado convive con identity y access_control, que
	// es lo que el rol de enrutado NO debe poder leer.
	aplicarSQL(t, ctx, adminCfg, registro, "migrations/registry")
	// Dos esquemas de empresa: el propio y el del vecino.
	for _, base := range []string{empresaA, empresaB} {
		for _, dir := range []string{"migrations/tenant/canonical/platform",
			"migrations/tenant/canonical/contacts", "migrations/tenant/canonical/templates"} {
			aplicarSQL(t, ctx, adminCfg, base, dir)
		}
	}

	claveRouter := secretoDePrueba()
	claveContacts := secretoDePrueba()
	correrRolScript(t, ctx, adminCfg, shim, registro,
		[]string{"TENANT_ROUTER_DB_PASSWORD=" + claveRouter}, "--router")
	correrRolScript(t, ctx, adminCfg, shim, registro,
		[]string{"CONTACTS_DB_PASSWORD=" + claveContacts}, "--service", "contacts")

	t.Run("enrutado: la vista publicada y nada mas", func(t *testing.T) {
		conn := conectarComo(t, ctx, adminCfg, "mail_router", claveRouter, registro)
		defer conn.Close(context.Background())

		var n int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM organization.v_tenant_routing`).Scan(&n); err != nil {
			t.Fatalf("el enrutado no puede leer su vista: %v", err)
		}
		for _, tabla := range []string{
			"organization.tenants", "organization.cells", "identity.users",
			"access_control.permissions", "platform.event_outbox",
		} {
			err := conn.QueryRow(ctx, `SELECT count(*) FROM `+tabla).Scan(&n)
			esperarSQLState(t, err, "42501", "el enrutado lee "+tabla)
		}
		// Ni siquiera entra en una base de empresa.
		for _, base := range []string{empresaA, empresaB} {
			c, err := conectar(ctx, adminCfg, "mail_router", claveRouter, base)
			if err == nil {
				c.Close(ctx)
			}
			esperarSQLState(t, err, "42501", "el enrutado conecta a "+base)
		}
	})

	t.Run("servicio de empresa: su esquema, no el del vecino ni el registro", func(t *testing.T) {
		conn := conectarComo(t, ctx, adminCfg, "mail_svc_contacts", claveContacts, empresaA)
		defer conn.Close(context.Background())

		tenant := uuid.New()
		if _, err := conn.Exec(ctx,
			`INSERT INTO contacts.contacts (tenant_id, email, source) VALUES ($1, $2, 'api')`,
			tenant, "rol-"+sufijo+"@example.test"); err != nil {
			t.Fatalf("alta de contacto con la credencial del servicio: %v", err)
		}
		var n int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM contacts.contacts WHERE tenant_id = $1`, tenant).Scan(&n); err != nil || n != 1 {
			t.Fatalf("lectura de lo suyo: %d, %v", n, err)
		}
		// La outbox de la empresa: encolar el evento en la misma transaccion que el cambio.
		if _, err := conn.Exec(ctx,
			`INSERT INTO platform.event_outbox (id, subject, tenant_id, payload) VALUES ($1, 'contacts.contact.created', $2, '{}')`,
			uuid.New(), tenant); err != nil {
			t.Fatalf("encolar en la outbox: %v", err)
		}

		// El esquema del servicio vecino, en su MISMA base, no se ve.
		err := conn.QueryRow(ctx, `SELECT count(*) FROM templates.templates`).Scan(&n)
		esperarSQLState(t, err, "42501", "contacts lee el esquema templates")

		for _, sentencia := range []string{
			`CREATE TABLE contacts.prohibida (id int)`,
			`DROP TABLE contacts.contacts`,
			`TRUNCATE contacts.contacts`,
			`CREATE TEMP TABLE prohibida (id int)`,
		} {
			_, err := conn.Exec(ctx, sentencia)
			esperarSQLState(t, err, "42501", sentencia)
		}

		// Ni el registro, que es donde viven usuarios, permisos y facturacion.
		c, err := conectar(ctx, adminCfg, "mail_svc_contacts", claveContacts, registro)
		if err == nil {
			c.Close(ctx)
		}
		esperarSQLState(t, err, "42501", "contacts conecta al registro")
	})

	t.Run("una empresa nueva deja entrar al servicio sin un paso aparte", func(t *testing.T) {
		// empresaB nacio despues de que existiera el rol, como una empresa dada de alta
		// mas tarde: el CONNECT se lo dio su propia migracion canonica.
		conectarComo(t, ctx, adminCfg, "mail_svc_contacts", claveContacts, empresaB).Close(ctx)
	})

	t.Run("rotacion: la contrasena retirada deja de valer", func(t *testing.T) {
		nueva := secretoDePrueba()
		correrRolScript(t, ctx, adminCfg, shim, registro,
			[]string{"CONTACTS_DB_PASSWORD=" + nueva}, "--service", "contacts")
		c, err := conectar(ctx, adminCfg, "mail_svc_contacts", claveContacts, empresaA)
		if err == nil {
			c.Close(ctx)
		}
		esperarSQLState(t, err, "28P01", "la contrasena anterior")
		conectarComo(t, ctx, adminCfg, "mail_svc_contacts", nueva, empresaA).Close(ctx)
	})
}

// El rol de los motores por celda. Va aparte porque monta celdas, no empresas, y porque lo
// que prueba es el contrato con Postfix y Dovecot: si aqui falta un SELECT, la celda deja de
// recibir correo.
func TestRolDeMotoresPorCelda(t *testing.T) {
	dsn := integrationEnv(t, "DB_ROLES_TEST_DSN")
	shim := rolesPsqlShim(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	adminCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	admin, err := pgxpool.NewWithConfig(ctx, adminCfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(admin.Close)

	sufijo := strings.Split(uuid.NewString(), "-")[0]
	celdaA := "mail_cell_mot_a_" + sufijo
	celdaB := "mail_cell_mot_b_" + sufijo
	rolA := celdaA + "_engine"
	rolB := celdaB + "_engine"
	t.Cleanup(func() {
		bg := context.Background()
		for _, n := range []string{celdaA, celdaB} {
			_, _ = admin.Exec(bg, "DROP DATABASE IF EXISTS "+n+" WITH (FORCE)")
		}
		for _, r := range []string{rolA, rolB, celdaA + "_svc", celdaB + "_svc"} {
			_, _ = admin.Exec(bg, "DROP ROLE IF EXISTS "+pgx.Identifier{r}.Sanitize())
		}
		// mail_engine es compartido y esta prueba lo retira: se devuelve como estaba para no
		// dejar el cluster de la ejecucion a medias.
		_, _ = admin.Exec(bg, "ALTER ROLE mail_engine WITH LOGIN")
	})

	for _, n := range []string{celdaA, celdaB} {
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+n); err != nil {
			t.Fatalf("crear %s: %v", n, err)
		}
		for _, dir := range []string{"migrations/cell/canonical/platform",
			"migrations/cell/canonical/mail-directory", "migrations/cell/canonical/mail-security"} {
			aplicarSQL(t, ctx, adminCfg, n, dir)
		}
	}

	claveA := secretoDePrueba()
	claveB := secretoDePrueba()
	correrScript(t, ctx, adminCfg, shim, "ops/db/cell-engine-role.sh",
		[]string{"MAIL_DB_PASSWORD=" + claveA}, "--cell", "mot-a-"+sufijo, "--cell-db", celdaA)
	correrScript(t, ctx, adminCfg, shim, "ops/db/cell-engine-role.sh",
		[]string{"MAIL_DB_PASSWORD=" + claveB}, "--cell", "mot-b-"+sufijo, "--cell-db", celdaB)

	t.Run("lee el directorio de su celda", func(t *testing.T) {
		conn := conectarComo(t, ctx, adminCfg, rolA, claveA, celdaA)
		defer conn.Close(context.Background())
		// Las tablas y vistas que consultan los mapas de Postfix y el userdb de Dovecot.
		for _, rel := range []string{
			"mail.domains", "mail.alias_domains", "mail.mailboxes", "mail.aliases",
			"mail.spam_aliases", "mail.sender_acl", "mail.relayhosts", "mail.transports",
			"mail.tls_policy_overrides", "mail.recipient_maps", "mail.bcc_maps",
			"mail.sieve_filters", "mail.v_sieve_before", "mail.v_sieve_after",
		} {
			var n int
			if err := conn.QueryRow(ctx, `SELECT count(*) FROM `+rel).Scan(&n); err != nil {
				t.Errorf("el motor no puede leer %s: %v", rel, err)
			}
		}
		// La cuota la escribe Dovecot.
		if _, err := conn.Exec(ctx,
			`INSERT INTO mail.quota_usage (username, bytes, messages) VALUES ($1, 0, 0)
			 ON CONFLICT (username) DO UPDATE SET bytes = 0`, "buzon-"+sufijo+"@example.test"); err != nil {
			t.Errorf("el motor no puede escribir la cuota: %v", err)
		}
	})

	t.Run("no ve credenciales ni entra en otra celda", func(t *testing.T) {
		conn := conectarComo(t, ctx, adminCfg, rolA, claveA, celdaA)
		defer conn.Close(context.Background())
		var n int
		for _, tabla := range []string{"mail.app_passwords", "mail.sasl_logins"} {
			err := conn.QueryRow(ctx, `SELECT count(*) FROM `+tabla).Scan(&n)
			esperarSQLState(t, err, "42501", "el motor lee "+tabla)
		}
		c, err := conectar(ctx, adminCfg, rolA, claveA, celdaB)
		if err == nil {
			c.Close(ctx)
		}
		esperarSQLState(t, err, "42501", "el motor de una celda entra en la otra")
	})

	t.Run("retirar el rol compartido no toca a los propios", func(t *testing.T) {
		correrScript(t, ctx, adminCfg, shim, "ops/db/cell-engine-role.sh",
			[]string{"MAIL_DB_PASSWORD=" + claveA}, "--cell", "mot-a-"+sufijo, "--cell-db", celdaA, "--retire-shared")
		var puedeEntrar bool
		if err := admin.QueryRow(ctx, `SELECT rolcanlogin FROM pg_roles WHERE rolname = 'mail_engine'`).Scan(&puedeEntrar); err != nil {
			t.Fatal(err)
		}
		if puedeEntrar {
			t.Error("mail_engine sigue pudiendo iniciar sesion despues de retirarlo")
		}
		// Y los de cada celda siguen trabajando: es lo que sostiene el correo.
		conectarComo(t, ctx, adminCfg, rolA, claveA, celdaA).Close(ctx)
		conectarComo(t, ctx, adminCfg, rolB, claveB, celdaB).Close(ctx)
	})
}

// ── Ayudas ───────────────────────────────────────────────────────────────────

// rolesPsqlShim devuelve un directorio con un psql que ejecuta el del contenedor, o "" si
// hay psql en el PATH: los scripts de ops/db hablan por psql y no hay cliente ni en el
// anfitrion ni en el runner de CI.
func rolesPsqlShim(t *testing.T) string {
	t.Helper()
	if contenedor := os.Getenv("DB_ROLES_TEST_CONTAINER"); contenedor != "" {
		dir := t.TempDir()
		shim := "#!/bin/sh\nexec docker exec -i -e PGPASSWORD \"$DB_ROLES_TEST_CONTAINER\" psql -U \"$PGUSER\" \"$@\"\n"
		if err := os.WriteFile(filepath.Join(dir, "psql"), []byte(shim), 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	if _, err := exec.LookPath("psql"); err != nil {
		if os.Getenv("INTEGRATION_REQUIRED") == "1" {
			t.Fatal("sin psql ni DB_ROLES_TEST_CONTAINER (INTEGRATION_REQUIRED=1)")
		}
		t.Skip("sin psql ni DB_ROLES_TEST_CONTAINER")
	}
	return ""
}

func raizDelRepositorio(t *testing.T) string {
	t.Helper()
	_, fichero, _, _ := runtime.Caller(0)
	raiz := filepath.Clean(filepath.Join(filepath.Dir(fichero), "..", ".."))
	if _, err := os.Stat(filepath.Join(raiz, "go.mod")); err != nil {
		t.Fatalf("raiz del repositorio no encontrada desde %s", fichero)
	}
	return raiz
}

// aplicarSQL aplica un directorio de migraciones en orden y DOS veces: una migracion que no
// tolere re-ejecutarse deja la base sin las siguientes.
func aplicarSQL(t *testing.T, ctx context.Context, adminCfg *pgxpool.Config, base, dir string) {
	t.Helper()
	ficheros, err := filepath.Glob(filepath.Join(raizDelRepositorio(t), dir, "*.sql"))
	if err != nil || len(ficheros) == 0 {
		t.Fatalf("migraciones de %s: %v", dir, err)
	}
	sort.Strings(ficheros)
	cfg := adminCfg.Copy()
	cfg.ConnConfig.Database = base
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for pasada := 1; pasada <= 2; pasada++ {
		for _, f := range ficheros {
			sql, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, string(sql)); err != nil {
				t.Fatalf("pasada %d, %s en %s: %v", pasada, filepath.Base(f), base, err)
			}
		}
	}
}

func correrScript(t *testing.T, ctx context.Context, adminCfg *pgxpool.Config, shim, script string, entorno []string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "bash", filepath.Join(raizDelRepositorio(t), script))
	cmd.Args = append(cmd.Args, args...)
	cc := adminCfg.ConnConfig
	cmd.Env = append(os.Environ(),
		"PGHOST="+cc.Host, "PGPORT="+strconv.Itoa(int(cc.Port)),
		"PGUSER="+cc.User, "PGPASSWORD="+cc.Password)
	cmd.Env = append(cmd.Env, entorno...)
	if shim != "" {
		cmd.Env = append(cmd.Env, "PATH="+shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", script, args, err, out)
	}
}

func correrRolScript(t *testing.T, ctx context.Context, adminCfg *pgxpool.Config, shim, registro string, entorno []string, args ...string) {
	t.Helper()
	correrScript(t, ctx, adminCfg, shim, "ops/db/tenant-service-role.sh",
		append(entorno, "POSTGRES_DB="+registro), args...)
}

func conectar(ctx context.Context, adminCfg *pgxpool.Config, usuario, clave, base string) (*pgx.Conn, error) {
	cc := adminCfg.ConnConfig.Copy()
	cc.User, cc.Password, cc.Database = usuario, clave, base
	return pgx.ConnectConfig(ctx, cc)
}

func conectarComo(t *testing.T, ctx context.Context, adminCfg *pgxpool.Config, usuario, clave, base string) *pgx.Conn {
	t.Helper()
	conn, err := conectar(ctx, adminCfg, usuario, clave, base)
	if err != nil {
		t.Fatalf("%s no conecta a %s: %v", usuario, base, err)
	}
	return conn
}

func esperarSQLState(t *testing.T, err error, codigo, que string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != codigo {
		t.Errorf("%s: se esperaba SQLSTATE %s, se obtuvo %v", que, codigo, err)
	}
}

// secretoDePrueba genera una contrasena que cumple lo que exigen los scripts (32 o mas
// caracteres ASCII imprimibles). Ninguna credencial de prueba vive en el repositorio.
func secretoDePrueba() string {
	return strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")
}

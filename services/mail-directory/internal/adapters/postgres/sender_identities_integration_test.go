//go:build integration

package postgres

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// legacySenderACLQuery es la consulta que postfix.sh escribia en pgsql_virtual_sender_acl.cf
// antes de pasar a mail.sender_login_owners, con %s, %d y %u como $1, $2 y $3. Se conserva
// congelada aqui para probar que la funcion decide exactamente lo mismo que decidia Postfix.
const legacySenderACLQuery = `SELECT goto FROM mail.aliases
  WHERE id IN (
      SELECT COALESCE (
        (SELECT id FROM mail.aliases WHERE address = $1::text AND active IN (1, 2) AND sender_allowed),
        (SELECT id FROM mail.aliases WHERE address = '@' || $2::text AND active IN (1, 2) AND sender_allowed)
      )
    )
    AND active = 1
    AND sender_allowed
    AND (domain IN (SELECT domain FROM mail.domains WHERE domain = $2::text AND active)
      OR domain IN (SELECT alias_domain FROM mail.alias_domains WHERE alias_domain = $2::text AND active))
  UNION
  SELECT logged_in_as FROM mail.sender_acl
    WHERE send_as = '@' || $2::text
      OR send_as = $1::text
      OR send_as = '*'
      OR send_as IN (SELECT '@' || target_domain FROM mail.alias_domains WHERE alias_domain = $2::text)
      OR send_as IN (SELECT $3::text || '@' || target_domain FROM mail.alias_domains WHERE alias_domain = $2::text)
      AND logged_in_as NOT IN (SELECT goto FROM mail.aliases WHERE address = $1::text)
  UNION
  SELECT username FROM mail.mailboxes WHERE username = $1::text AND active = 1
  UNION
  SELECT m.username FROM mail.mailboxes m, mail.alias_domains ad
    WHERE ad.alias_domain = $2::text
      AND m.username = $3::text || '@' || ad.target_domain
      AND m.active IN (1, 2)
      AND ad.active`

var ownerSeparators = regexp.MustCompile(`[,\s]+`)

// ownerSet parte las filas de duenos como Postfix (comas y espacios) y sin mayusculas.
func ownerSet(rows []string) []string {
	seen := map[string]bool{}
	for _, row := range rows {
		for _, name := range ownerSeparators.Split(row, -1) {
			if name != "" {
				seen[strings.ToLower(name)] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func queryStrings(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) []string {
	t.Helper()
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		t.Fatal(err)
	}
	return scanStrings(t, rows)
}

func scanStrings(t *testing.T, rows pgx.Rows) []string {
	t.Helper()
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestRemitentesComoPostfix comprueba que mail.sender_login_owners decide lo mismo que la
// consulta que tenia Postfix para cada remitente, que mail.sender_identities solo ofrece lo
// que esa regla acepta y que cada rol llega solo a lo que le corresponde.
func TestRemitentesComoPostfix(t *testing.T) {
	dsn := integrationEnv(t, "MAIL_DIRECTORY_TEST_DSN")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	applyCellMigrations(t, ctx, pool)

	tenant := uuid.New()
	s := strings.Split(uuid.NewString(), "-")[0]
	d1, off := "sid-"+s+".example", "sidoff-"+s+".example"
	ad, adOff := "sidalias-"+s+".example", "sidaliasoff-"+s+".example"
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, table := range []string{"sender_acl", "aliases", "mailboxes", "alias_domains", "domains"} {
			if _, err := pool.Exec(c, `DELETE FROM mail.`+table+` WHERE tenant_id = $1`, tenant); err != nil {
				t.Errorf("limpiar %s: %v", table, err)
			}
		}
	})

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	domain := func(name string, active bool) {
		exec(`INSERT INTO mail.domains (tenant_id, domain, active) VALUES ($1, $2, $3)`, tenant, name, active)
	}
	aliasDomain := func(name, target string, active bool) {
		exec(`INSERT INTO mail.alias_domains (tenant_id, alias_domain, target_domain, active) VALUES ($1, $2, $3, $4)`,
			tenant, name, target, active)
	}
	mailbox := func(local string, active int) {
		exec(`INSERT INTO mail.mailboxes (tenant_id, username, local_part, domain, password_hash, active)
 VALUES ($1, $2, $3, $4, 'x', $5)`, tenant, local+"@"+d1, local, d1, active)
	}
	alias := func(address, gotoList, dom string, senderAllowed bool, active int) {
		exec(`INSERT INTO mail.aliases (tenant_id, address, goto, domain, sender_allowed, active) VALUES ($1, $2, $3, $4, $5, $6)`,
			tenant, address, gotoList, dom, senderAllowed, active)
	}
	acl := func(login, sendAs string) {
		exec(`INSERT INTO mail.sender_acl (tenant_id, logged_in_as, send_as) VALUES ($1, $2, $3)`, tenant, login, sendAs)
	}

	ana, bob, eva, zoe := "ana@"+d1, "bob@"+d1, "eva@"+d1, "zoe@"+d1
	domain(d1, true)
	domain(off, false)
	aliasDomain(ad, d1, true)
	aliasDomain(adOff, d1, false)
	mailbox("ana", 1)
	mailbox("bob", 1)
	mailbox("eva", 2)
	mailbox("zoe", 0)
	alias("ventas@"+d1, ana+", "+bob, d1, true, 1)
	alias("info@"+d1, ana, d1, false, 1)
	alias("pausa@"+d1, ana, d1, true, 2)
	alias("@"+d1, bob, d1, true, 1)
	alias("fuera@"+off, ana, off, true, 1)
	alias("soporte@"+ad, ana, ad, true, 1)
	acl(ana, "jefe@"+d1)
	acl(ana, "pedro@"+d1)
	acl(ana, "@otro-"+s+".example")
	acl(eva, "x@"+d1)
	acl(zoe, "*")

	probes := []string{ana, bob, eva, zoe, "ventas@" + d1, "info@" + d1, "pausa@" + d1, "cualquiera@" + d1,
		"fuera@" + off, "soporte@" + ad, "ana@" + ad, "ana@" + adOff, "bob@" + ad, "jefe@" + d1, "jefe@" + ad,
		"pedro@" + d1, "pedro@" + ad, "pedro@" + adOff, "x@" + d1, "x@" + ad, "nadie@otro-" + s + ".example",
		"nadie@ajeno.example"}

	legacy := func(sender string) []string {
		at := strings.LastIndex(sender, "@")
		return ownerSet(queryStrings(t, ctx, pool, legacySenderACLQuery, sender, sender[at+1:], sender[:at]))
	}
	owners := func(sender string) []string {
		return ownerSet(queryStrings(t, ctx, pool, `SELECT * FROM mail.sender_login_owners($1)`, sender))
	}
	for _, sender := range probes {
		want, got := legacy(sender), owners(sender)
		if strings.Join(want, ",") != strings.Join(got, ",") {
			t.Errorf("%s: la consulta de Postfix da %v y la funcion %v", sender, want, got)
		}
	}
	// Postfix no consulta sin parte local o sin dominio.
	for _, key := range []string{"@" + d1, "ana", "", "@"} {
		if got := owners(key); len(got) != 0 {
			t.Errorf("%q no tiene dueno por esta via: %v", key, got)
		}
	}

	identities := func(login string) []string {
		return queryStrings(t, ctx, pool, `SELECT address FROM mail.sender_identities($1) AS address ORDER BY address COLLATE "C"`, login)
	}
	// Nada que la regla de Postfix no acepte sale en la lista.
	for _, login := range []string{ana, bob, eva, zoe} {
		for _, address := range identities(login) {
			if !contains(legacy(address), login) {
				t.Errorf("%s ofrece %s, que Postfix rechazaria", login, address)
			}
		}
	}
	wantAna := []string{ana, "ana@" + ad, "jefe@" + d1, "jefe@" + ad, "jefe@" + adOff, "pedro@" + d1,
		"pedro@" + ad, "pedro@" + adOff, "soporte@" + ad, "ventas@" + d1}
	sort.Strings(wantAna)
	if got := identities(strings.ToUpper(ana)); strings.Join(got, ",") != strings.Join(wantAna, ",") {
		t.Errorf("remitentes de ana:\n got %v\nwant %v", got, wantAna)
	}
	// El comodin (catch-all de bob, '*' de zoe) no se enumera: solo valida candidatos. zoe esta
	// inactiva, pero con '*' Postfix la acepta como duena de cualquier remitente, tambien de
	// las direcciones de su propio buzon.
	bobIDs := identities(bob)
	for _, want := range []string{bob, "bob@" + ad, "ventas@" + d1} {
		if !contains(bobIDs, want) {
			t.Errorf("a bob le falta %s: %v", want, bobIDs)
		}
	}
	for _, absent := range []string{"cualquiera@" + d1, "info@" + d1} {
		if contains(bobIDs, absent) {
			t.Errorf("bob no debe ofrecer %s (solo lo permite el comodin): %v", absent, bobIDs)
		}
	}
	wantZoe := []string{zoe, "zoe@" + ad, "zoe@" + adOff}
	sort.Strings(wantZoe)
	if got := identities(zoe); strings.Join(got, ",") != strings.Join(wantZoe, ",") {
		t.Errorf("remitentes de zoe: %v", got)
	}

	repo := NewSenderIdentityRepo(&db.ContextPool{})
	fromRepo, err := repo.ForLogin(db.WithPool(ctx, pool), ana, 3)
	if err != nil || len(fromRepo) != 3 || fromRepo[0] != wantAna[0] {
		t.Fatalf("repositorio con tope: %v %v", fromRepo, err)
	}

	// Postfix entra como mail_engine; mail-directory como mail_service; mail_app no llega.
	asRole := func(role, sql string, args ...any) ([]string, error) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+role); err != nil {
			t.Fatal(err)
		}
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, rows.Err()
	}
	if got, err := asRole("mail_engine", `SELECT owner FROM mail.sender_login_owners($1) AS owner`, "ventas@"+d1); err != nil ||
		strings.Join(ownerSet(got), ",") != strings.Join(legacy("ventas@"+d1), ",") {
		t.Fatalf("mail_engine (mapa de Postfix): %v %v", got, err)
	}
	if got, err := asRole("mail_service", `SELECT * FROM mail.sender_identities($1)`, ana); err != nil || len(got) != len(wantAna) {
		t.Fatalf("mail_service: %v %v", got, err)
	}
	var pgErr *pgconn.PgError
	if _, err := asRole("mail_app", `SELECT * FROM mail.sender_identities($1)`, ana); !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("mail_app no debe poder enumerar remitentes de la celda: %v", err)
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

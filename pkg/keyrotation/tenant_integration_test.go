//go:build integration

package keyrotation

import (
	"context"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

func integrationEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		if os.Getenv("INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("%s no definida con INTEGRATION_REQUIRED=1", name)
		}
		t.Skipf("%s no definida", name)
	}
	return v
}

func dsnFor(t *testing.T, base, dbName string) string {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + dbName
	return u.String()
}

// TenantPass recorre la base de cada empresa, tambien la de una suspendida, re-cifra con los datos
// autenticados de su empresa, cuenta lo ilegible y da como pendiente la empresa cuya base no abre.
func TestTenantPassRecorreTodasLasEmpresas(t *testing.T) {
	base := integrationEnv(t, "TEST_DATABASE_URL")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	prefix := "it_kr_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	regDB, dbA, dbB := prefix+"_reg", prefix+"_a", prefix+"_b"
	for _, name := range []string{regDB, dbA, dbB} {
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Fatal(err)
		}
	}
	var pools []*pgxpool.Pool
	open := func(name string) *pgxpool.Pool {
		p, err := pgxpool.New(ctx, dsnFor(t, base, name))
		if err != nil {
			t.Fatal(err)
		}
		pools = append(pools, p)
		return p
	}
	mgr := db.NewTenantPoolManager(func(name string) string { return dsnFor(t, base, name) }, zap.NewNop())
	t.Cleanup(func() {
		mgr.CloseAll()
		for _, p := range pools {
			p.Close()
		}
		for _, name := range []string{regDB, dbA, dbB} {
			_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		}
	})

	tenantA, tenantB, tenantC := uuid.New(), uuid.New(), uuid.New()
	reg := open(regDB)
	if _, err := reg.Exec(ctx, `
CREATE SCHEMA organization;
CREATE TABLE organization.tenants (id uuid PRIMARY KEY, slug text, status text, db_name text);
CREATE VIEW organization.v_tenant_routing AS
    SELECT id AS tenant_id, slug, status, db_name, NULL::text AS cell_code, NULL::text AS db_host, NULL::int AS db_port
      FROM organization.tenants;`); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		id             uuid.UUID
		status, dbName string
	}{{tenantA, "active", dbA}, {tenantB, "suspended", dbB}, {tenantC, "active", prefix + "_no_existe"}} {
		if _, err := reg.Exec(ctx, `INSERT INTO organization.tenants VALUES ($1, $2, $3, $4)`, r.id, r.id.String(), r.status, r.dbName); err != nil {
			t.Fatal(err)
		}
	}

	oldRing := ring(t, strings.Repeat("11", 32), "")
	aad := func(tenant, key uuid.UUID) []byte { return []byte(tenant.String() + "/" + key.String()) }
	type row struct {
		tenant, id uuid.UUID
		plain      string
	}
	var rows []row
	for _, tc := range []struct {
		tenant uuid.UUID
		pool   *pgxpool.Pool
		n      int
	}{{tenantA, open(dbA), 2}, {tenantB, open(dbB), 1}} {
		if _, err := tc.pool.Exec(ctx, `CREATE SCHEMA s; CREATE TABLE s.secrets (id uuid PRIMARY KEY, enc bytea)`); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < tc.n; i++ {
			r := row{tenant: tc.tenant, id: uuid.New(), plain: uuid.NewString()}
			sealed, err := oldRing.EncryptWithAAD([]byte(r.plain), aad(r.tenant, r.id))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tc.pool.Exec(ctx, `INSERT INTO s.secrets VALUES ($1, $2)`, r.id, sealed); err != nil {
				t.Fatal(err)
			}
			rows = append(rows, r)
		}
		if tc.tenant == tenantA {
			// Ilegible con cualquier llave, y una fila sin dato que no se recorre.
			if _, err := tc.pool.Exec(ctx, `INSERT INTO s.secrets VALUES ($1, '\x0102'), ($2, NULL)`, uuid.New(), uuid.New()); err != nil {
				t.Fatal(err)
			}
		}
	}

	tenants := db.NewTenantDB(reg, mgr)
	target := TenantTarget{Column: MustColumn(nil, "s.secrets", "id", "enc"), AAD: aad}
	rotating := ring(t, strings.Repeat("22", 32), strings.Repeat("11", 32))
	res, err := TenantPass(ctx, rotating, tenants, 2, time.Minute, zap.NewNop(), target)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rotated != 3 || res.Pending != 1 || res.Examined != 4 || !slices.Equal(res.UnreachedTenants, []string{tenantC.String()}) || res.Outstanding() != 2 {
		t.Fatalf("primera pasada: %+v", res)
	}
	retired := ring(t, strings.Repeat("22", 32), "")
	for _, r := range rows {
		dbName := dbA
		if r.tenant == tenantB {
			dbName = dbB
		}
		var sealed []byte
		if err := open(dbName).QueryRow(ctx, `SELECT enc FROM s.secrets WHERE id = $1`, r.id).Scan(&sealed); err != nil {
			t.Fatal(err)
		}
		if plain, err := retired.DecryptWithAAD(sealed, aad(r.tenant, r.id)); err != nil || string(plain) != r.plain {
			t.Fatalf("fila %s sin la llave vieja: %v", r.id, err)
		}
	}
	again, err := TenantPass(ctx, rotating, tenants, 2, time.Minute, zap.NewNop(), target)
	if err != nil || again.Rotated != 0 || again.Pending != 1 {
		t.Fatalf("segunda pasada: %+v %v", again, err)
	}
}

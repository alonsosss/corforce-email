//go:build integration

package postgres

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EffectiveModules lee organization.v_module_catalog y organization.v_tenant_modules. Esta
// prueba comprueba, con datos de varias empresas, que sus dos consultas devuelven lo mismo
// que las que leian las tablas a las que sustituyen, y que el resultado final es el
// esperado para una empresa con estado explicito y otra sin el.

const (
	legacyTenantHasModuleStateSQL = `SELECT EXISTS(SELECT 1 FROM organization.tenant_modules WHERE tenant_id = $1)`

	legacyTenantModuleAvailabilitySQL = `SELECT mc.module,
		        mc.tier = 'core' OR COALESCE(tm.enabled, false) AS enabled,
		        COALESCE(mc.permission_modules, '[]'::jsonb)
		   FROM organization.module_catalog mc
		   LEFT JOIN organization.tenant_modules tm
		     ON tm.module = mc.module AND tm.tenant_id = $1`
)

type moduleGateFixture struct {
	// explicit tiene estado por modulo; implicit ninguno (todo habilitado); disabledOnly
	// solo tiene filas apagadas, y other comparte modulos con explicit en sentido contrario
	// para que un JOIN que no filtre por empresa se note.
	explicit, implicit, disabledOnly, other uuid.UUID
	core, optional, partner, idle         string
	permOptional, permShared              string
}

func seedModuleGate(ctx context.Context, t *testing.T, pool *pgxpool.Pool) *moduleGateFixture {
	t.Helper()
	prefix := "it_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	f := &moduleGateFixture{
		explicit: uuid.New(), implicit: uuid.New(), disabledOnly: uuid.New(), other: uuid.New(),
		core: prefix + "_core", optional: prefix + "_opt", partner: prefix + "_partner", idle: prefix + "_idle",
		permOptional: prefix + "_perm_opt", permShared: prefix + "_perm_shared",
	}
	tenants := []uuid.UUID{f.explicit, f.implicit, f.disabledOnly, f.other}
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = pool.Exec(cctx, `DELETE FROM organization.tenant_modules WHERE tenant_id = ANY($1)`, tenants)
		_, _ = pool.Exec(cctx, `DELETE FROM organization.module_catalog WHERE module LIKE $1`, prefix+"_%")
	})

	// El core sin permission_modules se gatea por su propio nombre; permShared lo reclaman
	// dos modulos, y basta con que uno lo habilite.
	catalog := []struct {
		module, tier, permissionModules string
	}{
		{f.core, "core", `[]`},
		{f.optional, "optional", fmt.Sprintf(`[%q, %q]`, f.permOptional, f.permShared)},
		{f.partner, "optional", fmt.Sprintf(`[%q]`, f.permShared)},
		{f.idle, "optional", `[]`},
	}
	for _, m := range catalog {
		if _, err := pool.Exec(ctx,
			`INSERT INTO organization.module_catalog (module, tier, label, permission_modules) VALUES ($1, $2, 'Prueba', $3::jsonb)`,
			m.module, m.tier, m.permissionModules); err != nil {
			t.Fatalf("catalogo %s: %v", m.module, err)
		}
	}

	state := []struct {
		tenant  uuid.UUID
		module  string
		enabled bool
	}{
		{f.explicit, f.optional, true},
		{f.explicit, f.partner, false},
		{f.explicit, f.core, false},
		{f.explicit, "transactional", true},
		{f.explicit, "corporate_mail", false},
		{f.disabledOnly, f.optional, false},
		{f.other, f.optional, false},
		{f.other, f.partner, true},
		{f.other, f.idle, true},
		{f.other, "corporate_mail", true},
	}
	for _, s := range state {
		if _, err := pool.Exec(ctx,
			`INSERT INTO organization.tenant_modules (tenant_id, module, enabled) VALUES ($1, $2, $3)`,
			s.tenant, s.module, s.enabled); err != nil {
			t.Fatalf("estado de %s en %s: %v", s.module, s.tenant, err)
		}
	}
	return f
}

type moduleRow struct {
	module, permissionModules string
	enabled                   bool
}

func collectModuleRows(t *testing.T, pool *pgxpool.Pool, sql string, tenant uuid.UUID) []moduleRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), sql, tenant)
	if err != nil {
		t.Fatalf("consulta: %v", err)
	}
	defer rows.Close()
	out := []moduleRow{}
	for rows.Next() {
		var r moduleRow
		var raw []byte
		if err := rows.Scan(&r.module, &r.enabled, &raw); err != nil {
			t.Fatal(err)
		}
		r.permissionModules = string(raw)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(out, func(a, b moduleRow) int { return strings.Compare(a.module, b.module) })
	return out
}

func TestVistasDeModulosPublicanSoloLoNecesario(t *testing.T) {
	pool := registryDB(t)
	for view, want := range map[string][]string{
		"v_module_catalog": {"module", "tier", "permission_modules"},
		"v_tenant_modules": {"tenant_id", "module", "enabled"},
	} {
		rows, err := pool.Query(context.Background(),
			`SELECT column_name FROM information_schema.columns
			  WHERE table_schema = 'organization' AND table_name = $1 ORDER BY ordinal_position`, view)
		if err != nil {
			t.Fatal(err)
		}
		var cols []string
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			cols = append(cols, c)
		}
		rows.Close()
		if !slices.Equal(cols, want) {
			t.Fatalf("columnas de %s = %v, se esperaba %v", view, cols, want)
		}
	}
}

func TestModulosPorVistaIgualQueTabla(t *testing.T) {
	ctx := context.Background()
	pool := registryDB(t)
	f := seedModuleGate(ctx, t, pool)

	for name, tenant := range map[string]uuid.UUID{
		"con estado explicito": f.explicit, "sin estado": f.implicit,
		"solo apagados": f.disabledOnly, "otra empresa": f.other,
	} {
		var legacy, got bool
		if err := pool.QueryRow(ctx, legacyTenantHasModuleStateSQL, tenant).Scan(&legacy); err != nil {
			t.Fatalf("%s, estado por la tabla: %v", name, err)
		}
		if err := pool.QueryRow(ctx, tenantHasModuleStateSQL, tenant).Scan(&got); err != nil {
			t.Fatalf("%s, estado por la vista: %v", name, err)
		}
		if got != legacy {
			t.Fatalf("%s: estado explicito por la vista %v, por la tabla %v", name, got, legacy)
		}

		legacyRows := collectModuleRows(t, pool, legacyTenantModuleAvailabilitySQL, tenant)
		gotRows := collectModuleRows(t, pool, tenantModuleAvailabilitySQL, tenant)
		if !slices.Equal(gotRows, legacyRows) {
			t.Fatalf("%s: modulos por la vista %v, por la tabla %v", name, gotRows, legacyRows)
		}
	}
}

func TestEffectiveModulesPorVista(t *testing.T) {
	ctx := context.Background()
	pool := registryDB(t)
	f := seedModuleGate(ctx, t, pool)
	repo := NewTenantModuleGateRepo(pool)

	implicit, err := repo.EffectiveModules(ctx, f.implicit)
	if err != nil {
		t.Fatalf("empresa sin estado: %v", err)
	}
	if implicit.Restricted || len(implicit.Gated) != 0 || !implicit.Allows(f.permOptional) {
		t.Fatalf("empresa sin estado: %+v; se esperaba sin restriccion", implicit)
	}

	// Solo se comparan los modulos de la prueba y los de la semilla que la empresa toca: el
	// catalogo es comun y otras migraciones pueden anadirle modulos.
	cases := map[string]struct {
		tenant uuid.UUID
		want   map[string]bool
	}{
		"con estado explicito": {f.explicit, map[string]bool{
			f.core: true, f.permOptional: true, f.permShared: true,
			"transactional": true, "templates": true, "domains": false, "mailboxes": false,
		}},
		"solo apagados": {f.disabledOnly, map[string]bool{
			f.core: true, f.permOptional: false, f.permShared: false, "transactional": false,
		}},
		"otra empresa": {f.other, map[string]bool{
			f.core: true, f.permOptional: false, f.permShared: true, f.idle: true, "domains": true,
		}},
	}
	for name, tc := range cases {
		got, err := repo.EffectiveModules(ctx, tc.tenant)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !got.Restricted {
			t.Fatalf("%s: se esperaba restringida", name)
		}
		for module, enabled := range tc.want {
			value, gated := got.Gated[module]
			if !gated || value != enabled {
				t.Fatalf("%s: %s gateado=%v habilitado=%v, se esperaba habilitado=%v (resultado %v)",
					name, module, gated, value, enabled, slices.Sorted(maps.Keys(got.Gated)))
			}
		}
		if _, gated := got.Gated["identity"]; gated {
			t.Fatalf("%s: un modulo que ningun modulo del catalogo reclama no se gatea", name)
		}
	}
}

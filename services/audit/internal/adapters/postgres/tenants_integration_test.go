//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// registryView monta en la base desechable la vista publicada del registro tal como la deja la
// migracion 031 de organization (las columnas que lee RegistryTenants), sobre una tabla de la
// prueba: lo que se prueba es la consulta de audit, no el registro.
func registryView(t *testing.T, admin *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for _, sql := range []string{
		`CREATE SCHEMA IF NOT EXISTS organization`,
		`DROP VIEW IF EXISTS organization.v_tenant_routing`,
		`DROP TABLE IF EXISTS organization.it_tenants`,
		`CREATE TABLE organization.it_tenants (id uuid PRIMARY KEY, slug text, status text NOT NULL, db_name text NOT NULL)`,
		`CREATE VIEW organization.v_tenant_routing AS
		     SELECT id AS tenant_id, slug, status, db_name, NULL::text AS cell_code, NULL::text AS db_host, NULL::int AS db_port
		       FROM organization.it_tenants`,
	} {
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, `DROP VIEW IF EXISTS organization.v_tenant_routing`)
		_, _ = admin.Exec(ctx, `DROP TABLE IF EXISTS organization.it_tenants`)
	})
}

func TestElDirectorioListaSoloLasEmpresasActivasConSuSlug(t *testing.T) {
	e := setup(t)
	registryView(t, e.admin)
	active, paused, unnamed := uuid.New(), uuid.New(), uuid.New()
	for _, row := range []struct {
		id     uuid.UUID
		slug   *string
		status string
	}{
		{active, ptr("acme"), "active"},
		{paused, ptr("pausada"), "suspended"},
		{unnamed, nil, "active"},
	} {
		e.tamper(t, `INSERT INTO organization.it_tenants (id, slug, status, db_name) VALUES ($1, $2, $3, 'mail_tenant_x')`, row.id, row.slug, row.status)
	}
	got, err := NewRegistryTenants(e.admin).ActiveTenants(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("empresas: %+v", got)
	}
	bySlug := map[uuid.UUID]string{}
	for _, tr := range got {
		bySlug[tr.ID] = tr.Slug
	}
	if bySlug[active] != "acme" {
		t.Fatalf("acme: %+v", got)
	}
	// Un slug nulo no rompe la lectura: el informe lo escribe como "-".
	if slug, ok := bySlug[unnamed]; !ok || slug != "" {
		t.Fatalf("sin slug: %+v", got)
	}
	if _, ok := bySlug[paused]; ok {
		t.Fatal("una empresa suspendida no entra en el informe")
	}
}

func TestElDirectorioSinVistaFallaEnVezDeDarCero(t *testing.T) {
	e := setup(t)
	if _, err := e.admin.Exec(context.Background(), `DROP VIEW IF EXISTS organization.v_tenant_routing`); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRegistryTenants(e.admin).ActiveTenants(context.Background()); err == nil {
		t.Fatal("sin la vista del registro el informe no puede dar cero empresas como si fuera verdad")
	}
}

//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Indice global de dominios contra Postgres real (migracion 029, aplicada dos veces por
// organizationDB): reclamo idempotente, unicidad entre empresas de celdas distintas tambien con
// reclamos simultaneos, empresa desconocida, retirada solo por la duena, forma del nombre y
// caida con la empresa.

// empresaDePrueba registra una empresa en su propia celda y la retira al terminar.
func empresaDePrueba(t *testing.T, pool *pgxpool.Pool, prefix string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	suffix := itSuffix()
	var cellID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO organization.cells (code, region, db_host, db_port) VALUES ($1, 'it', '127.0.0.1', 5432) RETURNING id`,
		prefix+"-"+suffix).Scan(&cellID); err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO organization.tenants (id, slug, name, db_name, status, cell_id) VALUES ($1, $2, $2, $3, 'active', $4)`,
		id, prefix+"-"+suffix, "mail_tenant_"+prefix+"_"+suffix, cellID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = pool.Exec(cctx, `DELETE FROM organization.tenants WHERE id = $1`, id)
		_, _ = pool.Exec(cctx, `DELETE FROM organization.cells WHERE id = $1`, cellID)
	})
	return id
}

func TestIndiceDeDominiosContraPostgres(t *testing.T) {
	pool, _ := organizationDB(t)
	ctx := context.Background()
	repo := NewMailDomainRepo(pool)
	acme, beta := empresaDePrueba(t, pool, "acme"), empresaDePrueba(t, pool, "beta")
	name := "beta-" + itSuffix() + ".test"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM organization.mail_domain_cells WHERE domain = $1`, name)
	})
	sellos := func() (created, updated time.Time) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT created_at, updated_at FROM organization.mail_domain_cells WHERE domain = $1`, name).
			Scan(&created, &updated); err != nil {
			t.Fatal(err)
		}
		return created, updated
	}

	if err := repo.Claim(ctx, name, beta); err != nil {
		t.Fatalf("reclamo: %v", err)
	}
	created, updated := sellos()
	if err := repo.Claim(ctx, name, beta); err != nil {
		t.Fatalf("reclamo repetido: %v", err)
	}
	created2, updated2 := sellos()
	if !created2.Equal(created) || !updated2.After(updated) {
		t.Fatalf("el reclamo repetido solo confirma: creado %v -> %v, confirmado %v -> %v", created, created2, updated, updated2)
	}
	if err := repo.Claim(ctx, name, acme); !errors.Is(err, domain.ErrMailDomainClaimed) {
		t.Fatalf("otra empresa de otra celda: %v", err)
	}
	if owner, err := repo.TenantOf(ctx, name); err != nil || owner != beta {
		t.Fatalf("duena: %v %v", owner, err)
	}
	if err := repo.Claim(ctx, "otro-"+itSuffix()+".test", uuid.New()); !errors.Is(err, domain.ErrTenantNotFound) {
		t.Fatalf("empresa desconocida: %v", err)
	}
	if _, err := repo.TenantOf(ctx, "nadie-"+itSuffix()+".test"); !errors.Is(err, domain.ErrMailDomainNotFound) {
		t.Fatalf("dominio desconocido: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO organization.mail_domain_cells (domain, tenant_id) VALUES ('Mayusculas.Test', $1)`, beta); err == nil {
		t.Fatal("el indice guarda el nombre normalizado: una mayuscula se rechaza")
	}

	if released, err := repo.Release(ctx, name, acme); err != nil || released {
		t.Fatalf("soltar lo que no es suyo: %v %v", released, err)
	}
	if released, err := repo.Release(ctx, name, beta); err != nil || !released {
		t.Fatalf("la duena lo suelta: %v %v", released, err)
	}
	if released, err := repo.Release(ctx, name, beta); err != nil || released {
		t.Fatalf("soltarlo otra vez: %v %v", released, err)
	}

	// Reclamos simultaneos de dos empresas: gana una y la otra recibe siempre el conflicto.
	carrera := "carrera-" + itSuffix() + ".test"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM organization.mail_domain_cells WHERE domain = $1`, carrera)
	})
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]error, 16)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tenant := acme
			if i%2 == 1 {
				tenant = beta
			}
			<-start
			results[i] = repo.Claim(ctx, carrera, tenant)
		}(i)
	}
	close(start)
	wg.Wait()
	winner, err := repo.TenantOf(ctx, carrera)
	if err != nil {
		t.Fatal(err)
	}
	for i, err := range results {
		tenant := acme
		if i%2 == 1 {
			tenant = beta
		}
		switch {
		case tenant == winner && err != nil:
			t.Fatalf("la ganadora recibe error en el intento %d: %v", i, err)
		case tenant != winner && !errors.Is(err, domain.ErrMailDomainClaimed):
			t.Fatalf("la perdedora no recibe el conflicto en el intento %d: %v", i, err)
		}
	}

	// La fila cae con su empresa.
	if _, err := pool.Exec(ctx, `DELETE FROM organization.tenants WHERE id = $1`, winner); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.TenantOf(ctx, carrera); !errors.Is(err, domain.ErrMailDomainNotFound) {
		t.Fatalf("dominio de una empresa borrada: %v", err)
	}
}

// Una empresa con la baja en curso no reclama dominios, tampoco confirma uno suyo, y la saga suelta
// todos los suyos de una vez sin tocar los de otra empresa. Un alta en curso no impide reclamar.
func TestIndiceDeDominiosEnLaBajaDeUnaEmpresa(t *testing.T) {
	pool, _ := organizationDB(t)
	ctx := context.Background()
	repo := NewMailDomainRepo(pool)
	baja, otra := empresaDePrueba(t, pool, "baja"), empresaDePrueba(t, pool, "otra")
	suffix := itSuffix()
	propio, segundo, ajeno, nuevo := "propio-"+suffix+".test", "segundo-"+suffix+".test", "ajeno-"+suffix+".test", "nuevo-"+suffix+".test"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM organization.mail_domain_cells WHERE domain = ANY($1)`,
			[]string{propio, segundo, ajeno, nuevo})
	})
	saga := func(tenant uuid.UUID, operation string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO organization.tenant_sagas (tenant_id, operation, state, step) VALUES ($1, $2, 'running', 'registered')`,
			tenant, operation); err != nil {
			t.Fatal(err)
		}
	}

	for _, name := range []string{propio, segundo} {
		if err := repo.Claim(ctx, name, baja); err != nil {
			t.Fatalf("reclamo de %s: %v", name, err)
		}
	}
	saga(otra, domain.SagaCreate)
	if err := repo.Claim(ctx, ajeno, otra); err != nil {
		t.Fatalf("una empresa con el alta en curso reclama: %v", err)
	}

	saga(baja, domain.SagaDelete)
	for _, name := range []string{nuevo, propio} {
		if err := repo.Claim(ctx, name, baja); !errors.Is(err, domain.ErrTenantBeingRemoved) {
			t.Fatalf("reclamo de %s durante la baja: %v", name, err)
		}
	}
	if err := repo.Claim(ctx, propio, otra); !errors.Is(err, domain.ErrMailDomainClaimed) {
		t.Fatalf("otra empresa reclama un dominio que la baja aun no solto: %v", err)
	}
	if _, err := repo.TenantOf(ctx, nuevo); !errors.Is(err, domain.ErrMailDomainNotFound) {
		t.Fatalf("el reclamo rechazado no deja fila: %v", err)
	}

	if n, err := repo.ReleaseTenant(ctx, baja); err != nil || n != 2 {
		t.Fatalf("la baja suelta sus dominios: %d %v", n, err)
	}
	if n, err := repo.ReleaseTenant(ctx, baja); err != nil || n != 0 {
		t.Fatalf("soltarlos otra vez: %d %v", n, err)
	}
	if owner, err := repo.TenantOf(ctx, ajeno); err != nil || owner != otra {
		t.Fatalf("el dominio de la otra empresa sigue: %v %v", owner, err)
	}
	if err := repo.Claim(ctx, propio, otra); err != nil {
		t.Fatalf("el dominio soltado se puede volver a reclamar: %v", err)
	}
}

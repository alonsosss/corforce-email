//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// La libreta compartida contra Postgres real: solo los buzones activos de la empresa de quien
// pregunta, buscados como texto.
func TestLibretaCompartidaContraPostgres(t *testing.T) {
	dsn := integrationEnv(t, "MAIL_DIRECTORY_TEST_DSN")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	applyCellMigrations(t, ctx, pool)

	uc := newUseCase(&db.ContextPool{})
	tenantA, tenantB := uuid.New(), uuid.New()
	suffix := strings.Split(uuid.New().String(), "-")[0]
	domainA, domainB := "lib-"+suffix+"-a.example", "lib-"+suffix+"-b.example"
	t.Cleanup(func() { cleanup(t, pool, domainA, domainB, tenantA, tenantB) })
	as := func(tenant uuid.UUID) context.Context {
		return middleware.WithIdentity(db.WithPool(ctx, pool), uuid.New().String(), tenant.String())
	}
	ctxA, ctxB := as(tenantA), as(tenantB)
	for _, c := range []struct {
		ctx    context.Context
		tenant uuid.UUID
		domain string
	}{{ctxA, tenantA, domainA}, {ctxB, tenantB, domainB}} {
		if _, err := uc.SetDomainActivation(c.ctx, c.tenant, c.domain, true); err != nil {
			t.Fatalf("activar %s: %v", c.domain, err)
		}
	}
	quota := int64(50 << 20)
	mk := func(c context.Context, tenant uuid.UUID, dom, local, name string) *domain.Mailbox {
		m, err := uc.CreateMailbox(c, tenant, app.CreateMailboxRequest{
			LocalPart: local, Domain: dom, Password: "contrasena-de-prueba-1", DisplayName: name, QuotaBytes: &quota,
		})
		if err != nil {
			t.Fatalf("buzon %s@%s: %v", local, dom, err)
		}
		return m
	}
	ana := mk(ctxA, tenantA, domainA, "ana", "Ana Diaz")
	mk(ctxA, tenantA, domainA, "bea", "Beatriz")
	baja := mk(ctxA, tenantA, domainA, "baja", "Baja")
	mk(ctxB, tenantB, domainB, "eva", "Ana Otra Empresa")
	off := domain.ActiveOff
	if _, err := uc.UpdateMailbox(ctxA, tenantA, baja.ID, app.UpdateMailboxRequest{Active: &off}); err != nil {
		t.Fatalf("apagar buzon: %v", err)
	}

	svc := db.WithPool(ctx, pool)
	all, err := uc.SearchDirectoryByUsername(svc, ana.Username, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	addrs := map[string]string{}
	for _, e := range all {
		addrs[e.Address] = e.DisplayName
	}
	if len(addrs) != 2 || addrs[ana.Username] != "Ana Diaz" || addrs["bea@"+domainA] != "Beatriz" {
		t.Fatalf("la libreta de A debe traer a ana y bea, sin la apagada ni la de B: %+v", all)
	}

	// La busqueda mira direccion y nombre, sin distinguir mayusculas, y sin cruzar de empresa: "Ana"
	// existe tambien en B y no debe salir.
	found, err := uc.SearchDirectoryByUsername(svc, ana.Username, "ANA", 0)
	if err != nil || len(found) != 1 || found[0].Address != ana.Username {
		t.Fatalf("busqueda: %+v %v", found, err)
	}
	if found, _ := uc.SearchDirectoryByUsername(svc, ana.Username, "an%", 0); len(found) != 0 {
		t.Fatalf("un %% se busca como texto: %+v", found)
	}
	if limited, _ := uc.SearchDirectoryByUsername(svc, ana.Username, "", 1); len(limited) != 1 {
		t.Fatalf("el tope se aplica: %+v", limited)
	}
	// Quien esta apagado no puede pedir la libreta: Locate solo encuentra buzones activos.
	if _, err := uc.SearchDirectoryByUsername(svc, baja.Username, "", 0); err == nil {
		t.Fatal("un buzon apagado no debe poder consultar la libreta")
	}
}

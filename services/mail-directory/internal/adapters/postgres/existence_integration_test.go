//go:build integration

package postgres

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// La consulta de existencia de buzones (conciliacion de los servicios que guardan datos por id de buzon) bajo
// el rol y las politicas de fila del servicio: devuelve solo los buzones de la empresa de la sesion, aunque
// el id que se pregunta sea de otra, y un buzon borrado deja de volver.
func TestExistenciaDeBuzonesEstaAcotadaPorEmpresa(t *testing.T) {
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
	domA, domB := "ex-a-"+suffix+".example", "ex-b-"+suffix+".example"
	t.Cleanup(func() { cleanup(t, pool, domA, domA, tenantA) })
	t.Cleanup(func() { cleanup(t, pool, domB, domB, tenantB) })
	actxA := middleware.WithIdentity(db.WithPool(ctx, pool), uuid.New().String(), tenantA.String())
	actxB := middleware.WithIdentity(db.WithPool(ctx, pool), uuid.New().String(), tenantB.String())

	mailbox := func(actx context.Context, tenant uuid.UUID, dom, local string) uuid.UUID {
		t.Helper()
		if _, err := uc.SetDomainActivation(actx, tenant, dom, true); err != nil {
			t.Fatalf("activar %s: %v", dom, err)
		}
		mb, err := uc.CreateMailbox(actx, tenant, app.CreateMailboxRequest{LocalPart: local, Domain: dom, Password: "contrasena-de-prueba-1"})
		if err != nil {
			t.Fatalf("alta de buzon: %v", err)
		}
		return mb.ID
	}
	a1 := mailbox(actxA, tenantA, domA, "uno")
	mb2, err := uc.CreateMailbox(actxA, tenantA, app.CreateMailboxRequest{LocalPart: "dos", Domain: domA, Password: "contrasena-de-prueba-1"})
	if err != nil {
		t.Fatal(err)
	}
	a2 := mb2.ID
	b1 := mailbox(actxB, tenantB, domB, "uno")
	unknown := uuid.New()

	sorted := func(ids []uuid.UUID) []string {
		out := make([]string, len(ids))
		for i, id := range ids {
			out[i] = id.String()
		}
		sort.Strings(out)
		return out
	}
	same := func(got []uuid.UUID, want ...uuid.UUID) bool {
		g, w := sorted(got), sorted(want)
		if len(g) != len(w) {
			return false
		}
		for i := range g {
			if g[i] != w[i] {
				return false
			}
		}
		return true
	}

	got, err := uc.ExistingMailboxIDs(actxA, tenantA, []uuid.UUID{a1, b1, unknown})
	if err != nil {
		t.Fatal(err)
	}
	if !same(got, a1) {
		t.Fatalf("la empresa A solo ve el suyo: %v", got)
	}
	got, err = uc.ExistingMailboxIDs(actxB, tenantB, []uuid.UUID{a1, b1, unknown})
	if err != nil || !same(got, b1) {
		t.Fatalf("la empresa B solo ve el suyo: %v %v", got, err)
	}

	if err := uc.DeleteMailbox(actxA, tenantA, a1); err != nil {
		t.Fatal(err)
	}
	got, err = uc.ExistingMailboxIDs(actxA, tenantA, []uuid.UUID{a1, a2})
	if err != nil || !same(got, a2) {
		t.Fatalf("un buzon borrado no debe volver y el otro si: %v %v", got, err)
	}
}

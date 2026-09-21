package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
)

type fakeIndex struct {
	ids       []uuid.UUID
	err       error
	gotTenant uuid.UUID
	gotBefore time.Time
	gotAfter  uuid.UUID
	gotLimit  int
	calls     int
}

func (f *fakeIndex) StaleMailboxIDs(_ context.Context, tenantID uuid.UUID, before time.Time, after uuid.UUID, limit int) ([]uuid.UUID, error) {
	f.calls++
	f.gotTenant, f.gotBefore, f.gotAfter, f.gotLimit = tenantID, before, after, limit
	return f.ids, f.err
}

func reconcileUseCase(t *testing.T, index *fakeIndex, binder apptest.Binder) (*app.UseCase, *apptest.Store) {
	t.Helper()
	store := apptest.NewStore()
	uc, err := app.New(app.Deps{Auth: apptest.NewAuth(ana, bea, cris), Tenant: binder, Store: store, Calendars: store, Index: index, Config: testConfig})
	if err != nil {
		t.Fatal(err)
	}
	return uc, store
}

func TestLaEnumeracionPasaLaEmpresaLaGraciaYLaPaginaAlIndice(t *testing.T) {
	want := []uuid.UUID{uuid.New(), uuid.New()}
	index := &fakeIndex{ids: want}
	uc, _ := reconcileUseCase(t, index, apptest.Binder{})
	before, after := time.Now().Add(-24*time.Hour), uuid.New()

	got, err := uc.StaleMailboxes(context.Background(), ana.Principal.TenantID, before, after, 50)
	if err != nil || len(got) != 2 {
		t.Fatalf("StaleMailboxes: %v %v", got, err)
	}
	if index.gotTenant != ana.Principal.TenantID || !index.gotBefore.Equal(before) || index.gotAfter != after || index.gotLimit != 50 {
		t.Fatalf("consulta al indice: %+v", index)
	}
}

func TestLaEnumeracionExigeEmpresaYIndice(t *testing.T) {
	index := &fakeIndex{}
	uc, _ := reconcileUseCase(t, index, apptest.Binder{})
	if _, err := uc.StaleMailboxes(context.Background(), uuid.Nil, time.Now(), uuid.Nil, 10); !errors.Is(err, domain.ErrInvalidMailbox) || index.calls != 0 {
		t.Fatalf("empresa nula: %v (consultas %d)", err, index.calls)
	}

	store := apptest.NewStore()
	noIndex, err := app.New(app.Deps{Auth: apptest.NewAuth(ana), Tenant: apptest.Binder{}, Store: store, Calendars: store, Config: testConfig})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := noIndex.StaleMailboxes(context.Background(), ana.Principal.TenantID, time.Now(), uuid.Nil, 10); err == nil {
		t.Fatal("sin indice no hay enumeracion")
	}
}

func TestLaEnumeracionNoSeHaceSiNoSePuedeAbrirLaBaseDeLaEmpresa(t *testing.T) {
	index := &fakeIndex{}
	uc, _ := reconcileUseCase(t, index, apptest.Binder{Err: domain.ErrTenantUnknown})
	if _, err := uc.StaleMailboxes(context.Background(), ana.Principal.TenantID, time.Now(), uuid.Nil, 10); !errors.Is(err, domain.ErrTenantUnknown) || index.calls != 0 {
		t.Fatalf("err = %v, consultas %d", err, index.calls)
	}
}

func TestReconciliarUnBuzonRetiraLoMismoQueElConsumidorYCuentaLasColecciones(t *testing.T) {
	uc, _ := reconcileUseCase(t, &fakeIndex{}, apptest.Binder{})
	seedBook(t, uc, ana.Principal, "personal", "a1")
	seedBook(t, uc, ana.Principal, "familia", "a2")
	seedBook(t, uc, cris.Principal, "personal", "c1")

	removed, err := uc.ReconcileMailbox(context.Background(), ana.Principal.TenantID, ana.Principal.MailboxID)
	if err != nil || removed != 2 {
		t.Fatalf("ReconcileMailbox: %d %v", removed, err)
	}
	for _, slug := range []string{"personal", "familia"} {
		if _, _, err := uc.Contacts(context.Background(), ana.Principal, slug, true); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("la libreta %s sigue: %v", slug, err)
		}
	}
	if _, contacts, err := uc.Contacts(context.Background(), cris.Principal, "personal", true); err != nil || len(contacts) != 1 {
		t.Fatalf("otro buzon perdio sus contactos: %v %d", err, len(contacts))
	}
	if removed, err := uc.ReconcileMailbox(context.Background(), ana.Principal.TenantID, ana.Principal.MailboxID); err != nil || removed != 0 {
		t.Fatalf("es idempotente: %d %v", removed, err)
	}
}

func TestReconciliarExigeEmpresaYBuzon(t *testing.T) {
	uc, _ := reconcileUseCase(t, &fakeIndex{}, apptest.Binder{})
	for name, ids := range map[string][2]uuid.UUID{"empresa nula": {uuid.Nil, uuid.New()}, "buzon nulo": {uuid.New(), uuid.Nil}} {
		if _, err := uc.ReconcileMailbox(context.Background(), ids[0], ids[1]); !errors.Is(err, domain.ErrInvalidMailbox) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

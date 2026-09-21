package http

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
)

type dirMailboxes struct {
	ports.MailboxRepository
	tenant uuid.UUID
	filter ports.MailboxFilter
	page   ports.Page
}

func (f *dirMailboxes) List(_ context.Context, tenantID uuid.UUID, filter ports.MailboxFilter, page ports.Page) ([]domain.Mailbox, int64, error) {
	f.tenant, f.filter, f.page = tenantID, filter, page
	return []domain.Mailbox{
		{Username: "ana@acme.test", DisplayName: "Ana Diaz", QuotaBytes: 999, PasswordHash: "secreto"},
		{Username: "bea@acme.test"},
	}, 2, nil
}

func directoryServer(t *testing.T) (http.Handler, *dirMailboxes, *domain.Mailbox) {
	t.Helper()
	m := &domain.Mailbox{ID: uuid.New(), TenantID: uuid.New(), Username: "ana@acme.test"}
	repo := &dirMailboxes{}
	uc := app.New(app.Deps{Tx: vacTx{}, Mailboxes: repo, Retirements: vacRetirements{}, Locator: &vacLocator{m: m}})
	return NewHandler(uc, authz.NewChecker("http://127.0.0.1:9", "")).Routes(), repo, m
}

func TestLibretaInternaDevuelveSoloDireccionYNombre(t *testing.T) {
	h, repo, m := directoryServer(t)
	rec := doVacation(h, http.MethodGet, "/internal/mail-directory/directory?username=Ana@Acme.TEST&q=be&limit=7", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var env struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data) != 2 || env.Data[0]["address"] != "ana@acme.test" || env.Data[0]["display_name"] != "Ana Diaz" {
		t.Fatalf("cuerpo: %s", rec.Body)
	}
	for _, e := range env.Data {
		if len(e) != 2 {
			t.Fatalf("solo direccion y nombre, llego %v", e)
		}
	}
	if repo.tenant != m.TenantID || repo.filter.Search != "be" || !repo.filter.ActiveOnly || repo.page.Limit != 7 {
		t.Fatalf("consulta al repositorio: %v %+v %+v", repo.tenant, repo.filter, repo.page)
	}
}

func TestLibretaInternaRechazaUnLimiteInvalidoYUnBuzonDesconocido(t *testing.T) {
	h, _, _ := directoryServer(t)
	for _, limit := range []string{"0", "-1", "abc"} {
		if rec := doVacation(h, http.MethodGet, "/internal/mail-directory/directory?username=ana@acme.test&limit="+limit, ""); rec.Code != http.StatusBadRequest {
			t.Fatalf("limit=%s: %d", limit, rec.Code)
		}
	}
	if rec := doVacation(h, http.MethodGet, "/internal/mail-directory/directory?username=nadie@acme.test", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("buzon desconocido: %d %s", rec.Code, rec.Body)
	}
}

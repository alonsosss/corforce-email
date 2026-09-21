package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
)

type existenceMailboxes struct {
	ports.MailboxRepository
	byTenant map[uuid.UUID]map[uuid.UUID]bool
	asked    []uuid.UUID
}

func (f *existenceMailboxes) ExistingIDs(_ context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error) {
	f.asked = ids
	var out []uuid.UUID
	for _, id := range ids {
		if f.byTenant[tenantID][id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func existenceServer(t *testing.T, mailboxes *existenceMailboxes) http.Handler {
	t.Helper()
	uc := app.New(app.Deps{Tx: vacTx{}, Mailboxes: mailboxes})
	return middleware.InjectFromGateway(NewHandler(uc, authz.NewChecker("http://127.0.0.1:9", "")).Routes())
}

func doExistence(h http.Handler, tenantID, userID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/internal/mail-directory/mailboxes/existence", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if tenantID != "" {
		req.Header.Set("X-Tenant-ID", tenantID)
	}
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func idsBody(ids ...uuid.UUID) string {
	raw := make([]string, len(ids))
	for i, id := range ids {
		raw[i] = id.String()
	}
	b, _ := json.Marshal(map[string]any{"ids": raw})
	return string(b)
}

func TestExistenciaDevuelveSoloLosBuzonesDeLaEmpresaDeLaPeticion(t *testing.T) {
	tenant, other := uuid.New(), uuid.New()
	mine, gone, foreign := uuid.New(), uuid.New(), uuid.New()
	repo := &existenceMailboxes{byTenant: map[uuid.UUID]map[uuid.UUID]bool{
		tenant: {mine: true}, other: {foreign: true},
	}}
	h := existenceServer(t, repo)

	rec := doExistence(h, tenant.String(), "", idsBody(mine, gone, foreign))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Data struct {
			Existing []uuid.UUID `json:"existing"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data.Existing) != 1 || out.Data.Existing[0] != mine {
		t.Fatalf("existentes: %v (el borrado y el de otra empresa no deben volver)", out.Data.Existing)
	}
}

func TestExistenciaSinIdsResponde200VacioSinConsultarLaBase(t *testing.T) {
	repo := &existenceMailboxes{}
	rec := doExistence(existenceServer(t, repo), uuid.NewString(), "", `{"ids":[]}`)
	if rec.Code != http.StatusOK || repo.asked != nil {
		t.Fatalf("status %d, consulta %v", rec.Code, repo.asked)
	}
	if !strings.Contains(rec.Body.String(), `"existing":[]`) {
		t.Fatalf("debe ser una lista vacia y no null: %s", rec.Body)
	}
}

func TestExistenciaCierraLaRutaALasPersonasYExigeEmpresa(t *testing.T) {
	h := existenceServer(t, &existenceMailboxes{})
	if rec := doExistence(h, uuid.NewString(), uuid.NewString(), idsBody(uuid.New())); rec.Code != http.StatusForbidden {
		t.Fatalf("con usuario: %d", rec.Code)
	}
	if rec := doExistence(h, "", "", idsBody(uuid.New())); rec.Code != http.StatusUnauthorized {
		t.Fatalf("sin empresa: %d", rec.Code)
	}
}

func TestExistenciaRechazaCuerposInvalidosYDemasiadosIds(t *testing.T) {
	h := existenceServer(t, &existenceMailboxes{})
	tenant := uuid.NewString()
	tooMany := make([]uuid.UUID, app.MaxExistenceIDs+1)
	for i := range tooMany {
		tooMany[i] = uuid.New()
	}
	cases := map[string]struct {
		body string
		want int
	}{
		"mas del maximo": {idsBody(tooMany...), http.StatusUnprocessableEntity},
		"id nulo":        {idsBody(uuid.Nil), http.StatusUnprocessableEntity},
		"id no uuid":     {`{"ids":["no-es-un-id"]}`, http.StatusBadRequest},
		"campo de mas":   {`{"ids":[],"tenant_id":"x"}`, http.StatusBadRequest},
		"no es json":     {`ids`, http.StatusBadRequest},
		"cuerpo enorme":  {`{"ids":["` + strings.Repeat("a", 70<<10) + `"]}`, http.StatusBadRequest},
	}
	for name, tc := range cases {
		if rec := doExistence(h, tenant, "", tc.body); rec.Code != tc.want {
			t.Errorf("%s: status %d, quiero %d: %s", name, rec.Code, tc.want, rec.Body)
		}
	}
}

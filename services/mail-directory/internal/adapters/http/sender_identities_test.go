package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
)

type fakeSenders struct {
	username string
	limit    int
	out      []string
	err      error
}

func (f *fakeSenders) ForLogin(_ context.Context, username string, limit int) ([]string, error) {
	f.username, f.limit = username, limit
	return f.out, f.err
}

func senderIdentitiesRequest(t *testing.T, repo *fakeSenders, username string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewHandler(app.New(app.Deps{Senders: repo}), authz.NewChecker("http://127.0.0.1:9", ""))
	req := httptest.NewRequest(http.MethodGet, "/internal/mail-directory/sender-identities?username="+username, nil)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	return rec
}

func TestRemitentesDelBuzonNormalizaYAcota(t *testing.T) {
	repo := &fakeSenders{out: []string{"ana@empresa.pe", "ventas@empresa.pe"}}
	rec := senderIdentitiesRequest(t, repo, "%20Ana@Empresa.PE")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if repo.username != "ana@empresa.pe" || repo.limit != app.MaxSenderIdentities {
		t.Fatalf("consulta: %q limite %d", repo.username, repo.limit)
	}
	var env struct {
		Data struct {
			Addresses []string `json:"addresses"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(env.Data.Addresses, repo.out) {
		t.Fatalf("direcciones: %v", env.Data.Addresses)
	}
}

func TestRemitentesDelBuzonRechazaNombreInvalido(t *testing.T) {
	repo := &fakeSenders{}
	for _, username := range []string{"", "sin-arroba", "%40empresa.pe"} {
		if rec := senderIdentitiesRequest(t, repo, username); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%q: %d", username, rec.Code)
		}
	}
	if repo.username != "" {
		t.Fatal("un nombre invalido no llega a la base")
	}
}

func TestRemitentesDelBuzonFalloDeLaBase(t *testing.T) {
	rec := senderIdentitiesRequest(t, &fakeSenders{err: errors.New("conexion rechazada")}, "ana@empresa.pe")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
}

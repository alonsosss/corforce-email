package http

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/services/templates/internal/app"
	"github.com/alonsosss/corforce-email/services/templates/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/alonsosss/corforce-email/services/templates/internal/ports"
	"github.com/google/uuid"
)

type stubTestSender struct {
	err error
	got ports.TestSendRequest
}

func (s *stubTestSender) SendTest(_ context.Context, _ uuid.UUID, req ports.TestSendRequest) (*ports.TestSendResult, error) {
	s.got = req
	if s.err != nil {
		return nil, s.err
	}
	return &ports.TestSendResult{
		Messages:   []ports.TestSendMessage{{ID: uuid.New(), Status: "queued", Email: req.To[0]}},
		Suppressed: []ports.TestSendSuppressed{},
	}, nil
}

func newTestSendAPI(t *testing.T, sender ports.TestSender) (*editorAPI, uuid.UUID) {
	t.Helper()
	deps := app.Deps{Repo: apptest.NewRepo(), Tx: &apptest.Tx{}, Renderer: &apptest.Renderer{}, Events: &apptest.Events{}}
	if sender != nil {
		deps.TestSender = sender
	}
	uc := app.New(deps)
	a := &editorAPI{routes: NewHandler(uc, authz.NewChecker(unreachableAccessControl, "test")).Routes(), uc: uc, tenant: uuid.New(), user: uuid.New()}
	tpl, _, err := uc.CreateTemplate(context.Background(), a.tenant, a.user, app.CreateTemplateInput{
		Name: "bienvenida", Kind: domain.KindTransactional, Content: domain.Content{Subject: "Hola", HTML: "<p>hola</p>"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return a, tpl.ID
}

func TestTestSendVersionContract(t *testing.T) {
	sender := &stubTestSender{}
	a, id := newTestSendAPI(t, sender)
	path := "/" + id.String() + "/versions/1/test-send"
	body := []byte(`{"from":{"email":"hola@acme.test","name":"Acme"},"to":["qa@example.com"],"variables":{"name":"Ana"}}`)

	rec := a.do(http.MethodPost, path, "application/json", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("prueba aceptada: %d %s", rec.Code, rec.Body)
	}
	var env struct {
		Data ports.TestSendResult `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || len(env.Data.Messages) != 1 || env.Data.Messages[0].Email != "qa@example.com" {
		t.Fatalf("respuesta: %s", rec.Body)
	}
	if sender.got.RequestedBy != a.user || sender.got.FromName != "Acme" || string(sender.got.Variables["name"]) != `"Ana"` {
		t.Fatalf("peticion a transactional: %+v", sender.got)
	}

	if rec := a.do(http.MethodPost, path, "application/json", []byte(`{"to":["qa@example.com"]}`)); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("sin remitente: %d", rec.Code)
	}
	six := []byte(`{"from":{"email":"hola@acme.test"},"to":["a@x.test","b@x.test","c@x.test","d@x.test","e@x.test","f@x.test"]}`)
	if rec := a.do(http.MethodPost, path, "application/json", six); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("mas de cinco: %d", rec.Code)
	}
	if rec := a.do(http.MethodPost, "/"+id.String()+"/versions/7/test-send", "application/json", body); rec.Code != http.StatusNotFound {
		t.Fatalf("version inexistente: %d", rec.Code)
	}
}

// Un rechazo de transactional llega con su estado, su codigo y su espera.
func TestTestSendVersionRelaysTransactionalRejection(t *testing.T) {
	sender := &stubTestSender{err: &domain.TestSendRejectedError{
		Status: http.StatusTooManyRequests, Code: "TEST_SEND_LIMIT_REACHED", Message: "tope", RetryAfter: "60",
	}}
	a, id := newTestSendAPI(t, sender)
	rec := a.do(http.MethodPost, "/"+id.String()+"/versions/1/test-send", "application/json",
		[]byte(`{"from":{"email":"hola@acme.test"},"to":["qa@example.com"]}`))
	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "TEST_SEND_LIMIT_REACHED" || rec.Header().Get("Retry-After") != "60" {
		t.Fatalf("rechazo reenviado: %d %s %q", rec.Code, rec.Body, rec.Header().Get("Retry-After"))
	}
}

func TestTestSendVersionWithoutTransactional(t *testing.T) {
	a, id := newTestSendAPI(t, nil)
	rec := a.do(http.MethodPost, "/"+id.String()+"/versions/1/test-send", "application/json",
		[]byte(`{"from":{"email":"hola@acme.test"},"to":["qa@example.com"]}`))
	if rec.Code != http.StatusServiceUnavailable || errorCode(t, rec) != "TEST_SEND_UNAVAILABLE" {
		t.Fatalf("sin transactional: %d %s", rec.Code, rec.Body)
	}
}

func TestInternalRenderDePruebaExigeVersion(t *testing.T) {
	a, id := newTestSendAPI(t, nil)
	internal := NewHandler(a.uc, authz.NewChecker(unreachableAccessControl, "test")).InternalRoutes()
	a.routes = internal
	rec := a.do(http.MethodPost, "/"+id.String()+"/render", "application/json", []byte(`{"variables":{},"test":true}`))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("render de prueba sin version: %d %s", rec.Code, rec.Body)
	}
	rec = a.do(http.MethodPost, "/"+id.String()+"/render", "application/json", []byte(`{"version":1,"variables":{},"test":true}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("render de prueba de un borrador: %d %s", rec.Code, rec.Body)
	}
	rec = a.do(http.MethodPost, "/"+id.String()+"/render", "application/json", []byte(`{"version":1,"variables":{}}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("un render real de un borrador se rechaza: %d %s", rec.Code, rec.Body)
	}
}

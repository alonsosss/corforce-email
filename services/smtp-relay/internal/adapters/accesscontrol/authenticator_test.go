package accesscontrol

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/apikey"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/smtp-relay/internal/domain"
)

type stubResolver struct {
	p   *apikey.Principal
	err error
}

func (s stubResolver) Resolve(context.Context, string, string) (*apikey.Principal, error) {
	return s.p, s.err
}

func TestAuthenticate(t *testing.T) {
	if domain.TokenPrefix != apikey.TokenPrefix {
		t.Fatal("el relay y pkg/apikey deben reconocer el mismo formato de token")
	}
	send := middleware.APIKeyScope{Module: domain.SendModule, Resource: domain.SendResource, Action: domain.SendAction}
	read := middleware.APIKeyScope{Module: "transactional", Resource: "messages", Action: "read"}

	cred, err := New(stubResolver{p: &apikey.Principal{ID: "k", TenantID: "t", Prefix: "p", Scopes: []middleware.APIKeyScope{read, send}}}).
		Authenticate(context.Background(), "cfm_p_x", "198.51.100.7")
	if err != nil || cred.KeyID != "k" || cred.TenantID != "t" || cred.Token != "cfm_p_x" {
		t.Fatalf("clave con envio: %+v %v", cred, err)
	}
	if _, err := New(stubResolver{p: &apikey.Principal{ID: "k", TenantID: "t", Scopes: []middleware.APIKeyScope{read}}}).
		Authenticate(context.Background(), "cfm_p_x", ""); !errors.Is(err, domain.ErrAuthFailed) {
		t.Fatalf("una clave solo de lectura no envia por SMTP: %v", err)
	}
	if _, err := New(stubResolver{err: apikey.ErrInvalid}).Authenticate(context.Background(), "cfm_p_x", ""); !errors.Is(err, domain.ErrAuthFailed) {
		t.Fatalf("clave invalida: %v", err)
	}
	if _, err := New(stubResolver{err: apikey.ErrUnavailable}).Authenticate(context.Background(), "cfm_p_x", ""); !errors.Is(err, domain.ErrAuthUnavailable) {
		t.Fatalf("access-control caido: %v", err)
	}
}

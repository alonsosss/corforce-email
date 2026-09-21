package http

import (
	"net/http"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-auth/internal/domain"
	"github.com/google/uuid"
)

// migration-verify.lua envia el mismo cuerpo que passwd-verify.lua con service "migration".
const jobLuaBody = `{"username":"ana@empresa.pe","password":"cfmj1.token","real_rip":"172.22.2.5","service":"migration"}`

func TestVerifyDeUnaCredencialDeTrabajoSoloDevuelveSuccess(t *testing.T) {
	stub := &stubVerifier{result: domain.ResultOK, displayName: "Ana", tenantID: uuid.New(), mailboxID: uuid.New()}
	rec := post(t, NewHandler(stub).VerifyRoutes(), "/", jobLuaBody)
	if rec.Code != http.StatusOK || !decodeSuccess(t, rec) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if stub.got == nil || stub.got.Service != "migration" || stub.got.RemoteIP != "172.22.2.5" || stub.got.Password != "cfmj1.token" {
		t.Fatalf("peticion al caso de uso: %+v", stub.got)
	}
	for _, leak := range []string{"display_name", "tenant_id", "mailbox_id", "username", "Ana"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Fatalf("la respuesta a Dovecot expone %q: %s", leak, rec.Body)
		}
	}
}

func TestVerifyDeUnaCredencialDeTrabajoRechazadaEs401SinDetalle(t *testing.T) {
	for _, result := range []domain.Result{domain.ResultBadPassword, domain.ResultForbiddenNetwork, domain.ResultJobCredentialsDisabled, domain.ResultInactive, domain.ResultThrottled, domain.ResultError} {
		rec := post(t, NewHandler(&stubVerifier{result: result}).VerifyRoutes(), "/", jobLuaBody)
		if rec.Code != http.StatusUnauthorized || decodeSuccess(t, rec) {
			t.Errorf("%s: status %d: %s", result, rec.Code, rec.Body)
		}
		if strings.TrimSpace(rec.Body.String()) != `{"success":false}` {
			t.Errorf("%s: la respuesta dice mas de lo debido: %s", result, rec.Body)
		}
	}
}

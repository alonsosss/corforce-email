package migrationcli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-auth/internal/domain"
	"github.com/google/uuid"
)

const gatewayToken = "token-de-gateway-de-prueba"

func serve(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(srv.URL, gatewayToken)
}

func TestVerificaContraMailMigrationConElTokenDeGatewayYSinRepetirLaCredencial(t *testing.T) {
	tenant, mailbox, job := uuid.New(), uuid.New(), uuid.New()
	var gotBody map[string]string
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/mail-migration/credentials/verify" || r.Header.Get("X-Gateway-Token") != gatewayToken {
			t.Errorf("peticion inesperada: %s %s %v", r.Method, r.URL.Path, r.Header)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{
			"tenant_id": tenant.String(), "job_id": job.String(), "mailbox_id": mailbox.String(), "username": "ana@acme.test",
		}})
	})
	cred, err := c.Verify(context.Background(), "cfmj1.token", "ana@acme.test")
	if err != nil {
		t.Fatal(err)
	}
	if cred.TenantID != tenant || cred.MailboxID != mailbox || cred.JobID != job {
		t.Fatalf("credencial: %+v", cred)
	}
	if gotBody["token"] != "cfmj1.token" || gotBody["username"] != "ana@acme.test" {
		t.Fatalf("cuerpo: %v", gotBody)
	}
}

func TestUn401EsUnRechazoYCualquierOtraCosaNo(t *testing.T) {
	for name, tc := range map[string]struct {
		status   int
		body     string
		rejected bool
	}{
		"401":               {http.StatusUnauthorized, `{"error":{"code":"UNAUTHORIZED"}}`, true},
		"500":               {http.StatusInternalServerError, ``, false},
		"503":               {http.StatusServiceUnavailable, ``, false},
		"400":               {http.StatusBadRequest, ``, false},
		"404":               {http.StatusNotFound, ``, false},
		"200 ilegible":      {http.StatusOK, `no es json`, false},
		"200 sin identidad": {http.StatusOK, `{"data":{}}`, false},
		"200 con ids nulos": {http.StatusOK, `{"data":{"tenant_id":"` + uuid.Nil.String() + `","job_id":"` + uuid.Nil.String() + `","mailbox_id":"` + uuid.Nil.String() + `"}}`, false},
		"200 vacio":         {http.StatusOK, ``, false},
	} {
		c := serve(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		})
		_, err := c.Verify(context.Background(), "cfmj1.token", "ana@acme.test")
		if err == nil {
			t.Errorf("%s: abrio", name)
			continue
		}
		if errors.Is(err, domain.ErrJobCredentialRejected) != tc.rejected {
			t.Errorf("%s: rechazo=%v, error %v", name, !tc.rejected, err)
		}
	}
}

func TestSinMailMigrationNoSeAbre(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := New(url, gatewayToken).Verify(context.Background(), "cfmj1.token", "ana@acme.test")
	if err == nil || errors.Is(err, domain.ErrJobCredentialRejected) {
		t.Fatalf("un destino caido no es un rechazo: %v", err)
	}
}

func TestLaRespuestaGrandeSeAcota(t *testing.T) {
	var served atomic.Int64
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		chunk := strings.Repeat("x", 1<<10)
		for i := 0; i < 4096; i++ {
			n, err := w.Write([]byte(chunk))
			served.Add(int64(n))
			if err != nil {
				return
			}
		}
	})
	if _, err := c.Verify(context.Background(), "cfmj1.token", "ana@acme.test"); err == nil {
		t.Fatal("abrio con una respuesta desmesurada")
	}
}

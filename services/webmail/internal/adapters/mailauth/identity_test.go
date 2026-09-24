package mailauth

import (
	"context"
	"net/http"
	"testing"
)

// La empresa y el buzon que mail-auth devuelve para service webmail pasan a la identidad; uno que no
// sea un UUID se descarta y la sesion queda sin libreta personal ni calendario.
func TestVerifyLeeEmpresaYBuzon(t *testing.T) {
	body := `{"success":true,"display_name":"Ana","tenant_id":"11111111-1111-4111-8111-111111111111","mailbox_id":"22222222-2222-4222-8222-222222222222"}`
	srv, cfg := tlsServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
	id, err := newClient(t, srv.URL, cfg).Verify(context.Background(), "ana@empresa.pe", "x", "203.0.113.7")
	if err != nil || id.TenantID != "11111111-1111-4111-8111-111111111111" || id.MailboxID != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("%+v %v", id, err)
	}
	body = `{"success":true,"tenant_id":"../otra","mailbox_id":""}`
	id, err = newClient(t, srv.URL, cfg).Verify(context.Background(), "ana@empresa.pe", "x", "203.0.113.7")
	if err != nil || id.TenantID != "" || id.MailboxID != "" {
		t.Fatalf("identificadores invalidos: %+v %v", id, err)
	}
}

package nats

import "testing"

func TestDecodeToleraCamposYCuentaDestinatarios(t *testing.T) {
	p, err := decode(map[string]interface{}{
		"tenant_id":      "7b1c2f5e-9a7d-4c1e-8f00-2b5c3d4e5f60",
		"message_id":     "m-1",
		"ses_message_id": "ses-1",
		"to":             []interface{}{"a@example.com", "b@example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.TenantID != "7b1c2f5e-9a7d-4c1e-8f00-2b5c3d4e5f60" || p.Class != "" || p.recipients() != 2 {
		t.Fatalf("payload: %+v destinatarios=%d", p, p.recipients())
	}
	p, err = decode(map[string]interface{}{"tenant_id": "x", "class": "marketing", "bounce_type": "permanent", "to": "a@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Class != "marketing" || p.BounceType != "permanent" || p.recipients() != 0 {
		t.Fatalf("un to que no es lista no cuenta: %+v", p)
	}
	if _, err := decode(map[string]interface{}{"tenant_id": 42}); err == nil {
		t.Fatal("un tenant_id que no es texto no se puede leer")
	}
}

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
	if p.TenantID != "7b1c2f5e-9a7d-4c1e-8f00-2b5c3d4e5f60" || p.Class != "" || countOf(p) != 2 {
		t.Fatalf("payload: %+v destinatarios=%d", p, countOf(p))
	}
	p, err = decode(map[string]interface{}{"tenant_id": "x", "class": "marketing", "bounce_type": "permanent", "to": "a@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Class != "marketing" || p.BounceType != "permanent" || countOf(p) != 0 {
		t.Fatalf("un to que no es lista no cuenta: %+v", p)
	}
	if _, err := decode(map[string]interface{}{"tenant_id": 42}); err == nil {
		t.Fatal("un tenant_id que no es texto no se puede leer")
	}
}

func countOf(p payload) int64 {
	n, _ := p.recipients()
	return n
}

// El simulador de SES no cuenta en la cuenta de SES, asi que tampoco en la reputacion.
func TestElSimuladorDeSESNoCuenta(t *testing.T) {
	cases := []struct {
		name      string
		data      map[string]interface{}
		n         int64
		simulated bool
	}{
		{"queja del simulador", map[string]interface{}{"email": "complaint@simulator.amazonses.com"}, 0, true},
		{"rebote real", map[string]interface{}{"email": "ana@empresa.com"}, 0, false},
		{"envio solo al simulador", map[string]interface{}{"to": []interface{}{"bounce@Simulator.AmazonSES.com"}}, 0, true},
		{"envio mixto", map[string]interface{}{"to": []interface{}{"success@simulator.amazonses.com", "ana@empresa.com"}}, 1, false},
		{"sin destinatarios", map[string]interface{}{}, 0, false},
	}
	for _, tc := range cases {
		p, err := decode(tc.data)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		n, simulated := p.recipients()
		if n != tc.n || simulated != tc.simulated {
			t.Errorf("%s: destinatarios=%d simulado=%v; se esperaba %d %v", tc.name, n, simulated, tc.n, tc.simulated)
		}
	}
}

package events

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// Un suscriptor comodin ("*.>") tambien recibe los subjects internos de NATS: el token
// "*" casa con "$JS". Sin este filtro, cada acuse de JetStream ("+ACK", que no es JSON)
// llegaba a los servicios que escuchan todo el bus y se registraba como error.
func TestIsSystemSubject(t *testing.T) {
	cases := []struct {
		subject string
		want    bool
	}{
		{"$JS.ACK.AUDIT_API.audit-api-trail.1.1070.1070.1785262933186895254.0", true},
		{"$SYS.REQ.SERVER.PING", true},
		{"$JS.API.STREAM.INFO.EVENTS", true},
		{"service_orders.order.created", false},
		{"invoicing.invoice.accepted", false},
		{"dlq.hr.payroll.approved", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isSystemSubject(c.subject); got != c.want {
			t.Errorf("isSystemSubject(%q) = %v; want %v", c.subject, got, c.want)
		}
	}
}

// Todo stream de aplicacion acota su disco por tamano ademas de por tiempo: solo con la retencion
// por tiempo, un productor desbocado llenaria el volumen del servidor antes de que caduque nada.
func TestStreamConfigBoundsDiskUse(t *testing.T) {
	cfg := streamConfig("TEST", []string{"test.>"}, 7*24*time.Hour)
	if cfg.Storage != nats.FileStorage {
		t.Errorf("Storage = %v; se esperaba disco", cfg.Storage)
	}
	if cfg.MaxBytes <= 0 || cfg.MaxBytes != streamMaxBytes {
		t.Errorf("MaxBytes = %d; se esperaba %d", cfg.MaxBytes, streamMaxBytes)
	}
	if cfg.MaxAge != 7*24*time.Hour {
		t.Errorf("MaxAge = %v", cfg.MaxAge)
	}
}

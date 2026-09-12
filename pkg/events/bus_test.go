package events

import "testing"

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

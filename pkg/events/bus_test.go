package events

import (
	"strings"
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

// Un stream no admite subjects que se solapan: el consumidor que pide uno ya capturado por el comodin
// del dueno (audit con mail.mailbox.mfa_enabled sobre mail.>) no lo anade, y el dueno que declara su
// comodin sobre subjects concretos que otro declaro antes los sustituye.
func TestMergeSubjectsNoSolapa(t *testing.T) {
	cases := []struct {
		have, want, merged []string
		changed            bool
	}{
		{[]string{"mail.>"}, []string{"mail.mailbox.mfa_enabled"}, []string{"mail.>"}, false},
		{[]string{"mail.>"}, []string{"mail.>"}, []string{"mail.>"}, false},
		{[]string{"identity.user.deleted"}, []string{"identity.>"}, []string{"identity.>"}, true},
		{[]string{"a.b", "c.>"}, []string{"a.*"}, []string{"c.>", "a.*"}, true},
		{[]string{"a.*"}, []string{"a.b.c"}, []string{"a.*", "a.b.c"}, true},
		{[]string{"a.*"}, []string{"a.>"}, []string{"a.>"}, true},
		{[]string{"a.>"}, []string{"a"}, []string{"a.>", "a"}, true},
		{nil, []string{"x.>", "x.y"}, []string{"x.>"}, true},
	}
	for _, c := range cases {
		got, changed := mergeSubjects(c.have, c.want)
		if changed != c.changed || strings.Join(got, ",") != strings.Join(c.merged, ",") {
			t.Errorf("mergeSubjects(%v, %v) = %v %v; se esperaba %v %v", c.have, c.want, got, changed, c.merged, c.changed)
		}
	}
}

func TestSubjectCovers(t *testing.T) {
	cases := []struct {
		pattern, sub string
		want         bool
	}{
		{"mail.>", "mail.mailbox.created", true},
		{"mail.>", "mail", false},
		{"mail.*", "mail.policy", true},
		{"mail.*", "mail.>", false},
		{"mail.*", "mail.policy.updated", false},
		{"mail.policy.updated", "mail.policy.updated", true},
		{"mail.policy.updated", "mail.>", false},
		{"*.>", "mail.x", true},
	}
	for _, c := range cases {
		if got := subjectCovers(c.pattern, c.sub); got != c.want {
			t.Errorf("subjectCovers(%q, %q) = %v", c.pattern, c.sub, got)
		}
	}
}

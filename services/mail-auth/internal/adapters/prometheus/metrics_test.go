package prometheus

import (
	"sort"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-auth/internal/domain"
	"github.com/prometheus/client_golang/prometheus"
)

// La metrica etiqueta el servicio de Dovecot como auth_service: con service chocaria con la
// etiqueta del objetivo y el recolector la renombraria. Un servicio que Dovecot no declara se
// cuenta como unknown para no abrir la cardinalidad.
func TestIntentosPorAuthService(t *testing.T) {
	m := New()
	m.Attempt("imap", domain.ResultOK)
	m.Attempt("servicio-inventado", domain.ResultBadPassword)

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range families {
		if f.GetName() != "mail_auth_attempts_total" {
			continue
		}
		for _, metric := range f.GetMetric() {
			var names []string
			labels := map[string]string{}
			for _, l := range metric.GetLabel() {
				names = append(names, l.GetName())
				labels[l.GetName()] = l.GetValue()
			}
			sort.Strings(names)
			if strings.Join(names, ",") != "auth_service,result" {
				t.Fatalf("etiquetas %v; se esperaba auth_service y result", names)
			}
			seen[labels["auth_service"]+"|"+labels["result"]] = true
		}
	}
	for _, want := range []string{"imap|ok", "unknown|bad_password"} {
		if !seen[want] {
			t.Errorf("falta la serie %s; hay %v", want, seen)
		}
	}
}

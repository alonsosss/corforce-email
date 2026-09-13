package app

import (
	"context"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
)

func TestExpandCadenaDeAliasesHastaUnBuzon(t *testing.T) {
	dir := apptest.NewDirectory()
	dir.Mailboxes["ana@acme.com"] = domain.Mailbox{TenantID: uuid.New(), Username: "ana@acme.com", Domain: "acme.com", Active: 1}
	dir.Aliases["ventas@acme.com"] = "soporte@acme.com"
	dir.Aliases["soporte@acme.com"] = "ana@acme.com"

	got, err := NewExpander(dir).ExpandToSingle(context.Background(), "Ventas+promo@ACME.com")
	if err != nil || got != "ana@acme.com" {
		t.Fatalf("obtuve %q, %v", got, err)
	}
}

func TestExpandCatchAllYVariosBuzonesNoDevuelveUnico(t *testing.T) {
	dir := apptest.NewDirectory()
	dir.Mailboxes["ana@acme.com"] = domain.Mailbox{Username: "ana@acme.com", Domain: "acme.com", Active: 1}
	dir.Mailboxes["luis@acme.com"] = domain.Mailbox{Username: "luis@acme.com", Domain: "acme.com", Active: 2}
	dir.Aliases["@acme.com"] = "ana@acme.com,luis@acme.com,null@localhost"

	ex := NewExpander(dir)
	all, err := ex.Expand(context.Background(), "cualquiera@acme.com")
	if err != nil || len(all) != 2 || all[0].Username != "ana@acme.com" || all[1].Username != "luis@acme.com" {
		t.Fatalf("obtuve %+v, %v", all, err)
	}
	single, _ := ex.ExpandToSingle(context.Background(), "cualquiera@acme.com")
	if single != "" {
		t.Fatalf("con dos buzones finales /aliasexp debe ir vacio, obtuve %q", single)
	}
}

func TestExpandDominioAliasYBuzonInactivo(t *testing.T) {
	dir := apptest.NewDirectory()
	dir.AliasDomains["acme-alias.com"] = "acme.com"
	dir.Mailboxes["ana@acme.com"] = domain.Mailbox{Username: "ana@acme.com", Domain: "acme.com", Active: 1}
	dir.Mailboxes["baja@acme.com"] = domain.Mailbox{Username: "baja@acme.com", Domain: "acme.com", Active: 0}
	dir.Mailboxes["sala@acme.com"] = domain.Mailbox{Username: "sala@acme.com", Domain: "acme.com", Active: 1, Kind: "location"}

	ex := NewExpander(dir)
	if got, _ := ex.ExpandToSingle(context.Background(), "ana@acme-alias.com"); got != "ana@acme.com" {
		t.Fatalf("dominio alias: obtuve %q", got)
	}
	if got, _ := ex.ExpandToSingle(context.Background(), "baja@acme.com"); got != "" {
		t.Fatalf("buzon inactivo no recibe: obtuve %q", got)
	}
	if got, _ := ex.ExpandToSingle(context.Background(), "sala@acme.com"); got != "" {
		t.Fatalf("un recurso no recibe: obtuve %q", got)
	}
	if got, _ := ex.ExpandToSingle(context.Background(), "alguien@ajeno.com"); got != "" {
		t.Fatalf("dominio ajeno: obtuve %q", got)
	}
}

func TestExpandCortaLosBuclesYLaProfundidad(t *testing.T) {
	dir := apptest.NewDirectory()
	dir.Aliases["a@acme.com"] = "b@acme.com"
	dir.Aliases["b@acme.com"] = "a@acme.com,c@acme.com"
	dir.Mailboxes["c@acme.com"] = domain.Mailbox{Username: "c@acme.com", Domain: "acme.com", Active: 1}

	ex := NewExpander(dir)
	got, err := ex.ExpandToSingle(context.Background(), "a@acme.com")
	if err != nil || got != "c@acme.com" {
		t.Fatalf("bucle a<->b: obtuve %q, %v", got, err)
	}

	// Cadena lineal de 30 aliases distintos: se corta a MaxExpansionHops sin error y
	// sin llegar al buzon del final.
	deep := apptest.NewDirectory()
	for i := 0; i < 30; i++ {
		deep.Aliases[aliasName(i)] = aliasName(i + 1)
	}
	deep.Mailboxes[aliasName(30)] = domain.Mailbox{Username: aliasName(30), Domain: "acme.com", Active: 1}
	got, err = NewExpander(deep).ExpandToSingle(context.Background(), aliasName(0))
	if err != nil || got != "" {
		t.Fatalf("profundidad: obtuve %q, %v", got, err)
	}
	if deep.Lookups > (MaxExpansionHops+2)*3 {
		t.Fatalf("la expansion siguio mas alla del tope: %d consultas", deep.Lookups)
	}

	// Con 20 saltos exactos si llega.
	short := apptest.NewDirectory()
	for i := 0; i < MaxExpansionHops; i++ {
		short.Aliases[aliasName(i)] = aliasName(i + 1)
	}
	short.Mailboxes[aliasName(MaxExpansionHops)] = domain.Mailbox{Username: aliasName(MaxExpansionHops), Domain: "acme.com", Active: 1}
	if got, _ := NewExpander(short).ExpandToSingle(context.Background(), aliasName(0)); got != aliasName(MaxExpansionHops) {
		t.Fatalf("20 saltos deben resolverse, obtuve %q", got)
	}
}

func aliasName(i int) string { return "a" + string(rune('a'+i%26)) + itoa(i) + "@acme.com" }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

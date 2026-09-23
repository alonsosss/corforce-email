package render

import (
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
)

func TestCompileDraftDeclaraLasVariablesQueFaltan(t *testing.T) {
	c := domain.Content{Subject: "Hola {{.first_name}}", HTML: `<a href="{{.unsubscribe_url}}">{{.coupon}}</a>`,
		Variables: vars(domain.Variable{Name: "coupon", Type: domain.VarString, Required: true})}
	if _, err := New().Compile(c); !errors.Is(err, domain.ErrInvalidTemplate) {
		t.Fatalf("guardar sigue exigiendo declarar: %v", err)
	}
	compiled, err := New().CompileDraft(c)
	if err != nil {
		t.Fatal(err)
	}
	got := compiled.Variables()
	if len(got) != 2 || got[0].Name != "coupon" || got[1].Name != "first_name" || got[1].Type != domain.VarString || got[1].Required {
		t.Fatalf("variables: %+v", got)
	}
	if len(c.Variables) != 1 {
		t.Fatal("CompileDraft no modifica las declaraciones recibidas")
	}
	if _, err := New().CompileDraft(domain.Content{Subject: "x", HTML: "<p>{{.Nombre}}</p>"}); !errors.Is(err, domain.ErrInvalidVariableDeclaration) {
		t.Fatalf("un nombre invalido sigue rechazandose: %v", err)
	}
	if _, err := New().CompileDraft(domain.Content{Subject: "x", HTML: "<script>x</script>"}); !errors.Is(err, domain.ErrInvalidTemplate) {
		t.Fatalf("lo prohibido sigue prohibido: %v", err)
	}
}

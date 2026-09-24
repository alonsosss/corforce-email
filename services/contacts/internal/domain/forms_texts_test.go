package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestNormalizeRedirectURL(t *testing.T) {
	if u, err := NormalizeRedirectURL("   "); u != nil || err != nil {
		t.Fatalf("vacia es sin redireccion: %v %v", u, err)
	}
	u, err := NormalizeRedirectURL(" https://acme.pe/gracias?origen=web ")
	if err != nil || *u != "https://acme.pe/gracias?origen=web" {
		t.Fatalf("redireccion valida: %v %v", u, err)
	}
	for _, bad := range []string{
		"javascript:alert(1)", "//acme.pe/x", "https://acme.pe/#frag", "https:///sin-host",
		"https://acme.pe/\"><script>", "data:text/html,x", "https://acme.pe/" + strings.Repeat("a", MaxFormRedirectURLLength),
	} {
		if _, err := NormalizeRedirectURL(bad); !errors.Is(err, ErrInvalidForm) {
			t.Errorf("redireccion aceptada: %q", bad)
		}
	}
}

func TestTextosDelFormulario(t *testing.T) {
	f := validForm()
	f.Texts = FormTexts{
		Title: "  Boletin  ", Description: "Linea 1\r\nLinea 2", SubmitLabel: " Quiero ",
		ConsentText: "Acepto\r\nla politica", SuccessMessage: " Gracias ",
	}
	if err := f.Normalize(Definitions{}); err != nil {
		t.Fatal(err)
	}
	if f.Texts.Title != "Boletin" || f.Texts.Description != "Linea 1\nLinea 2" || f.Texts.SubmitLabel != "Quiero" ||
		f.Texts.ConsentText != "Acepto\nla politica" || f.Texts.SuccessMessage != "Gracias" {
		t.Fatalf("textos %+v", f.Texts)
	}
	for name, mutate := range map[string]func(*FormTexts){
		"titulo largo":         func(x *FormTexts) { x.Title = strings.Repeat("t", MaxFormTitleLength+1) },
		"titulo con salto":     func(x *FormTexts) { x.Title = "a\nb" },
		"boton largo":          func(x *FormTexts) { x.SubmitLabel = strings.Repeat("b", MaxFormSubmitLabelLength+1) },
		"consentimiento largo": func(x *FormTexts) { x.ConsentText = strings.Repeat("c", MaxFormConsentTextLength+1) },
		"mensaje largo":        func(x *FormTexts) { x.SuccessMessage = strings.Repeat("m", MaxFormSuccessMessageLen+1) },
		"descripcion con nulo": func(x *FormTexts) { x.Description = "a\x00b" },
		"utf8 roto":            func(x *FormTexts) { x.Title = string([]byte{0xff, 0xfe}) },
	} {
		g := validForm()
		mutate(&g.Texts)
		if err := g.Normalize(Definitions{}); !errors.Is(err, ErrInvalidForm) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestFormEstadoYConsentSource(t *testing.T) {
	f := validForm()
	if err := f.Normalize(Definitions{}); err != nil || !f.Active() {
		t.Fatalf("un formulario nuevo esta activo: %v", err)
	}
	f.Status = FormDisabled
	if f.Active() {
		t.Fatal("desactivado no admite envios")
	}
	id := uuid.New()
	if FormConsentSource(id) != "form:"+id.String() {
		t.Fatal("origen del consentimiento")
	}
	if len(FormStatuses()) != 2 || len(BuiltinFormFields()) != 3 {
		t.Fatal("catalogo")
	}
	if typ, err := FieldType("email", nil); err != nil || typ != FieldTypeEmail {
		t.Fatal("tipo del email")
	}
}

package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

func TestUpdateFormParcial(t *testing.T) {
	f := newFormFixture(t)
	ctx := context.Background()
	redirect := "https://acme.pe/gracias"
	origins := []string{"https://Acme.PE", "https://acme.pe"}
	got, err := f.uc.UpdateForm(ctx, f.tenant, f.form.ID, FormPatch{
		RedirectURL: &OptionalString{Value: &redirect}, AllowedOrigins: &origins,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.RedirectURL == nil || *got.RedirectURL != redirect || len(got.AllowedOrigins) != 1 || got.AllowedOrigins[0] != "https://acme.pe" {
		t.Fatalf("parche aplicado: %+v", got)
	}
	if got.Name != "portada" || len(got.Fields) != 2 {
		t.Fatalf("un parche cambio lo que no traia: %+v", got)
	}
	got, err = f.uc.UpdateForm(ctx, f.tenant, f.form.ID, FormPatch{RedirectURL: &OptionalString{}})
	if err != nil || got.RedirectURL != nil {
		t.Fatalf("quitar la redireccion: %+v %v", got, err)
	}
	other := uuid.New()
	if _, err := f.uc.UpdateForm(ctx, f.tenant, f.form.ID, FormPatch{ListID: &other}); !errors.Is(err, domain.ErrInvalidForm) {
		t.Fatalf("cambiar a una lista ajena: %v", err)
	}
	empty := []domain.FormField{}
	if _, err := f.uc.UpdateForm(ctx, f.tenant, f.form.ID, FormPatch{Fields: &empty}); !errors.Is(err, domain.ErrInvalidForm) {
		t.Fatalf("un formulario sin campos: %v", err)
	}
	if _, err := f.uc.UpdateForm(ctx, uuid.New(), f.form.ID, FormPatch{}); !errors.Is(err, domain.ErrFormNotFound) {
		t.Fatalf("formulario de otra empresa: %v", err)
	}
	stored, _ := f.forms.Get(ctx, f.tenant, f.form.ID)
	if stored.RedirectURL != nil {
		t.Fatal("un parche rechazado se guardo")
	}
}

func TestCreateFormRespetaLosAtributosObligatorios(t *testing.T) {
	f := newFormFixture(t)
	f.declare(t, "empresa", domain.AttrString, true)
	ctx := context.Background()
	in := FormInput{
		Name: "empresas", ListID: f.list.ID, Fields: []domain.FormField{{Key: "email", Label: "Correo"}},
		Texts: domain.FormTexts{ConsentText: "Acepto", SuccessMessage: "Gracias"},
	}
	if _, err := f.uc.CreateForm(ctx, f.tenant, uuid.New(), in); !errors.Is(err, domain.ErrInvalidForm) {
		t.Fatalf("sin el atributo obligatorio: %v", err)
	}
	in.Fields = append(in.Fields, domain.FormField{Key: "empresa", Label: "Empresa", Required: true})
	if _, err := f.uc.CreateForm(ctx, f.tenant, uuid.New(), in); err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.CreateForm(ctx, f.tenant, uuid.New(), in); !errors.Is(err, domain.ErrFormExists) {
		t.Fatalf("nombre repetido: %v", err)
	}
}

func TestSubmitFormDesdeElFormularioHTMLConAtributos(t *testing.T) {
	f := newFormFixture(t)
	f.declare(t, "edad", domain.AttrNumber, false)
	f.declare(t, "vip", domain.AttrBoolean, false)
	ctx := context.Background()
	fields := append(f.form.Fields, domain.FormField{Key: "edad", Label: "Edad"}, domain.FormField{Key: "vip", Label: "VIP"})
	form, err := f.uc.UpdateForm(ctx, f.tenant, f.form.ID, FormPatch{Fields: &fields})
	if err != nil {
		t.Fatal(err)
	}
	_, defs, err := f.uc.PublicForm(ctx, f.tenant, form.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := f.tokens.Issue(f.tenant, form.ID, f.now.Add(-5*time.Second))
	res, err := f.uc.SubmitForm(ctx, form, defs, SubmitInput{
		Token: token, Consent: true, IP: "2001:db8:5:6::7",
		Strings: map[string]string{"email": "html@cliente.test", "edad": "42", "vip": "on"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != domain.OutcomeConfirmationSent {
		t.Fatalf("resultado %+v", res)
	}
	var c *domain.Contact
	for _, x := range f.s.contacts {
		c = x
	}
	if n, _ := c.Attributes["edad"].(json.Number); n.String() != "42" || c.Attributes["vip"] != true {
		t.Fatalf("atributos %v", c.Attributes)
	}
	if p := f.s.consents[len(f.s.consents)-1].Evidence["ip_prefix"]; p != "2001:db8:5::/48" {
		t.Fatalf("prefijo IPv6 %v", p)
	}
	token, _ = f.tokens.Issue(f.tenant, form.ID, f.now.Add(-5*time.Second))
	if _, err := f.uc.SubmitForm(ctx, form, defs, SubmitInput{
		Token: token, Consent: true, Strings: map[string]string{"email": "x@cliente.test", "edad": "cuarenta"},
	}); !errors.Is(err, domain.ErrInvalidSubmission) {
		t.Fatalf("un numero no valido: %v", err)
	}
}

func TestSubmitFormAtributoObligatorioAnadidoDespues(t *testing.T) {
	f := newFormFixture(t)
	f.declare(t, "empresa", domain.AttrString, true)
	_, defs, err := f.uc.PublicForm(context.Background(), f.tenant, f.form.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := f.tokens.Issue(f.tenant, f.form.ID, f.now.Add(-5*time.Second))
	_, err = f.uc.SubmitForm(context.Background(), f.form, defs, SubmitInput{
		Token: token, Consent: true, Values: map[string]json.RawMessage{"email": raw(`"a@cliente.test"`)},
	})
	if !errors.Is(err, domain.ErrInvalidSubmission) {
		t.Fatalf("el contacto no se puede crear sin el atributo obligatorio: %v", err)
	}
	if len(f.s.contacts) != 0 {
		t.Fatal("se creo un contacto incompleto")
	}
}

func TestSubmitFormUnaBajaRecibeElDobleOptIn(t *testing.T) {
	f := newFormFixture(t)
	f.sup.causes["baja@cliente.test"] = []domain.ActiveCause{{Cause: domain.CauseUnsubscribe}}
	res, err := f.submit(t, "baja@cliente.test", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != domain.OutcomeConfirmationSent || f.ev.count("consent.requested|baja@cliente.test") != 1 {
		t.Fatalf("la confirmacion es lo unico que devuelve a quien se dio de baja: %+v", res)
	}
}

// raceContacts simula el alta simultanea de la misma direccion: el primer Insert choca y el
// contacto aparece creado por otro.
type raceContacts struct {
	fakeContacts
	collided bool
}

func (r *raceContacts) Insert(ctx context.Context, c *domain.Contact) error {
	if !r.collided {
		r.collided = true
		other := *c
		if err := r.fakeContacts.Insert(ctx, &other); err != nil {
			return err
		}
		return domain.ErrContactExists
	}
	return r.fakeContacts.Insert(ctx, c)
}

func TestSubmitFormAltaSimultaneaSeRepiteSobreElContactoCreado(t *testing.T) {
	f := newFormFixture(t)
	race := &raceContacts{fakeContacts: fakeContacts{f.s}}
	f.uc.contacts = race
	res, err := f.submit(t, "carrera@cliente.test", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !race.collided || res.Outcome != domain.OutcomeConfirmationSent || len(f.s.contacts) != 1 {
		t.Fatalf("resultado %+v, contactos %d", res, len(f.s.contacts))
	}
}

func TestFormulariosNoDisponibles(t *testing.T) {
	uc := New(Deps{})
	ctx := context.Background()
	if _, _, err := uc.ListForms(ctx, uuid.New(), 1, 10); !errors.Is(err, ErrFormsUnavailable) {
		t.Fatalf("listar: %v", err)
	}
	if _, _, err := uc.PublicForm(ctx, uuid.New(), uuid.New()); !errors.Is(err, ErrFormsUnavailable) {
		t.Fatalf("publico: %v", err)
	}
	if err := uc.CheckSubmitRate(ctx, "203.0.113.1"); !errors.Is(err, ErrFormsUnavailable) {
		t.Fatalf("cupo: %v", err)
	}
	f := newFormFixture(t)
	partial := New(Deps{Forms: f.forms, Attributes: fakeAttributes{f.s}})
	if _, _, err := partial.PublicForm(ctx, f.tenant, f.form.ID); !errors.Is(err, ErrFormsUnavailable) {
		t.Fatal("sin freno ni tokens no se sirven formularios publicos")
	}
	if _, err := partial.GetForm(ctx, f.tenant, f.form.ID); err != nil {
		t.Fatalf("la gestion no necesita el freno: %v", err)
	}
}

func TestConfirmSinFormularioNoTocaListas(t *testing.T) {
	f := newFormFixture(t)
	c := f.addContact(t, "api@cliente.test", domain.StatusActive, domain.ConsentNone)
	if _, err := f.uc.RequestConfirmation(context.Background(), f.tenant, c.ID, "e2e"); err != nil {
		t.Fatal(err)
	}
	if err := f.uc.Confirm(context.Background(), f.tenant, tokenFromURL(t, f.fixture), "", ""); err != nil {
		t.Fatal(err)
	}
	if f.s.members[f.list.ID][c.ID] {
		t.Fatal("un doble opt-in pedido por API no entra en la lista de un formulario")
	}
	consents := f.s.consents
	if ev := consents[0].Evidence; len(ev) != 1 || ev["token_id"] == nil {
		t.Fatalf("la evidencia del doble opt-in por API no cambia: %v", ev)
	}
}

func TestDeleteFormLiberaLaLista(t *testing.T) {
	f := newFormFixture(t)
	ctx := context.Background()
	if err := f.uc.DeleteForm(ctx, f.tenant, f.form.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.uc.DeleteList(ctx, f.tenant, f.list.ID); err != nil {
		t.Fatalf("sin formularios la lista se borra: %v", err)
	}
}

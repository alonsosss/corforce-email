package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

func strp(s string) *string { return &s }

func TestActualizarContacto(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.declare(t, "plan", domain.AttrString, true)
	f.declare(t, "score", domain.AttrNumber, false)

	if _, err := f.uc.CreateContact(ctx, f.tenant, CreateContactInput{Email: "sinplan@example.com"}); !errors.Is(err, domain.ErrRequiredAttribute) {
		t.Fatalf("obligatorio al crear: %v", err)
	}
	c, err := f.uc.CreateContact(ctx, f.tenant, CreateContactInput{
		Email: "ana@example.com", Attributes: domain.RawAttributes{"plan": raw(`"free"`), "score": raw(`5`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.ev.events = nil

	tags := []string{"VIP"}
	got, err := f.uc.UpdateContact(ctx, f.tenant, c.ID, UpdateContactInput{
		FirstName: strp("Ana"), Locale: strp("es"), Tags: &tags,
		Attributes: domain.RawAttributes{"plan": raw(`"pro"`), "score": raw(`null`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.FirstName != "Ana" || *got.Locale != "es" || got.Tags[0] != "vip" || got.Attributes["plan"] != "pro" {
		t.Fatalf("cambios: %+v", got)
	}
	if _, has := got.Attributes["score"]; has {
		t.Fatal("null quita el atributo")
	}
	if len(f.ev.events) != 1 || f.ev.events[0] != "contact.updated|ana@example.com|first_name,locale,tags,attributes.plan,attributes.score" {
		t.Fatalf("evento con los nombres de lo que cambio: %v", f.ev.events)
	}

	// Sin cambios reales no hay evento.
	f.ev.events = nil
	if _, err := f.uc.UpdateContact(ctx, f.tenant, c.ID, UpdateContactInput{FirstName: strp(" Ana ")}); err != nil || len(f.ev.events) != 0 {
		t.Fatalf("sin cambios: %v %v", err, f.ev.events)
	}
	// Locale vacio lo borra.
	if got, _ := f.uc.UpdateContact(ctx, f.tenant, c.ID, UpdateContactInput{Locale: strp("")}); got.Locale != nil {
		t.Fatal("locale vacio debe borrarlo")
	}

	for _, tc := range []struct {
		in   UpdateContactInput
		want error
	}{
		{UpdateContactInput{Email: strp("otra@example.com")}, domain.ErrEmailImmutable},
		{UpdateContactInput{Timezone: strp("Luna/Base")}, domain.ErrInvalidTimezone},
		{UpdateContactInput{Attributes: domain.RawAttributes{"plan": raw(`null`)}}, domain.ErrRequiredAttribute},
		{UpdateContactInput{Attributes: domain.RawAttributes{"nuevo": raw(`1`)}}, domain.ErrUndeclaredAttribute},
		{UpdateContactInput{Attributes: domain.RawAttributes{"score": raw(`"7"`)}}, domain.ErrAttributeValue},
	} {
		if _, err := f.uc.UpdateContact(ctx, f.tenant, c.ID, tc.in); !errors.Is(err, tc.want) {
			t.Errorf("%+v: se esperaba %v, hubo %v", tc.in, tc.want, err)
		}
	}
	// La misma direccion no es un cambio de direccion.
	if _, err := f.uc.UpdateContact(ctx, f.tenant, c.ID, UpdateContactInput{Email: strp("ANA@example.com")}); err != nil {
		t.Fatalf("misma direccion: %v", err)
	}
}

func TestBorrarSeudonimizaLaEvidencia(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c, err := f.uc.CreateContact(ctx, f.tenant, CreateContactInput{
		Email: "borrar@example.com", Consent: &ConsentInput{Status: "granted", Method: "form", Source: "web", IP: "203.0.113.5", UserAgent: "UA"},
	})
	if err != nil {
		t.Fatal(err)
	}
	list, _ := f.uc.CreateList(ctx, f.tenant, "L", "")
	if _, err := f.uc.AddMembers(ctx, f.tenant, list.ID, []uuid.UUID{c.ID}); err != nil {
		t.Fatal(err)
	}

	export, err := f.uc.ExportContact(ctx, f.tenant, c.ID)
	if err != nil || len(export.Consents) != 1 || len(export.Lists) != 1 || export.Contact.Email != "borrar@example.com" {
		t.Fatalf("exportacion: %+v %v", export, err)
	}

	if err := f.uc.DeleteContact(ctx, f.tenant, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.GetContact(ctx, f.tenant, c.ID); !errors.Is(err, domain.ErrContactNotFound) {
		t.Fatal("el contacto debe desaparecer")
	}
	cs := f.s.consents[0]
	if cs.ContactID == c.ID || cs.IP != nil || cs.UserAgent != nil || cs.Evidence["email_sha256"] != domain.EmailSHA256("borrar@example.com") {
		t.Fatalf("seudonimizacion: %+v", cs)
	}
	last := f.ev.events[len(f.ev.events)-1]
	if last != "contact.deleted|"+c.ID.String() || strings.Contains(last, "@") {
		t.Fatalf("el evento de borrado no lleva la direccion: %q", last)
	}
	if err := f.uc.DeleteContact(ctx, f.tenant, c.ID); !errors.Is(err, domain.ErrContactNotFound) {
		t.Fatalf("segundo borrado: %v", err)
	}
}

func TestSegmentosYSusReferencias(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.declare(t, "plan", domain.AttrString, false)
	list, _ := f.uc.CreateList(ctx, f.tenant, "Clientes", "")

	if _, err := f.uc.CreateSegment(ctx, f.tenant, SegmentInput{Name: "x", Definition: raw(`{"match":"all","rules":[{"field":"attributes.otro","op":"exists"}]}`)}); !errors.Is(err, domain.ErrInvalidSegment) {
		t.Fatalf("atributo no declarado: %v", err)
	}
	if _, err := f.uc.CreateSegment(ctx, f.tenant, SegmentInput{Name: "x", Definition: raw(`{"match":"all","rules":[{"field":"list","op":"in_list","value":"` + uuid.NewString() + `"}]}`)}); !errors.Is(err, domain.ErrInvalidSegment) {
		t.Fatalf("lista inexistente: %v", err)
	}
	s, err := f.uc.CreateSegment(ctx, f.tenant, SegmentInput{
		Name: " Pro en clientes ",
		Definition: raw(`{"match":"all","rules":[
			{"field":"attributes.plan","op":"eq","value":"pro"},
			{"field":"list","op":"in_list","value":"` + strings.ToUpper(list.ID.String()) + `"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "Pro en clientes" || strings.Contains(string(s.Definition), "\n") {
		t.Fatalf("se guarda la forma canonica: %+v %s", s, s.Definition)
	}
	var def map[string]any
	if err := json.Unmarshal(s.Definition, &def); err != nil || def["match"] != "all" {
		t.Fatalf("definicion: %s", s.Definition)
	}

	if err := f.uc.DeleteAttribute(ctx, f.tenant, "plan"); !errors.Is(err, domain.ErrAttributeInUse) {
		t.Fatalf("atributo en uso: %v", err)
	}
	if err := f.uc.DeleteList(ctx, f.tenant, list.ID); !errors.Is(err, domain.ErrListInUse) {
		t.Fatalf("lista en uso: %v", err)
	}
	preview, err := f.uc.PreviewSegment(ctx, f.tenant, s.Definition)
	if err != nil || preview.Sample == nil {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	if err := f.uc.DeleteSegment(ctx, f.tenant, s.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.uc.DeleteList(ctx, f.tenant, list.ID); err != nil {
		t.Fatalf("sin segmentos la lista se borra: %v", err)
	}

	c, _ := f.uc.CreateContact(ctx, f.tenant, CreateContactInput{Email: "p@example.com", Attributes: domain.RawAttributes{"plan": raw(`"pro"`)}})
	if err := f.uc.DeleteAttribute(ctx, f.tenant, "plan"); err != nil {
		t.Fatal(err)
	}
	if _, has := f.contact(t, c.ID).Attributes["plan"]; has {
		t.Fatal("retirar el atributo quita su valor de los contactos")
	}
	if _, err := f.uc.CreateAttribute(ctx, f.tenant, CreateAttributeInput{Key: "email", Type: "string"}); !errors.Is(err, domain.ErrReservedAttributeKey) {
		t.Fatalf("clave reservada: %v", err)
	}
	if _, err := f.uc.CreateAttribute(ctx, f.tenant, CreateAttributeInput{Key: "fecha", Type: "datetime"}); !errors.Is(err, domain.ErrInvalidAttributeType) {
		t.Fatalf("tipo desconocido: %v", err)
	}
}

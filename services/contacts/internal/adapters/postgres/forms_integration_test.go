//go:build integration

package postgres

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

func TestFormulariosYEnvios(t *testing.T) {
	_, ctx := testPool(t)
	cp := &db.ContextPool{}
	forms := NewFormRepository(cp)
	lists := NewListRepository(cp)
	contacts := NewContactRepository(cp)
	tokens := NewTokenRepository(cp)
	tenant := uuid.New()

	l := &domain.List{TenantID: tenant, Name: "boletin"}
	if err := lists.Create(ctx, l); err != nil {
		t.Fatal(err)
	}
	redirect := "https://acme.pe/gracias"
	f := &domain.SubscriptionForm{
		TenantID: tenant, Name: "Portada", Status: domain.FormActive, ListID: l.ID,
		Fields:         []domain.FormField{{Key: "email", Label: "Correo", Required: true}},
		Texts:          domain.FormTexts{ConsentText: "Acepto", SuccessMessage: "Gracias"},
		RedirectURL:    &redirect,
		AllowedOrigins: []string{"https://acme.pe"}, CreatedBy: uuid.New(),
	}
	if err := forms.Create(ctx, f); err != nil {
		t.Fatal(err)
	}
	dup := *f
	if err := forms.Create(ctx, &dup); !errors.Is(err, domain.ErrFormExists) {
		t.Fatalf("nombre repetido: %v", err)
	}
	other := *f
	other.Name, other.ListID = "otro", uuid.New()
	if err := forms.Create(ctx, &other); !errors.Is(err, domain.ErrInvalidForm) {
		t.Fatalf("lista inexistente: %v", err)
	}
	got, err := forms.Get(ctx, tenant, f.ID)
	if err != nil || got.Fields[0].Key != "email" || *got.RedirectURL != redirect || got.AllowedOrigins[0] != "https://acme.pe" {
		t.Fatalf("leido: %+v %v", got, err)
	}
	if _, err := forms.Get(ctx, uuid.New(), f.ID); !errors.Is(err, domain.ErrFormNotFound) {
		t.Fatal("un formulario de otra empresa se lee")
	}
	if used, _ := forms.UsingList(ctx, tenant, l.ID); !used {
		t.Fatal("la lista esta en uso")
	}
	if err := lists.Delete(ctx, tenant, l.ID); err == nil {
		t.Fatal("la base deja borrar la lista destino de un formulario")
	}

	c := newContact(tenant, "form@example.com")
	insert(t, ctx, contacts, c)
	tok := &domain.ConfirmationToken{TenantID: tenant, ContactID: c.ID, TokenHash: strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", ""), ExpiresAt: time.Now().Add(time.Hour)}
	if err := tokens.Create(ctx, tok); err != nil {
		t.Fatal(err)
	}
	rec := &domain.FormSubmissionRecord{TenantID: tenant, FormID: f.ID, ContactID: c.ID, TokenID: &tok.ID, ListID: l.ID, Outcome: domain.OutcomeConfirmationSent}
	if err := forms.InsertSubmission(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := forms.InsertSubmission(ctx, &domain.FormSubmissionRecord{TenantID: tenant, FormID: f.ID, ContactID: c.ID, ListID: l.ID, Outcome: domain.OutcomeNotReachable}); err != nil {
		t.Fatal(err)
	}
	listID, err := forms.ConfirmSubmission(ctx, tenant, tok.ID, time.Now())
	if err != nil || listID == nil || *listID != l.ID {
		t.Fatalf("confirmar: %v %v", listID, err)
	}
	if again, _ := forms.ConfirmSubmission(ctx, tenant, tok.ID, time.Now()); again != nil {
		t.Fatal("una confirmacion cuenta dos veces")
	}
	if none, _ := forms.ConfirmSubmission(ctx, tenant, uuid.New(), time.Now()); none != nil {
		t.Fatal("un token sin formulario devuelve lista")
	}

	now := time.Now().UTC()
	to := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	st, err := forms.Stats(ctx, tenant, f.ID, to.AddDate(0, 0, -7), to)
	if err != nil {
		t.Fatal(err)
	}
	if st.Submitted != 2 || st.ConfirmationSent != 1 || st.NotReachable != 1 || st.Confirmed != 1 || len(st.Daily) != 7 {
		t.Fatalf("estadisticas %+v", st)
	}
	if last := st.Daily[6]; last.Submitted != 2 || last.Confirmed != 1 || last.Date != now.Format("2006-01-02") {
		t.Fatalf("dia de hoy %+v", last)
	}

	f.Name = "Portada 2"
	if err := forms.Update(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := forms.Delete(ctx, tenant, f.ID); err != nil {
		t.Fatal(err)
	}
	if err := forms.Delete(ctx, tenant, f.ID); !errors.Is(err, domain.ErrFormNotFound) {
		t.Fatalf("borrar dos veces: %v", err)
	}
	if err := lists.Delete(ctx, tenant, l.ID); err != nil {
		t.Fatalf("sin formularios la lista se borra: %v", err)
	}
}

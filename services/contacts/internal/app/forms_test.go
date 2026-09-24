package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

type fakeForms struct {
	forms       map[uuid.UUID]*domain.SubscriptionForm
	submissions []*domain.FormSubmissionRecord
	confirmed   map[uuid.UUID]bool
}

func (f *fakeForms) Create(_ context.Context, form *domain.SubscriptionForm) error {
	for _, other := range f.forms {
		if other.TenantID == form.TenantID && other.Name == form.Name {
			return domain.ErrFormExists
		}
	}
	form.ID = uuid.New()
	cp := *form
	f.forms[form.ID] = &cp
	return nil
}

func (f *fakeForms) Get(_ context.Context, tenantID, id uuid.UUID) (*domain.SubscriptionForm, error) {
	form, ok := f.forms[id]
	if !ok || form.TenantID != tenantID {
		return nil, domain.ErrFormNotFound
	}
	cp := *form
	return &cp, nil
}

func (f *fakeForms) Update(_ context.Context, form *domain.SubscriptionForm) error {
	cp := *form
	f.forms[form.ID] = &cp
	return nil
}

func (f *fakeForms) Delete(_ context.Context, _, id uuid.UUID) error {
	delete(f.forms, id)
	return nil
}

func (f *fakeForms) List(_ context.Context, _ uuid.UUID, _, _ int) ([]domain.SubscriptionForm, int64, error) {
	return nil, 0, nil
}

func (f *fakeForms) UsingList(_ context.Context, tenantID, listID uuid.UUID) (bool, error) {
	for _, form := range f.forms {
		if form.TenantID == tenantID && form.ListID == listID {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeForms) InsertSubmission(_ context.Context, s *domain.FormSubmissionRecord) error {
	f.submissions = append(f.submissions, s)
	return nil
}

func (f *fakeForms) ConfirmSubmission(_ context.Context, _, tokenID uuid.UUID, _ time.Time) (*uuid.UUID, error) {
	for _, s := range f.submissions {
		if s.TokenID != nil && *s.TokenID == tokenID && !f.confirmed[s.ID] {
			f.confirmed[s.ID] = true
			list := s.ListID
			return &list, nil
		}
	}
	return nil, nil
}

func (f *fakeForms) Stats(_ context.Context, _, _ uuid.UUID, from, to time.Time) (*domain.FormStats, error) {
	return &domain.FormStats{From: from, To: to}, nil
}

type fakeGuard struct {
	ipAllowed   bool
	formAllowed bool
	nonces      map[string]bool
}

func (g *fakeGuard) AllowIP(context.Context, string) (bool, time.Duration) {
	return g.ipAllowed, time.Minute
}

func (g *fakeGuard) AllowForm(context.Context, uuid.UUID, uuid.UUID) (bool, time.Duration) {
	return g.formAllowed, time.Hour
}

func (g *fakeGuard) FirstUse(_ context.Context, nonce string) bool {
	if g.nonces[nonce] {
		return false
	}
	g.nonces[nonce] = true
	return true
}

type formFixture struct {
	*fixture
	forms  *fakeForms
	guard  *fakeGuard
	tokens *FormTokenSigner
	list   *domain.List
	form   *domain.SubscriptionForm
}

func newFormFixture(t *testing.T) *formFixture {
	t.Helper()
	f := &formFixture{fixture: newFixture(t)}
	f.forms = &fakeForms{forms: map[uuid.UUID]*domain.SubscriptionForm{}, confirmed: map[uuid.UUID]bool{}}
	f.guard = &fakeGuard{ipAllowed: true, formAllowed: true, nonces: map[string]bool{}}
	var err error
	if f.tokens, err = NewFormTokenSigner(strings.Repeat("s", 40), &counterReader{}); err != nil {
		t.Fatal(err)
	}
	f.uc = New(Deps{
		Contacts: fakeContacts{f.s}, Consents: fakeConsents{f.s}, Tokens: fakeTokens{f.s},
		Lists: fakeLists{f.s}, Attributes: fakeAttributes{f.s}, Segments: fakeSegments{f.s},
		Query: f.query, Imports: fakeImports{f.s}, Tx: fakeTx{}, Events: f.ev, Suppression: f.sup,
		Forms: f.forms, FormGuard: f.guard, FormTokens: f.tokens,
		Config: Config{PublicBaseURL: "https://app.example.com", DOITTL: 72 * time.Hour, ImportMaxRows: 1000,
			Forms: FormConfig{MinFill: 3 * time.Second, TokenTTL: time.Hour, PlatformOrigin: "https://app.example.com"}},
		Now: func() time.Time { return f.now }, Random: &counterReader{},
	})
	ctx := context.Background()
	if f.list, err = f.uc.CreateList(ctx, f.tenant, "boletin", ""); err != nil {
		t.Fatal(err)
	}
	f.form, err = f.uc.CreateForm(ctx, f.tenant, uuid.New(), FormInput{
		Name: "portada", ListID: f.list.ID,
		Fields: []domain.FormField{{Key: "email", Label: "Correo"}, {Key: "first_name", Label: "Nombre"}},
		Texts:  domain.FormTexts{ConsentText: "Acepto recibir el boletin", SuccessMessage: "Revisa tu correo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// submit envia con un token emitido min segundos antes.
func (f *formFixture) submit(t *testing.T, email, name, honeypot string, consent bool) (*SubmitResult, error) {
	t.Helper()
	token, err := f.tokens.Issue(f.tenant, f.form.ID, f.now.Add(-5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return f.uc.SubmitForm(context.Background(), f.form, domain.Definitions{}, SubmitInput{
		Token: token, Honeypot: honeypot, Consent: consent, IP: "203.0.113.77", UserAgent: "Navegador",
		Values: map[string]json.RawMessage{"email": raw(`"` + email + `"`), "first_name": raw(`"` + name + `"`)},
	})
}

func TestSubmitFormCreaElContactoPendienteConEvidencia(t *testing.T) {
	f := newFormFixture(t)
	res, err := f.submit(t, "Nueva@Cliente.test", "Nora", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != domain.OutcomeConfirmationSent || res.Ignored {
		t.Fatalf("resultado %+v", res)
	}
	var c *domain.Contact
	for _, x := range f.s.contacts {
		c = x
	}
	if c == nil || c.Email != "nueva@cliente.test" || c.Source != domain.SourceForm || c.ConsentStatus != domain.ConsentPending {
		t.Fatalf("contacto %+v", c)
	}
	if f.ev.count("consent.requested|nueva@cliente.test") != 1 {
		t.Fatalf("eventos %v", f.ev.events)
	}
	pending := f.s.consents[len(f.s.consents)-1]
	if pending.Method != domain.MethodDoubleOptIn || pending.Source != domain.FormConsentSource(f.form.ID) {
		t.Fatalf("consentimiento %+v", pending)
	}
	if pending.Evidence["consent_text"] != "Acepto recibir el boletin" || pending.Evidence["ip_prefix"] != "203.0.113.0/24" {
		t.Fatalf("evidencia %v", pending.Evidence)
	}
	if pending.IP != nil {
		t.Fatal("la ip completa no se guarda en la evidencia del formulario")
	}
	if len(f.forms.submissions) != 1 || f.forms.submissions[0].TokenID == nil {
		t.Fatalf("envios %+v", f.forms.submissions)
	}
}

func TestSubmitFormLaConfirmacionEntraEnLaLista(t *testing.T) {
	f := newFormFixture(t)
	if _, err := f.submit(t, "lista@cliente.test", "", "", true); err != nil {
		t.Fatal(err)
	}
	k := tokenFromURL(t, f.fixture)
	if err := f.uc.Confirm(context.Background(), f.tenant, k, "198.51.100.1", "UA"); err != nil {
		t.Fatal(err)
	}
	var id uuid.UUID
	for cid := range f.s.contacts {
		id = cid
	}
	if !f.s.members[f.list.ID][id] {
		t.Fatal("quien confirma entra en la lista destino del formulario")
	}
}

func TestSubmitFormNoEnviaAUnaDireccionExcluida(t *testing.T) {
	f := newFormFixture(t)
	f.sup.causes["rebota@cliente.test"] = []domain.ActiveCause{{Cause: domain.CauseHardBounce}}
	res, err := f.submit(t, "rebota@cliente.test", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != domain.OutcomeNotReachable {
		t.Fatalf("resultado %+v", res)
	}
	if f.ev.count("consent.requested") != 0 {
		t.Fatal("una direccion suprimida no recibe el correo de confirmacion")
	}
}

func TestSubmitFormNoCambiaLosDatosDeUnContactoExistente(t *testing.T) {
	f := newFormFixture(t)
	c := f.addContact(t, "ya@cliente.test", domain.StatusActive, domain.ConsentGranted)
	c.FirstName = "Original"
	_ = (fakeContacts{f.s}).Update(context.Background(), c)
	res, err := f.submit(t, "ya@cliente.test", "Intruso", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != domain.OutcomeAlreadySubscribed {
		t.Fatalf("resultado %+v", res)
	}
	if got := f.contact(t, c.ID); got.FirstName != "Original" {
		t.Fatalf("un envio publico reescribio el nombre: %q", got.FirstName)
	}
	if f.ev.count("consent.requested") != 0 {
		t.Fatal("a quien ya consiente no se le vuelve a pedir")
	}
}

func TestSubmitFormCampoTrampa(t *testing.T) {
	f := newFormFixture(t)
	res, err := f.submit(t, "robot@cliente.test", "", "https://spam.test", true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ignored || len(f.s.contacts) != 0 || len(f.forms.submissions) != 0 {
		t.Fatalf("el campo trampa no descarto el envio: %+v", res)
	}
}

func TestSubmitFormTokenDeUnSoloUsoYTiempoMinimo(t *testing.T) {
	f := newFormFixture(t)
	token, _ := f.tokens.Issue(f.tenant, f.form.ID, f.now.Add(-5*time.Second))
	in := SubmitInput{Token: token, Consent: true, Values: map[string]json.RawMessage{"email": raw(`"a@cliente.test"`)}}
	if _, err := f.uc.SubmitForm(context.Background(), f.form, domain.Definitions{}, in); err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.SubmitForm(context.Background(), f.form, domain.Definitions{}, in); !errors.Is(err, ErrFormTokenInvalid) {
		t.Fatalf("un token reutilizado: %v", err)
	}
	fast, _ := f.tokens.Issue(f.tenant, f.form.ID, f.now.Add(-time.Second))
	in.Token = fast
	if _, err := f.uc.SubmitForm(context.Background(), f.form, domain.Definitions{}, in); !errors.Is(err, ErrFormTokenInvalid) {
		t.Fatalf("un envio antes del tiempo minimo: %v", err)
	}
}

func TestSubmitFormExigeConsentimientoYRespetaLosCupos(t *testing.T) {
	f := newFormFixture(t)
	if _, err := f.submit(t, "b@cliente.test", "", "", false); !errors.Is(err, domain.ErrConsentNotAccepted) {
		t.Fatalf("sin la casilla: %v", err)
	}
	f.guard.formAllowed = false
	var limited *RateLimitedError
	if _, err := f.submit(t, "c@cliente.test", "", "", true); !errors.As(err, &limited) {
		t.Fatalf("por encima del cupo del formulario: %v", err)
	}
	f.guard.ipAllowed = false
	if err := f.uc.CheckSubmitRate(context.Background(), "203.0.113.1"); !errors.As(err, &limited) {
		t.Fatalf("por encima del cupo por ip: %v", err)
	}
}

func TestSubmitFormSinSuppressionNoEscribe(t *testing.T) {
	f := newFormFixture(t)
	f.sup.err = errors.New("caido")
	if _, err := f.submit(t, "d@cliente.test", "", "", true); !errors.Is(err, ErrSuppressionUnavailable) {
		t.Fatalf("sin suppression: %v", err)
	}
	if len(f.s.contacts) != 0 {
		t.Fatal("se escribio un contacto sin comprobar suppression")
	}
}

func TestDeleteListUsadaPorUnFormulario(t *testing.T) {
	f := newFormFixture(t)
	if err := f.uc.DeleteList(context.Background(), f.tenant, f.list.ID); !errors.Is(err, domain.ErrListInUseByForm) {
		t.Fatalf("borrar la lista destino: %v", err)
	}
}

func TestCreateFormConListaAjena(t *testing.T) {
	f := newFormFixture(t)
	_, err := f.uc.CreateForm(context.Background(), f.tenant, uuid.New(), FormInput{
		Name: "otro", ListID: uuid.New(), Fields: []domain.FormField{{Key: "email", Label: "Correo"}},
		Texts: domain.FormTexts{ConsentText: "Acepto", SuccessMessage: "Gracias"},
	})
	if !errors.Is(err, domain.ErrInvalidForm) {
		t.Fatalf("lista que no es de la empresa: %v", err)
	}
}

func TestFormStatsValidaElPeriodo(t *testing.T) {
	f := newFormFixture(t)
	if _, err := f.uc.FormStats(context.Background(), f.tenant, f.form.ID, 0); !errors.Is(err, domain.ErrInvalidForm) {
		t.Fatalf("days=0: %v", err)
	}
	st, err := f.uc.FormStats(context.Background(), f.tenant, f.form.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	if st.To.Sub(st.From) != 7*24*time.Hour {
		t.Fatalf("periodo %v - %v", st.From, st.To)
	}
}

func TestPublicFormDesactivadoNoExiste(t *testing.T) {
	f := newFormFixture(t)
	disabled := "disabled"
	if _, err := f.uc.UpdateForm(context.Background(), f.tenant, f.form.ID, FormPatch{Status: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.uc.PublicForm(context.Background(), f.tenant, f.form.ID); !errors.Is(err, domain.ErrFormNotFound) {
		t.Fatalf("formulario desactivado: %v", err)
	}
}

func TestFormTokenSigner(t *testing.T) {
	if _, err := NewFormTokenSigner("corto", &counterReader{}); err == nil {
		t.Fatal("un secreto corto se acepta")
	}
	s, _ := NewFormTokenSigner(strings.Repeat("k", 40), &counterReader{})
	tenant, form := uuid.New(), uuid.New()
	issued := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	tok, _ := s.Issue(tenant, form, issued)
	at := issued.Add(10 * time.Second)
	if _, err := s.Verify(tok, tenant, form, at, 3*time.Second, time.Hour); err != nil {
		t.Fatalf("token valido: %v", err)
	}
	cases := map[string]func() (string, uuid.UUID, time.Time){
		"otro formulario": func() (string, uuid.UUID, time.Time) { return tok, uuid.New(), at },
		"caducado":        func() (string, uuid.UUID, time.Time) { return tok, form, issued.Add(2 * time.Hour) },
		"demasiado rapido": func() (string, uuid.UUID, time.Time) {
			return tok, form, issued.Add(time.Second)
		},
		"firma alterada": func() (string, uuid.UUID, time.Time) { return tok[:len(tok)-2] + "AA", form, at },
		"hora alterada": func() (string, uuid.UUID, time.Time) {
			return "1" + tok, form, at
		},
		"vacio": func() (string, uuid.UUID, time.Time) { return "", form, at },
	}
	for name, c := range cases {
		token, f, now := c()
		if _, err := s.Verify(token, tenant, f, now, 3*time.Second, time.Hour); !errors.Is(err, ErrFormTokenInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	other, _ := NewFormTokenSigner(strings.Repeat("x", 40), &counterReader{})
	if _, err := other.Verify(tok, tenant, form, at, 3*time.Second, time.Hour); !errors.Is(err, ErrFormTokenInvalid) {
		t.Fatal("un token de otra clave se acepta")
	}
}

func TestOriginOf(t *testing.T) {
	for in, want := range map[string]string{
		"https://Acme.PE":          "https://acme.pe",
		"https://acme.pe/pagina?x": "https://acme.pe",
		"http://localhost:8080":    "http://localhost:8080",
		"null":                     "",
		"ftp://acme.pe":            "",
		"":                         "",
	} {
		if got := OriginOf(in); got != want {
			t.Errorf("OriginOf(%q) = %q, quiero %q", in, got, want)
		}
	}
}

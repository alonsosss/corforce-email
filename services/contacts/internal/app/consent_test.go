package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

func TestNoSeResuscribePorAPI(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.addContact(t, "baja@example.com", domain.StatusUnsubscribed, domain.ConsentRevoked)

	for _, in := range []ConsentInput{
		{Status: "granted", Method: "api", Source: "crm"},
		{Status: "granted", Method: "form", Source: "https://example.com/form"},
	} {
		if _, err := f.uc.RecordConsent(ctx, f.tenant, c.ID, in); !errors.Is(err, domain.ErrResubscribeRequiresOptIn) {
			t.Fatalf("%+v: se esperaba ErrResubscribeRequiresOptIn, hubo %v", in, err)
		}
	}
	if got := f.contact(t, c.ID); got.Status != domain.StatusUnsubscribed || len(f.s.consents) != 0 || len(f.ev.events) != 0 {
		t.Fatalf("un intento rechazado no deja rastro: %+v consents=%d eventos=%v", got, len(f.s.consents), f.ev.events)
	}

	// Un formulario con la ip de quien lo envio si lo reactiva.
	consent, err := f.uc.RecordConsent(ctx, f.tenant, c.ID, ConsentInput{
		Status: "granted", Method: "form", Source: "https://example.com/form", IP: "203.0.113.9", UserAgent: "Mozilla/5.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if consent.IP == nil || *consent.IP != "203.0.113.9" || consent.Method != domain.MethodForm {
		t.Fatalf("evidencia: %+v", consent)
	}
	got := f.contact(t, c.ID)
	if got.Status != domain.StatusActive || got.ConsentStatus != domain.ConsentGranted {
		t.Fatalf("debe quedar activo y concedido: %+v", got)
	}
	want := []string{"contact.updated|baja@example.com|status", "contact.resubscribed|baja@example.com", "consent.granted|form"}
	if strings.Join(f.ev.events, ";") != strings.Join(want, ";") {
		t.Fatalf("eventos: %v", f.ev.events)
	}
}

func TestRevocarSiempreSePuedeYProtegeIgualQueLaBaja(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.addContact(t, "ana@example.com", domain.StatusActive, domain.ConsentGranted)

	if _, err := f.uc.RecordConsent(ctx, f.tenant, c.ID, ConsentInput{Status: "revoked", Method: "api", Source: "crm"}); err != nil {
		t.Fatal(err)
	}
	if f.contact(t, c.ID).ConsentStatus != domain.ConsentRevoked || f.ev.count("consent.revoked") != 1 {
		t.Fatalf("revocar: %+v %v", f.contact(t, c.ID), f.ev.events)
	}
	// Retirado el consentimiento, la empresa no lo devuelve por API.
	if _, err := f.uc.RecordConsent(ctx, f.tenant, c.ID, ConsentInput{Status: "granted", Method: "api", Source: "crm"}); !errors.Is(err, domain.ErrResubscribeRequiresOptIn) {
		t.Fatalf("se esperaba ErrResubscribeRequiresOptIn, hubo %v", err)
	}
	if _, err := f.uc.RecordConsent(ctx, f.tenant, c.ID, ConsentInput{Status: "granted", Method: "double_opt_in", Source: "x"}); !errors.Is(err, domain.ErrInvalidConsentMethod) {
		t.Fatalf("double_opt_in no se declara por API: %v", err)
	}
	if _, err := f.uc.RecordConsent(ctx, f.tenant, uuid.New(), ConsentInput{Status: "revoked", Method: "api", Source: "x"}); !errors.Is(err, domain.ErrContactNotFound) {
		t.Fatalf("contacto inexistente: %v", err)
	}
}

func TestAltaConConsentimiento(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c, err := f.uc.CreateContact(ctx, f.tenant, CreateContactInput{
		Email: " Luis@Example.com ", FirstName: " Luis ", Locale: "es_pe", Timezone: "America/Lima", Tags: []string{"VIP", "vip"},
		Consent: &ConsentInput{Status: "granted", Method: "form", Source: "https://example.com/alta", IP: "198.51.100.4"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.Email != "luis@example.com" || c.FirstName != "Luis" || *c.Locale != "es-PE" || len(c.Tags) != 1 || c.Source != domain.SourceAPI {
		t.Fatalf("normalizacion: %+v", c)
	}
	if c.ConsentStatus != domain.ConsentGranted || f.contact(t, c.ID).ConsentStatus != domain.ConsentGranted {
		t.Fatalf("consentimiento vigente: %+v", c)
	}
	if strings.Join(f.ev.events, ";") != "contact.created|luis@example.com;consent.granted|form" {
		t.Fatalf("eventos: %v", f.ev.events)
	}
	if _, err := f.uc.CreateContact(ctx, f.tenant, CreateContactInput{Email: "LUIS@example.com"}); !errors.Is(err, domain.ErrContactExists) {
		t.Fatalf("duplicado: %v", err)
	}
	if _, err := f.uc.CreateContact(ctx, f.tenant, CreateContactInput{
		Email: "otro@example.com", Consent: &ConsentInput{Status: "revoked", Method: "api", Source: "x"},
	}); !errors.Is(err, domain.ErrInvalidConsentStatus) {
		t.Fatalf("al crear solo se concede: %v", err)
	}
	if _, err := f.uc.CreateContact(ctx, f.tenant, CreateContactInput{Email: "x@example.com", Source: "import"}); !errors.Is(err, domain.ErrInvalidSource) {
		t.Fatalf("import lo reserva la importacion: %v", err)
	}
}

// flipFirst altera el primer caracter del token sin salirse del alfabeto base64url.
func flipFirst(k string) string {
	if k[0] == 'A' {
		return "B" + k[1:]
	}
	return "A" + k[1:]
}

// tokenFromURL extrae el token del enlace que viaja en contacts.consent.requested.
func tokenFromURL(t *testing.T, f *fixture) string {
	t.Helper()
	u, err := url.Parse(f.ev.confirmURL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != "app.example.com" || u.Path != ConfirmPath {
		t.Fatalf("enlace: %s", f.ev.confirmURL)
	}
	if u.Query().Get("t") != f.tenant.String() {
		t.Fatalf("el enlace debe llevar la empresa: %s", f.ev.confirmURL)
	}
	return u.Query().Get("k")
}

func TestDobleOptIn(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.addContact(t, "doi@example.com", domain.StatusUnsubscribed, domain.ConsentRevoked)

	req, err := f.uc.RequestConfirmation(ctx, f.tenant, c.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if req.Status != domain.ConsentPending || !req.ExpiresAt.Equal(f.now.Add(72*time.Hour)) {
		t.Fatalf("peticion: %+v", req)
	}
	if f.contact(t, c.ID).ConsentStatus != domain.ConsentPending {
		t.Fatal("la peticion deja el consentimiento pending")
	}
	k := tokenFromURL(t, f)
	rawToken, err := base64.RawURLEncoding.DecodeString(k)
	if err != nil || len(rawToken) != 32 {
		t.Fatalf("token de 32 bytes en base64url: %q", k)
	}
	sum := sha256.Sum256(rawToken)
	if len(f.s.tokens) != 1 || f.s.tokens[0].TokenHash != hex.EncodeToString(sum[:]) || strings.Contains(f.s.tokens[0].TokenHash, k) {
		t.Fatalf("solo se guarda el sha256: %+v", f.s.tokens)
	}

	if err := f.uc.CheckConfirmation(ctx, f.tenant, k); err != nil {
		t.Fatalf("el enlace recien pedido debe valer: %v", err)
	}
	// Otra empresa, un token manipulado o uno con otra forma: la misma respuesta.
	for _, tc := range []struct {
		tenant uuid.UUID
		k      string
	}{
		{uuid.New(), k},
		{f.tenant, flipFirst(k)},
		// Mismo token con otros bits de relleno en el ultimo caracter: la forma no
		// canonica tambien se rechaza.
		{f.tenant, k[:len(k)-1] + string(k[len(k)-1]+1)},
		{f.tenant, "no-es-base64!"},
		{f.tenant, base64.RawURLEncoding.EncodeToString([]byte("corto"))},
		{f.tenant, ""},
	} {
		if err := f.uc.Confirm(ctx, tc.tenant, tc.k, "203.0.113.1", "ua"); !errors.Is(err, domain.ErrInvalidConfirmation) {
			t.Fatalf("%+v: se esperaba ErrInvalidConfirmation, hubo %v", tc, err)
		}
	}

	f.ev.events = nil
	if err := f.uc.Confirm(ctx, f.tenant, k, "203.0.113.1", "Mozilla/5.0"); err != nil {
		t.Fatal(err)
	}
	got := f.contact(t, c.ID)
	if got.Status != domain.StatusActive || got.ConsentStatus != domain.ConsentGranted {
		t.Fatalf("tras confirmar: %+v", got)
	}
	last := f.s.consents[len(f.s.consents)-1]
	if last.Method != domain.MethodDoubleOptIn || last.IP == nil || *last.IP != "203.0.113.1" || last.UserAgent == nil {
		t.Fatalf("evidencia del opt-in: %+v", last)
	}
	want := "contact.updated|doi@example.com|status;contact.resubscribed|doi@example.com;consent.granted|double_opt_in"
	if strings.Join(f.ev.events, ";") != want {
		t.Fatalf("eventos: %v", f.ev.events)
	}
	// Un token usado ya no confirma, y la respuesta es la misma que la de uno falso.
	if err := f.uc.Confirm(ctx, f.tenant, k, "203.0.113.1", "ua"); !errors.Is(err, domain.ErrInvalidConfirmation) {
		t.Fatalf("reutilizar: %v", err)
	}
	if err := f.uc.CheckConfirmation(ctx, f.tenant, k); !errors.Is(err, domain.ErrInvalidConfirmation) {
		t.Fatalf("pagina de un token usado: %v", err)
	}
}

func TestDobleOptInCaducaYSoloValeElUltimo(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.addContact(t, "tarde@example.com", domain.StatusActive, domain.ConsentNone)

	if _, err := f.uc.RequestConfirmation(ctx, f.tenant, c.ID, "https://example.com/form"); err != nil {
		t.Fatal(err)
	}
	first := tokenFromURL(t, f)
	if _, err := f.uc.RequestConfirmation(ctx, f.tenant, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	second := tokenFromURL(t, f)
	if first == second || len(f.s.tokens) != 1 {
		t.Fatalf("una peticion nueva retira el enlace anterior: %d tokens", len(f.s.tokens))
	}
	if err := f.uc.Confirm(ctx, f.tenant, first, "", ""); !errors.Is(err, domain.ErrInvalidConfirmation) {
		t.Fatalf("el primer enlace ya no vale: %v", err)
	}

	f.now = f.now.Add(72*time.Hour + time.Second)
	if err := f.uc.Confirm(ctx, f.tenant, second, "", ""); !errors.Is(err, domain.ErrInvalidConfirmation) {
		t.Fatalf("un enlace caducado no confirma: %v", err)
	}
	if f.contact(t, c.ID).ConsentStatus != domain.ConsentPending {
		t.Fatal("un intento caducado no cambia el consentimiento")
	}
}

func TestPedirConfirmacionReglas(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rebota := f.addContact(t, "rebota@example.com", domain.StatusBounced, domain.ConsentNone)
	ya := f.addContact(t, "ya@example.com", domain.StatusActive, domain.ConsentGranted)
	if _, err := f.uc.RequestConfirmation(ctx, f.tenant, rebota.ID, ""); !errors.Is(err, domain.ErrContactNotReachable) {
		t.Fatalf("rebote: %v", err)
	}
	if _, err := f.uc.RequestConfirmation(ctx, f.tenant, ya.ID, ""); !errors.Is(err, domain.ErrConsentAlreadyGranted) {
		t.Fatalf("ya concedido: %v", err)
	}
	if len(f.s.tokens) != 0 || len(f.ev.events) != 0 {
		t.Fatal("una peticion rechazada no deja token ni evento")
	}
}

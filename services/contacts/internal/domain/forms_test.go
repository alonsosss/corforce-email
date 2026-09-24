package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func validForm() *SubscriptionForm {
	return &SubscriptionForm{
		TenantID: uuid.New(), Name: "  Portada ", ListID: uuid.New(),
		Fields: []FormField{{Key: "email", Label: "Correo"}, {Key: "first_name", Label: "Nombre"}},
		Texts:  FormTexts{ConsentText: "Acepto recibir el boletin", SuccessMessage: "Revisa tu correo"},
	}
}

func TestFormNormalizeFuerzaElEmailObligatorio(t *testing.T) {
	f := validForm()
	if err := f.Normalize(Definitions{}); err != nil {
		t.Fatal(err)
	}
	if f.Name != "Portada" || f.Status != FormActive || !f.Fields[0].Required {
		t.Fatalf("formulario normalizado %+v", f)
	}
}

func TestFormNormalizeRechaza(t *testing.T) {
	defs := Definitions{
		"empresa": {Key: "empresa", Type: AttrString, Required: true},
		"edad":    {Key: "edad", Type: AttrNumber},
	}
	cases := map[string]func(f *SubscriptionForm){
		"sin email":          func(f *SubscriptionForm) { f.Fields = f.Fields[1:] },
		"campo repetido":     func(f *SubscriptionForm) { f.Fields = append(f.Fields, FormField{Key: "email", Label: "Otro"}) },
		"campo desconocido":  func(f *SubscriptionForm) { f.Fields = append(f.Fields, FormField{Key: "cargo", Label: "Cargo"}) },
		"sin etiqueta":       func(f *SubscriptionForm) { f.Fields[1].Label = " " },
		"sin consentimiento": func(f *SubscriptionForm) { f.Texts.ConsentText = "" },
		"sin mensaje":        func(f *SubscriptionForm) { f.Texts.SuccessMessage = "" },
		"sin lista":          func(f *SubscriptionForm) { f.ListID = uuid.Nil },
		"estado":             func(f *SubscriptionForm) { f.Status = "borrado" },
		"redireccion http":   func(f *SubscriptionForm) { s := "http://acme.pe/gracias"; f.RedirectURL = &s },
		"redireccion con credenciales": func(f *SubscriptionForm) {
			s := "https://user:pw@acme.pe/"
			f.RedirectURL = &s
		},
		"origen con ruta":    func(f *SubscriptionForm) { f.AllowedOrigins = []string{"https://acme.pe/blog"} },
		"origen http":        func(f *SubscriptionForm) { f.AllowedOrigins = []string{"http://acme.pe"} },
		"origen comodin":     func(f *SubscriptionForm) { f.AllowedOrigins = []string{"https://*.acme.pe"} },
		"demasiados campos":  func(f *SubscriptionForm) { f.Fields = make([]FormField, MaxFormFields+1) },
		"nombre muy largo":   func(f *SubscriptionForm) { f.Name = strings.Repeat("n", MaxFormNameLength+1) },
		"etiqueta con salto": func(f *SubscriptionForm) { f.Fields[1].Label = "a\nb" },
	}
	for name, mutate := range cases {
		f := validForm()
		f.Fields = append(f.Fields, FormField{Key: "empresa", Label: "Empresa", Required: true})
		mutate(f)
		if err := f.Normalize(defs); !errors.Is(err, ErrInvalidForm) {
			t.Errorf("%s: %v", name, err)
		}
	}
	f := validForm()
	if err := f.Normalize(defs); err == nil || !strings.Contains(err.Error(), "empresa") {
		t.Fatalf("un atributo obligatorio que falta: %v", err)
	}
	f = validForm()
	f.Fields = append(f.Fields, FormField{Key: "empresa", Label: "Empresa"})
	if err := f.Normalize(defs); !errors.Is(err, ErrInvalidForm) {
		t.Fatalf("un atributo obligatorio opcional en el formulario: %v", err)
	}
}

func TestNormalizeOrigins(t *testing.T) {
	got, err := NormalizeOrigins([]string{"https://Acme.PE", "https://acme.pe:443/", "https://acme.pe:8443", "https://acme.pe"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "https://acme.pe,https://acme.pe:8443" {
		t.Fatalf("origenes %v", got)
	}
	if _, err := NormalizeOrigins(make([]string, MaxFormAllowedOrigins+1)); !errors.Is(err, ErrInvalidForm) {
		t.Fatal("demasiados origenes")
	}
}

func TestAllowsOrigin(t *testing.T) {
	f := validForm()
	f.AllowedOrigins = []string{"https://acme.pe"}
	for origin, want := range map[string]bool{
		"https://acme.pe":           true,
		"https://app.plataforma.io": true,
		"https://evil.test":         false,
		"https://acme.pe.evil.test": false,
		"":                          false,
	} {
		if got := f.AllowsOrigin(origin, "https://app.plataforma.io"); got != want {
			t.Errorf("%q: %v", origin, got)
		}
	}
}

func TestParseSubmission(t *testing.T) {
	defs := Definitions{"edad": {Key: "edad", Type: AttrNumber}, "vip": {Key: "vip", Type: AttrBoolean}}
	f := validForm()
	f.Fields = append(f.Fields, FormField{Key: "edad", Label: "Edad"}, FormField{Key: "vip", Label: "VIP"})
	if err := f.Normalize(defs); err != nil {
		t.Fatal(err)
	}
	sub, err := f.ParseSubmission(map[string]json.RawMessage{
		"email": json.RawMessage(`" Ana@Acme.PE "`), "first_name": json.RawMessage(`"Ana"`),
		"edad": json.RawMessage(`41`), "vip": json.RawMessage(`true`),
	}, defs)
	if err != nil {
		t.Fatal(err)
	}
	if sub.Email != "ana@acme.pe" || sub.FirstName != "Ana" || len(sub.Attributes) != 2 {
		t.Fatalf("envio %+v", sub)
	}
	bad := map[string]map[string]json.RawMessage{
		"sin email":       {"first_name": json.RawMessage(`"Ana"`)},
		"email no valido": {"email": json.RawMessage(`"ana"`)},
		"campo ajeno":     {"email": json.RawMessage(`"a@acme.pe"`), "salario": json.RawMessage(`1`)},
		"tipo":            {"email": json.RawMessage(`"a@acme.pe"`), "edad": json.RawMessage(`"cuarenta"`)},
		"nombre largo":    {"email": json.RawMessage(`"a@acme.pe"`), "first_name": json.RawMessage(`"` + strings.Repeat("x", 201) + `"`)},
	}
	for name, values := range bad {
		if _, err := f.ParseSubmission(values, defs); !errors.Is(err, ErrInvalidSubmission) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestFormValuesFromStrings(t *testing.T) {
	defs := Definitions{
		"edad": {Key: "edad", Type: AttrNumber}, "vip": {Key: "vip", Type: AttrBoolean},
		"alta": {Key: "alta", Type: AttrDate},
	}
	f := validForm()
	f.Fields = append(f.Fields, FormField{Key: "edad", Label: "Edad"}, FormField{Key: "vip", Label: "VIP"}, FormField{Key: "alta", Label: "Alta"})
	out, err := f.FormValuesFromStrings(map[string]string{"email": "a@acme.pe", "edad": "12345678901234567890.5", "alta": "2026-01-02"}, defs)
	if err != nil {
		t.Fatal(err)
	}
	if string(out["edad"]) != "12345678901234567890.5" || string(out["vip"]) != "false" || string(out["alta"]) != `"2026-01-02"` {
		t.Fatalf("valores %v", out)
	}
	if _, err := f.FormValuesFromStrings(map[string]string{"email": "a@acme.pe", "edad": "1e999999"}, defs); err != nil {
		t.Fatalf("el literal lo valida el tipo, no la conversion: %v", err)
	}
	if _, err := f.FormValuesFromStrings(map[string]string{"email": "a@acme.pe", "edad": "doce"}, defs); !errors.Is(err, ErrInvalidSubmission) {
		t.Fatal("un numero no valido se acepta")
	}
	if _, err := f.FormValuesFromStrings(map[string]string{"otro": "x"}, defs); !errors.Is(err, ErrInvalidSubmission) {
		t.Fatal("un campo que el formulario no pide se acepta")
	}
	out, _ = f.FormValuesFromStrings(map[string]string{"email": "a@acme.pe", "vip": "on"}, defs)
	if string(out["vip"]) != "true" {
		t.Fatalf("casilla marcada: %s", out["vip"])
	}
}

func TestActiveFieldsIgnoraUnAtributoRetirado(t *testing.T) {
	f := validForm()
	f.Fields = append(f.Fields, FormField{Key: "retirado", Label: "Retirado", Required: true})
	if got := len(f.ActiveFields(Definitions{})); got != 2 {
		t.Fatalf("campos activos %d", got)
	}
	if _, err := f.ParseSubmission(map[string]json.RawMessage{"email": json.RawMessage(`"a@acme.pe"`), "retirado": json.RawMessage(`"x"`)}, Definitions{}); err != nil {
		t.Fatalf("un atributo retirado bloquea el formulario: %v", err)
	}
}

func TestSubmissionOutcomeFor(t *testing.T) {
	cases := []struct {
		status  Status
		consent ConsentStatus
		want    SubmissionOutcome
	}{
		{StatusActive, ConsentNone, OutcomeConfirmationSent},
		{StatusActive, ConsentPending, OutcomeConfirmationSent},
		{StatusUnsubscribed, ConsentRevoked, OutcomeConfirmationSent},
		{StatusActive, ConsentGranted, OutcomeAlreadySubscribed},
		{StatusBounced, ConsentNone, OutcomeNotReachable},
		{StatusComplained, ConsentGranted, OutcomeNotReachable},
		{StatusInvalid, ConsentNone, OutcomeNotReachable},
		{StatusExcluded, ConsentNone, OutcomeNotReachable},
	}
	for _, c := range cases {
		if got := SubmissionOutcomeFor(&Contact{Status: c.status, ConsentStatus: c.consent}); got != c.want {
			t.Errorf("%s/%s: %s", c.status, c.consent, got)
		}
	}
}

func TestTruncateIPYEvidencia(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.77":        "203.0.113.0/24",
		"2001:db8:1:2:3::4":   "2001:db8:1::/48",
		"::ffff:198.51.100.9": "198.51.100.0/24",
		"no-ip":               "",
	} {
		if got := TruncateIP(in); got != want {
			t.Errorf("TruncateIP(%q) = %q", in, got)
		}
	}
	f := validForm()
	f.ID = uuid.New()
	ev := FormConsentEvidence(f, "203.0.113.77", "https://acme.pe")
	if ev["consent_text"] != f.Texts.ConsentText || ev["ip_prefix"] != "203.0.113.0/24" || ev["origin"] != "https://acme.pe" {
		t.Fatalf("evidencia %v", ev)
	}
	if s, _ := ev["consent_text_sha256"].(string); len(s) != 64 {
		t.Fatalf("huella %v", ev["consent_text_sha256"])
	}
	for _, v := range ev {
		if s, _ := v.(string); strings.Contains(s, "203.0.113.77") {
			t.Fatal("la evidencia lleva la ip completa")
		}
	}
}

package http

import (
	"bytes"
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/contacts/internal/app"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/alonsosss/corforce-email/services/contacts/internal/ports"
	"github.com/google/uuid"
)

// Dobles minimos para el alta y la importacion. Embeben el puerto: un metodo que estas
// pruebas no deberian alcanzar entra en panico en lugar de responder algo inventado.

type admissionContacts struct {
	ports.ContactRepository
	byEmail map[string]domain.Contact
}

func (r *admissionContacts) Insert(_ context.Context, c *domain.Contact) error {
	if _, ok := r.byEmail[c.Email]; ok {
		return domain.ErrContactExists
	}
	c.ID, c.ConsentStatus = uuid.New(), domain.ConsentNone
	if c.Attributes == nil {
		c.Attributes = map[string]any{}
	}
	if c.Tags == nil {
		c.Tags = []string{}
	}
	r.byEmail[c.Email] = *c
	return nil
}

func (r *admissionContacts) FindByEmailsForUpdate(_ context.Context, _ uuid.UUID, emails []string) ([]domain.Contact, error) {
	out := []domain.Contact{}
	for _, e := range emails {
		if c, ok := r.byEmail[e]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}

func (r *admissionContacts) InsertMany(ctx context.Context, contacts []domain.Contact) ([]domain.Contact, error) {
	out := make([]domain.Contact, 0, len(contacts))
	for i := range contacts {
		c := contacts[i]
		if err := r.Insert(ctx, &c); err == nil {
			out = append(out, c)
		}
	}
	return out, nil
}

type admissionConsents struct {
	ports.ConsentRepository
	appended int
}

func (r *admissionConsents) Append(_ context.Context, c *domain.Consent) error {
	c.ID, c.OccurredAt = uuid.New(), time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	r.appended++
	return nil
}

func (r *admissionConsents) AppendMany(_ context.Context, cs []domain.Consent) error {
	r.appended += len(cs)
	return nil
}

type admissionImports struct {
	ports.ImportRepository
	saved []domain.Import
}

func (r *admissionImports) Create(_ context.Context, imp *domain.Import) error {
	r.saved = append(r.saved, *imp)
	return nil
}

type admissionEvents struct{ ports.EventPublisher }

func (admissionEvents) ContactCreated(context.Context, *domain.Contact) error { return nil }
func (admissionEvents) ConsentGranted(context.Context, *domain.Consent) error { return nil }
func (admissionEvents) ImportCompleted(context.Context, *domain.Import) error { return nil }

type passTx struct{}

func (passTx) Transact(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

type causesByEmail struct {
	causes map[string][]domain.ActiveCause
	err    error
}

func (s causesByEmail) ActiveCauses(ctx context.Context, tenantID uuid.UUID, email string) ([]domain.ActiveCause, error) {
	out, err := s.ActiveCausesOf(ctx, tenantID, []string{email})
	return out[email], err
}

func (s causesByEmail) ActiveCausesOf(_ context.Context, _ uuid.UUID, emails []string) (map[string][]domain.ActiveCause, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := map[string][]domain.ActiveCause{}
	for _, e := range emails {
		if c, ok := s.causes[e]; ok {
			out[e] = c
		}
	}
	return out, nil
}

type admissionFixture struct {
	h        *Handler
	contacts *admissionContacts
	consents *admissionConsents
	imports  *admissionImports
	tenant   uuid.UUID
}

func newAdmissionFixture(sup causesByEmail) *admissionFixture {
	f := &admissionFixture{
		contacts: &admissionContacts{byEmail: map[string]domain.Contact{}},
		consents: &admissionConsents{}, imports: &admissionImports{}, tenant: uuid.New(),
	}
	uc := app.New(app.Deps{
		Contacts: f.contacts, Consents: f.consents, Imports: f.imports, Attributes: attributesOnly{},
		Tx: passTx{}, Events: admissionEvents{}, Suppression: sup,
	})
	f.h = NewHandler(Deps{UC: uc, Perms: &recordingGuard{}})
	return f
}

func (f *admissionFixture) post(handle func(nethttp.ResponseWriter, *nethttp.Request), body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(nethttp.MethodPost, "/", strings.NewReader(body))
	req = req.WithContext(middleware.WithIdentity(req.Context(), uuid.NewString(), f.tenant.String()))
	rec := httptest.NewRecorder()
	handle(rec, req)
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("cuerpo de error ilegible: %v %s", err, rec.Body)
	}
	return env.Error.Code, env.Error.Message
}

func causes(list ...domain.SuppressionCause) []domain.ActiveCause {
	out := make([]domain.ActiveCause, len(list))
	for i, c := range list {
		out[i] = domain.ActiveCause{Cause: c}
	}
	return out
}

// El alta devuelve el contacto con el estado con que entro; una baja sin prueba de que la
// persona vuelve es 409 RESUBSCRIBE_REQUIRES_OPT_IN; sin suppression, 503
// SUPPRESSION_UNAVAILABLE con un mensaje fijo, sin el detalle interno del fallo.
func TestAltaContratoConSuppression(t *testing.T) {
	f := newAdmissionFixture(causesByEmail{causes: map[string][]domain.ActiveCause{
		"baja@example.com": causes(domain.CauseUnsubscribe),
	}})
	rec := f.post(f.h.CreateContact, `{"email":"BAJA@example.com"}`)
	if rec.Code != nethttp.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var created struct {
		Data struct {
			Status        string `json:"status"`
			ConsentStatus string `json:"consent_status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.Data.Status != "unsubscribed" || created.Data.ConsentStatus != "none" {
		t.Fatalf("el alta devuelve el estado con que entro: %s %v", rec.Body, err)
	}

	f = newAdmissionFixture(causesByEmail{causes: map[string][]domain.ActiveCause{
		"baja@example.com": causes(domain.CauseUnsubscribe),
	}})
	rec = f.post(f.h.CreateContact, `{"email":"baja@example.com","consent":{"status":"granted","method":"api","source":"crm"}}`)
	if code, _ := errorCode(t, rec); rec.Code != nethttp.StatusConflict || code != "RESUBSCRIBE_REQUIRES_OPT_IN" {
		t.Fatalf("consentimiento sin prueba sobre una baja: %d %s", rec.Code, rec.Body)
	}
	if len(f.contacts.byEmail) != 0 || f.consents.appended != 0 {
		t.Fatal("el 409 no escribe nada")
	}

	f = newAdmissionFixture(causesByEmail{err: context.DeadlineExceeded})
	rec = f.post(f.h.CreateContact, `{"email":"ana@example.com"}`)
	code, msg := errorCode(t, rec)
	if rec.Code != nethttp.StatusServiceUnavailable || code != "SUPPRESSION_UNAVAILABLE" || msg != app.ErrSuppressionUnavailable.Error() {
		t.Fatalf("suppression caido: %d %s", rec.Code, rec.Body)
	}
	if len(f.contacts.byEmail) != 0 {
		t.Fatal("el 503 no escribe nada")
	}
}

// importResponseContract escribe a mano los nombres JSON de la respuesta de la importacion
// que consume la interfaz; suppressed es aditivo y siempre es un objeto.
type importResponseContract struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Total   int    `json:"total"`
	Created int    `json:"created"`
	Updated int    `json:"updated"`
	Skipped int    `json:"skipped"`
	Errors  []struct {
		Line   int    `json:"line"`
		Reason string `json:"reason"`
	} `json:"errors"`
	Suppressed map[string]int `json:"suppressed"`
}

func decodeImport(t *testing.T, rec *httptest.ResponseRecorder) importResponseContract {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	var got importResponseContract
	dec := json.NewDecoder(bytes.NewReader(env.Data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("la respuesta no cumple el contrato: %v", err)
	}
	return got
}

func TestImportacionContratoConSuppression(t *testing.T) {
	f := newAdmissionFixture(causesByEmail{causes: map[string][]domain.ActiveCause{
		"baja@example.com":   causes(domain.CauseUnsubscribe),
		"manual@example.com": causes(domain.CauseManual),
	}})
	body := `{"rows":[{"email":"baja@example.com"},{"email":"manual@example.com"},{"email":"libre@example.com"},{"email":"mala"}],` +
		`"consent":{"status":"granted","basis":"Formulario de la feria"}}`
	rec := f.post(f.h.Import, body)
	if rec.Code != nethttp.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decodeImport(t, rec)
	if got.Status != "completed" || got.Total != 4 || got.Created != 3 || got.Skipped != 1 || len(got.Errors) != 1 {
		t.Fatalf("conteos: %+v", got)
	}
	if want := map[string]int{"unsubscribed": 1, "excluded": 1}; !reflect.DeepEqual(got.Suppressed, want) {
		t.Fatalf("suppressed = %v, se esperaba %v", got.Suppressed, want)
	}
	if f.consents.appended != 1 {
		t.Fatalf("solo el active recibe el consentimiento declarado: %d", f.consents.appended)
	}

	f = newAdmissionFixture(causesByEmail{})
	rec = f.post(f.h.Import, `{"rows":[{"email":"libre@example.com"}]}`)
	if rec.Code != nethttp.StatusCreated || !strings.Contains(rec.Body.String(), `"suppressed":{}`) {
		t.Fatalf("sin excluidos suppressed es un objeto vacio, nunca null: %s", rec.Body)
	}

	f = newAdmissionFixture(causesByEmail{err: context.DeadlineExceeded})
	rec = f.post(f.h.Import, `{"rows":[{"email":"libre@example.com"}]}`)
	if code, _ := errorCode(t, rec); rec.Code != nethttp.StatusServiceUnavailable || code != "SUPPRESSION_UNAVAILABLE" {
		t.Fatalf("suppression caido: %d %s", rec.Code, rec.Body)
	}
	if len(f.contacts.byEmail) != 0 || len(f.imports.saved) != 1 || f.imports.saved[0].Status != domain.ImportFailed {
		t.Fatalf("sin contactos y con el rastro failed: %d %+v", len(f.contacts.byEmail), f.imports.saved)
	}
}

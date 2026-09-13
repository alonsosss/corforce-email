package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/alonsosss/corforce-email/services/automations/internal/ports"
	"github.com/google/uuid"
)

const confirmURL = "https://app.example.com/api/v1/public/contacts/confirm?t=tenant&k=secreto-de-un-solo-uso"

func (f *fixture) enableDOI() uuid.UUID {
	tpl := uuid.New()
	f.store.Settings[f.tenant] = domain.DOISettings{
		TenantID: f.tenant, Enabled: true, TemplateID: &tpl, FromEmail: "hola@shop.example.com", FromName: "Tienda",
	}
	return tpl
}

func (f *fixture) consentRequest(contactID uuid.UUID) domain.ConsentRequest {
	return domain.ConsentRequest{
		EventID: uuid.NewString(), TenantID: f.tenant, ContactID: contactID,
		Email: "ana@example.com", ConfirmURL: confirmURL, FirstName: "Ana",
	}
}

func TestDOISinAjustesODesactivadoQuedaOmitido(t *testing.T) {
	f := newFixture(t, Config{})
	req := f.consentRequest(uuid.New())
	status, err := f.uc.HandleConsentRequested(ctx, req)
	if err != nil || status != domain.DOISkipped {
		t.Fatalf("sin ajustes: %s %v", status, err)
	}
	d, _ := f.store.Delivery(req.EventID)
	if d.Reason != domain.ReasonNotConfigured {
		t.Fatalf("motivo: %q", d.Reason)
	}

	f.enableDOI()
	s := f.store.Settings[f.tenant]
	s.Enabled = false
	f.store.Settings[f.tenant] = s
	req2 := f.consentRequest(uuid.New())
	if status, err := f.uc.HandleConsentRequested(ctx, req2); err != nil || status != domain.DOISkipped {
		t.Fatalf("desactivado: %s %v", status, err)
	}
	if d, _ := f.store.Delivery(req2.EventID); d.Reason != domain.ReasonDisabled {
		t.Fatalf("motivo: %q", d.Reason)
	}
	if len(f.sender.DOICalls) != 0 {
		t.Fatal("no se envia nada")
	}
}

func TestDOIEnviaUnCorreoTransaccionalConElEnlace(t *testing.T) {
	f := newFixture(t, Config{})
	tpl := f.enableDOI()
	req := f.consentRequest(uuid.New())

	status, err := f.uc.HandleConsentRequested(ctx, req)
	if err != nil || status != domain.DOISent {
		t.Fatalf("%s %v", status, err)
	}
	if len(f.sender.DOICalls) != 1 {
		t.Fatalf("un envio: %d", len(f.sender.DOICalls))
	}
	m := f.sender.DOICalls[0]
	if m.IdempotencyKey != "doi:"+req.EventID || m.TemplateID != tpl || m.ToEmail != "ana@example.com" || m.ToName != "Ana" ||
		m.FromEmail != "hola@shop.example.com" || m.Variables["confirm_url"] != confirmURL || m.Variables["first_name"] != "Ana" {
		t.Fatalf("mensaje: %+v", m)
	}
	d, _ := f.store.Delivery(req.EventID)
	if d.Status != domain.DOISent || d.MessageID == nil || d.SentAt == nil {
		t.Fatalf("entrega: %+v", d)
	}
	if f.store.ContactLocks != 1 {
		t.Fatal("el intento se reclama con el contacto bloqueado")
	}
	list, _, err := f.uc.ListDeliveries(ctx, f.tenant, "", 1, 25)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(list)
	if strings.Contains(string(body), "secreto-de-un-solo-uso") || strings.Contains(string(body), "confirm_url") {
		t.Fatalf("el historial no expone el enlace: %s", body)
	}
}

func TestDOIReentregaNoEnviaDosVeces(t *testing.T) {
	f := newFixture(t, Config{})
	f.enableDOI()
	req := f.consentRequest(uuid.New())

	// Primera entrega: transactional no responde; queda pending y se pide reintentar.
	f.sender.DOIErrs = []error{ports.ErrUnavailable}
	if _, err := f.uc.HandleConsentRequested(ctx, req); err == nil || errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("un fallo transitorio pide reintento: %v", err)
	}
	if d, _ := f.store.Delivery(req.EventID); d.Status != domain.DOIPending || d.Attempts != 1 {
		t.Fatalf("queda en vuelo: %+v", d)
	}
	// Reentrega: mismo evento, misma clave.
	if status, err := f.uc.HandleConsentRequested(ctx, req); err != nil || status != domain.DOISent {
		t.Fatalf("reentrega: %s %v", status, err)
	}
	// Otra reentrega ya terminada: no llama a nadie.
	if status, err := f.uc.HandleConsentRequested(ctx, req); err != nil || status != domain.DOISent {
		t.Fatalf("tercera entrega: %s %v", status, err)
	}
	if len(f.sender.DOICalls) != 2 || f.sender.Created() != 1 {
		t.Fatalf("dos llamadas con la misma clave y un solo mensaje: %d llamadas, %d mensajes", len(f.sender.DOICalls), f.sender.Created())
	}
	for _, c := range f.sender.DOICalls {
		if c.IdempotencyKey != "doi:"+req.EventID {
			t.Fatalf("clave: %s", c.IdempotencyKey)
		}
	}
}

func TestDOILimitePorContacto(t *testing.T) {
	f := newFixture(t, Config{DOILimits: domain.DOILimits{PerDay: 1, Per30Days: 3}})
	f.enableDOI()
	contact := uuid.New()
	send := func() domain.DOIStatus {
		t.Helper()
		req := f.consentRequest(contact)
		status, err := f.uc.HandleConsentRequested(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if status == domain.DOISkipped {
			if d, _ := f.store.Delivery(req.EventID); d.Reason != domain.ReasonRateLimited {
				t.Fatalf("motivo: %q", d.Reason)
			}
		}
		return status
	}
	if send() != domain.DOISent {
		t.Fatal("el primero sale")
	}
	if send() != domain.DOISkipped {
		t.Fatal("el segundo del mismo dia se omite")
	}
	for i := 0; i < 2; i++ {
		f.clock.Advance(25 * time.Hour)
		if send() != domain.DOISent {
			t.Fatalf("dia %d: cabe en la ventana de 30 dias", i+2)
		}
	}
	f.clock.Advance(25 * time.Hour)
	if send() != domain.DOISkipped {
		t.Fatal("el cuarto en 30 dias se omite")
	}
	if status, _ := f.uc.HandleConsentRequested(ctx, f.consentRequest(uuid.New())); status != domain.DOISent {
		t.Fatal("otro contacto no comparte el limite")
	}
	f.clock.Advance(30 * 24 * time.Hour)
	if send() != domain.DOISent {
		t.Fatal("pasada la ventana vuelve a caber")
	}
}

func TestDOI429Y503NoConfirmanYAgotanLosIntentos(t *testing.T) {
	f := newFixture(t, Config{})
	f.enableDOI()
	req := f.consentRequest(uuid.New())
	f.sender.DOIErrs = []error{&ports.RateLimitedError{RetryAfter: 30 * time.Second}}
	if _, err := f.uc.HandleConsentRequested(ctx, req); err == nil {
		t.Fatal("un 429 no se confirma")
	}
	for i := 0; i < domain.DOIMaxAttempts; i++ {
		f.sender.DOIErrs = append(f.sender.DOIErrs, ports.ErrUnavailable)
	}
	var status domain.DOIStatus
	var err error
	for i := 1; i < domain.DOIMaxAttempts; i++ {
		status, err = f.uc.HandleConsentRequested(ctx, req)
	}
	if err != nil || status != domain.DOIFailed {
		t.Fatalf("al agotar los intentos queda fallido y se confirma: %s %v", status, err)
	}
	d, _ := f.store.Delivery(req.EventID)
	if d.Status != domain.DOIFailed || d.Attempts != domain.DOIMaxAttempts || !strings.HasPrefix(d.Reason, domain.CodeUnavailable) {
		t.Fatalf("entrega: %+v", d)
	}
}

func TestDOIRechazosDeNegocioFallanYSeConfirman(t *testing.T) {
	cases := map[string]error{
		"dominio":   &ports.RejectedError{Status: 422, Code: "SENDING_DOMAIN_NOT_VERIFIED", Message: "x"},
		"plantilla": &ports.RejectedError{Status: 422, Code: "TEMPLATE_NOT_TRANSACTIONAL", Message: "x"},
		"bloqueo":   &ports.BlockedError{Code: "SENDING_RESTRICTED", Message: "suspended"},
	}
	for name, cause := range cases {
		f := newFixture(t, Config{})
		f.enableDOI()
		req := f.consentRequest(uuid.New())
		f.sender.DOIErrs = []error{cause}
		status, err := f.uc.HandleConsentRequested(ctx, req)
		if err != nil || status != domain.DOIFailed {
			t.Fatalf("%s: %s %v", name, status, err)
		}
		d, _ := f.store.Delivery(req.EventID)
		if !strings.Contains(d.Reason, cause.(interface{ Error() string }).Error()[:10]) {
			t.Fatalf("%s: el motivo lleva el codigo: %q", name, d.Reason)
		}
	}
}

func TestDOISuprimidoQuedaFallido(t *testing.T) {
	f := newFixture(t, Config{})
	f.enableDOI()
	f.sender.DOIStatus = ports.MessageStatusSuppressed
	f.sender.DOISuppressed = []ports.Suppressed{{Email: "ana@example.com", Reason: "hard_bounce"}}
	req := f.consentRequest(uuid.New())
	if status, err := f.uc.HandleConsentRequested(ctx, req); err != nil || status != domain.DOIFailed {
		t.Fatalf("%s %v", status, err)
	}
	if d, _ := f.store.Delivery(req.EventID); d.Reason != "SUPPRESSED: hard_bounce" {
		t.Fatalf("motivo: %q", d.Reason)
	}
}

func TestDOIEventoInvalidoNoSeReintenta(t *testing.T) {
	f := newFixture(t, Config{})
	f.enableDOI()
	req := f.consentRequest(uuid.New())
	req.ConfirmURL = ""
	if _, err := f.uc.HandleConsentRequested(ctx, req); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("%v", err)
	}
}

func TestAjustesDOIValidanLaPlantilla(t *testing.T) {
	f := newFixture(t, Config{})
	tpl := uuid.New()
	in := domain.DOISettings{Enabled: true, TemplateID: &tpl, FromEmail: "hola@shop.example.com"}

	f.templates.Kind = KindMarketing
	if _, err := f.uc.UpdateDOISettings(ctx, f.tenant, in, &f.user); !errors.Is(err, domain.ErrTemplateNotTransactional) {
		t.Fatalf("plantilla de marketing: %v", err)
	}
	f.templates.Kind = KindTransactional
	f.templates.OmitConfirm = true
	if _, err := f.uc.UpdateDOISettings(ctx, f.tenant, in, &f.user); !errors.Is(err, domain.ErrTemplateMissingConfirmURL) {
		t.Fatalf("sin confirm_url: %v", err)
	}
	f.templates.OmitConfirm = false
	f.templates.Kind = ""
	if _, err := f.uc.UpdateDOISettings(ctx, f.tenant, in, &f.user); !errors.Is(err, domain.ErrTemplateKindUnknown) {
		t.Fatalf("sin tipo se falla cerrado: %v", err)
	}
	f.templates.Kind = KindTransactional
	f.templates.Err = domain.ErrTemplateVariables
	if _, err := f.uc.UpdateDOISettings(ctx, f.tenant, in, &f.user); !errors.Is(err, domain.ErrTemplateVariables) {
		t.Fatalf("variables que no se proporcionan: %v", err)
	}
	f.templates.Err = nil
	saved, err := f.uc.UpdateDOISettings(ctx, f.tenant, in, &f.user)
	if err != nil || !saved.Enabled || saved.UpdatedBy == nil || *saved.UpdatedBy != f.user {
		t.Fatalf("guardar: %v %+v", err, saved)
	}
	last := f.templates.Calls[len(f.templates.Calls)-1]
	var probe string
	_ = json.Unmarshal(last.Variables["confirm_url"], &probe)
	if !strings.HasPrefix(probe, "https://app.example.com"+doiProbePath) {
		t.Fatalf("el enlace de prueba cuelga de la URL publica: %q", probe)
	}
	got, err := f.uc.GetDOISettings(ctx, f.tenant)
	if err != nil || got.TemplateID == nil || *got.TemplateID != tpl {
		t.Fatalf("leer: %v %+v", err, got)
	}
	if _, err := f.uc.UpdateDOISettings(ctx, f.tenant, domain.DOISettings{Enabled: true}, nil); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("activado sin plantilla: %v", err)
	}
	empty, err := f.uc.GetDOISettings(ctx, uuid.New())
	if err != nil || empty.Enabled {
		t.Fatalf("sin fila, desactivado: %v %+v", err, empty)
	}
}

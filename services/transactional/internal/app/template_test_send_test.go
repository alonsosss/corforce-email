package app

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/alonsosss/corforce-email/services/transactional/internal/ports"
	"github.com/google/uuid"
)

func templateTestCommand(f *fixture, to ...string) TemplateTestCommand {
	requester := uuid.New()
	return TemplateTestCommand{
		TenantID:        f.tenant,
		RequestedBy:     &requester,
		From:            domain.Recipient{Email: " hola@" + shopDomain, Name: " Tienda "},
		To:              to,
		TemplateID:      uuid.New(),
		TemplateVersion: 2,
		Variables:       map[string]any{"first_name": "Ana"},
	}
}

func newTemplateTestFixture(t *testing.T, cfg Config) *fixture {
	t.Helper()
	f := newFixture(t, cfg)
	f.setDomain(f.tenant, shopDomain, "verified", "both")
	return f
}

func TestTemplateTestTransactionalGoesOutMarkedAsTest(t *testing.T) {
	f := newTemplateTestFixture(t, Config{})
	res, err := f.uc.CreateTemplateTest(ctx, templateTestCommand(f, "qa@example.com", "Dev@Example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 2 || len(res.Suppressed) != 0 {
		t.Fatalf("respuesta: %+v", res)
	}
	for _, s := range res.Messages {
		m := f.repo.messages[s.ID]
		if !m.Test || m.Class != domain.ClassTransactional || m.Unsubscribable || m.CampaignID != nil {
			t.Fatalf("mensaje de prueba mal clasificado: %+v", m)
		}
		if !strings.HasPrefix(m.Subject, domain.TestSubjectPrefix) || m.FromEmail != "hola@"+shopDomain || m.FromName != "Tienda" {
			t.Fatalf("asunto o remitente: %q %q %q", m.Subject, m.FromEmail, m.FromName)
		}
		if m.Tags[domain.TestSendTag] != domain.TestSendValue || m.CreatedBy == nil {
			t.Fatalf("etiquetas o autor: %+v %v", m.Tags, m.CreatedBy)
		}
	}
	if got := len(f.repo.published("transactional.message.queued")); got != 2 {
		t.Fatalf("se encolan en el carril transaccional: %d", got)
	}
	if len(f.rep.calls) != 1 || f.rep.calls[0] != (reputationCall{Class: domain.ClassTransactional, Count: 2}) {
		t.Fatalf("reputation autoriza la prueba: %+v", f.rep.calls)
	}
	call, ok := f.tpl.callFor("qa@example.com")
	if !ok || !call.Test || call.Version == nil || *call.Version != 2 {
		t.Fatalf("render de prueba: %+v", call)
	}
	if !strings.Contains(call.Reserved.ViewInBrowserURL, domain.ViewInBrowserPath) || call.Reserved.UnsubscribeURL == "" {
		t.Fatalf("los enlaces reservados son los reales del mensaje: %+v", call.Reserved)
	}
}

func TestTemplateTestMarketingUsesMarketingLaneWithoutCampaign(t *testing.T) {
	f := newTemplateTestFixture(t, Config{})
	f.tpl.kind = domain.TemplateKindMarketing
	res, err := f.uc.CreateTemplateTest(ctx, templateTestCommand(f, "qa@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	m := f.repo.messages[res.Messages[0].ID]
	if m.Class != domain.ClassMarketing || !m.Unsubscribable || !m.Test || m.CampaignID != nil || m.ContactID != nil {
		t.Fatalf("prueba de marketing: %+v", m)
	}
	if got := len(f.repo.published("transactional.marketing.queued")); got != 1 {
		t.Fatalf("sale por la cola de marketing: %d", got)
	}
	if f.rep.calls[0].Class != domain.ClassMarketing {
		t.Fatalf("reputation autoriza la clase marketing: %+v", f.rep.calls)
	}
}

func TestTemplateTestRespectsSuppression(t *testing.T) {
	f := newTemplateTestFixture(t, Config{})
	f.supp.suppressed = map[string]string{"baja@example.com": "unsubscribe"}
	res, err := f.uc.CreateTemplateTest(ctx, templateTestCommand(f, "baja@example.com", "qa@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 1 || res.Messages[0].To[0].Email != "qa@example.com" || len(res.Suppressed) != 1 {
		t.Fatalf("la suprimida no se envia: %+v", res)
	}

	f2 := newTemplateTestFixture(t, Config{})
	f2.supp.suppressed = map[string]string{"baja@example.com": "complaint"}
	res, err = f2.uc.CreateTemplateTest(ctx, templateTestCommand(f2, "baja@example.com"))
	if err != nil || len(res.Messages) != 0 || len(f2.repo.messages) != 0 || len(f2.rep.calls) != 0 {
		t.Fatalf("todas suprimidas: nada creado ni autorizado: %+v %v", res, err)
	}
}

func TestTemplateTestRejections(t *testing.T) {
	six := []string{"a@example.com", "b@example.com", "c@example.com", "d@example.com", "e@example.com", "f@example.com"}
	cases := map[string]struct {
		mutate func(f *fixture, cmd *TemplateTestCommand)
		want   func(error) bool
	}{
		"sin destinatarios": {func(_ *fixture, c *TemplateTestCommand) { c.To = nil }, domain.IsValidation},
		"mas de cinco":      {func(_ *fixture, c *TemplateTestCommand) { c.To = six }, domain.IsValidation},
		"repetido":          {func(_ *fixture, c *TemplateTestCommand) { c.To = []string{"qa@example.com", "QA@example.com"} }, domain.IsValidation},
		"direccion mala":    {func(_ *fixture, c *TemplateTestCommand) { c.To = []string{"no-es-correo"} }, domain.IsValidation},
		"sin version":       {func(_ *fixture, c *TemplateTestCommand) { c.TemplateVersion = 0 }, domain.IsValidation},
		"remitente sin verificar": {func(_ *fixture, c *TemplateTestCommand) { c.From.Email = "hola@otro.example" }, func(err error) bool {
			return errors.Is(err, domain.ErrSendingDomainNotVerified)
		}},
		"sin tipo de plantilla": {func(f *fixture, _ *TemplateTestCommand) { f.tpl.kind = "" }, func(err error) bool {
			return errors.Is(err, domain.ErrTemplatesUnavailable)
		}},
		"reputation deniega": {func(f *fixture, _ *TemplateTestCommand) {
			f.rep.auth = &ports.Authorization{Allowed: false, Reason: domain.DenySuspended}
		}, func(err error) bool {
			var d *domain.SendingDeniedError
			return errors.As(err, &d) && d.Restricted()
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newTemplateTestFixture(t, Config{})
			cmd := templateTestCommand(f, "qa@example.com")
			tc.mutate(f, &cmd)
			_, err := f.uc.CreateTemplateTest(ctx, cmd)
			if !tc.want(err) {
				t.Fatalf("error inesperado: %v", err)
			}
			if len(f.repo.messages) != 0 || len(f.repo.outbox) != 0 {
				t.Fatal("un rechazo no deja nada creado")
			}
		})
	}
}

func TestTemplateTestHourlyLimit(t *testing.T) {
	f := newTemplateTestFixture(t, Config{TestSendsPerHour: 3})
	if _, err := f.uc.CreateTemplateTest(ctx, templateTestCommand(f, "a@example.com", "b@example.com")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.CreateTemplateTest(ctx, templateTestCommand(f, "c@example.com", "d@example.com")); !errors.Is(err, domain.ErrTestSendLimit) {
		t.Fatalf("la tercera y cuarta prueba de la hora superan el tope de 3: %v", err)
	}
	if _, err := f.uc.CreateTemplateTest(ctx, templateTestCommand(f, "c@example.com")); err != nil {
		t.Fatalf("la tercera cabe: %v", err)
	}
	f.now = f.now.Add(testSendWindow + time.Minute)
	if _, err := f.uc.CreateTemplateTest(ctx, templateTestCommand(f, "e@example.com", "g@example.com")); err != nil {
		t.Fatalf("pasada la hora el tope se libera: %v", err)
	}
}

// Las pruebas no cuentan en las estadisticas del servicio.
func TestStatsExcludeTestSends(t *testing.T) {
	f := newTemplateTestFixture(t, Config{})
	if _, err := f.uc.CreateTemplateTest(ctx, templateTestCommand(f, "qa@example.com")); err != nil {
		t.Fatal(err)
	}
	stats, err := f.uc.Stats(ctx, f.tenant, f.now.Add(-time.Hour), f.now.Add(time.Hour))
	if err != nil || len(stats) != 0 {
		t.Fatalf("una prueba no suma: %+v %v", stats, err)
	}
}

func viewLinkOf(t *testing.T, raw string) (domain.ViewClaims, string) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	seconds, err := strconv.ParseInt(q.Get("x"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return domain.ViewClaims{
		TenantID:  uuid.MustParse(q.Get("t")),
		MessageID: uuid.MustParse(q.Get("m")),
		ExpiresAt: time.Unix(seconds, 0).UTC(),
	}, q.Get("sig")
}

func TestViewInBrowserShowsTheStoredMessage(t *testing.T) {
	f := newTemplateTestFixture(t, Config{ViewInBrowserTTL: 48 * time.Hour})
	res, err := f.uc.CreateMessages(ctx, CreateMessagesCommand{
		TenantID: f.tenant, From: domain.Recipient{Email: "hola@" + shopDomain},
		To: []domain.Recipient{{Email: "ana@example.com"}}, TemplateID: ptrUUID(uuid.New()),
	})
	if err != nil {
		t.Fatal(err)
	}
	id := res.Messages[0].ID
	call, _ := f.tpl.callFor("ana@example.com")
	claims, sig := viewLinkOf(t, call.Reserved.ViewInBrowserURL)
	if claims.MessageID != id || !claims.ExpiresAt.Equal(f.now.Add(48*time.Hour)) {
		t.Fatalf("enlace del mensaje %s con la vigencia configurada: %+v", id, claims)
	}
	viewed, err := f.uc.ViewMessage(ctx, claims, sig)
	if err != nil || viewed.HTML != *f.repo.messages[id].HTML {
		t.Fatalf("se sirve el HTML guardado: %+v %v", viewed, err)
	}

	claims.ExpiresAt = claims.ExpiresAt.Add(time.Hour)
	if _, err := f.uc.ViewMessage(ctx, claims, sig); !errors.Is(err, domain.ErrInvalidSignature) {
		t.Fatalf("una caducidad alterada invalida la firma: %v", err)
	}
	claims.ExpiresAt = claims.ExpiresAt.Add(-time.Hour)
	f.now = f.now.Add(48 * time.Hour)
	if _, err := f.uc.ViewMessage(ctx, claims, sig); !errors.Is(err, domain.ErrLinkExpired) {
		t.Fatalf("caducado: %v", err)
	}

	gone := domain.ViewClaims{TenantID: f.tenant, MessageID: uuid.New(), ExpiresAt: f.now.Add(time.Hour)}
	if _, err := f.uc.ViewMessage(ctx, gone, f.links.SignView(gone)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("mensaje inexistente: %v", err)
	}
}

func TestMarketingRenderCarriesViewInBrowserURL(t *testing.T) {
	f := newMarketingFixture(t)
	if _, err := f.uc.CreateMarketingBatch(ctx, batchCommand(f, "ana@example.com")); err != nil {
		t.Fatal(err)
	}
	call, ok := f.tpl.callFor("ana@example.com")
	if !ok || !strings.Contains(call.Reserved.ViewInBrowserURL, domain.ViewInBrowserPath) {
		t.Fatalf("el render de campana lleva el enlace de ver en el navegador: %+v", call.Reserved)
	}
	if call.Test {
		t.Fatal("un lote de campana no pide el render de prueba")
	}
}

func ptrUUID(id uuid.UUID) *uuid.UUID { return &id }

package app

import (
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/alonsosss/corforce-email/services/transactional/internal/ports"
	"github.com/google/uuid"
)

func newMarketingFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "both")
	f.tpl.kind = domain.TemplateKindMarketing
	return f
}

func batchCommand(f *fixture, emails ...string) MarketingBatchCommand {
	cmd := MarketingBatchCommand{
		TenantID:        f.tenant,
		Class:           domain.ClassMarketing,
		CampaignID:      uuid.New(),
		IdempotencyKey:  "campaign-otono-batch-1",
		From:            domain.Recipient{Email: "news@" + shopDomain, Name: "Tienda"},
		TemplateID:      uuid.New(),
		TemplateVersion: 7,
		Tags:            map[string]string{"campaign": "otono"},
	}
	for _, e := range emails {
		cmd.Recipients = append(cmd.Recipients, MarketingRecipient{
			Email: e, Name: "Cliente", ContactID: uuid.New(), Variables: map[string]any{"first_name": "Ana"},
		})
	}
	return cmd
}

// nothingCreated comprueba que un lote rechazado no dejo mensajes, peticion ni eventos.
func nothingCreated(t *testing.T, f *fixture, what string) {
	t.Helper()
	if len(f.repo.messages) != 0 || len(f.repo.submissions) != 0 || len(f.repo.outbox) != 0 {
		t.Fatalf("%s: no debe quedar nada creado (mensajes=%d peticiones=%d outbox=%d)",
			what, len(f.repo.messages), len(f.repo.submissions), len(f.repo.outbox))
	}
}

func TestMarketingBatchCreatesOneMessagePerRecipient(t *testing.T) {
	f := newMarketingFixture(t)
	f.supp.suppressed = map[string]string{"eva@example.com": "unsubscribe"}
	cmd := batchCommand(f, "ana@example.com", "Eva@Example.com", "luis@example.com")
	contacts := map[string]uuid.UUID{}
	for _, r := range cmd.Recipients {
		contacts[strings.ToLower(r.Email)] = r.ContactID
	}

	res, err := f.uc.CreateMarketingBatch(ctx, cmd)
	if err != nil || res.Replayed {
		t.Fatalf("lote: %+v, %v", res, err)
	}
	if res.Accepted != 2 || len(res.MessageIDs) != 2 || len(res.Suppressed) != 1 || res.Suppressed[0].Reason != "unsubscribe" {
		t.Fatalf("respuesta: %+v", res)
	}
	if len(f.supp.checks) != 1 || len(f.supp.checks[0]) != 3 {
		t.Fatalf("la supresion se consulta una vez con todo el lote: %v", f.supp.checks)
	}
	if len(f.rep.calls) != 1 || f.rep.calls[0] != (reputationCall{Class: domain.ClassMarketing, Count: 2}) {
		t.Fatalf("reputation autoriza la clase marketing por los NO suprimidos: %+v", f.rep.calls)
	}
	if len(f.tpl.calls) != 2 {
		t.Fatalf("un render por destinatario no suprimido: %d", len(f.tpl.calls))
	}
	for _, id := range res.MessageIDs {
		msg := f.repo.messages[id]
		email := msg.To[0].Email
		if msg.Class != domain.ClassMarketing || msg.CampaignID == nil || *msg.CampaignID != cmd.CampaignID ||
			msg.ContactID == nil || *msg.ContactID != contacts[email] {
			t.Fatalf("atribucion del mensaje: %+v", msg)
		}
		if !msg.Unsubscribable || len(msg.To) != 1 || len(msg.Cc) != 0 || len(msg.Bcc) != 0 || msg.Status != domain.StatusQueued {
			t.Fatalf("un mensaje de marketing es de una persona, con baja y encolado: %+v", msg)
		}
		if msg.TemplateVersion == nil || *msg.TemplateVersion != 7 || msg.SubmissionID == nil || msg.Tags["campaign"] != "otono" {
			t.Fatalf("version fijada, peticion y etiquetas: %+v", msg)
		}
		call, ok := f.tpl.callFor(email)
		if !ok || call.Version == nil || *call.Version != 7 || call.Variables["first_name"] != "Ana" {
			t.Fatalf("render de %s: %+v", email, call)
		}
		u, _ := url.Parse(call.Reserved.UnsubscribeURL)
		if u.Query().Get("m") != id.String() || !f.links.Verify(domain.UnsubscribeClaims{TenantID: f.tenant, MessageID: id, Email: email}, u.Query().Get("sig")) {
			t.Fatalf("el enlace de baja debe ser el de ese mensaje y persona: %s", u)
		}
	}
	if len(f.repo.published("transactional.marketing.queued")) != 2 || len(f.repo.published("transactional.message.queued")) != 0 {
		t.Fatal("el lote se encola solo en la cola de marketing")
	}
	sub := f.repo.submissions[submissionKey(f.tenant, cmd.IdempotencyKey)]
	if sub.Class != domain.ClassMarketing || len(sub.MessageIDs) != 2 || len(sub.Suppressed) != 1 {
		t.Fatalf("la peticion guarda clase, mensajes y suprimidos: %+v", sub)
	}
}

func TestMarketingBatchReplayReturnsSameBodyWithoutCreating(t *testing.T) {
	f := newMarketingFixture(t)
	f.supp.suppressed = map[string]string{"eva@example.com": "hard_bounce"}
	cmd := batchCommand(f, "ana@example.com", "eva@example.com", "luis@example.com")

	first, err := f.uc.CreateMarketingBatch(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.uc.CreateMarketingBatch(ctx, cmd)
	if err != nil || !second.Replayed {
		t.Fatalf("la repeticion de la clave es una repeticion: %+v, %v", second, err)
	}
	if second.Accepted != first.Accepted || fmt.Sprint(second.MessageIDs) != fmt.Sprint(first.MessageIDs) ||
		fmt.Sprint(second.Suppressed) != fmt.Sprint(first.Suppressed) {
		t.Fatalf("la repeticion devuelve el mismo cuerpo: %+v vs %+v", second, first)
	}
	if len(f.repo.messages) != 2 || len(f.repo.outbox) != 2 || len(f.tpl.calls) != 2 || len(f.rep.calls) != 1 || len(f.supp.checks) != 1 {
		t.Fatal("la repeticion no crea, no encola, no renderiza ni vuelve a pedir autorizacion o supresion")
	}

	other := cmd
	other.TenantID = uuid.New()
	f.setDomain(other.TenantID, shopDomain, "verified", "sending")
	if res, err := f.uc.CreateMarketingBatch(ctx, other); err != nil || res.Replayed {
		t.Fatalf("la misma clave en otra empresa es un lote nuevo: %+v, %v", res, err)
	}
}

func TestMarketingBatchConcurrentWinner(t *testing.T) {
	f := newMarketingFixture(t)
	cmd := batchCommand(f, "ana@example.com")
	winner := uuid.New()
	f.repo.commitBeforeNextTx = func(r *fakeRepo) {
		r.submissions[submissionKey(f.tenant, cmd.IdempotencyKey)] = domain.Submission{
			ID: uuid.New(), TenantID: f.tenant, IdempotencyKey: cmd.IdempotencyKey, Class: domain.ClassMarketing,
			MessageIDs: []uuid.UUID{winner},
		}
	}
	res, err := f.uc.CreateMarketingBatch(ctx, cmd)
	if err != nil || !res.Replayed || len(res.MessageIDs) != 1 || res.MessageIDs[0] != winner {
		t.Fatalf("el lote que pierde la carrera devuelve el del ganador: %+v, %v", res, err)
	}
	if len(f.repo.messages) != 0 || len(f.repo.outbox) != 0 {
		t.Fatal("lo escrito por el perdedor se deshace, eventos incluidos")
	}
}

func TestMarketingBatchAllSuppressed(t *testing.T) {
	f := newMarketingFixture(t)
	f.supp.suppressed = map[string]string{"ana@example.com": "unsubscribe", "eva@example.com": "complaint"}
	cmd := batchCommand(f, "ana@example.com", "eva@example.com")

	res, err := f.uc.CreateMarketingBatch(ctx, cmd)
	if err != nil || res.Accepted != 0 || res.MessageIDs == nil || len(res.MessageIDs) != 0 || len(res.Suppressed) != 2 {
		t.Fatalf("todos suprimidos: accepted 0 con la lista de suprimidos: %+v, %v", res, err)
	}
	if len(f.rep.calls) != 0 || len(f.tpl.calls) != 0 || len(f.repo.messages) != 0 || len(f.repo.outbox) != 0 {
		t.Fatal("sin destinatarios no se autoriza, no se renderiza ni se encola nada")
	}
	replay, err := f.uc.CreateMarketingBatch(ctx, cmd)
	if err != nil || !replay.Replayed || len(replay.Suppressed) != 2 || replay.Accepted != 0 {
		t.Fatalf("la repeticion devuelve los mismos suprimidos: %+v, %v", replay, err)
	}
}

func intPtr(v int) *int { return &v }

func TestMarketingBatchRateLimitedCreatesNothing(t *testing.T) {
	f := newMarketingFixture(t)
	f.rep.auth = &ports.Authorization{Allowed: false, Class: domain.ClassMarketing, Reason: domain.DenyRateLimited, RetryAfterSeconds: intPtr(30)}

	_, err := f.uc.CreateMarketingBatch(ctx, batchCommand(f, "ana@example.com", "eva@example.com"))
	var denied *domain.SendingDeniedError
	if !errors.As(err, &denied) || !denied.RateLimited() || denied.RetryAfter == nil || *denied.RetryAfter != 30*time.Second || denied.Class != domain.ClassMarketing {
		t.Fatalf("rate_limited llega con su espera: %v", err)
	}
	nothingCreated(t, f, "rate_limited")
	if len(f.tpl.calls) != 0 {
		t.Fatal("una denegacion no llega a renderizar")
	}
}

// denialKind devuelve la unica categoria de la denegacion; las cuatro son excluyentes.
func denialKind(d *domain.SendingDeniedError) string {
	var kinds []string
	for kind, match := range map[string]bool{
		"rate": d.RateLimited(), "window": d.ExceedsRateWindow(), "restricted": d.Restricted(), "plan": d.PlanLimit(),
	} {
		if match {
			kinds = append(kinds, kind)
		}
	}
	if len(kinds) != 1 {
		return fmt.Sprint(kinds)
	}
	return kinds[0]
}

func TestMarketingBatchDenialCategories(t *testing.T) {
	cases := []struct {
		reason string
		retry  *int
		want   string
	}{
		{domain.DenyRateLimited, intPtr(0), "rate"},
		{domain.DenyRateLimited, nil, "window"},
		{domain.DenySuspended, nil, "restricted"},
		{domain.DenyReputationRestricted, nil, "restricted"},
		{"limit_reached", nil, "plan"},
		{"no_subscription", nil, "plan"},
		{domain.DenyPlanDenied, nil, "plan"},
		{domain.DenyPlanLimitExceeded, nil, "plan"},
		{"", nil, "plan"},
	}
	for _, tc := range cases {
		f := newMarketingFixture(t)
		f.rep.auth = &ports.Authorization{Allowed: false, Reason: tc.reason, RetryAfterSeconds: tc.retry}
		_, err := f.uc.CreateMarketingBatch(ctx, batchCommand(f, "ana@example.com"))
		var denied *domain.SendingDeniedError
		if !errors.As(err, &denied) || denied.Reason != tc.reason || denialKind(denied) != tc.want {
			t.Errorf("%q (espera %v): categoria %q, err %v", tc.reason, tc.retry != nil, denialKind(denied), err)
		}
		nothingCreated(t, f, tc.reason)
	}
}

func TestMarketingBatchFailsClosedWhenReputationIsDown(t *testing.T) {
	f := newMarketingFixture(t)
	f.rep.err = errors.New("connection refused")
	if _, err := f.uc.CreateMarketingBatch(ctx, batchCommand(f, "ana@example.com")); !errors.Is(err, domain.ErrReputationUnavailable) {
		t.Fatalf("marketing con reputation caido falla cerrado: %v", err)
	}
	nothingCreated(t, f, "reputation caido")

	f = newMarketingFixture(t)
	f.deps.Reputation = nil
	f.uc = New(f.deps)
	if _, err := f.uc.CreateMarketingBatch(ctx, batchCommand(f, "ana@example.com")); !errors.Is(err, domain.ErrReputationUnavailable) {
		t.Fatalf("sin cliente de reputation tambien falla cerrado: %v", err)
	}
}

func TestMarketingBatchFailsClosedWhenSuppressionIsDown(t *testing.T) {
	f := newMarketingFixture(t)
	f.supp.checkErr = errors.New("503")
	if _, err := f.uc.CreateMarketingBatch(ctx, batchCommand(f, "ana@example.com")); !errors.Is(err, domain.ErrSuppressionUnavailable) {
		t.Fatalf("sin supresion no se encola: %v", err)
	}
	if len(f.rep.calls) != 0 {
		t.Fatal("sin supresion no se llega a autorizar")
	}
	nothingCreated(t, f, "suppression caido")
}

func TestMarketingBatchRejectsNonMarketingTemplate(t *testing.T) {
	// La plantilla transaccional usa unsubscribe_url y su cuerpo lleva el enlace de baja del
	// mensaje: lo que decide es el tipo que declara templates, no el contenido.
	f := newMarketingFixture(t)
	f.tpl.kind = domain.TemplateKindTransactional
	if _, err := f.uc.CreateMarketingBatch(ctx, batchCommand(f, "ana@example.com", "eva@example.com", "luis@example.com")); !errors.Is(err, domain.ErrTemplateNotMarketing) {
		t.Fatalf("una plantilla transaccional no sale por marketing aunque lleve el enlace de baja: %v", err)
	}
	nothingCreated(t, f, "plantilla transaccional")

	f = newMarketingFixture(t)
	f.tpl.err = domain.ErrTemplateNotFound
	if _, err := f.uc.CreateMarketingBatch(ctx, batchCommand(f, "ana@example.com")); !errors.Is(err, domain.ErrTemplateNotFound) {
		t.Fatalf("plantilla inexistente: %v", err)
	}
	nothingCreated(t, f, "plantilla inexistente")
}

func TestMarketingBatchRejectsTemplateWithoutUnsubscribe(t *testing.T) {
	f := newMarketingFixture(t)
	f.tpl.withoutUnsubscribe = true
	_, err := f.uc.CreateMarketingBatch(ctx, batchCommand(f, "ana@example.com", "eva@example.com"))
	if !errors.Is(err, domain.ErrTemplateMissingUnsubscribe) || errors.Is(err, domain.ErrTemplateNotMarketing) {
		t.Fatalf("una plantilla de marketing sin enlace de baja es otra regla: %v", err)
	}
	nothingCreated(t, f, "marketing sin enlace de baja")
}

func TestMarketingBatchFailsClosedWithoutTemplateKind(t *testing.T) {
	f := newMarketingFixture(t)
	f.tpl.kind = ""
	if _, err := f.uc.CreateMarketingBatch(ctx, batchCommand(f, "ana@example.com")); !errors.Is(err, domain.ErrTemplatesUnavailable) {
		t.Fatalf("sin el tipo de plantilla el lote falla cerrado: %v", err)
	}
	nothingCreated(t, f, "templates sin kind")
}

func TestTransactionalRejectsMarketingTemplate(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	f.tpl.kind = domain.TemplateKindMarketing
	cmd := templateCommand(f, "ana@example.com", "eva@example.com")
	cmd.IdempotencyKey = "k-campana"
	if _, err := f.uc.CreateMessages(ctx, cmd); !errors.Is(err, domain.ErrTemplateNotTransactional) {
		t.Fatalf("una plantilla de marketing no sale por el carril transaccional: %v", err)
	}
	if len(f.repo.messages) != 0 || len(f.repo.submissions) != 0 || len(f.repo.outbox) != 0 {
		t.Fatal("el rechazo no deja mensajes, peticion ni eventos")
	}
}

func TestTransactionalContinuesWithoutTemplateKind(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	f.tpl.kind = ""
	res, err := f.uc.CreateMessages(ctx, templateCommand(f, "ana@example.com"))
	if err != nil || len(res.Messages) != 1 || res.Messages[0].Status != domain.StatusQueued {
		t.Fatalf("un templates sin kind no para el carril transaccional: %+v, %v", res, err)
	}
}

func TestMarketingBatchRequiresVerifiedSendingDomain(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "corporate")
	if _, err := f.uc.CreateMarketingBatch(ctx, batchCommand(f, "ana@example.com")); !errors.Is(err, domain.ErrSendingDomainNotVerified) {
		t.Fatalf("remitente sin proposito de envio: %v", err)
	}
	if len(f.supp.checks) != 0 || len(f.rep.calls) != 0 {
		t.Fatal("un remitente no verificado no consulta supresion ni reputation")
	}
}

func manyBatchRecipients(n int) []MarketingRecipient {
	out := make([]MarketingRecipient, n)
	for i := range out {
		out[i] = MarketingRecipient{Email: fmt.Sprintf("r%d@example.com", i), ContactID: uuid.New()}
	}
	return out
}

func TestMarketingBatchValidation(t *testing.T) {
	cases := map[string]func(c *MarketingBatchCommand){
		"clase transaccional":     func(c *MarketingBatchCommand) { c.Class = domain.ClassTransactional },
		"sin clase":               func(c *MarketingBatchCommand) { c.Class = "" },
		"sin campana":             func(c *MarketingBatchCommand) { c.CampaignID = uuid.Nil },
		"sin clave":               func(c *MarketingBatchCommand) { c.IdempotencyKey = "  " },
		"clave enorme":            func(c *MarketingBatchCommand) { c.IdempotencyKey = strings.Repeat("k", 201) },
		"remitente invalido":      func(c *MarketingBatchCommand) { c.From.Email = "no-es-email" },
		"reply_to invalido":       func(c *MarketingBatchCommand) { c.ReplyTo = "x@" },
		"sin plantilla":           func(c *MarketingBatchCommand) { c.TemplateID = uuid.Nil },
		"sin version":             func(c *MarketingBatchCommand) { c.TemplateVersion = 0 },
		"sin destinatarios":       func(c *MarketingBatchCommand) { c.Recipients = nil },
		"mas de 500":              func(c *MarketingBatchCommand) { c.Recipients = manyBatchRecipients(501) },
		"sin contacto":            func(c *MarketingBatchCommand) { c.Recipients[0].ContactID = uuid.Nil },
		"email invalido":          func(c *MarketingBatchCommand) { c.Recipients[0].Email = "ana@@example.com" },
		"destinatario repetido":   func(c *MarketingBatchCommand) { c.Recipients[1].Email = "ANA@example.com" },
		"etiqueta reservada":      func(c *MarketingBatchCommand) { c.Tags = map[string]string{"message_id": "x"} },
		"etiqueta con espacios":   func(c *MarketingBatchCommand) { c.Tags = map[string]string{"campaign": "otono 2026"} },
		"version negativa":        func(c *MarketingBatchCommand) { c.TemplateVersion = -1 },
		"clave solo con espacios": func(c *MarketingBatchCommand) { c.IdempotencyKey = "\t" },
		"destinatario sin arroba": func(c *MarketingBatchCommand) { c.Recipients[0].Email = "ana" },
		"clase en mayusculas":     func(c *MarketingBatchCommand) { c.Class = "MARKETING" },
	}
	for name, mutate := range cases {
		f := newMarketingFixture(t)
		cmd := batchCommand(f, "ana@example.com", "eva@example.com")
		mutate(&cmd)
		if _, err := f.uc.CreateMarketingBatch(ctx, cmd); !domain.IsValidation(err) {
			t.Errorf("%s: se esperaba error de validacion, err = %v", name, err)
		}
		if len(f.supp.checks) != 0 || len(f.rep.calls) != 0 {
			t.Errorf("%s: una peticion invalida no consulta supresion ni reputation", name)
		}
	}
	f := newMarketingFixture(t)
	cmd := batchCommand(f)
	cmd.Recipients = manyBatchRecipients(domain.MaxBatchRecipients)
	if res, err := f.uc.CreateMarketingBatch(ctx, cmd); err != nil || res.Accepted != domain.MaxBatchRecipients {
		t.Fatalf("un lote de 500 es valido: %v", err)
	}
}

func TestMarketingLaneUsesItsSenderAndRate(t *testing.T) {
	f := newMarketingFixture(t)
	res, err := f.uc.CreateMarketingBatch(ctx, batchCommand(f, "ana@example.com", "eva@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range res.MessageIDs {
		out, err := f.uc.SendQueued(ctx, f.tenant, id)
		if err != nil || !out.Ack || out.Status != domain.StatusSent {
			t.Fatalf("envio de marketing: %+v, %v", out, err)
		}
	}
	if len(f.mSender.sent) != 2 || f.mLimiter.waits != 2 {
		t.Fatalf("el marketing sale por su emisor y su tasa: enviados=%d esperas=%d", len(f.mSender.sent), f.mLimiter.waits)
	}
	if f.sender.calls != 0 || f.limiter.waits != 0 {
		t.Fatal("el carril transaccional no se toca")
	}
	for _, sent := range f.mSender.sent {
		u, err := url.Parse(sent.UnsubscribeURL)
		if err != nil || sent.UnsubscribeURL == "" {
			t.Fatal("todo mensaje de marketing lleva List-Unsubscribe")
		}
		to, err := mail.ParseAddress(sent.To[0])
		if err != nil {
			t.Fatal(err)
		}
		claims := domain.UnsubscribeClaims{TenantID: f.tenant, MessageID: sent.MessageID, Email: to.Address}
		if !f.links.Verify(claims, u.Query().Get("sig")) {
			t.Fatalf("List-Unsubscribe firmado para ese mensaje y persona: %s", sent.UnsubscribeURL)
		}
	}

	id := createQueued(t, f, rawCommand(f, "luis@example.com"))
	if _, err := f.uc.SendQueued(ctx, f.tenant, id); err != nil {
		t.Fatal(err)
	}
	if f.sender.calls != 1 || f.limiter.waits != 1 || f.mSender.calls != 2 {
		t.Fatal("un transaccional sale por su carril, no por el de marketing")
	}
}

func TestMarketingMessageNeverFallsBackToTransactionalLane(t *testing.T) {
	f := newMarketingFixture(t)
	res, err := f.uc.CreateMarketingBatch(ctx, batchCommand(f, "ana@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	f.deps.Marketing = Lane{}
	f.uc = New(f.deps)
	out, err := f.uc.SendQueued(ctx, f.tenant, res.MessageIDs[0])
	if !errors.Is(err, domain.ErrLaneNotConfigured) || out.Ack {
		t.Fatalf("sin carril de marketing el mensaje espera en la cola: %+v, %v", out, err)
	}
	if f.sender.calls != 0 || f.repo.messages[res.MessageIDs[0]].Status != domain.StatusQueued {
		t.Fatal("un mensaje de marketing nunca sale por el emisor transaccional")
	}
}

func TestTransactionalReputationGate(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	f.rep.err = errors.New("connection refused")
	res, err := f.uc.CreateMessages(ctx, rawCommand(f, "ana@example.com", "eva@example.com"))
	if err != nil || res.Messages[0].Status != domain.StatusQueued {
		t.Fatalf("el transaccional se envia aunque reputation no responda: %+v, %v", res, err)
	}
	if len(f.rep.calls) != 1 || f.rep.calls[0] != (reputationCall{Class: domain.ClassTransactional, Count: 2}) {
		t.Fatalf("reputation cuenta destinatarios: un cuerpo crudo a dos personas son dos: %+v", f.rep.calls)
	}

	f.rep.err = nil
	cmd := rawCommand(f, "ana@example.com", "eva@example.com")
	cmd.Cc = []domain.Recipient{{Email: "copia@example.com"}}
	cmd.Bcc = []domain.Recipient{{Email: "oculta@example.com"}}
	f.supp.suppressed = map[string]string{"eva@example.com": "hard_bounce"}
	if _, err := f.uc.CreateMessages(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	if f.rep.calls[1] != (reputationCall{Class: domain.ClassTransactional, Count: 3}) {
		t.Fatalf("to, cc y bcc cuentan, los suprimidos no: %+v", f.rep.calls)
	}
	f.supp.suppressed = map[string]string{}
	if _, err := f.uc.CreateMessages(ctx, templateCommand(f, "ana@example.com", "eva@example.com", "luis@example.com")); err != nil {
		t.Fatal(err)
	}
	if f.rep.calls[2] != (reputationCall{Class: domain.ClassTransactional, Count: 3}) {
		t.Fatalf("con plantilla se autoriza un destinatario por mensaje: %+v", f.rep.calls)
	}

	for _, reason := range []string{domain.DenyRateLimited, domain.DenySuspended, "limit_reached"} {
		g := newFixture(t, Config{})
		g.setDomain(g.tenant, shopDomain, "verified", "sending")
		g.rep.auth = &ports.Authorization{Allowed: false, Reason: reason, RetryAfterSeconds: intPtr(5)}
		_, err := g.uc.CreateMessages(ctx, rawCommand(g, "ana@example.com"))
		var denied *domain.SendingDeniedError
		if !errors.As(err, &denied) || denied.Reason != reason || denied.Class != domain.ClassTransactional {
			t.Errorf("%s: una denegacion explicita se respeta en el transaccional: %v", reason, err)
		}
		if len(g.repo.messages) != 0 || len(g.repo.outbox) != 0 {
			t.Errorf("%s: la denegacion no deja nada creado", reason)
		}
	}

	g := newFixture(t, Config{})
	g.setDomain(g.tenant, shopDomain, "verified", "sending")
	g.supp.suppressed = map[string]string{"ana@example.com": "hard_bounce"}
	if _, err := g.uc.CreateMessages(ctx, rawCommand(g, "ana@example.com")); err != nil || len(g.rep.calls) != 0 {
		t.Fatalf("sin destinatarios no se pide autorizacion: %v, %+v", err, g.rep.calls)
	}
}

func TestIdempotencyKeyCannotCrossClasses(t *testing.T) {
	f := newMarketingFixture(t)
	raw := rawCommand(f, "ana@example.com")
	raw.IdempotencyKey = "k-compartida"
	if _, err := f.uc.CreateMessages(ctx, raw); err != nil {
		t.Fatal(err)
	}
	batch := batchCommand(f, "eva@example.com")
	batch.IdempotencyKey = "k-compartida"
	if _, err := f.uc.CreateMarketingBatch(ctx, batch); !errors.Is(err, domain.ErrIdempotencyKeyReused) {
		t.Fatalf("una clave transaccional no devuelve un lote: %v", err)
	}

	batch.IdempotencyKey = "k-lote"
	if _, err := f.uc.CreateMarketingBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	raw.IdempotencyKey = "k-lote"
	if _, err := f.uc.CreateMessages(ctx, raw); !errors.Is(err, domain.ErrIdempotencyKeyReused) {
		t.Fatalf("una clave de lote no devuelve mensajes transaccionales: %v", err)
	}
}

func TestTransactionalReplayKeepsSuppressed(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	f.supp.suppressed = map[string]string{"eva@example.com": "hard_bounce"}
	cmd := rawCommand(f, "ana@example.com", "eva@example.com")
	cmd.IdempotencyKey = "k-1"
	if _, err := f.uc.CreateMessages(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	replay, err := f.uc.CreateMessages(ctx, cmd)
	if err != nil || !replay.Replayed || len(replay.Suppressed) != 1 || replay.Suppressed[0].Email != "eva@example.com" {
		t.Fatalf("la repeticion conserva los suprimidos de la primera respuesta: %+v, %v", replay, err)
	}
}

// marketingMessage crea un lote de un destinatario y devuelve el mensaje y su contacto.
func marketingMessage(t *testing.T, f *fixture) (MarketingBatchCommand, uuid.UUID) {
	t.Helper()
	cmd := batchCommand(f, "ana@example.com")
	res, err := f.uc.CreateMarketingBatch(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	return cmd, res.MessageIDs[0]
}

func assertAttribution(t *testing.T, subject string, e outboxEvent, class string, campaign, contact *uuid.UUID) {
	t.Helper()
	if e.Payload["class"] != class {
		t.Errorf("%s: class = %v", subject, e.Payload["class"])
	}
	if at, _ := e.Payload["occurred_at"].(string); at == "" {
		t.Errorf("%s: falta occurred_at", subject)
	} else if _, err := time.Parse(time.RFC3339, at); err != nil {
		t.Errorf("%s: occurred_at no es RFC 3339: %q", subject, at)
	}
	for key, want := range map[string]*uuid.UUID{"campaign_id": campaign, "contact_id": contact} {
		got, present := e.Payload[key]
		if !present {
			t.Errorf("%s: falta %s (debe viajar aunque sea null)", subject, key)
			continue
		}
		if want == nil && got != nil || want != nil && got != want.String() {
			t.Errorf("%s: %s = %v, se esperaba %v", subject, key, got, want)
		}
	}
}

func TestMarketingEventsCarryClassCampaignAndContact(t *testing.T) {
	f := newMarketingFixture(t)
	cmd, id := marketingMessage(t, f)
	campaign, contact := cmd.CampaignID, cmd.Recipients[0].ContactID
	if _, err := f.uc.SendQueued(ctx, f.tenant, id); err != nil {
		t.Fatal(err)
	}
	assertAttribution(t, "sent", f.repo.published("transactional.email.sent")[0], domain.ClassMarketing, &campaign, &contact)

	ingest := func(ev domain.InboundEvent) {
		t.Helper()
		ev.TenantID, ev.MessageID = f.tenant, id
		if ev.Recipients == nil {
			ev.Recipients = []string{"ana@example.com"}
		}
		if err := f.uc.IngestSESEvent(ctx, f.tenant, ev); err != nil {
			t.Fatal(err)
		}
	}
	deliveredAt := time.Date(2026, 9, 12, 11, 58, 30, 0, time.UTC)
	ingest(domain.InboundEvent{Type: domain.EventDelivery, SNSMessageID: "sns-d", OccurredAt: deliveredAt})
	if at := f.repo.published("transactional.email.delivered")[0].Payload["occurred_at"]; at != "2026-09-12T11:58:30Z" {
		t.Fatalf("occurred_at es la hora del hecho en SES, no la del rele: %v", at)
	}
	ingest(domain.InboundEvent{Type: domain.EventOpen, SNSMessageID: "sns-o", Detail: map[string]any{"ip_address": "203.0.113.9"}})
	ingest(domain.InboundEvent{Type: domain.EventClick, SNSMessageID: "sns-c", Detail: map[string]any{"link": "https://shop.example.com/otono"}})
	ingest(domain.InboundEvent{Type: domain.EventComplaint, SNSMessageID: "sns-q", Detail: map[string]any{"reason": "abuse"}})

	for _, subject := range []string{"transactional.email.delivered", "transactional.email.opened", "transactional.email.clicked", "transactional.email.complained"} {
		got := f.repo.published(subject)
		if len(got) != 1 {
			t.Fatalf("%s: %d eventos", subject, len(got))
		}
		assertAttribution(t, subject, got[0], domain.ClassMarketing, &campaign, &contact)
		if got[0].Payload["email"] != "ana@example.com" || got[0].Payload["message_id"] != id.String() || got[0].Payload["tenant_id"] != f.tenant.String() {
			t.Errorf("%s: payload %+v", subject, got[0].Payload)
		}
	}
	if f.repo.published("transactional.email.clicked")[0].Payload["link"] != "https://shop.example.com/otono" {
		t.Fatal("clicked lleva el enlace")
	}
	if _, ok := f.repo.published("transactional.email.opened")[0].Payload["ip_address"]; ok {
		t.Fatal("la IP de quien abre no sale del servicio")
	}
	if len(f.supp.added) != 1 || f.supp.added[0].CampaignID != campaign.String() {
		t.Fatalf("la queja llega a suppression con su campana: %+v", f.supp.added)
	}

	claims := domain.UnsubscribeClaims{TenantID: f.tenant, MessageID: id, Email: "ana@example.com"}
	if err := f.uc.Unsubscribe(ctx, claims, f.links.Sign(claims)); err != nil {
		t.Fatal(err)
	}
	unsub := f.repo.published("transactional.email.unsubscribed")
	if len(unsub) != 1 {
		t.Fatal("se publica la baja")
	}
	assertAttribution(t, "unsubscribed", unsub[0], domain.ClassMarketing, &campaign, &contact)
	if last := f.supp.added[len(f.supp.added)-1]; last.Reason != "unsubscribe" || last.CampaignID != campaign.String() {
		t.Fatalf("la baja llega a suppression con su campana: %+v", last)
	}
}

func TestFailedMarketingEventCarriesClass(t *testing.T) {
	f := newMarketingFixture(t)
	cmd, id := marketingMessage(t, f)
	f.mSender.err = &domain.SendError{Kind: domain.ErrorPermanent, Code: "MessageRejected"}
	if _, err := f.uc.SendQueued(ctx, f.tenant, id); err != nil {
		t.Fatal(err)
	}
	failed := f.repo.published("transactional.email.failed")
	if len(failed) != 1 {
		t.Fatal("se publica el fallo")
	}
	contact := cmd.Recipients[0].ContactID
	assertAttribution(t, "failed", failed[0], domain.ClassMarketing, &cmd.CampaignID, &contact)
}

func TestTransactionalEventsCarryNullCampaign(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	id := createQueued(t, f, rawCommand(f, "ana@example.com", "eva@example.com"))
	if _, err := f.uc.SendQueued(ctx, f.tenant, id); err != nil {
		t.Fatal(err)
	}
	assertAttribution(t, "sent", f.repo.published("transactional.email.sent")[0], domain.ClassTransactional, nil, nil)

	both := []string{"ana@example.com", "eva@example.com"}
	if err := f.uc.IngestSESEvent(ctx, f.tenant, domain.InboundEvent{Type: domain.EventDelivery, TenantID: f.tenant, MessageID: id, Recipients: both, SNSMessageID: "d"}); err != nil {
		t.Fatal(err)
	}
	delivered := f.repo.published("transactional.email.delivered")
	if len(delivered) != 2 || delivered[1].Payload["email"] != "eva@example.com" {
		t.Fatalf("una entrega por destinatario: %+v", delivered)
	}
	assertAttribution(t, "delivered", delivered[0], domain.ClassTransactional, nil, nil)

	if err := f.uc.IngestSESEvent(ctx, f.tenant, domain.InboundEvent{Type: domain.EventOpen, TenantID: f.tenant, MessageID: id, Recipients: both, SNSMessageID: "o"}); err != nil {
		t.Fatal(err)
	}
	if opened := f.repo.published("transactional.email.opened"); len(opened) != 1 || opened[0].Payload["email"] != "" {
		t.Fatalf("una apertura de un mensaje con varios destinatarios no se atribuye a nadie: %+v", opened)
	}

	if err := f.uc.IngestSESEvent(ctx, f.tenant, bounce(f, id, domain.BounceTypePermanent, "b")); err != nil {
		t.Fatal(err)
	}
	assertAttribution(t, "bounced", f.repo.published("transactional.email.bounced")[0], domain.ClassTransactional, nil, nil)
	if f.supp.added[0].CampaignID != "" {
		t.Fatal("un transaccional no lleva campana a suppression")
	}
}

func TestIngestSuppressesEvenIfAttributionIsUnreadable(t *testing.T) {
	f := newFixture(t, Config{})
	id := sentMessage(t, f)
	f.repo.attributionErr = errors.New("database unavailable")
	err := f.uc.IngestSESEvent(ctx, f.tenant, bounce(f, id, domain.BounceTypePermanent, "sns-x"))
	if err == nil {
		t.Fatal("sin atribucion el evento no se registra: SNS reintentara")
	}
	if len(f.supp.added) != 1 || f.supp.added[0].Reason != "hard_bounce" {
		t.Fatalf("la supresion no espera a la atribucion: %+v", f.supp.added)
	}
	if f.repo.eventsOfType(id, domain.EventBounce) != 0 || len(f.repo.published("transactional.email.bounced")) != 0 {
		t.Fatal("el evento y su publicacion se deshacen juntos")
	}
}

func TestListMessagesByClass(t *testing.T) {
	f := newMarketingFixture(t)
	if _, err := f.uc.CreateMarketingBatch(ctx, batchCommand(f, "ana@example.com")); err != nil {
		t.Fatal(err)
	}
	createQueued(t, f, rawCommand(f, "eva@example.com"))
	list, total, err := f.uc.ListMessages(ctx, f.tenant, domain.MessageFilter{Class: domain.ClassMarketing}, 1, 10)
	if err != nil || total != 1 || list[0].Class != domain.ClassMarketing {
		t.Fatalf("filtro por clase: %d %v", total, err)
	}
	if _, _, err := f.uc.ListMessages(ctx, f.tenant, domain.MessageFilter{Class: "promo"}, 1, 10); !domain.IsValidation(err) {
		t.Fatalf("clase desconocida: %v", err)
	}
}

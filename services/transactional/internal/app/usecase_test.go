package app

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/google/uuid"
)

const shopDomain = "shop.example.com"

var ctx = context.Background()

func rawCommand(f *fixture, to ...string) CreateMessagesCommand {
	cmd := CreateMessagesCommand{
		TenantID: f.tenant,
		From:     domain.Recipient{Email: "no-reply@" + shopDomain, Name: "Tienda"},
		Subject:  "Pedido confirmado",
		HTML:     "<p>Gracias por tu compra</p>",
	}
	for _, e := range to {
		cmd.To = append(cmd.To, domain.Recipient{Email: e})
	}
	return cmd
}

func templateCommand(f *fixture, to ...string) CreateMessagesCommand {
	id := uuid.New()
	cmd := rawCommand(f, to...)
	cmd.Subject, cmd.HTML = "", ""
	cmd.TemplateID = &id
	cmd.Variables = map[string]any{"order": "A-100"}
	return cmd
}

func TestCreateRawHTMLSingleMessage(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")

	res, err := f.uc.CreateMessages(ctx, rawCommand(f, "ana@example.com", "eva@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 1 || res.Messages[0].Status != domain.StatusQueued || len(res.Messages[0].To) != 2 {
		t.Fatalf("un cuerpo crudo produce un mensaje con todos los destinatarios: %+v", res.Messages)
	}
	queued := f.repo.published("transactional.message.queued")
	if len(queued) != 1 || queued[0].Payload["message_id"] != res.Messages[0].ID.String() || queued[0].Payload["tenant_id"] != f.tenant.String() {
		t.Fatalf("debe encolarse transactional.message.queued en la misma transaccion: %+v", queued)
	}
	if len(f.supp.checks) != 1 || len(f.supp.checks[0]) != 2 {
		t.Fatalf("suppression debe consultarse una vez con todos los destinatarios: %v", f.supp.checks)
	}
	if len(f.tpl.calls) != 0 {
		t.Fatal("sin plantilla no se llama a templates")
	}
}

func TestCreateRejectsUnverifiedFrom(t *testing.T) {
	cases := []struct {
		name            string
		status, purpose string
		from            string
	}{
		{"sin dominio en la proyeccion", "", "", "no-reply@" + shopDomain},
		{"dominio fallido", "failed", "sending", "no-reply@" + shopDomain},
		{"dominio pendiente", "pending", "both", "no-reply@" + shopDomain},
		{"dominio solo corporativo", "verified", "corporate", "no-reply@" + shopDomain},
		{"subdominio no verificado", "verified", "sending", "no-reply@mail." + shopDomain},
	}
	for _, tc := range cases {
		f := newFixture(t, Config{})
		if tc.status != "" {
			f.setDomain(f.tenant, shopDomain, tc.status, tc.purpose)
		}
		cmd := rawCommand(f, "ana@example.com")
		cmd.From.Email = tc.from
		_, err := f.uc.CreateMessages(ctx, cmd)
		if !errors.Is(err, domain.ErrSendingDomainNotVerified) {
			t.Errorf("%s: err = %v", tc.name, err)
		}
		if len(f.repo.messages) != 0 || len(f.supp.checks) != 0 || len(f.repo.outbox) != 0 {
			t.Errorf("%s: un remitente no verificado no debe dejar rastro ni consultar supresion", tc.name)
		}
	}
}

func TestCreateAcceptsSendingAndBothPurposes(t *testing.T) {
	for _, purpose := range []string{"sending", "both"} {
		f := newFixture(t, Config{})
		f.setDomain(f.tenant, shopDomain, "verified", purpose)
		cmd := rawCommand(f, "ana@example.com")
		cmd.From.Email = "No-Reply@SHOP.Example.com"
		if _, err := f.uc.CreateMessages(ctx, cmd); err != nil {
			t.Errorf("purpose %s: %v", purpose, err)
		}
	}
}

func TestCreateDomainOfOtherTenantDoesNotCount(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(uuid.New(), shopDomain, "verified", "sending")
	if _, err := f.uc.CreateMessages(ctx, rawCommand(f, "ana@example.com")); !errors.Is(err, domain.ErrSendingDomainNotVerified) {
		t.Fatalf("el dominio verificado de otra empresa no autoriza: %v", err)
	}
}

func TestCreateFiltersSuppressed(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	f.supp.suppressed = map[string]string{"eva@example.com": "hard_bounce", "copia@example.com": "complaint"}

	cmd := rawCommand(f, "ana@example.com", "Eva@Example.com")
	cmd.Cc = []domain.Recipient{{Email: "copia@example.com"}}
	res, err := f.uc.CreateMessages(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.supp.checks[0]) != 3 {
		t.Fatalf("to, cc y bcc se consultan juntos: %v", f.supp.checks[0])
	}
	if len(res.Suppressed) != 2 {
		t.Fatalf("se informa de los suprimidos: %+v", res.Suppressed)
	}
	msg := f.repo.messages[res.Messages[0].ID]
	if len(msg.To) != 1 || msg.To[0].Email != "ana@example.com" || len(msg.Cc) != 0 {
		t.Fatalf("los suprimidos se quitan de cada lista: to=%v cc=%v", msg.To, msg.Cc)
	}
	if res.Messages[0].Status != domain.StatusQueued {
		t.Fatalf("queda un destinatario: debe encolarse")
	}
}

func TestCreateAllSuppressed(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	f.supp.suppressed = map[string]string{"ana@example.com": "unsubscribe"}

	res, err := f.uc.CreateMessages(ctx, templateCommand(f, "ana@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 1 || res.Messages[0].Status != domain.StatusSuppressed {
		t.Fatalf("todos suprimidos: un mensaje en suppressed, %+v", res.Messages)
	}
	if len(res.Suppressed) != 1 || res.Suppressed[0].Reason != "unsubscribe" {
		t.Fatalf("suppressed = %+v", res.Suppressed)
	}
	if len(f.repo.outbox) != 0 || len(f.tpl.calls) != 0 {
		t.Fatal("nada se encola ni se renderiza si no queda destinatario")
	}
}

func TestCreateSuppressionUnavailableQueuesNothing(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	f.supp.checkErr = errors.New("connection refused")
	if _, err := f.uc.CreateMessages(ctx, rawCommand(f, "ana@example.com")); !errors.Is(err, domain.ErrSuppressionUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if len(f.repo.messages) != 0 || len(f.repo.outbox) != 0 {
		t.Fatal("sin respuesta de supresion no se encola nada")
	}
}

func TestCreateTemplateRendersPerRecipient(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "both")
	to := []string{"ana@example.com", "eva@example.com", "luis@example.com"}
	cmd := templateCommand(f, to...)

	res, err := f.uc.CreateMessages(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.tpl.calls) != 3 || len(res.Messages) != 3 {
		t.Fatalf("un render y un mensaje por destinatario: renders=%d mensajes=%d", len(f.tpl.calls), len(res.Messages))
	}
	seenURLs := map[string]bool{}
	for i, call := range f.tpl.calls {
		if call.TemplateID != *cmd.TemplateID || call.Variables["order"] != "A-100" {
			t.Errorf("render %d: plantilla o variables incorrectas", i)
		}
		if call.Reserved.RecipientEmail != to[i] {
			t.Errorf("render %d: recipient_email = %q", i, call.Reserved.RecipientEmail)
		}
		seenURLs[call.Reserved.UnsubscribeURL] = true

		msg := f.repo.messages[res.Messages[i].ID]
		if len(msg.To) != 1 || msg.To[0].Email != to[i] {
			t.Errorf("mensaje %d: debe tener un solo destinatario, %v", i, msg.To)
		}
		if msg.Subject != "Hola "+to[i] || msg.TemplateVersion == nil || *msg.TemplateVersion != 3 {
			t.Errorf("mensaje %d: se guarda el render y la version resuelta", i)
		}
		u, err := url.Parse(call.Reserved.UnsubscribeURL)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		if q.Get("m") != msg.ID.String() || q.Get("e") != to[i] || q.Get("t") != f.tenant.String() {
			t.Errorf("render %d: el enlace de baja debe ser el de ese mensaje y persona: %s", i, u)
		}
		claims := domain.UnsubscribeClaims{TenantID: f.tenant, MessageID: msg.ID, Email: to[i]}
		if !f.links.Verify(claims, q.Get("sig")) {
			t.Errorf("render %d: el enlace de baja no verifica", i)
		}
	}
	if len(seenURLs) != 3 {
		t.Fatal("las variables reservadas deben cambiar por destinatario")
	}
	if len(f.repo.published("transactional.message.queued")) != 3 {
		t.Fatal("se encola cada mensaje")
	}
}

func TestCreateTemplateNotFound(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	f.tpl.err = domain.ErrTemplateNotFound
	if _, err := f.uc.CreateMessages(ctx, templateCommand(f, "ana@example.com", "eva@example.com")); !errors.Is(err, domain.ErrTemplateNotFound) {
		t.Fatalf("err = %v", err)
	}
	if len(f.repo.messages) != 0 || len(f.repo.outbox) != 0 {
		t.Fatal("un render fallido no deja mensajes a medias")
	}
}

func TestIdempotencyKeyReplaysWithoutResending(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	cmd := templateCommand(f, "ana@example.com", "eva@example.com")
	cmd.IdempotencyKey = "order-100"

	first, err := f.uc.CreateMessages(ctx, cmd)
	if err != nil || first.Replayed {
		t.Fatalf("primera peticion: %+v, %v", first, err)
	}
	second, err := f.uc.CreateMessages(ctx, cmd)
	if err != nil || !second.Replayed {
		t.Fatalf("la segunda peticion debe ser una repeticion: %+v, %v", second, err)
	}
	if len(second.Messages) != 2 || second.Messages[0].ID != first.Messages[0].ID || second.Messages[1].ID != first.Messages[1].ID {
		t.Fatalf("la repeticion devuelve los mismos mensajes: %+v vs %+v", second.Messages, first.Messages)
	}
	if len(f.repo.messages) != 2 || len(f.repo.published("transactional.message.queued")) != 2 {
		t.Fatal("la repeticion no crea ni encola nada")
	}
	if len(f.supp.checks) != 1 || len(f.tpl.calls) != 2 {
		t.Fatal("la repeticion no vuelve a consultar supresion ni a renderizar")
	}

	other := cmd
	other.TenantID = uuid.New()
	f.setDomain(other.TenantID, shopDomain, "verified", "sending")
	third, err := f.uc.CreateMessages(ctx, other)
	if err != nil || third.Replayed {
		t.Fatalf("la misma clave en otra empresa es una peticion nueva: %+v, %v", third, err)
	}
}

func TestIdempotencyKeyConcurrentWinner(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	winner := domain.Message{ID: uuid.New(), TenantID: f.tenant, Status: domain.StatusQueued, To: []domain.Recipient{{Email: "ana@example.com"}}}
	f.repo.commitBeforeNextTx = func(r *fakeRepo) {
		r.messages[winner.ID] = winner
		r.order = append(r.order, winner.ID)
		r.submissions[submissionKey(f.tenant, "k-1")] = domain.Submission{ID: uuid.New(), TenantID: f.tenant, IdempotencyKey: "k-1", MessageIDs: []uuid.UUID{winner.ID}}
	}
	cmd := rawCommand(f, "ana@example.com")
	cmd.IdempotencyKey = "k-1"
	res, err := f.uc.CreateMessages(ctx, cmd)
	if err != nil || !res.Replayed || len(res.Messages) != 1 || res.Messages[0].ID != winner.ID {
		t.Fatalf("la peticion que pierde la carrera devuelve la del ganador: %+v, %v", res, err)
	}
	if len(f.repo.messages) != 1 || len(f.repo.outbox) != 0 {
		t.Fatal("lo escrito por la perdedora se deshace, evento incluido")
	}
}

func TestCreateValidation(t *testing.T) {
	cases := map[string]func(c *CreateMessagesCommand){
		"plantilla y cuerpo":      func(c *CreateMessagesCommand) { id := uuid.New(); c.TemplateID = &id },
		"sin plantilla ni cuerpo": func(c *CreateMessagesCommand) { c.HTML = "" },
		"sin asunto":              func(c *CreateMessagesCommand) { c.Subject = " " },
		"version sin plantilla":   func(c *CreateMessagesCommand) { v := 2; c.TemplateVersion = &v },
		"from invalido":           func(c *CreateMessagesCommand) { c.From.Email = "no-es-email" },
		"reply_to invalido":       func(c *CreateMessagesCommand) { c.ReplyTo = "x@" },
		"to vacio":                func(c *CreateMessagesCommand) { c.To = nil },
		"to invalido":             func(c *CreateMessagesCommand) { c.To[0].Email = "ana@@example.com" },
		"destinatario repetido":   func(c *CreateMessagesCommand) { c.Cc = []domain.Recipient{{Email: "ANA@example.com"}} },
		"cabecera estandar":       func(c *CreateMessagesCommand) { c.Headers = map[string]string{"Bcc": "x@example.com"} },
		"etiqueta reservada":      func(c *CreateMessagesCommand) { c.Tags = map[string]string{"message_id": "x"} },
		"cc con baja": func(c *CreateMessagesCommand) {
			c.Unsubscribable = true
			c.Cc = []domain.Recipient{{Email: "c@example.com"}}
		},
		"programado a mas de 30 dias":   func(c *CreateMessagesCommand) { t := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC); c.ScheduledAt = &t },
		"cuerpo por encima de 10 MiB":   func(c *CreateMessagesCommand) { c.HTML = strings.Repeat("a", domain.MaxBodyBytes+1) },
		"clave de idempotencia enorme":  func(c *CreateMessagesCommand) { c.IdempotencyKey = strings.Repeat("k", 201) },
		"mas de 50 destinatarios":       func(c *CreateMessagesCommand) { c.To = manyRecipients(51) },
		"to+cc+bcc por encima del tope": func(c *CreateMessagesCommand) { c.To = manyRecipients(40); c.Bcc = manyRecipients(11) },
	}
	for name, mutate := range cases {
		f := newFixture(t, Config{})
		f.setDomain(f.tenant, shopDomain, "verified", "sending")
		cmd := rawCommand(f, "ana@example.com")
		mutate(&cmd)
		_, err := f.uc.CreateMessages(ctx, cmd)
		if !domain.IsValidation(err) {
			t.Errorf("%s: se esperaba error de validacion, err = %v", name, err)
		}
		if len(f.supp.checks) != 0 {
			t.Errorf("%s: una peticion invalida no consulta supresion", name)
		}
	}
}

func manyRecipients(n int) []domain.Recipient {
	out := make([]domain.Recipient, n)
	for i := range out {
		out[i] = domain.Recipient{Email: "r" + strings.Repeat("x", i) + "@example.com"}
	}
	return out
}

func TestCreateRejectsAttachments(t *testing.T) {
	f := newFixture(t, Config{})
	cmd := rawCommand(f, "ana@example.com")
	cmd.HasAttachments = true
	if _, err := f.uc.CreateMessages(ctx, cmd); !errors.Is(err, domain.ErrAttachmentsNotSupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestScheduledMessageReleasedWhenDue(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	cmd := rawCommand(f, "ana@example.com")
	at := f.now.Add(10 * time.Minute)
	cmd.ScheduledAt = &at

	res, err := f.uc.CreateMessages(ctx, cmd)
	if err != nil || res.Messages[0].Status != domain.StatusAccepted {
		t.Fatalf("un programado futuro queda en accepted: %+v, %v", res, err)
	}
	if len(f.repo.outbox) != 0 {
		t.Fatal("un programado no se encola antes de su hora")
	}
	if n, _ := f.uc.ReleaseDue(ctx, f.tenant); n != 0 {
		t.Fatal("antes de la hora no se libera nada")
	}
	f.now = f.now.Add(11 * time.Minute)
	n, err := f.uc.ReleaseDue(ctx, f.tenant)
	if err != nil || n != 1 {
		t.Fatalf("vencido: liberados=%d err=%v", n, err)
	}
	if f.repo.messages[res.Messages[0].ID].Status != domain.StatusQueued || len(f.repo.published("transactional.message.queued")) != 1 {
		t.Fatal("al vencer pasa a queued y se encola")
	}

	past := f.now.Add(-time.Minute)
	cmd.ScheduledAt = &past
	res, _ = f.uc.CreateMessages(ctx, cmd)
	if res.Messages[0].Status != domain.StatusQueued {
		t.Fatal("un scheduled_at pasado se encola de inmediato")
	}
}

func createQueued(t *testing.T, f *fixture, cmd CreateMessagesCommand) uuid.UUID {
	t.Helper()
	res, err := f.uc.CreateMessages(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	return res.Messages[0].ID
}

func TestSendQueuedSuccessAndIdempotence(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	cmd := rawCommand(f, "ana@example.com")
	cmd.ReplyTo = "soporte@" + shopDomain
	id := createQueued(t, f, cmd)

	out, err := f.uc.SendQueued(ctx, f.tenant, id)
	if err != nil || !out.Ack || out.Status != domain.StatusSent {
		t.Fatalf("envio: %+v, %v", out, err)
	}
	msg := f.repo.messages[id]
	if msg.Status != domain.StatusSent || msg.SESMessageID == nil || *msg.SESMessageID != "ses-1" || msg.SentAt == nil {
		t.Fatalf("mensaje tras el envio: %+v", msg)
	}
	if f.repo.eventsOfType(id, domain.EventSend) != 1 || len(f.repo.published("transactional.email.sent")) != 1 {
		t.Fatal("se registra el evento send y se publica transactional.email.sent")
	}
	sent := f.sender.sent[0]
	if sent.From != `"Tienda" <no-reply@shop.example.com>` || sent.ReplyTo[0] != "soporte@"+shopDomain || sent.MessageID != id || sent.TenantID != f.tenant {
		t.Fatalf("correo saliente: %+v", sent)
	}
	if sent.UnsubscribeURL != "" {
		t.Fatal("un transaccional puro no lleva enlace de baja")
	}
	if f.limiter.waits != 1 {
		t.Fatal("cada intento pasa por el limitador de tasa")
	}

	out, err = f.uc.SendQueued(ctx, f.tenant, id)
	if err != nil || !out.Ack || f.sender.calls != 1 {
		t.Fatalf("una reentrega de un mensaje ya enviado se confirma sin reenviar: %+v calls=%d", out, f.sender.calls)
	}
}

func TestSendQueuedUnsubscribableCarriesSignedLink(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	cmd := rawCommand(f, "ana@example.com", "eva@example.com")
	cmd.Unsubscribable = true
	res, err := f.uc.CreateMessages(ctx, cmd)
	if err != nil || len(res.Messages) != 2 {
		t.Fatalf("un envio dable de baja se reparte por persona: %+v, %v", res, err)
	}
	if _, err := f.uc.SendQueued(ctx, f.tenant, res.Messages[1].ID); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(f.sender.sent[0].UnsubscribeURL)
	claims := domain.UnsubscribeClaims{TenantID: f.tenant, MessageID: res.Messages[1].ID, Email: "eva@example.com"}
	if !f.links.Verify(claims, u.Query().Get("sig")) {
		t.Fatalf("el enlace de List-Unsubscribe debe ser el de ese mensaje: %s", u)
	}
}

func TestSendQueuedPermanentFailure(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	id := createQueued(t, f, rawCommand(f, "ana@example.com"))
	f.sender.err = &domain.SendError{Kind: domain.ErrorPermanent, Code: "MessageRejected", Message: "Email address is not verified."}

	out, err := f.uc.SendQueued(ctx, f.tenant, id)
	if err != nil || !out.Ack || out.Status != domain.StatusFailed {
		t.Fatalf("un rechazo de contenido se confirma como failed: %+v, %v", out, err)
	}
	msg := f.repo.messages[id]
	if msg.Status != domain.StatusFailed || msg.Error == nil || !strings.Contains(*msg.Error, "MessageRejected") {
		t.Fatalf("mensaje: %+v", msg)
	}
	if f.sender.calls != 1 {
		t.Fatal("un error permanente no se reintenta")
	}
	failed := f.repo.published("transactional.email.failed")
	if len(failed) != 1 || failed[0].Payload["code"] != "MessageRejected" {
		t.Fatalf("se publica transactional.email.failed: %+v", failed)
	}
}

func TestSendQueuedTransientRetriesThenSucceeds(t *testing.T) {
	fastRetries(t)
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	id := createQueued(t, f, rawCommand(f, "ana@example.com"))
	throttled := &domain.SendError{Kind: domain.ErrorTransient, Code: "TooManyRequestsException"}
	f.sender.errs = []error{throttled, throttled}

	out, err := f.uc.SendQueued(ctx, f.tenant, id)
	if err != nil || !out.Ack || out.Status != domain.StatusSent || f.sender.calls != 3 {
		t.Fatalf("throttling pasajero: %+v err=%v calls=%d", out, err, f.sender.calls)
	}
}

func TestSendQueuedTransientExhausted(t *testing.T) {
	fastRetries(t)
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	id := createQueued(t, f, rawCommand(f, "ana@example.com"))
	f.sender.err = &domain.SendError{Kind: domain.ErrorTransient, Code: "InternalServiceErrorException"}

	for i := 1; i < domain.MaxSendAttempts; i++ {
		out, err := f.uc.SendQueued(ctx, f.tenant, id)
		if err != nil || out.Ack || out.Status != domain.StatusQueued {
			t.Fatalf("entrega %d: un transitorio no se confirma (reentrega con backoff): %+v, %v", i, out, err)
		}
		if f.repo.messages[id].Attempts != i {
			t.Fatalf("entrega %d: attempts = %d", i, f.repo.messages[id].Attempts)
		}
	}
	out, err := f.uc.SendQueued(ctx, f.tenant, id)
	if err != nil || !out.Ack || out.Status != domain.StatusFailed {
		t.Fatalf("agotado el presupuesto pasa a failed: %+v, %v", out, err)
	}
	if !strings.Contains(*f.repo.messages[id].Error, "reintentos agotados") || len(f.repo.published("transactional.email.failed")) != 1 {
		t.Fatal("el motivo y el evento de fallo deben quedar")
	}
}

func TestSendQueuedUnknownMessageIsAcked(t *testing.T) {
	f := newFixture(t, Config{})
	out, err := f.uc.SendQueued(ctx, f.tenant, uuid.New())
	if err != nil || !out.Ack || f.sender.calls != 0 {
		t.Fatalf("un mensaje que no existe se descarta: %+v, %v", out, err)
	}
}

func sentMessage(t *testing.T, f *fixture) uuid.UUID {
	t.Helper()
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	id := createQueued(t, f, rawCommand(f, "ana@example.com"))
	if _, err := f.uc.SendQueued(ctx, f.tenant, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func bounce(f *fixture, id uuid.UUID, bounceType, snsID string) domain.InboundEvent {
	return domain.InboundEvent{
		Type: domain.EventBounce, TenantID: f.tenant, MessageID: id, Recipients: []string{"ana@example.com"},
		BounceType: bounceType, SNSMessageID: snsID, OccurredAt: f.now,
		Detail: map[string]any{"reason": "Permanent/General", "diagnostic_code": "smtp; 550 5.1.1 user unknown"},
	}
}

func TestIngestPermanentBounceSuppressesAndIsIdempotent(t *testing.T) {
	f := newFixture(t, Config{})
	id := sentMessage(t, f)

	if err := f.uc.IngestSESEvent(ctx, f.tenant, bounce(f, id, domain.BounceTypePermanent, "sns-1")); err != nil {
		t.Fatal(err)
	}
	if len(f.supp.added) != 1 {
		t.Fatalf("un rebote permanente da de alta en suppression: %+v", f.supp.added)
	}
	added := f.supp.added[0]
	if added.Email != "ana@example.com" || added.Reason != "hard_bounce" || added.MessageID != id.String() || added.Source != "transactional" || added.Detail != "Permanent/General: smtp; 550 5.1.1 user unknown" {
		t.Fatalf("alta en suppression: %+v", added)
	}
	if f.repo.messages[id].Status != domain.StatusBounced {
		t.Fatal("el mensaje pasa a bounced")
	}
	bounced := f.repo.published("transactional.email.bounced")
	if len(bounced) != 1 || bounced[0].Payload["bounce_type"] != "permanent" || bounced[0].Payload["email"] != "ana@example.com" ||
		bounced[0].Payload["message_id"] != id.String() || bounced[0].Payload["tenant_id"] != f.tenant.String() {
		t.Fatalf("transactional.email.bounced: %+v", bounced)
	}
	if _, ok := bounced[0].Payload["detail"].(string); !ok {
		t.Fatal("detail viaja como texto (suppression lo guarda asi)")
	}

	if err := f.uc.IngestSESEvent(ctx, f.tenant, bounce(f, id, domain.BounceTypePermanent, "sns-1")); err != nil {
		t.Fatal(err)
	}
	if len(f.repo.published("transactional.email.bounced")) != 1 || f.repo.eventsOfType(id, domain.EventBounce) != 1 {
		t.Fatal("la misma notificacion SNS no se cuenta dos veces")
	}
}

func TestIngestTransientBounceDoesNotSuppress(t *testing.T) {
	f := newFixture(t, Config{})
	id := sentMessage(t, f)
	if err := f.uc.IngestSESEvent(ctx, f.tenant, bounce(f, id, domain.BounceTypeTransient, "sns-2")); err != nil {
		t.Fatal(err)
	}
	if len(f.supp.added) != 0 {
		t.Fatal("un rebote transitorio no suprime")
	}
	if b := f.repo.published("transactional.email.bounced"); len(b) != 1 || b[0].Payload["bounce_type"] != "transient" {
		t.Fatalf("se publica con bounce_type transient: %+v", b)
	}
}

func TestIngestComplaintAfterDelivery(t *testing.T) {
	f := newFixture(t, Config{})
	id := sentMessage(t, f)
	delivery := domain.InboundEvent{Type: domain.EventDelivery, TenantID: f.tenant, MessageID: id, Recipients: []string{"ana@example.com"}, SNSMessageID: "sns-d"}
	if err := f.uc.IngestSESEvent(ctx, f.tenant, delivery); err != nil || f.repo.messages[id].Status != domain.StatusDelivered {
		t.Fatalf("delivery: %v", err)
	}
	complaint := domain.InboundEvent{Type: domain.EventComplaint, TenantID: f.tenant, MessageID: id, Recipients: []string{"ana@example.com"},
		SNSMessageID: "sns-c", Detail: map[string]any{"reason": "abuse"}}
	if err := f.uc.IngestSESEvent(ctx, f.tenant, complaint); err != nil {
		t.Fatal(err)
	}
	if f.repo.messages[id].Status != domain.StatusComplained {
		t.Fatal("una queja tras la entrega deja el mensaje en complained")
	}
	if len(f.supp.added) != 1 || f.supp.added[0].Reason != "complaint" || len(f.repo.published("transactional.email.complained")) != 1 {
		t.Fatal("la queja suprime y se publica")
	}
}

func TestIngestLateDeliveryDoesNotOverwriteBounce(t *testing.T) {
	f := newFixture(t, Config{})
	id := sentMessage(t, f)
	_ = f.uc.IngestSESEvent(ctx, f.tenant, bounce(f, id, domain.BounceTypePermanent, "sns-b"))
	_ = f.uc.IngestSESEvent(ctx, f.tenant, domain.InboundEvent{Type: domain.EventDelivery, TenantID: f.tenant, MessageID: id, SNSMessageID: "sns-late"})
	if f.repo.messages[id].Status != domain.StatusBounced {
		t.Fatalf("un delivery desordenado no pisa bounced: %s", f.repo.messages[id].Status)
	}
}

func TestIngestRejectsTenantMismatch(t *testing.T) {
	f := newFixture(t, Config{})
	id := sentMessage(t, f)
	err := f.uc.IngestSESEvent(ctx, uuid.New(), bounce(f, id, domain.BounceTypePermanent, "sns-x"))
	if !errors.Is(err, domain.ErrTenantMismatch) || len(f.supp.added) != 0 || f.repo.eventsOfType(id, domain.EventBounce) != 0 {
		t.Fatalf("un evento de otra empresa se rechaza sin efectos: %v", err)
	}
}

func TestIngestUnknownMessageIsIgnorable(t *testing.T) {
	f := newFixture(t, Config{})
	err := f.uc.IngestSESEvent(ctx, f.tenant, domain.InboundEvent{Type: domain.EventDelivery, TenantID: f.tenant, MessageID: uuid.New(), SNSMessageID: "sns-u"})
	if !IsIgnorableIngestError(err) {
		t.Fatalf("mensaje desconocido: err = %v", err)
	}
}

func TestIngestSuppressionUnavailableStoresNothing(t *testing.T) {
	f := newFixture(t, Config{})
	id := sentMessage(t, f)
	f.supp.addErr = errors.New("503")
	err := f.uc.IngestSESEvent(ctx, f.tenant, bounce(f, id, domain.BounceTypePermanent, "sns-r"))
	if !errors.Is(err, domain.ErrSuppressionUnavailable) || f.repo.eventsOfType(id, domain.EventBounce) != 0 {
		t.Fatalf("sin supresion no se registra el evento (SNS reintentara): %v", err)
	}
}

func TestUnsubscribe(t *testing.T) {
	f := newFixture(t, Config{})
	id := sentMessage(t, f)
	claims := domain.UnsubscribeClaims{TenantID: f.tenant, MessageID: id, Email: "ana@example.com"}
	sig := f.links.Sign(claims)

	if err := f.uc.Unsubscribe(ctx, claims, sig); err != nil {
		t.Fatal(err)
	}
	if len(f.supp.added) != 1 || f.supp.added[0].Reason != "unsubscribe" || f.supp.added[0].MessageID != id.String() {
		t.Fatalf("la baja se da de alta en suppression: %+v", f.supp.added)
	}
	if len(f.repo.published("transactional.email.unsubscribed")) != 1 {
		t.Fatal("se publica transactional.email.unsubscribed")
	}
	if err := f.uc.Unsubscribe(ctx, claims, sig); err != nil {
		t.Fatal(err)
	}
	if len(f.repo.published("transactional.email.unsubscribed")) != 1 {
		t.Fatal("una segunda baja del mismo enlace no publica de nuevo")
	}

	calls := len(f.supp.added)
	claims.Email = "eva@example.com"
	if err := f.uc.Unsubscribe(ctx, claims, sig); !errors.Is(err, domain.ErrInvalidSignature) || len(f.supp.added) != calls {
		t.Fatalf("un enlace alterado no da de baja a nadie: %v", err)
	}
}

func TestInternalSend(t *testing.T) {
	cfg := Config{PlatformFromEmail: "no-reply@platform.example.com", PlatformFromName: "Core Force Mail"}
	f := newFixture(t, cfg)
	f.setDomain(f.tenant, "platform.example.com", "verified", "sending")

	res, err := f.uc.InternalSend(ctx, InternalSendCommand{TenantID: f.tenant, To: "ana@example.com", Subject: "Restablecer contrasena", HTMLBody: "<p>enlace</p>", TextBody: "enlace"})
	if err != nil || res.Status != domain.StatusQueued {
		t.Fatalf("envio interno: %+v, %v", res, err)
	}
	msg := f.repo.messages[res.MessageID]
	if msg.FromEmail != "no-reply@platform.example.com" || msg.FromName != "Core Force Mail" || msg.Unsubscribable {
		t.Fatalf("remitente de plataforma: %+v", msg)
	}
	// Las dos partes se guardan: el envio por SES las junta en un multipart/alternative.
	if msg.HTML == nil || *msg.HTML != "<p>enlace</p>" || msg.Text == nil || *msg.Text != "enlace" {
		t.Fatalf("el envio interno pierde una de sus partes: html %v, texto %v", msg.HTML, msg.Text)
	}
	if len(f.repo.published("transactional.message.queued")) != 1 {
		t.Fatal("el envio interno se encola igual que el del API")
	}

	f.supp.suppressed = map[string]string{"eva@example.com": "hard_bounce"}
	res, err = f.uc.InternalSend(ctx, InternalSendCommand{TenantID: f.tenant, To: "eva@example.com", Subject: "x", HTMLBody: "y"})
	if err != nil || res.Status != domain.StatusSuppressed || len(f.repo.published("transactional.message.queued")) != 1 {
		t.Fatalf("la supresion se respeta tambien en el envio interno: %+v, %v", res, err)
	}

	for name, cmd := range map[string]InternalSendCommand{
		"to invalido":   {TenantID: f.tenant, To: "no", Subject: "x", HTMLBody: "y"},
		"sin asunto":    {TenantID: f.tenant, To: "ana@example.com", HTMLBody: "y"},
		"sin cuerpo":    {TenantID: f.tenant, To: "ana@example.com", Subject: "x"},
		"cuerpo enorme": {TenantID: f.tenant, To: "ana@example.com", Subject: "x", HTMLBody: strings.Repeat("a", domain.MaxBodyBytes+1)},
	} {
		if _, err := f.uc.InternalSend(ctx, cmd); !domain.IsValidation(err) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestInternalSendPlatformDomainGate(t *testing.T) {
	cfg := Config{PlatformFromEmail: "no-reply@platform.example.com", PlatformFromName: "Core Force Mail"}
	cmd := InternalSendCommand{To: "ana@example.com", Subject: "x", HTMLBody: "y"}

	f := newFixture(t, cfg)
	cmd.TenantID = f.tenant
	if _, err := f.uc.InternalSend(ctx, cmd); !errors.Is(err, domain.ErrSendingDomainNotVerified) {
		t.Fatalf("sin dominio verificado y sin la excepcion de arranque: %v", err)
	}

	cfg.AllowUnverifiedPlatformFrom = true
	f = newFixture(t, cfg)
	cmd.TenantID = f.tenant
	if _, err := f.uc.InternalSend(ctx, cmd); err != nil {
		t.Fatalf("con PLATFORM_FROM_ALLOW_UNVERIFIED se permite: %v", err)
	}

	f = newFixture(t, Config{})
	cmd.TenantID = f.tenant
	if _, err := f.uc.InternalSend(ctx, cmd); !domain.IsValidation(err) {
		t.Fatalf("sin PLATFORM_FROM_EMAIL: %v", err)
	}
}

func TestApplyDomainEventProjection(t *testing.T) {
	f := newFixture(t, Config{})
	apply := func(action, status, purpose string) {
		t.Helper()
		if err := f.uc.ApplyDomainEvent(ctx, DomainEvent{Action: action, TenantID: f.tenant, Domain: "Shop.Example.com", Purpose: purpose, Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	apply("created", "pending", "sending")
	if len(f.repo.domains) != 0 {
		t.Fatal("created no cambia la capacidad de envio")
	}
	apply("verified", "verified", "sending")
	if _, err := f.uc.CreateMessages(ctx, rawCommand(f, "ana@example.com")); err != nil {
		t.Fatalf("tras verified se puede enviar: %v", err)
	}
	apply("failed", "failed", "sending")
	if _, err := f.uc.CreateMessages(ctx, rawCommand(f, "ana@example.com")); !errors.Is(err, domain.ErrSendingDomainNotVerified) {
		t.Fatalf("tras failed no: %v", err)
	}
	apply("deleted", "", "")
	if len(f.repo.domains) != 0 {
		t.Fatal("deleted retira el dominio de la proyeccion")
	}
}

// Con la integracion de SES de domain-service, un dominio verificado en su DNS solo envia cuando SES lo
// verifico; un evento que no dice nada de SES no borra lo ultimo que se supo.
func TestSoloEnviaElDominioQueSESVerifico(t *testing.T) {
	f := newFixture(t, Config{})
	ready, notReady := true, false
	apply := func(action, status string, sendingReady *bool) {
		t.Helper()
		if err := f.uc.ApplyDomainEvent(ctx, DomainEvent{Action: action, TenantID: f.tenant, Domain: "shop.example.com",
			Purpose: "both", Status: status, SendingReady: sendingReady}); err != nil {
			t.Fatal(err)
		}
	}
	blocked := func(msg string) {
		t.Helper()
		if _, err := f.uc.CreateMessages(ctx, rawCommand(f, "ana@example.com")); !errors.Is(err, domain.ErrSendingDomainNotVerified) {
			t.Fatalf("%s: %v", msg, err)
		}
	}

	apply("verified", "verified", &notReady)
	blocked("verificado en DNS pero no en SES")
	apply("failed", "failed", nil)
	apply("verified", "verified", nil)
	blocked("un evento sin sending_ready conserva el no de SES")
	apply("sending_status_changed", "verified", &ready)
	if _, err := f.uc.CreateMessages(ctx, rawCommand(f, "ana@example.com")); err != nil {
		t.Fatalf("SES lo verifico: %v", err)
	}
	apply("sending_status_changed", "verified", &notReady)
	blocked("SES dejo de aceptarlo")

	if err := f.uc.ApplyDomainEvent(ctx, DomainEvent{Action: "sending_status_changed", TenantID: f.tenant, Domain: "shop.example.com", SendingReady: &ready}); !domain.IsValidation(err) {
		t.Fatalf("sending_status_changed sin status: %v", err)
	}
}

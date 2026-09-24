package app

import (
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/alonsosss/corforce-email/services/transactional/internal/ports"
	"github.com/google/uuid"
)

func smtpCommand(f *fixture, to ...string) RawMessageCommand {
	return RawMessageCommand{
		TenantID:     f.tenant,
		EnvelopeFrom: "bounces@" + shopDomain,
		Recipients:   to,
		From:         domain.Recipient{Email: "No-Reply@" + shopDomain, Name: "Tienda"},
		Subject:      "Pedido",
		Text:         "Gracias",
		Raw:          []byte("From: no-reply@" + shopDomain + "\r\nSubject: Pedido\r\n\r\nGracias\r\n"),
	}
}

func TestRawMessageSeEncolaConSuMIME(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	key := uuid.New()
	cmd := smtpCommand(f, "ana@example.com", "Eva@Example.com")
	cmd.APIKeyID = &key
	res, err := f.uc.CreateRawMessage(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 1 || res.Messages[0].Status != domain.StatusQueued || len(res.Messages[0].To) != 2 {
		t.Fatalf("un mensaje con todos los destinatarios del sobre: %+v", res.Messages)
	}
	m := f.repo.messages[res.Messages[0].ID]
	if m.Origin != domain.OriginSMTP || m.Class != domain.ClassTransactional || m.Test || m.APIKeyID == nil || *m.APIKeyID != key {
		t.Fatalf("fila: %+v", m)
	}
	if m.FromEmail != domain.NormalizeEmail("No-Reply@"+shopDomain) || m.To[1].Email != domain.NormalizeEmail("Eva@Example.com") || m.Text == nil {
		t.Fatalf("direcciones normalizadas y cuerpo: %+v", m)
	}
	if string(f.repo.raw[m.ID]) != string(cmd.Raw) {
		t.Fatal("el MIME se guarda con la fila")
	}
	if len(f.rep.calls) != 1 || f.rep.calls[0] != (reputationCall{Class: domain.ClassTransactional, Count: 2}) {
		t.Fatalf("reputation autoriza el carril transaccional por destinatario: %+v", f.rep.calls)
	}
	if len(f.repo.published("transactional.message.queued")) != 1 {
		t.Fatal("se encola por el carril transaccional")
	}

	out, err := f.uc.SendQueued(ctx, f.tenant, m.ID)
	if err != nil || out.Status != domain.StatusSent {
		t.Fatalf("envio: %+v %v", out, err)
	}
	sent := f.sender.sent[0]
	if string(sent.Raw) != string(cmd.Raw) || len(sent.To) != 2 || sent.To[0] != "ana@example.com" {
		t.Fatalf("sale el MIME al sobre: %+v", sent)
	}
	if f.repo.events[0].Detail["source"] != domain.OriginSMTP {
		t.Fatalf("el evento de envio dice por donde entro: %+v", f.repo.events[0].Detail)
	}
	if len(f.mSender.sent) != 0 {
		t.Fatal("nunca sale por el carril de marketing")
	}
}

// Cualquiera de los tres remitentes (sobre, From, Sender) de un dominio sin verificar, o de otra
// empresa, rechaza el mensaje sin crear nada.
func TestRawMessageRemitenteNoVerificado(t *testing.T) {
	for name, mutate := range map[string]func(*RawMessageCommand){
		"sobre":  func(c *RawMessageCommand) { c.EnvelopeFrom = "x@ajeno.test" },
		"from":   func(c *RawMessageCommand) { c.From.Email = "x@ajeno.test" },
		"sender": func(c *RawMessageCommand) { c.Sender = "x@ajeno.test" },
	} {
		f := newFixture(t, Config{})
		f.setDomain(f.tenant, shopDomain, "verified", "sending")
		f.setDomain(uuid.New(), "ajeno.test", "verified", "sending")
		cmd := smtpCommand(f, "ana@example.com")
		mutate(&cmd)
		if _, err := f.uc.CreateRawMessage(ctx, cmd); !errors.Is(err, domain.ErrSendingDomainNotVerified) {
			t.Errorf("%s: %v", name, err)
		}
		if len(f.repo.messages) != 0 || len(f.supp.checks) != 0 {
			t.Errorf("%s: no se crea ni se consulta nada", name)
		}
	}
}

func TestRawMessageSupresion(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	f.supp.suppressed["eva@example.com"] = "hard_bounce"
	res, err := f.uc.CreateRawMessage(ctx, smtpCommand(f, "ana@example.com", "eva@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	m := f.repo.messages[res.Messages[0].ID]
	if len(m.To) != 1 || m.To[0].Email != "ana@example.com" || len(res.Suppressed) != 1 {
		t.Fatalf("la suprimida sale del sobre: %+v %+v", m.To, res.Suppressed)
	}

	f = newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	f.supp.suppressed["ana@example.com"] = "complaint"
	res, err = f.uc.CreateRawMessage(ctx, smtpCommand(f, "ana@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Messages[0].Status != domain.StatusSuppressed || len(f.repo.raw) != 0 || len(f.rep.calls) != 0 ||
		len(f.repo.published("transactional.message.queued")) != 0 {
		t.Fatalf("todos suprimidos: queda constancia sin MIME, sin reputation ni cola: %+v", res.Messages)
	}

	f = newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	f.supp.checkErr = errors.New("caido")
	if _, err := f.uc.CreateRawMessage(ctx, smtpCommand(f, "ana@example.com")); !errors.Is(err, domain.ErrSuppressionUnavailable) {
		t.Fatalf("sin suppression no se encola: %v", err)
	}
}

func TestRawMessageDenegadoPorReputation(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	f.rep.auth = &ports.Authorization{Allowed: false, Class: domain.ClassTransactional, Reason: domain.DenySuspended}
	var denied *domain.SendingDeniedError
	if _, err := f.uc.CreateRawMessage(ctx, smtpCommand(f, "ana@example.com")); !errors.As(err, &denied) {
		t.Fatalf("la denegacion se respeta: %v", err)
	}
	if len(f.repo.messages) != 0 {
		t.Fatal("nada se crea")
	}
}

func TestRawMessageIdempotente(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	cmd := smtpCommand(f, "ana@example.com")
	cmd.IdempotencyKey = "smtp-abc"
	first, err := f.uc.CreateRawMessage(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.uc.CreateRawMessage(ctx, smtpCommand(f, "ana@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	cmd2 := smtpCommand(f, "ana@example.com")
	cmd2.IdempotencyKey = "smtp-abc"
	replay, err := f.uc.CreateRawMessage(ctx, cmd2)
	if err != nil || !replay.Replayed || replay.Messages[0].ID != first.Messages[0].ID || again.Messages[0].ID == first.Messages[0].ID {
		t.Fatalf("la repeticion con la misma clave devuelve lo creado: %+v %v", replay, err)
	}
	if len(f.repo.messages) != 2 {
		t.Fatalf("mensajes: %d", len(f.repo.messages))
	}
}

func TestRawMessageValidacion(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	many := make([]string, domain.MaxRecipients+1)
	for i := range many {
		many[i] = uuid.NewString() + "@example.com"
	}
	for name, mutate := range map[string]func(*RawMessageCommand){
		"sin destinatarios": func(c *RawMessageCommand) { c.Recipients = nil },
		"demasiados":        func(c *RawMessageCommand) { c.Recipients = many },
		"repetido":          func(c *RawMessageCommand) { c.Recipients = []string{"a@example.com", "A@example.com"} },
		"destinatario roto": func(c *RawMessageCommand) { c.Recipients = []string{"no es correo"} },
		"sobre roto":        func(c *RawMessageCommand) { c.EnvelopeFrom = "" },
		"sin MIME":          func(c *RawMessageCommand) { c.Raw = nil },
		"MIME enorme":       func(c *RawMessageCommand) { c.Raw = make([]byte, domain.MaxRawBytes+1) },
		"reply-to roto":     func(c *RawMessageCommand) { c.ReplyTo = []string{"roto"} },
		"clave de idempotencia": func(c *RawMessageCommand) {
			c.IdempotencyKey = string(make([]byte, 201))
		},
	} {
		cmd := smtpCommand(f, "ana@example.com")
		mutate(&cmd)
		if _, err := f.uc.CreateRawMessage(ctx, cmd); !domain.IsValidation(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSendQueuedSinMIMEFallaDefinitivo(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	res, err := f.uc.CreateRawMessage(ctx, smtpCommand(f, "ana@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	delete(f.repo.raw, res.Messages[0].ID)
	out, err := f.uc.SendQueued(ctx, f.tenant, res.Messages[0].ID)
	if err != nil || !out.Ack || out.Status != domain.StatusFailed || f.sender.calls != 0 {
		t.Fatalf("sin contenido el mensaje falla sin llamar a SES: %+v %v", out, err)
	}
}

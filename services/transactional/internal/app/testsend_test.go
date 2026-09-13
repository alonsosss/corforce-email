package app

import (
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/google/uuid"
)

// emailEvents devuelve los transactional.email.* publicados.
func emailEvents(f *fixture) []outboxEvent {
	var out []outboxEvent
	for _, e := range f.repo.outbox {
		if strings.HasPrefix(e.Subject, "transactional.email.") {
			out = append(out, e)
		}
	}
	return out
}

// recorre el ciclo de vida completo de un mensaje ya encolado: envio, entrega, apertura,
// clic, queja, rebote y baja por enlace, para que salgan todos los transactional.email.*.
func driveLifecycle(t *testing.T, f *fixture, id uuid.UUID) {
	t.Helper()
	if _, err := f.uc.SendQueued(ctx, f.tenant, id); err != nil {
		t.Fatal(err)
	}
	email := f.repo.messages[id].To[0].Email
	for i, ev := range []domain.InboundEvent{
		{Type: domain.EventDelivery},
		{Type: domain.EventOpen},
		{Type: domain.EventClick, Detail: map[string]any{"link": "https://shop.example.com/p"}},
		{Type: domain.EventComplaint},
		{Type: domain.EventBounce, BounceType: domain.BounceTypePermanent},
	} {
		ev.TenantID, ev.MessageID, ev.Recipients, ev.OccurredAt = f.tenant, id, []string{email}, f.now
		ev.SNSMessageID = id.String() + "-" + string(rune('a'+i))
		if err := f.uc.IngestSESEvent(ctx, f.tenant, ev); err != nil {
			t.Fatalf("%s: %v", ev.Type, err)
		}
	}
	claims := domain.UnsubscribeClaims{TenantID: f.tenant, MessageID: id, Email: email}
	if err := f.uc.Unsubscribe(ctx, claims, f.links.Sign(claims)); err != nil {
		t.Fatal(err)
	}
}

func assertTestFlag(t *testing.T, f *fixture, want bool) {
	t.Helper()
	events := emailEvents(f)
	seen := map[string]bool{}
	for _, e := range events {
		got, ok := e.Payload["test"].(bool)
		if !ok || got != want {
			t.Fatalf("%s: test=%v (presente=%v), se esperaba %v", e.Subject, e.Payload["test"], ok, want)
		}
		seen[e.Subject] = true
	}
	for _, subject := range []string{"sent", "delivered", "opened", "clicked", "complained", "bounced", "unsubscribed"} {
		if !seen["transactional.email."+subject] {
			t.Fatalf("no salio transactional.email.%s: %v", subject, seen)
		}
	}
}

// Un envio de prueba de campaigns (lote interno con test=true) queda marcado y la marca
// viaja en todos los transactional.email.* del mensaje.
func TestEnvioDePruebaMarcaTodosLosEventos(t *testing.T) {
	f := newMarketingFixture(t)
	cmd := batchCommand(f, "ana@example.com")
	cmd.Tags = map[string]string{"test": "true"}
	res, err := f.uc.CreateMarketingBatch(ctx, cmd)
	if err != nil || len(res.MessageIDs) != 1 {
		t.Fatalf("lote de prueba: %+v %v", res, err)
	}
	id := res.MessageIDs[0]
	if !f.repo.messages[id].Test {
		t.Fatal("el mensaje de prueba se guarda marcado")
	}
	driveLifecycle(t, f, id)
	assertTestFlag(t, f, true)
}

func TestEnvioRealDeCampanaNoEsPrueba(t *testing.T) {
	f := newMarketingFixture(t)
	res, err := f.uc.CreateMarketingBatch(ctx, batchCommand(f, "ana@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	id := res.MessageIDs[0]
	if f.repo.messages[id].Test {
		t.Fatal("un lote sin la etiqueta no es de prueba")
	}
	driveLifecycle(t, f, id)
	assertTestFlag(t, f, false)
}

// La etiqueta test del API publico es solo una etiqueta de SES: no marca el mensaje ni
// saca sus eventos de la analitica.
func TestEtiquetaTestDelAPIPublicoNoMarcaPrueba(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	cmd := rawCommand(f, "ana@example.com")
	cmd.Unsubscribable = true
	cmd.Tags = map[string]string{"test": "true"}
	res, err := f.uc.CreateMessages(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	id := res.Messages[0].ID
	msg := f.repo.messages[id]
	if msg.Test || msg.Tags["test"] != "true" {
		t.Fatalf("la etiqueta se conserva para SES pero no marca prueba: %+v", msg)
	}
	driveLifecycle(t, f, id)
	assertTestFlag(t, f, false)
}

package app

import (
	"testing"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/google/uuid"
)

// doiCommand es el correo del doble opt-in tal como lo pide automations.
func doiCommand(f *fixture, to ...string) CreateMessagesCommand {
	cmd := templateCommand(f, to...)
	cmd.IdempotencyKey = "doi:" + uuid.NewString()
	cmd.Purpose = domain.PurposeDoubleOptIn
	cmd.Variables = map[string]any{"confirm_url": "https://app.example.com/api/v1/public/contacts/confirm?t=a&k=b"}
	return cmd
}

// Quien se dio de baja y pide volver debe recibir la confirmacion: con el proposito del
// doble opt-in la baja voluntaria no bloquea, y el resto de reglas se aplican igual.
func TestDoubleOptInNoLoBloqueaLaBajaVoluntaria(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	f.supp.suppressed["ana@example.com"] = domain.SuppressionReasonUnsubscribe

	res, err := f.uc.CreateMessages(ctx, doiCommand(f, "ana@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 1 || res.Messages[0].Status != domain.StatusQueued {
		t.Fatalf("la confirmacion debe encolarse: %+v", res.Messages)
	}
	if len(res.Suppressed) != 0 {
		t.Fatalf("la baja ignorada no figura como suprimida: %+v", res.Suppressed)
	}
	if n := len(f.repo.published("transactional.message.queued")); n != 1 {
		t.Fatalf("sale por el carril transaccional: %d encolados", n)
	}
	if len(f.rep.calls) != 1 || f.rep.calls[0].Class != domain.ClassTransactional || f.rep.calls[0].Count != 1 {
		t.Fatalf("reputation autoriza la clase transactional por un destinatario: %+v", f.rep.calls)
	}
}

func TestDoubleOptInSigueBloqueadoPorRebotesQuejasYExclusiones(t *testing.T) {
	for _, reason := range []string{"hard_bounce", "complaint", "manual", "invalid"} {
		f := newFixture(t, Config{})
		f.setDomain(f.tenant, shopDomain, "verified", "sending")
		f.supp.suppressed["ana@example.com"] = reason

		res, err := f.uc.CreateMessages(ctx, doiCommand(f, "ana@example.com"))
		if err != nil {
			t.Fatalf("%s: %v", reason, err)
		}
		if len(res.Messages) != 1 || res.Messages[0].Status != domain.StatusSuppressed {
			t.Fatalf("%s debe bloquear aun con el proposito: %+v", reason, res.Messages)
		}
		if len(res.Suppressed) != 1 || res.Suppressed[0].Reason != reason {
			t.Fatalf("%s: la respuesta dice por que: %+v", reason, res.Suppressed)
		}
		if n := len(f.repo.published("transactional.message.queued")); n != 0 {
			t.Fatalf("%s: no se encola nada", reason)
		}
	}
}

func TestSinPropositoLaBajaBloquea(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	f.supp.suppressed["ana@example.com"] = domain.SuppressionReasonUnsubscribe
	cmd := doiCommand(f, "ana@example.com")
	cmd.Purpose = ""

	res, err := f.uc.CreateMessages(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 1 || res.Messages[0].Status != domain.StatusSuppressed {
		t.Fatalf("sin proposito la baja se respeta: %+v", res.Messages)
	}
}

// La repeticion con la misma clave devuelve lo mismo y no crea un segundo mensaje: es lo
// que protege a automations ante la reentrega del evento.
func TestDoubleOptInRepetidoNoEnviaDosVeces(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	cmd := doiCommand(f, "ana@example.com")
	first, err := f.uc.CreateMessages(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.uc.CreateMessages(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Replayed || again.Messages[0].ID != first.Messages[0].ID {
		t.Fatalf("la misma clave repite la respuesta: %+v", again)
	}
	if n := len(f.repo.published("transactional.message.queued")); n != 1 {
		t.Fatalf("un solo mensaje encolado: %d", n)
	}
}

func TestPropositoValidado(t *testing.T) {
	f := newFixture(t, Config{})
	f.setDomain(f.tenant, shopDomain, "verified", "sending")
	unknown := doiCommand(f, "ana@example.com")
	unknown.Purpose = "marketing"
	two := doiCommand(f, "ana@example.com", "eva@example.com")
	raw := rawCommand(f, "ana@example.com")
	raw.Purpose = domain.PurposeDoubleOptIn
	raw.Cc = []domain.Recipient{{Email: "copia@example.com"}}
	for name, cmd := range map[string]CreateMessagesCommand{"desconocido": unknown, "dos destinatarios": two, "con copia": raw} {
		if _, err := f.uc.CreateMessages(ctx, cmd); !domain.IsValidation(err) {
			t.Errorf("%s: se esperaba un error de validacion, hubo %v", name, err)
		}
	}
	if len(f.supp.checks) != 0 {
		t.Fatal("una peticion invalida no llega a consultar la supresion")
	}
}

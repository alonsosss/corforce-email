//go:build integration

package app

import (
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

// La caducidad de una exclusion manual contra la base: el contacto excluded vuelve a
// active con el consentimiento que tenia y enviable, el cambio sale por la outbox como
// contacts.contact.updated en la misma transaccion, y la reentrega no escribe nada mas.
func TestCaducidadContraLaBase(t *testing.T) {
	d := setupAdmission(t)
	const email = "temporal@example.com"
	d.sup.causes[email] = []domain.ActiveCause{{Cause: domain.CauseManual, RegisteredAt: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)}}
	c, err := d.uc.CreateContact(d.ctx, d.tenant, CreateContactInput{
		Email: email, Consent: &ConsentInput{Status: "granted", Method: "api", Source: "crm"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if st, cs := d.stored(t, email); st != domain.StatusExcluded || cs != domain.ConsentGranted {
		t.Fatalf("entra excluded con su consentimiento: %s/%s", st, cs)
	}
	updated := func() int {
		return d.count(t, `SELECT count(*) FROM platform.event_outbox WHERE tenant_id = $1 AND subject = 'contacts.contact.updated'`, d.tenant)
	}
	consents := func() int {
		return d.count(t, `SELECT count(*) FROM contacts.consents WHERE tenant_id = $1`, d.tenant)
	}
	before, consentsBefore := updated(), consents()

	delete(d.sup.causes, email)
	ev := SuppressionEvent{Subject: SubjectSuppressionExpired, TenantID: d.tenant, Email: email, Reason: "manual", Source: "api", HasReasons: true}
	for delivery := 1; delivery <= 2; delivery++ {
		res, err := d.uc.ApplySuppression(d.ctx, ev)
		if err != nil {
			t.Fatal(err)
		}
		if res.Changed != (delivery == 1) {
			t.Fatalf("entrega %d: %+v", delivery, res)
		}
	}
	if st, cs := d.stored(t, email); st != domain.StatusActive || cs != domain.ConsentGranted {
		t.Fatalf("vuelve a active con su consentimiento: %s/%s", st, cs)
	}
	if got := updated(); got != before+1 {
		t.Fatalf("un solo contacts.contact.updated: %d, antes %d", got, before)
	}
	if got := consents(); got != consentsBefore {
		t.Fatalf("la caducidad no escribe evidencia: %d, antes %d", got, consentsBefore)
	}
	sendable, err := d.uc.SendableContacts(d.ctx, d.tenant, []uuid.UUID{c.ID})
	if err != nil || len(sendable) != 1 {
		t.Fatalf("vuelve a ser enviable: %+v %v", sendable, err)
	}
}

package postgres

import (
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/contacts/internal/ports"
	"github.com/google/uuid"
)

// La consulta de enviables por id y la audiencia comparten la regla: si una cambiara sin
// la otra, automations enviaria a quien una campana no enviaria (o al reves).
func TestEnviablesYAudienciaCompartenLaRegla(t *testing.T) {
	tenant := uuid.New()
	sendable, args := sendableSQL(tenant, []uuid.UUID{uuid.New()})
	audience, _, err := audienceSQL(tenant, ports.AudienceSpec{ListIDs: []uuid.UUID{uuid.New()}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, cond := range sendableConditions() {
		if !strings.Contains(sendable, cond) || !strings.Contains(audience, cond) {
			t.Fatalf("la condicion %q debe estar en las dos consultas:\n%s\n%s", cond, sendable, audience)
		}
	}
	if !strings.Contains(sendable, "c.id = ANY($2::uuid[])") || len(args) != 2 {
		t.Fatalf("filtra por los ids pedidos: %s %v", sendable, args)
	}
}

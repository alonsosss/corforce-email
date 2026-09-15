// Package outbox encola los eventos de las claves DKIM en platform.event_outbox de la base de la
// empresa, dentro de la transaccion que cambia las claves y escribe su historial; el rele de
// pkg/outbox (RunForTenants en main) los entrega al stream DOMAINS. Una rotacion o una revocacion
// no puede quedar sin su evento, que es su rastro en la bitacora de auditoria.
package outbox

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/google/uuid"
)

// Subjects propios del stream DOMAINS que salen por la outbox; el resto sale por el publicador
// de NATS (adapters/nats).
const (
	SubjectDKIMRotated = "domains.domain.dkim_rotated"
	SubjectDKIMRevoked = "domains.domain.dkim_revoked"
	source             = "domain-service"
)

type Publisher struct {
	q outbox.Execer
}

// NewPublisher recibe el db.ContextPool: Enqueue escribe por la transaccion del contexto.
func NewPublisher(q outbox.Execer) *Publisher { return &Publisher{q: q} }

// DKIMRotated anuncia una rotacion programada: la clave nueva y la que queda en gracia.
func (p *Publisher) DKIMRotated(ctx context.Context, d *domain.Domain, r *domain.DKIMRotation) error {
	data := keyData(d, r)
	data["previous_selector"] = r.PreviousSelector
	return p.enqueue(ctx, SubjectDKIMRotated, d, r, data)
}

// DKIMRevoked anuncia una revocacion por clave comprometida: las claves retiradas de los motores,
// los TXT que el cliente debe quitar de su DNS ya (remove_dns_records) y el motivo.
func (p *Publisher) DKIMRevoked(ctx context.Context, d *domain.Domain, r *domain.DKIMRotation) error {
	revoked := make([]string, len(r.RevokedSelectors))
	hosts := make([]string, len(r.RevokedSelectors))
	for i, s := range r.RevokedSelectors {
		revoked[i] = s
		hosts[i] = domain.DKIMHost(s, d.Domain)
	}
	data := keyData(d, r)
	data["revoked_selectors"] = revoked
	data["remove_dns_records"] = hosts
	data["reason"] = r.Reason
	return p.enqueue(ctx, SubjectDKIMRevoked, d, r, data)
}

// keyData es el payload comun: los campos de todo domains.domain.* mas la clave actual. Nunca
// lleva material privado.
func keyData(d *domain.Domain, r *domain.DKIMRotation) map[string]interface{} {
	return map[string]interface{}{
		"tenant_id":  d.TenantID.String(),
		"domain_id":  d.ID.String(),
		"domain":     d.Domain,
		"purpose":    string(d.Purpose),
		"status":     string(d.Status),
		"selector":   r.Selector,
		"rotated_at": r.RotatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// enqueue arma el envelope. UserID lleva a quien pidio la rotacion: es la pista de auditoria.
func (p *Publisher) enqueue(ctx context.Context, subject string, d *domain.Domain, r *domain.DKIMRotation, data map[string]interface{}) error {
	evt := events.Event{Type: subject, Source: source, TenantID: d.TenantID.String(), Data: data}
	if r.ActorID != uuid.Nil {
		evt.UserID = r.ActorID.String()
	}
	return outbox.Enqueue(ctx, p.q, subject, evt)
}
